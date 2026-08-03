$env:Path = [System.Environment]::GetEnvironmentVariable("Path", "Machine") + ";" + [System.Environment]::GetEnvironmentVariable("Path", "User") + ";$HOME\go\bin"
$version = (go run ./scripts/release version -config ./build/config.yml).Trim()
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

$directory = Join-Path $PSScriptRoot "bin"
New-Item -ItemType Directory -Path $directory -Force | Out-Null
$baseName = "cursor-byok-$version"
$destination = Join-Path $directory "$baseName.exe"
$index = 1
while (Test-Path -LiteralPath $destination) {
    $destination = Join-Path $directory "$baseName($index).exe"
    $index++
}

task build:windows:binary OUTPUT=$destination
if ($LASTEXITCODE -ne 0) {
    exit $LASTEXITCODE
}

Write-Host "Created: $destination"