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
$actualFiles = @(Get-ChildItem -LiteralPath $root -File | ForEach-Object { $_.Name } | Sort-Object)
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
$oldEnvironment = @{}
$environment = @{
    NODEHARBOR_TEST_IPLARK_ENDPOINT = "http://127.0.0.1:$fakePort/score"
    NODEHARBOR_TEST_IPV4_IDENTITY_ENDPOINT = "http://127.0.0.1:$fakePort/identity"
    NODEHARBOR_TEST_IPV6_IDENTITY_ENDPOINT = "http://127.0.0.1:$fakePort/identity-v6"
}
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

    foreach ($name in $environment.Keys) {
        $oldEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
        [Environment]::SetEnvironmentVariable($name, $environment[$name], 'Process')
    }
    $data = Join-Path $root 'smoke-data'
    $process = Start-Process -FilePath $executable -ArgumentList @('--listen', "0.0.0.0:$appPort", '--data', $data, '--open-browser=false') -WorkingDirectory $root -PassThru -WindowStyle Hidden

    $baseURL = "http://127.0.0.1:$appPort"
    $healthy = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        if ($process.HasExited) { throw "NodeHarbor exited before becoming healthy: $($process.ExitCode)" }
        try {
            $health = Invoke-HTTP "$baseURL/api/health"
            if ($health.Status -eq 200 -and (($health.Body | ConvertFrom-Json).status -eq 'healthy')) {
                $healthy = $true
                break
            }
        }
        catch { }
        Start-Sleep -Milliseconds 250
    }
    if (-not $healthy) { throw 'NodeHarbor did not become healthy' }

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

    $lanAddress = Get-NetIPAddress -AddressFamily IPv4 | Where-Object { $_.IPAddress -notmatch '^127\.' -and $_.IPAddress -notmatch '^169\.254\.' } | Select-Object -First 1 -ExpandProperty IPAddress
    if ([string]::IsNullOrWhiteSpace($lanAddress)) { throw 'No non-loopback IPv4 address is available for LAN boundary smoke testing' }
    $lanBaseURL = "http://$lanAddress`:$appPort"
    if ((Invoke-HTTP "$lanBaseURL/sub/clash.yaml").Status -ne 200) { throw 'LAN Published Subscription access failed' }
    if ((Invoke-HTTP "$lanBaseURL/api/settings").Status -ne 403) { throw 'LAN management access was not rejected' }

    Stop-Process -Id $process.Id -Force
    $process = $null
    $process = Start-Process -FilePath $executable -ArgumentList @('--listen', "0.0.0.0:$appPort", '--data', $data, '--open-browser=false') -WorkingDirectory $root -PassThru -WindowStyle Hidden
    $restarted = $false
    for ($attempt = 0; $attempt -lt 60; $attempt++) {
        if ($process.HasExited) { throw "NodeHarbor did not restart: $($process.ExitCode)" }
        try {
            if ((Invoke-HTTP "$baseURL/api/health").Status -eq 200) { $restarted = $true; break }
        }
        catch { }
        Start-Sleep -Milliseconds 250
    }
    if (-not $restarted) { throw 'NodeHarbor did not become healthy after restart' }
    $persistedSources = (Invoke-HTTP "$baseURL/api/upstream-subscriptions").Body | ConvertFrom-Json
    if (@($persistedSources).Count -ne 1 -or $persistedSources[0].name -ne 'Smoke Source') { throw 'Upstream Subscription state did not persist across restart' }
    $persistedPublication = Invoke-HTTP "$baseURL/sub/clash.yaml"
    if ($persistedPublication.Status -ne 200 -or $persistedPublication.Body -notmatch 'Smoke Proxy') { throw 'Publication Snapshot did not persist across restart' }
}
finally {
    if ($null -ne $process -and -not $process.HasExited) { Stop-Process -Id $process.Id -Force }
    foreach ($name in $environment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $oldEnvironment[$name], 'Process')
    }
    if ($null -ne $fakeJob) {
        Stop-Job -Job $fakeJob -ErrorAction SilentlyContinue
        Remove-Job -Job $fakeJob -Force -ErrorAction SilentlyContinue
    }
}
