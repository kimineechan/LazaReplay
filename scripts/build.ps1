$ErrorActionPreference = 'Stop'
Set-Location (Join-Path $PSScriptRoot '..')
if (-not (Get-Command go -ErrorAction SilentlyContinue)) {
    throw 'Install Go from https://go.dev/dl/, reopen PowerShell, then run this script.'
}
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'
$env:CGO_ENABLED = '0'
New-Item -ItemType Directory -Force dist | Out-Null
go run -buildvcs=false ./tools/mkres
if ($LASTEXITCODE -ne 0) { throw 'Resource generation failed.' }
go vet -buildvcs=false ./...
if ($LASTEXITCODE -ne 0) { throw 'Go vet failed.' }
go test -buildvcs=false ./...
if ($LASTEXITCODE -ne 0) { throw 'Tests failed.' }
go build -buildvcs=false -trimpath -ldflags '-H=windowsgui -s -w' -o dist/LazaReplay.exe ./cmd/lazareplay
if ($LASTEXITCODE -ne 0) { throw 'LazaReplay build failed.' }
go build -buildvcs=false -trimpath -ldflags '-s -w' -o dist/lazareplay-check.exe ./cmd/lazareplay-check
if ($LASTEXITCODE -ne 0) { throw 'Self-check build failed.' }
Write-Host 'Built dist\LazaReplay.exe and dist\lazareplay-check.exe'
Write-Host 'Run dist\lazareplay-check.exe to test the recorder on this computer.'
