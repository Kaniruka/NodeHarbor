param(
    [string]$Listen = '127.0.0.1:9876',
    [string]$Data = 'data'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot
$binDirectory = Join-Path $projectRoot 'bin'
$corePath = Join-Path $binDirectory 'nodeharbor-core.exe'

if (-not (Test-Path -LiteralPath $corePath)) {
    New-Item -ItemType Directory -Path $binDirectory -Force | Out-Null
    $scratchDirectory = Join-Path $projectRoot '.scratch'
    New-Item -ItemType Directory -Path $scratchDirectory -Force | Out-Null
    $archive = Join-Path $scratchDirectory 'mihomo-windows-amd64-v1.19.30.zip'
    Invoke-WebRequest 'https://github.com/MetaCubeX/mihomo/releases/download/v1.19.30/mihomo-windows-amd64-v1.19.30.zip' -OutFile $archive
    $actualDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $archive).Hash
    $expectedDigest = '22C09FD67673895EF7CD6B1820563918275C3D316F2462B306208675118DB3C0'
    if ($actualDigest -ne $expectedDigest) {
        throw "Mihomo checksum mismatch: $actualDigest"
    }
    Expand-Archive -LiteralPath $archive -DestinationPath $binDirectory -Force
    Move-Item -LiteralPath (Join-Path $binDirectory 'mihomo-windows-amd64.exe') -Destination $corePath
}
$actualCoreDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $corePath).Hash
$expectedCoreDigest = 'F55B3028D9160BEB9044F21B05DD7405B46524614A19642D6291492F5F985761'
if ($actualCoreDigest -ne $expectedCoreDigest) {
    throw "Mihomo executable checksum mismatch: $actualCoreDigest"
}

Push-Location (Join-Path $projectRoot 'web')
try {
    npm ci
    npm run build
}
finally {
    Pop-Location
}

Push-Location $projectRoot
try {
    go build -o (Join-Path $binDirectory 'nodeharbor.exe') ./cmd/nodeharbor
    & (Join-Path $binDirectory 'nodeharbor.exe') --listen $Listen --data $Data
}
finally {
    Pop-Location
}
