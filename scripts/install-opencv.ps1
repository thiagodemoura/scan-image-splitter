# Installs the native OpenCV libraries required by GoCV on Windows, if missing.
#
# The OpenCV version is taken from the GoCV module (go.mod, or latest if the
# project does not depend on GoCV yet), so the two always match. OpenCV is
# built from source with MinGW into C:\opencv, the path GoCV's cgo flags expect.
# A full build takes 30-90 minutes.
#
# Usage:
#   .\scripts\install-opencv.ps1           # install only if needed
#   .\scripts\install-opencv.ps1 -Force    # rebuild even if already installed
param(
    [switch]$Force
)

$ErrorActionPreference = "Stop"
$installDir = "C:\opencv"
$binDir = "$installDir\build\install\x64\mingw\bin"

function Update-SessionPath {
    $env:PATH = [Environment]::GetEnvironmentVariable("PATH", "Machine") + ";" +
                [Environment]::GetEnvironmentVariable("PATH", "User")
}

function Test-Command($name) {
    [bool](Get-Command $name -ErrorAction SilentlyContinue)
}

# Resolve the GoCV version used by this project and the OpenCV version it pins.
Push-Location (Split-Path $PSScriptRoot -Parent)
try {
    # Not a dependency yet -> go prints to stderr, which "Stop" would make fatal.
    $ErrorActionPreference = "Continue"
    $gocvVersion = go list -m -f "{{.Version}}" gocv.io/x/gocv 2>$null
    $ErrorActionPreference = "Stop"
    if ($LASTEXITCODE -ne 0 -or -not $gocvVersion) { $gocvVersion = "latest" }
    $module = go mod download -json "gocv.io/x/gocv@$gocvVersion" | ConvertFrom-Json
    if ($LASTEXITCODE -ne 0) { throw "could not download gocv.io/x/gocv@$gocvVersion" }
}
finally {
    Pop-Location
}

$gocvDir = $module.Dir
$match = Select-String -Path "$gocvDir\Makefile" -Pattern '^OPENCV_VERSION\?=(\S+)'
if (-not $match) { throw "could not determine OpenCV version from $gocvDir\Makefile" }
$opencvVersion = $match.Matches[0].Groups[1].Value
$libSuffix = $opencvVersion -replace '\.', ''

Write-Host "GoCV $($module.Version) requires OpenCV $opencvVersion"

$coreDll = "$binDir\libopencv_core$libSuffix.dll"
if ((Test-Path $coreDll) -and -not $Force) {
    Write-Host "OpenCV $opencvVersion is already installed in $installDir"
}
else {
    # Toolchain: MinGW (gcc/g++/mingw32-make) and CMake.
    $missing = @()
    if (-not ((Test-Command gcc) -and (Test-Command g++) -and (Test-Command mingw32-make))) { $missing += "mingw" }
    if (-not (Test-Command cmake)) { $missing += "cmake" }
    if ($missing) {
        if (-not (Test-Command choco)) {
            throw "Missing $($missing -join ', '). Install Chocolatey (https://chocolatey.org) or install them manually and re-run."
        }
        $admin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
            [Security.Principal.WindowsBuiltInRole]::Administrator)
        if (-not $admin) {
            throw "Missing $($missing -join ', '). Re-run this script from an elevated PowerShell so Chocolatey can install them."
        }
        $chocoArgs = @("install", "-y") + $missing
        if ($missing -contains "cmake") { $chocoArgs += "--installargs", "'ADD_CMAKE_TO_PATH=System'" }
        choco @chocoArgs
        if ($LASTEXITCODE -ne 0) { throw "choco install failed" }
        Update-SessionPath
    }

    # Sources: skip the ~150MB download if already extracted.
    if (-not ((Test-Path "$installDir\opencv-$opencvVersion") -and (Test-Path "$installDir\opencv_contrib-$opencvVersion"))) {
        & "$gocvDir\win_download_opencv.cmd" $installDir
        if (-not (Test-Path "$installDir\opencv-$opencvVersion")) { throw "OpenCV source download failed" }
    }

    Write-Host "Building OpenCV $opencvVersion (this takes a while)..."
    & "$gocvDir\win_build_opencv.cmd" $installDir
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $coreDll)) { throw "OpenCV build failed" }
}

# The DLLs must be on PATH for GoCV programs to run.
$userPath = [Environment]::GetEnvironmentVariable("PATH", "User")
if (($userPath -split ";") -notcontains $binDir) {
    [Environment]::SetEnvironmentVariable("PATH", "$userPath;$binDir", "User")
    Write-Host "Added $binDir to the user PATH (restart your terminal to pick it up)"
}
if (($env:PATH -split ";") -notcontains $binDir) { $env:PATH += ";$binDir" }

Write-Host "Verifying..."
Push-Location $gocvDir
try {
    $env:CGO_ENABLED = "1"
    go run ./cmd/version/main.go
    if ($LASTEXITCODE -ne 0) { throw "GoCV verification failed" }
}
finally {
    Pop-Location
}
