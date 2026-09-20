$ErrorActionPreference = 'Stop'
$installRoot = Join-Path $env:LOCALAPPDATA 'cursor-agent'
$version = Get-ChildItem -LiteralPath (Join-Path $installRoot 'versions') -Directory |
    Where-Object { $_.Name -match '^\d{4}\.\d{2}\.\d{2}(-\d{2}-\d{2}-\d{2})?-[a-f0-9]+$' } |
    Sort-Object Name -Descending | Select-Object -First 1
if (-not $version) { throw 'Install the official native Windows Cursor CLI first.' }
$node = Join-Path $version.FullName 'node.exe'
& $node --disable-warning=ExperimentalWarning (Join-Path $PSScriptRoot 'launcher.cjs') @args
exit $LASTEXITCODE
