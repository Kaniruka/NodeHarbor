param(
    [Parameter(Mandatory=$true)][string]$MihomoPath,
    [string]$OutputDirectory = (Join-Path $PSScriptRoot '..\dist'),
    [string]$Version = 'dev'
)
$ErrorActionPreference = 'Stop'
$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..'))
$modulePath = Join-Path $projectRoot 'kernelsu\module.prop'
$moduleText = Get-Content -Raw -LiteralPath $modulePath
$moduleVersionMatch = [regex]::Match($moduleText, '(?m)^version=(.+)$')
if (-not $moduleVersionMatch.Success) { throw 'KernelSU module version is missing' }
$moduleVersion = $moduleVersionMatch.Groups[1].Value.Trim()
$outputRoot = [IO.Path]::GetFullPath($OutputDirectory)
$stage = Join-Path $outputRoot "NodeHarbor-kernelsu-$Version"
$archive = Join-Path $outputRoot "NodeHarbor-kernelsu-$Version.zip"
$mihomo = [IO.Path]::GetFullPath($MihomoPath)
if (-not (Test-Path -LiteralPath $mihomo)) { throw "Mihomo arm64 binary is missing: $mihomo" }
$metadataPath = Join-Path $projectRoot 'kernelsu\mihomo.json'
$metadata = Get-Content -Raw -LiteralPath $metadataPath | ConvertFrom-Json
if ($metadata.platform -ne 'android-arm64-v8' -or $metadata.version -ne 'v1.19.30' -or $metadata.license -ne 'GPL-3.0-or-later') {
    throw 'KernelSU Mihomo metadata is invalid'
}
$mihomoBytes = [IO.File]::ReadAllBytes($mihomo)
if ($mihomoBytes.Length -lt 20 -or $mihomoBytes[0] -ne 0x7f -or $mihomoBytes[1] -ne 0x45 -or $mihomoBytes[2] -ne 0x4c -or $mihomoBytes[3] -ne 0x46 -or $mihomoBytes[4] -ne 2 -or $mihomoBytes[5] -ne 1 -or $mihomoBytes[18] -ne 0xb7 -or $mihomoBytes[19] -ne 0) {
    throw 'Mihomo binary is not an ELF AArch64 (arm64-v8) executable'
}
$mihomoDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $mihomo).Hash.ToUpperInvariant()
if ($mihomoDigest -ne $metadata.executableSHA256.ToUpperInvariant() -or $mihomoDigest -ne '94344144936968F25E7089BBEAC2D87F3CAF67574BA433511424724AD7435DAD') {
    throw "Mihomo Android arm64-v8 executable checksum mismatch: $mihomoDigest"
}
$noticePath = Join-Path $projectRoot 'THIRD_PARTY_NOTICES'
$licensePath = Join-Path $projectRoot 'LICENSE'
if (-not (Test-Path -LiteralPath $noticePath -PathType Leaf)) { throw 'THIRD_PARTY_NOTICES is missing' }
if (-not (Test-Path -LiteralPath $licensePath -PathType Leaf)) { throw 'LICENSE is missing' }
$noticeText = Get-Content -Raw -LiteralPath $noticePath
foreach ($requiredNotice in @(
    'NodeHarbor bundles Mihomo v1.19.30 for Windows x64 and Android arm64-v8.',
    'Android arm64-v8 asset: mihomo-android-arm64-v8-v1.19.30.gz',
    'Android arm64-v8 executable SHA-256: 94344144936968F25E7089BBEAC2D87F3CAF67574BA433511424724AD7435DAD',
    'License: GNU General Public License v3.0 or later (GPL-3.0-or-later)',
    'Source: https://github.com/MetaCubeX/mihomo/tree/v1.19.30',
    'License text: https://www.gnu.org/licenses/gpl-3.0.txt'
)) {
    if (-not $noticeText.Contains($requiredNotice)) { throw "THIRD_PARTY_NOTICES is incomplete: missing '$requiredNotice'" }
}
if (Test-Path -LiteralPath $stage) { Remove-Item -LiteralPath $stage -Recurse -Force }
New-Item -ItemType Directory -Force -Path (Join-Path $stage 'bin') | Out-Null
New-Item -ItemType Directory -Force -Path (Join-Path $stage 'webroot') | Out-Null
New-Item -ItemType Directory -Force -Path $outputRoot | Out-Null
Push-Location $projectRoot
try { $env:GOOS='android'; $env:GOARCH='arm64'; $env:CGO_ENABLED='0'; go build -trimpath -ldflags "-X main.version=$moduleVersion" -o (Join-Path $stage 'bin\nodeharbor') ./cmd/nodeharbor }
finally { Remove-Item Env:GOOS -ErrorAction SilentlyContinue; Remove-Item Env:GOARCH -ErrorAction SilentlyContinue; Remove-Item Env:CGO_ENABLED -ErrorAction SilentlyContinue; Pop-Location }
$nodeharborPath = Join-Path $stage 'bin\nodeharbor'
$nodeharborDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $nodeharborPath).Hash.ToUpperInvariant()
$nodeharborMetadata = [ordered]@{
    platform = 'android-arm64-v8'
    version = $moduleVersion
    executableSHA256 = $nodeharborDigest
    license = 'MIT'
}
$nodeharborMetadata | ConvertTo-Json | Set-Content -NoNewline -LiteralPath (Join-Path $stage 'bin\nodeharbor.json')
Copy-Item -LiteralPath $mihomo -Destination (Join-Path $stage 'bin\nodeharbor-core')
Copy-Item -LiteralPath $metadataPath -Destination (Join-Path $stage 'bin\nodeharbor-core.json')
Copy-Item -LiteralPath $modulePath -Destination $stage
Copy-Item -LiteralPath (Join-Path $projectRoot 'kernelsu\service.sh') -Destination $stage
Copy-Item -LiteralPath (Join-Path $projectRoot 'kernelsu\uninstall.sh') -Destination $stage
Copy-Item -LiteralPath (Join-Path $projectRoot 'kernelsu\action.sh') -Destination $stage
Copy-Item -LiteralPath (Join-Path $projectRoot 'kernelsu\smoke.sh') -Destination $stage
Copy-Item -LiteralPath (Join-Path $projectRoot 'kernelsu\nodeharbor-lifecycle.sh') -Destination $stage
Copy-Item -LiteralPath $noticePath -Destination $stage
Copy-Item -LiteralPath $licensePath -Destination $stage
Copy-Item -Path (Join-Path $projectRoot 'internal\web\dist\*') -Destination (Join-Path $stage 'webroot') -Recurse
if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
Compress-Archive -Path (Join-Path $stage '*') -DestinationPath $archive
Write-Output $archive
