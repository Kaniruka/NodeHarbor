param(
    [Parameter(Mandatory=$true)][string]$MihomoPath,
    [string]$OutputDirectory = (Join-Path $PSScriptRoot '..\dist'),
    [string]$Version = 'dev'
)
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
$stage = Join-Path $outputRoot "NodeHarbor-kernelsu-$Version"
$archive = Join-Path $outputRoot "NodeHarbor-kernelsu-$Version.zip"
$mihomo = [IO.Path]::GetFullPath($MihomoPath)
if (-not (Test-Path -LiteralPath $mihomo)) { throw "Mihomo arm64 binary is missing: $mihomo" }
$mihomoDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $mihomo).Hash
if ($mihomoDigest -ne '94344144936968F25E7089BBEAC2D87F3CAF67574BA433511424724AD7435DAD') {
    throw "Mihomo Android arm64-v8 executable checksum mismatch: $mihomoDigest"
}
if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
New-Item -ItemType Directory -Force -Path (Join-Path $stage 'bin') | Out-Null
New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
Push-Location $projectRoot
try { $env:GOOS='android'; $env:GOARCH='arm64'; $env:CGO_ENABLED='0'; go build -o (Join-Path $stage 'bin\nodeharbor') ./cmd/nodeharbor }
finally { Remove-Item Env:GOOS -ErrorAction SilentlyContinue; Remove-Item Env:GOARCH -ErrorAction SilentlyContinue; Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue; Pop-Location }
Copy-Item -LiteralPath $mihomo -Destination (Join-Path $stage 'bin\nodeharbor-core')
Copy-Item -LiteralPath (Join-Path $projectRoot 'kernelsu\module.prop') -Destination $stage
Copy-Item -LiteralPath (Join-Path $projectRoot 'kernelsu\service.sh') -Destination $stage
Copy-Item -LiteralPath (Join-Path $projectRoot 'THIRD_PARTY_NOTICES') -Destination $stage
if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive
Write-Output $archive
