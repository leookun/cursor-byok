@echo off
setlocal
cd /d "%~dp0"
powershell -NoProfile -ExecutionPolicy Bypass -Command "$env:Path = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' + [System.Environment]::GetEnvironmentVariable('Path','User') + ';$HOME\go\bin'; task build; if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; $version = (go run ./scripts/release version -config ./build/config.yml).Trim(); if ($LASTEXITCODE -ne 0) { exit $LASTEXITCODE }; $source = Join-Path $PWD 'bin\windows-64.exe'; if (-not (Test-Path -LiteralPath $source)) { Write-Error ('Build completed but output was not found: ' + $source); exit 1 }; $directory = Split-Path -Parent $source; $baseName = 'cursor-byok-' + $version; $destination = Join-Path $directory ($baseName + '.exe'); $index = 1; while (Test-Path -LiteralPath $destination) { $destination = Join-Path $directory ($baseName + '(' + $index + ').exe'); $index++ }; Copy-Item -LiteralPath $source -Destination $destination; Write-Host ('Created: ' + $destination)"
set "EXIT_CODE=%ERRORLEVEL%"
pause
exit /b %EXIT_CODE%