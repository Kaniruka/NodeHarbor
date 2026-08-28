param([Parameter(Mandatory=$true)][string]$PackageDirectory)
$ErrorActionPreference = 'Stop'
$root = [IO.Path]::GetFullPath($PackageDirectory)
foreach ($path in @('module.prop', 'service.sh', 'bin\nodeharbor', 'bin\nodeharbor-core', 'THIRD_PARTY_NOTICES')) {
    if (-not (Test-Path -LiteralPath (Join-Path $root $path))) { throw "KernelSU package is missing $path" }
}
$prop = Get-Content -Raw -LiteralPath (Join-Path $root 'module.prop')
if ($prop -notmatch '(?m)^id=nodeharbor$' -or $prop -notmatch '(?m)^versionCode=') { throw 'KernelSU module metadata is invalid' }
$service = Get-Content -Raw -LiteralPath (Join-Path $root 'service.sh')
if ($service -notmatch 'nodeharbor-core' -or $service -notmatch 'nodeharbor.pid' -or $service -match '(?m)^\s*(killall|iptables|ip route)') { throw 'KernelSU service violates ownership boundary' }
Write-Output 'KernelSU package layout smoke check passed'
