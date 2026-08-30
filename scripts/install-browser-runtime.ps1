param(
    [string]$RuntimeDirectory = (Join-Path $PSScriptRoot '..\bin\browser-runtime')
)

$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$runtimeRoot = [IO.Path]::GetFullPath($RuntimeDirectory)
$driverDirectory = Join-Path $runtimeRoot 'driver'
$browserDirectory = Join-Path $runtimeRoot 'browsers'
New-Item -ItemType Directory -Force -Path $runtimeRoot | Out-Null
Push-Location $projectRoot
try {
    go run ./cmd/nodeharbor-browser-install --driver $driverDirectory --browsers $browserDirectory
    if (-not (Test-Path -LiteralPath $driverDirectory -PathType Container)) { throw "Playwright driver directory is missing: $driverDirectory" }
    $chromium = Get-ChildItem -LiteralPath $browserDirectory -Filter chrome.exe -File -Recurse | Select-Object -First 1
    if ($null -eq $chromium) { throw "Chromium executable was not installed under: $browserDirectory" }
    Write-Output $runtimeRoot
}
finally {
    Pop-Location
}
