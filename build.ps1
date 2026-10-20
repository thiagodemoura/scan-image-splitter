# Cross-compiles image-splitter for common platforms into ./dist.
# Usage:
#   .\build.ps1                          # build all targets
#   .\build.ps1 -Targets linux/amd64     # build selected targets
#   .\build.ps1 -Version 1.2.0           # name archives/binaries with a version
param(
    [string[]]$Targets = @(
        "windows/amd64",
        "windows/arm64",
        "linux/amd64",
        "linux/arm64",
        "darwin/amd64",
        "darwin/arm64"
    ),
    [string]$Version = "dev",
    [string]$OutDir = "dist"
)

$ErrorActionPreference = "Stop"
$name = "image-splitter"

New-Item -ItemType Directory -Force $OutDir | Out-Null

$prev = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
try {
    foreach ($target in $Targets) {
        $goos, $goarch = $target.Split("/")
        $ext = if ($goos -eq "windows") { ".exe" } else { "" }
        $out = Join-Path $OutDir "$name-$Version-$goos-$goarch$ext"

        Write-Host "Building $out"
        $env:GOOS = $goos
        $env:GOARCH = $goarch
        $env:CGO_ENABLED = "0"
        go build -trimpath -ldflags "-s -w" -o $out .
        if ($LASTEXITCODE -ne 0) { throw "build failed for $target" }
    }
}
finally {
    foreach ($k in $prev.Keys) { Set-Item "env:$k" $prev[$k] -ErrorAction SilentlyContinue }
}
