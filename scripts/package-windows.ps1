param(
    [string]$OutputDirectory = (Join-Path $PSScriptRoot '..\dist'),
    [string]$Version = 'dev'
)

$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
$stage = Join-Path $outputRoot "NodeHarbor-windows-$Version"
$core = Join-Path $projectRoot 'bin\nodeharbor-core.exe'
$archive = Join-Path $outputRoot "NodeHarbor-windows-$Version.zip"
$notices = Join-Path $projectRoot 'THIRD_PARTY_NOTICES'

if (-not (Test-Path -LiteralPath $notices -PathType Leaf)) {
    throw 'THIRD_PARTY_NOTICES is missing'
}
$noticeText = Get-Content -Raw -LiteralPath $notices
foreach ($requiredNotice in @(
    'Windows x64 asset: mihomo-windows-amd64-v1.19.30.zip',
    'Windows x64 archive SHA-256: 22C09FD67673895EF7CD6B1820563918275C3D316F2462B306208675118DB3C0',
    'Windows x64 executable SHA-256: F55B3028D9160BEB9044F21B05DD7405B46524614A19642D6291492F5F985761',
    'License: GNU General Public License v3.0 or later (GPL-3.0-or-later)',
    'Source: https://github.com/MetaCubeX/mihomo/tree/v1.19.30'
)) {
    if (-not $noticeText.Contains($requiredNotice)) {
        throw "THIRD_PARTY_NOTICES is incomplete: missing '$requiredNotice'"
    }
}

if (-not (Test-Path -LiteralPath $core)) {
    $downloadRoot = Join-Path $env:TEMP 'nodeharbor-package-mihomo'
    New-Item -ItemType Directory -Force -Path $downloadRoot | Out-Null
    $download = Join-Path $downloadRoot 'mihomo.zip'
    Invoke-WebRequest 'https://github.com/MetaCubeX/mihomo/releases/download/v1.19.30/mihomo-windows-amd64-v1.19.30.zip' -OutFile $download
    if ((Get-FileHash -Algorithm SHA256 -LiteralPath $download).Hash -ne '22C09FD67673895EF7CD6B1820563918275C3D316F2462B306208675118DB3C0') { throw 'Mihomo archive checksum mismatch' }
    Expand-Archive -LiteralPath $download -DestinationPath $downloadRoot -Force
    $downloadedCore = Join-Path $downloadRoot 'mihomo-windows-amd64.exe'
    if (-not (Test-Path -LiteralPath $downloadedCore)) { throw 'Downloaded Mihomo executable is missing' }
    $core = $downloadedCore
}
$digest = (Get-FileHash -Algorithm SHA256 -LiteralPath $core).Hash
if ($digest -ne 'F55B3028D9160BEB9044F21B05DD7405B46524614A19642D6291492F5F985761') { throw "Mihomo checksum mismatch: $digest" }
New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
New-Item -ItemType Directory -Force -Path $stage | Out-Null
Push-Location $projectRoot
try { go build -o (Join-Path $stage 'nodeharbor.exe') ./cmd/nodeharbor }
finally { Pop-Location }
Copy-Item -LiteralPath $core -Destination (Join-Path $stage 'nodeharbor-core.exe')
Copy-Item -LiteralPath $notices -Destination $stage
Copy-Item -LiteralPath (Join-Path $projectRoot 'README.md') -Destination $stage
if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive
Write-Output $archive
