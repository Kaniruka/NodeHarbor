param([Parameter(Mandatory = $true)][string]$PackageDirectory)

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($PackageDirectory)
$executable = Join-Path $root 'nodeharbor.exe'
$core = Join-Path $root 'nodeharbor-core.exe'
$notices = Join-Path $root 'THIRD_PARTY_NOTICES'
$expectedFiles = @('README.md', 'THIRD_PARTY_NOTICES', 'nodeharbor-core.exe', 'nodeharbor.exe')

foreach ($requiredFile in @($executable, $core, $notices)) {
    if (-not (Test-Path -LiteralPath $requiredFile -PathType Leaf)) {
        throw "Package file is missing: $requiredFile"
    }
}
$actualDirectories = @(Get-ChildItem -LiteralPath $root -Recurse -Directory)
if ($actualDirectories.Count -ne 0) {
    throw "Package contains unexpected directories: $($actualDirectories.FullName -join ', ')"
}
$actualFiles = @(Get-ChildItem -LiteralPath $root -Recurse -File | ForEach-Object { $_.FullName.Substring($root.Length).TrimStart('\') } | Sort-Object)
if (@(Compare-Object $expectedFiles $actualFiles).Count -ne 0) {
    throw "Package contains unexpected or missing files: $($actualFiles -join ', ')"
}

function Get-FreeTcpPort {
    $listener = [Net.Sockets.TcpListener]::new([Net.IPAddress]::Loopback, 0)
    try {
        $listener.Start()
        return $listener.LocalEndpoint.Port
    }
    finally {
        $listener.Stop()
    }
}

function Invoke-HTTP {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [string]$Method = 'GET',
        [string]$Body
    )
    $handler = [Net.Http.HttpClientHandler]::new()
    $handler.UseProxy = $false
    $client = [Net.Http.HttpClient]::new($handler)
    $request = $null
    $response = $null
    try {
        $request = [Net.Http.HttpRequestMessage]::new([Net.Http.HttpMethod]::new($Method), $Uri)
        if ($null -ne $Body) {
            $request.Content = [Net.Http.StringContent]::new($Body, [Text.Encoding]::UTF8, 'application/json')
        }
        $response = $client.SendAsync($request).GetAwaiter().GetResult()
        $responseBody = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
        return [PSCustomObject]@{
            Status = [int]$response.StatusCode
            Body = $responseBody
        }
    }
    finally {
        if ($null -ne $request) { $request.Dispose() }
        if ($null -ne $response) { $response.Dispose() }
        $client.Dispose()
        $handler.Dispose()
    }
}

function Wait-For-HTTPStatus {
    param(
        [Parameter(Mandatory = $true)][string]$Uri,
        [System.Diagnostics.Process]$Process,
        [int]$ExpectedStatus = 200
    )
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        if ($null -ne $Process -and $Process.HasExited) { throw "NodeHarbor exited while waiting for $Uri`: $($Process.ExitCode)" }
        try {
            $response = Invoke-HTTP $Uri
            if ($response.Status -eq $ExpectedStatus) { return $response }
        }
        catch { }
        Start-Sleep -Milliseconds 250
    }
    throw "Timed out waiting for HTTP $ExpectedStatus from $Uri"
}

$fakePort = Get-FreeTcpPort
$appPort = Get-FreeTcpPort
$subscription = @"
proxies:
  - name: Smoke Proxy
    type: http
    server: 127.0.0.1
    port: $fakePort
"@
$fakeJob = Start-Job -ArgumentList $fakePort, $subscription -ScriptBlock {
    param($Port, $Subscription)
    $ErrorActionPreference = 'Stop'
    $listener = [Net.HttpListener]::new()
    $listener.Prefixes.Add("http://127.0.0.1:$Port/")
    $listener.Start()
    try {
        while ($true) {
            $context = $listener.GetContext()
            try {
                $path = $context.Request.Url.AbsolutePath
                $body = ''
                $contentType = 'text/plain; charset=utf-8'
                $status = 200
                switch ($path) {
                    '/subscription' {
                        $body = $Subscription
                        $contentType = 'application/yaml; charset=utf-8'
                    }
                    '/probe' {
                        $status = 204
                    }
                    '/identity' {
                        $body = '203.0.113.8'
                    }
                    '/score' {
                        $body = '{"status":"success","data":{"ip_score":99}}'
                        $contentType = 'application/json; charset=utf-8'
                    }
                    default {
                        $status = 404
                        $body = 'not found'
                    }
                }
                $bytes = [Text.Encoding]::UTF8.GetBytes($body)
                $context.Response.StatusCode = $status
                $context.Response.ContentType = $contentType
                $context.Response.ContentLength64 = $bytes.Length
                if ($bytes.Length -gt 0) {
                    $context.Response.OutputStream.Write($bytes, 0, $bytes.Length)
                }
            }
            finally {
                $context.Response.Close()
            }
        }
    }
    finally {
        $listener.Stop()
        $listener.Close()
    }
}
$process = $null
try {
    $fakeReady = $false
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        if ($fakeJob.State -eq 'Failed') { throw "Deterministic fake server failed: $($fakeJob.ChildJobs[0].JobStateInfo.Reason)" }
        try {
            if ((Invoke-HTTP "http://127.0.0.1:$fakePort/subscription").Status -eq 200) {
                $fakeReady = $true
                break
            }
        }
        catch { }
        Start-Sleep -Milliseconds 100
    }
    if (-not $fakeReady) { throw 'Deterministic fake server did not become ready' }

    $data = Join-Path $root 'smoke-data'
    $oldPackageSmoke = [Environment]::GetEnvironmentVariable('NODEHARBOR_PACKAGE_SMOKE', 'Process')
    [Environment]::SetEnvironmentVariable('NODEHARBOR_PACKAGE_SMOKE', '1', 'Process')
    $testArguments = @(
        '--test-iplark-endpoint', "http://127.0.0.1:$fakePort/score",
        '--test-ipv4-identity-endpoint', "http://127.0.0.1:$fakePort/identity",
        '--test-ipv6-identity-endpoint', "http://127.0.0.1:$fakePort/identity-v6"
    )
    $process = Start-Process -FilePath $executable -ArgumentList (@('--listen', "0.0.0.0:$appPort", '--data', $data, '--open-browser=false') + $testArguments) -WorkingDirectory $root -PassThru -WindowStyle Hidden

    $baseURL = "http://127.0.0.1:$appPort"
    $health = Wait-For-HTTPStatus -Uri "$baseURL/api/health" -Process $process
    if (($health.Body | ConvertFrom-Json).status -ne 'healthy') { throw 'NodeHarbor did not become healthy' }
    $databasePath = Join-Path $data 'nodeharbor.db'
    if (-not (Test-Path -LiteralPath $databasePath -PathType Leaf)) { throw 'SQLite state file was not created' }
    $databaseStream = [IO.File]::OpenRead($databasePath)
    try {
        $databaseHeader = New-Object byte[] 16
        if ($databaseStream.Read($databaseHeader, 0, $databaseHeader.Length) -ne $databaseHeader.Length -or [Text.Encoding]::ASCII.GetString($databaseHeader) -ne 'SQLite format 3' + [char]0) {
            throw 'Persistent state file is not SQLite'
        }
    }
    finally {
        $databaseStream.Dispose()
    }
    $webUI = Wait-For-HTTPStatus -Uri "$baseURL/" -Process $process
    if ($webUI.Body -notmatch '<div id="root"></div>') { throw 'Embedded WebUI smoke check failed' }
    $assetPaths = @([regex]::Matches($webUI.Body, '(?:src|href)="([^"#?]+)"') | ForEach-Object { $_.Groups[1].Value } | Where-Object { $_ -like '/assets/*' } | Select-Object -Unique)
    if ($assetPaths.Count -eq 0) { throw 'Embedded WebUI does not reference any assets' }
    foreach ($assetPath in $assetPaths) {
        $asset = Wait-For-HTTPStatus -Uri "$baseURL$assetPath" -Process $process
        if ([string]::IsNullOrWhiteSpace($asset.Body)) { throw "Embedded WebUI asset is empty: $assetPath" }
    }

    $settings = @{
        scoringProvider = 'iplark'
        iplarkThreshold = 70
        availabilityAttempts = 1
        availabilityRequiredSuccesses = 1
        availabilityTimeoutSeconds = 5
        availabilityMaxLatencyMs = 1500
        availabilityURLs = @("http://127.0.0.1:$fakePort/probe")
        evaluationWorkerCount = 1
        scoringJitterMs = 0
    } | ConvertTo-Json -Compress
    $settingsResponse = Invoke-HTTP "$baseURL/api/settings" 'PUT' $settings
    if ($settingsResponse.Status -ne 204) { throw "Could not configure deterministic Evaluation Run: HTTP $($settingsResponse.Status) $($settingsResponse.Body)" }

    $source = @{ name = 'Smoke Source'; kind = 'url'; url = "http://127.0.0.1:$fakePort/subscription" } | ConvertTo-Json -Compress
    $sourceResponse = Invoke-HTTP "$baseURL/api/upstream-subscriptions" 'POST' $source
    if ($sourceResponse.Status -ne 201) { throw "Could not create controlled Upstream Subscription: HTTP $($sourceResponse.Status) $($sourceResponse.Body)" }

    $runResponse = Invoke-HTTP "$baseURL/api/evaluation-runs" 'POST' '{}'
    if ($runResponse.Status -ne 202) { throw "Could not start Evaluation Run: HTTP $($runResponse.Status) $($runResponse.Body)" }
    $runID = ($runResponse.Body | ConvertFrom-Json).id
    $completed = $false
    for ($attempt = 0; $attempt -lt 120; $attempt++) {
        $run = (Invoke-HTTP "$baseURL/api/evaluation-runs/$runID").Body | ConvertFrom-Json
        if ($run.status -in @('completed', 'failed', 'paused')) {
            $completed = $true
            break
        }
        Start-Sleep -Milliseconds 250
    }
    if (-not $completed -or $run.status -ne 'completed' -or $run.passed -ne 1 -or $run.publicationResult -ne 'published') {
        throw "Controlled Evaluation Run did not publish a Qualified Node: $($run | ConvertTo-Json -Compress)"
    }

    $publication = Invoke-HTTP "$baseURL/sub/clash.yaml"
    if ($publication.Status -ne 200 -or $publication.Body -notmatch 'Smoke Proxy' -or $publication.Body -notmatch 'proxy-groups') {
        throw 'Published Subscription does not contain the qualified controlled Proxy Node'
    }
    if ((Invoke-HTTP "$baseURL/api/settings").Status -ne 200) { throw 'loopback management access failed' }

    $lanAddresses = @(Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -notmatch '^127\.' -and $_.IPAddress -notmatch '^169\.254\.' } | Select-Object -ExpandProperty IPAddress -Unique)
    if ($lanAddresses.Count -eq 0) { throw 'No non-loopback IPv4 address is available for LAN boundary smoke testing' }
    $lanCheckPassed = $false
    foreach ($lanAddress in $lanAddresses) {
        try {
            $lanBaseURL = "http://$lanAddress`:$appPort"
            $lanPublication = Invoke-HTTP "$lanBaseURL/sub/clash.yaml"
            $lanManagement = Invoke-HTTP "$lanBaseURL/api/settings"
            $lanWebUI = Invoke-HTTP "$lanBaseURL/"
            if ($lanPublication.Status -eq 200 -and $lanManagement.Status -eq 403 -and $lanWebUI.Status -eq 403) {
                $lanCheckPassed = $true
                break
            }
        }
        catch { }
    }
    if (-not $lanCheckPassed) { throw "LAN boundary checks failed for all non-loopback addresses: $($lanAddresses -join ', ')" }

    Stop-Process -Id $process.Id -Force
    $process = $null
    $process = Start-Process -FilePath $executable -ArgumentList (@('--listen', "0.0.0.0:$appPort", '--data', $data, '--open-browser=false') + $testArguments) -WorkingDirectory $root -PassThru -WindowStyle Hidden
    [void](Wait-For-HTTPStatus -Uri "$baseURL/api/health" -Process $process)
    $persistedSources = (Invoke-HTTP "$baseURL/api/upstream-subscriptions").Body | ConvertFrom-Json
    if (@($persistedSources).Count -ne 1 -or $persistedSources[0].name -ne 'Smoke Source') { throw 'Upstream Subscription state did not persist across restart' }
    $persistedPublication = Invoke-HTTP "$baseURL/sub/clash.yaml"
    if ($persistedPublication.Status -ne 200 -or $persistedPublication.Body -notmatch 'Smoke Proxy') { throw 'Publication Snapshot did not persist across restart' }
}
finally {
    if ($null -ne $process -and -not $process.HasExited) { Stop-Process -Id $process.Id -Force }
    if ($null -ne $oldPackageSmoke) {
        [Environment]::SetEnvironmentVariable('NODEHARBOR_PACKAGE_SMOKE', $oldPackageSmoke, 'Process')
    }
    else {
        [Environment]::SetEnvironmentVariable('NODEHARBOR_PACKAGE_SMOKE', $null, 'Process')
    }
    if ($null -ne $fakeJob) {
        Stop-Job -Job $fakeJob -ErrorAction SilentlyContinue
        Remove-Job -Job $fakeJob -Force -ErrorAction SilentlyContinue
    }
}
