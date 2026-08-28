param(
    [string]$Listen = '127.0.0.1:9876',
    [string]$Data = 'data'
)

$ErrorActionPreference = 'Stop'
$projectRoot = Split-Path -Parent $PSScriptRoot

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
    go run ./cmd/nodeharbor --listen $Listen --data $Data
}
finally {
    Pop-Location
}
