param([Parameter(Mandatory = $true)][string]$PackageDirectory)

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($PackageDirectory)
$executable = Join-Path $root 'nodeharbor.exe'
$core = Join-Path $root 'nodeharbor-core.exe'
$notices = Join-Path $root 'THIRD_PARTY_NOTICES'
$expectedCoreDigest = 'F55B3028D9160BEB9044F21B05DD7405B46524614A19642D6291492F5F985761'
$expectedFiles = @('README.md', 'THIRD_PARTY_NOTICES', 'nodeharbor-core.exe', 'nodeharbor.exe')
$generatedPaths = @('smoke-data', 'smoke-fake.ready', 'smoke-fake.mode') | ForEach-Object { Join-Path $root $_ }
foreach ($generatedPath in $generatedPaths) {
    if (Test-Path -LiteralPath $generatedPath) {
        Remove-Item -LiteralPath $generatedPath -Recurse -Force
    }
}

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
$actualCoreDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $core).Hash.ToUpperInvariant()
if ($actualCoreDigest -ne $expectedCoreDigest) {
    throw "Package contains an unpinned Mihomo core: $actualCoreDigest"
}
$noticeText = Get-Content -Raw -LiteralPath $notices
foreach ($requiredNotice in @(
    'NodeHarbor bundles Mihomo v1.19.30 for Windows x64 and Android arm64-v8.',
    'Windows x64 asset: mihomo-windows-amd64-v1.19.30.zip',
    'Windows x64 executable SHA-256: F55B3028D9160BEB9044F21B05DD7405B46524614A19642D6291492F5F985761',
    'License: GNU General Public License v3.0 or later (GPL-3.0-or-later)',
    'Source: https://github.com/MetaCubeX/mihomo/tree/v1.19.30',
    'License text: https://www.gnu.org/licenses/gpl-3.0.txt'
)) {
    if (-not $noticeText.Contains($requiredNotice)) {
        throw "THIRD_PARTY_NOTICES is incomplete: missing '$requiredNotice'"
    }
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
    $client.Timeout = [TimeSpan]::FromSeconds(5)
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
$readyFile = Join-Path $root 'smoke-fake.ready'
$modeFile = Join-Path $root 'smoke-fake.mode'
$subscription = @"
proxies:
  - name: Smoke Proxy
    type: direct
"@
$fakeJob = Start-Job -ArgumentList $fakePort, $readyFile, $modeFile, $subscription -ScriptBlock {
    param($Port, $ReadyFile, $ModeFile, $Subscription)
    $ErrorActionPreference = 'Stop'
    $listener = [Net.HttpListener]::new()
    $listener.Prefixes.Add("http://127.0.0.1:$Port/")
    $listener.Start()
    Set-Content -LiteralPath $ReadyFile -Value 'ready' -NoNewline
    try {
        $pendingContext = $listener.BeginGetContext($null, $null)
        while ($true) {
            if (-not $pendingContext.AsyncWaitHandle.WaitOne(250)) { continue }
            try {
                $context = $listener.EndGetContext($pendingContext)
            }
            catch {
                break
            }
            try {
                $path = $context.Request.Url.AbsolutePath
                if ($context.Request.RawUrl -match '^https?://[^/]+(?<absolutePath>/[^?]*)') {
                    $path = $Matches['absolutePath']
                }
                $body = ''
                $contentType = 'text/plain; charset=utf-8'
                $status = 200
                $mode = if (Test-Path -LiteralPath $ModeFile) { Get-Content -Raw -LiteralPath $ModeFile } else { '' }
                switch ($path) {
                    '/subscription' {
                        if ($mode.Trim() -eq 'upstream-failure') {
                            $status = 503
                            $body = 'upstream unavailable'
                        }
                        else {
                            $body = $Subscription
                            $contentType = 'application/yaml; charset=utf-8'
                        }
                    }
                    '/probe' {
                        $status = 204
                    }
                    '/identity' {
                        $body = '203.0.113.8'
                    }
                    '/score' {
                        if ($mode.Trim() -eq 'provider-failure') {
                            $status = 503
                            $body = 'provider unavailable'
                        }
                        else {
                            $body = '{"status":"success","data":{"ip_score":99}}'
                            $contentType = 'application/json; charset=utf-8'
                        }
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
            $pendingContext = $listener.BeginGetContext($null, $null)
        }
    }
    finally {
        $listener.Stop()
        $listener.Close()
    }
}
$process = $null
$oldPath = $env:PATH
try {
    for ($attempt = 0; $attempt -lt 50; $attempt++) {
        if (Test-Path -LiteralPath $readyFile) { break }
        if ($fakeJob.State -eq 'Failed') { throw "Deterministic fake server failed: $($fakeJob.ChildJobs[0].JobStateInfo.Reason)" }
        Start-Sleep -Milliseconds 100
    }
    if (-not (Test-Path -LiteralPath $readyFile)) { throw 'Deterministic fake server did not become ready' }

    $data = Join-Path $root 'smoke-data'
    $oldPackageSmoke = [Environment]::GetEnvironmentVariable('NODEHARBOR_PACKAGE_SMOKE', 'Process')
    [Environment]::SetEnvironmentVariable('NODEHARBOR_PACKAGE_SMOKE', '1', 'Process')
    $env:PATH = "$env:SystemRoot\System32;$env:SystemRoot"
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
    $databaseStream = [IO.File]::Open($databasePath, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::ReadWrite)
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

    $publicationBeforeFailures = $publication.Body
    Set-Content -LiteralPath $modeFile -Value 'upstream-failure' -NoNewline
    $failureRunResponse = Invoke-HTTP "$baseURL/api/evaluation-runs" 'POST' '{}'
    if ($failureRunResponse.Status -ne 202) { throw "Could not start upstream failure Evaluation Run: HTTP $($failureRunResponse.Status)" }
    $failureRunID = ($failureRunResponse.Body | ConvertFrom-Json).id
    $failureRun = $null
    for ($attempt = 0; $attempt -lt 120; $attempt++) {
        $failureRun = (Invoke-HTTP "$baseURL/api/evaluation-runs/$failureRunID").Body | ConvertFrom-Json
        if ($failureRun.status -in @('completed', 'failed', 'paused')) { break }
        Start-Sleep -Milliseconds 250
    }
    if ($failureRun.status -ne 'failed' -or $failureRun.reason -notmatch 'all Upstream Subscriptions failed to refresh') {
        throw "Upstream failure was not diagnosed: $($failureRun | ConvertTo-Json -Compress)"
    }
    if ((Invoke-HTTP "$baseURL/sub/clash.yaml").Body -ne $publicationBeforeFailures) { throw 'Upstream failure replaced the previous Publication Snapshot' }

    Set-Content -LiteralPath $modeFile -Value 'provider-failure' -NoNewline
    $failureRunResponse = Invoke-HTTP "$baseURL/api/evaluation-runs" 'POST' '{"ignoreCache":true}'
    if ($failureRunResponse.Status -ne 202) { throw "Could not start provider failure Evaluation Run: HTTP $($failureRunResponse.Status)" }
    $failureRunID = ($failureRunResponse.Body | ConvertFrom-Json).id
    $failureRun = $null
    for ($attempt = 0; $attempt -lt 120; $attempt++) {
        $failureRun = (Invoke-HTTP "$baseURL/api/evaluation-runs/$failureRunID").Body | ConvertFrom-Json
        if ($failureRun.status -in @('completed', 'failed', 'paused')) { break }
        Start-Sleep -Milliseconds 250
    }
    if ($failureRun.status -ne 'failed' -or $failureRun.reason -notmatch 'all scoring') {
        throw "Provider failure was not diagnosed: $($failureRun | ConvertTo-Json -Compress)"
    }
    if ((Invoke-HTTP "$baseURL/sub/clash.yaml").Body -ne $publicationBeforeFailures) { throw 'Provider failure replaced the previous Publication Snapshot' }
    Remove-Item -LiteralPath $modeFile -Force

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
    [void]$process.WaitForExit(5000)
    $process = $null
    $restartPort = Get-FreeTcpPort
    $restartBaseURL = "http://127.0.0.1:$restartPort"
    $process = Start-Process -FilePath $executable -ArgumentList (@('--listen', "0.0.0.0:$restartPort", '--data', $data, '--open-browser=false') + $testArguments) -WorkingDirectory $root -PassThru -WindowStyle Hidden
    [void](Wait-For-HTTPStatus -Uri "$restartBaseURL/api/health" -Process $process)
    $persistedSources = (Invoke-HTTP "$restartBaseURL/api/upstream-subscriptions").Body | ConvertFrom-Json
    if (@($persistedSources).Count -ne 1 -or $persistedSources[0].name -ne 'Smoke Source') { throw 'Upstream Subscription state did not persist across restart' }
    $persistedPublication = Invoke-HTTP "$restartBaseURL/sub/clash.yaml"
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
    if ([IO.File]::Exists($readyFile)) { [IO.File]::Delete($readyFile) }
    if ([IO.File]::Exists($modeFile)) { [IO.File]::Delete($modeFile) }
    $env:PATH = $oldPath
}
