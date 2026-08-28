param([string]$PackageDirectory)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($PackageDirectory)
$executable = Join-Path $root 'nodeharbor.exe'
if (-not (Test-Path -LiteralPath $executable)) { throw "NodeHarbor executable is missing" }
$port = 19876
$data = Join-Path $root 'smoke-data'
$process = Start-Process -FilePath $executable -ArgumentList @('--listen', "127.0.0.1:$port", '--data', $data, '--open-browser=false') -PassThru -WindowStyle Hidden
try {
    $healthy = $false
    for ($attempt = 0; $attempt -lt 30; $attempt++) { try { $response = Invoke-RestMethod "http://127.0.0.1:$port/api/health"; if ($response.status -eq 'healthy') { $healthy = $true; break } } catch {}; Start-Sleep -Milliseconds 200 }
    if (-not $healthy) { throw 'NodeHarbor did not become healthy' }
    $subscriptionText = (& curl.exe --fail --silent --show-error "http://127.0.0.1:$port/sub/clash.yaml" | Out-String)
    if ($subscriptionText.Length -eq 0 -or $subscriptionText -notmatch 'proxy-groups') { throw 'Published Subscription smoke check failed' }
}
finally { if (-not $process.HasExited) { Stop-Process -Id $process.Id -Force } }
