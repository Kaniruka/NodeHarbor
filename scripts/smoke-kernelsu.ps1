param([Parameter(Mandatory=$true)][string]$PackageDirectory)

$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($PackageDirectory)
$requiredFiles = @(
    'module.prop',
    'service.sh',
    'uninstall.sh',
    'action.sh',
    'smoke.sh',
    'nodeharbor-lifecycle.sh',
    'bin\nodeharbor',
    'bin\nodeharbor.json',
    'bin\nodeharbor-core',
    'bin\nodeharbor-core.json',
    'webroot\index.html',
    'LICENSE',
    'THIRD_PARTY_NOTICES'
)
foreach ($relativePath in $requiredFiles) {
    $path = Join-Path $root $relativePath
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "KernelSU package is missing $relativePath"
    }
}

function Assert-Arm64Elf([string]$Path, [string]$Subject) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 20 -or $bytes[0] -ne 0x7f -or $bytes[1] -ne 0x45 -or $bytes[2] -ne 0x4c -or $bytes[3] -ne 0x46 -or $bytes[4] -ne 2 -or $bytes[5] -ne 1 -or $bytes[18] -ne 0xb7 -or $bytes[19] -ne 0) {
        throw "$Subject is not an ELF AArch64 (arm64-v8) executable"
    }
}

$prop = Get-Content -Raw -LiteralPath (Join-Path $root 'module.prop')
$moduleVersionMatch = [regex]::Match($prop, '(?m)^version=(.+)$')
if ($prop -notmatch '(?m)^id=nodeharbor$' -or -not $moduleVersionMatch.Success -or $prop -notmatch '(?m)^versionCode=20$') {
    throw 'KernelSU module metadata is invalid'
}
$metadata = Get-Content -Raw -LiteralPath (Join-Path $root 'bin\nodeharbor-core.json') | ConvertFrom-Json
if ($metadata.platform -ne 'android-arm64-v8' -or $metadata.version -ne 'v1.19.30' -or $metadata.license -ne 'GPL-3.0-or-later') {
    throw "KernelSU Mihomo metadata is invalid: $($metadata | ConvertTo-Json -Compress)"
}
$corePath = Join-Path $root 'bin\nodeharbor-core'
$nodeharborPath = Join-Path $root 'bin\nodeharbor'
Assert-Arm64Elf $nodeharborPath 'NodeHarbor'
Assert-Arm64Elf $corePath 'Mihomo'
$nodeharborMetadata = Get-Content -Raw -LiteralPath (Join-Path $root 'bin\nodeharbor.json') | ConvertFrom-Json
if ($nodeharborMetadata.platform -ne 'android-arm64-v8' -or $nodeharborMetadata.version -ne $moduleVersionMatch.Groups[1].Value.Trim() -or $nodeharborMetadata.license -ne 'MIT') {
    throw "NodeHarbor metadata is invalid: $($nodeharborMetadata | ConvertTo-Json -Compress)"
}
$nodeharborDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $nodeharborPath).Hash.ToUpperInvariant()
if ($nodeharborDigest -ne $nodeharborMetadata.executableSHA256.ToUpperInvariant()) { throw "NodeHarbor digest mismatch: $nodeharborDigest" }
if (-not [Text.Encoding]::ASCII.GetString([IO.File]::ReadAllBytes($nodeharborPath)).Contains($nodeharborMetadata.version)) { throw 'NodeHarbor version is not embedded in the binary' }
$coreDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath $corePath).Hash.ToUpperInvariant()
if ($coreDigest -ne $metadata.executableSHA256.ToUpperInvariant()) { throw "KernelSU Mihomo digest mismatch: $coreDigest" }
if ($coreDigest -ne '94344144936968F25E7089BBEAC2D87F3CAF67574BA433511424724AD7435DAD') { throw "KernelSU Mihomo digest is not the pinned arm64 digest: $coreDigest" }

$noticeText = Get-Content -Raw -LiteralPath (Join-Path $root 'THIRD_PARTY_NOTICES')
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
if (-not (Get-Content -Raw -LiteralPath (Join-Path $root 'LICENSE')).Contains('MIT License')) { throw 'MIT license notice is missing' }

$service = Get-Content -Raw -LiteralPath (Join-Path $root 'service.sh')
$lifecycle = Get-Content -Raw -LiteralPath (Join-Path $root 'nodeharbor-lifecycle.sh')
$uninstall = Get-Content -Raw -LiteralPath (Join-Path $root 'uninstall.sh')
$action = Get-Content -Raw -LiteralPath (Join-Path $root 'action.sh')
foreach ($scriptText in @($service, $lifecycle, $uninstall, $action)) {
    if ($scriptText -match '(?m)^\s*(killall|pkill|iptables|ip6tables|ip route|route)\b') { throw 'KernelSU lifecycle scripts contain a foreign-process or routing command' }
}
if ($lifecycle -notmatch 'nodeharbor-core|bin/nodeharbor' -or $lifecycle -notmatch 'nodeharbor\.pid' -or $lifecycle -notmatch '/proc/.*exe' -or $lifecycle -notmatch '/proc/.*cmdline') {
    throw 'KernelSU lifecycle does not prove exact NodeHarbor PID ownership'
}
if ($service -notmatch 'nodeharbor-lifecycle\.sh' -or $lifecycle -notmatch 'TMPDIR') { throw 'KernelSU service does not use the shared owned lifecycle and temp directory' }
if ($uninstall -notmatch 'nodeharbor_stop' -or $action -notmatch 'nodeharbor_stop') { throw 'KernelSU stop lifecycle is not wired to the owned stop operation' }

Write-Output 'KernelSU package contract smoke check passed'
