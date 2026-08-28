param([Parameter(Mandatory=$true)][string]$PackageDirectory)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($PackageDirectory)
foreach ($path in @('module.prop', 'service.sh', 'bin\nodeharbor', 'bin\nodeharbor-core', 'THIRD_PARTY_NOTICES')) {
    if (-not (Test-Path -LiteralPath (Join-Path $root $path))) { throw "KernelSU package is missing $path" }
}
$prop = Get-Content -Raw -LiteralPath (Join-Path $root 'module.prop')
if ($prop -notmatch '(?m)^id=nodeharbor$' -or $prop -notmatch '(?m)^versionCode=') { throw 'KernelSU module metadata is invalid' }
$coreDigest = (Get-FileHash -Algorithm SHA256 -LiteralPath (Join-Path $root 'bin\nodeharbor-core')).Hash
if ($coreDigest -ne '94344144936968F25E7089BBEAC2D87F3CAF67574BA433511424724AD7435DAD') { throw "KernelSU Mihomo digest mismatch: $coreDigest" }
$service = Get-Content -Raw -LiteralPath (Join-Path $root 'service.sh')
if ($service -notmatch 'nodeharbor-core' -or $service -notmatch 'nodeharbor.pid' -or $service -match '(?m)^\s*(killall|iptables|ip route)') { throw 'KernelSU service violates ownership boundary' }
Write-Output 'KernelSU package layout smoke check passed'
