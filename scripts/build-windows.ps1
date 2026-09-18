$ErrorActionPreference = 'Stop'

$repoRoot = Split-Path -Parent $PSScriptRoot
$distDir = Join-Path $repoRoot 'dist\windows-amd64'
New-Item -ItemType Directory -Path $distDir -Force | Out-Null

$env:CGO_ENABLED = '1'
$env:GOOS = 'windows'
$env:GOARCH = 'amd64'

$output = Join-Path $distDir 'cpa-codex-turn-state.dll'
go build -trimpath -ldflags='-s -w' -buildmode=c-shared -o $output $repoRoot
if ($LASTEXITCODE -ne 0) { throw 'Go plugin build failed' }

$header = [System.IO.Path]::ChangeExtension($output, '.h')
if (Test-Path -LiteralPath $header) {
    Remove-Item -LiteralPath $header
}

Get-FileHash -Algorithm SHA256 -LiteralPath $output
