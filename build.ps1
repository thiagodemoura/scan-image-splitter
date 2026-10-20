# Builds image-splitter release packages into ./dist.
#
# GoCV links OpenCV through cgo, so binaries cannot be cross-compiled from one
# machine like plain Go:
#   windows/amd64  built natively. Needs OpenCV in C:\opencv and MinGW on PATH
#                  (scripts\install-opencv.ps1). The exe is zipped together with
#                  the OpenCV and MinGW DLLs it uses, so it runs on PCs without
#                  OpenCV installed.
#   linux/amd64    built in the gocv/opencv container (Podman or Docker). The
#                  binary needs OpenCV installed on the target (scripts/install-opencv.sh).
#   macOS          build on a Mac: ./scripts/install-opencv.sh, then go build.
#
# Usage:
#   .\build.ps1                            # all targets, version from git describe
#   .\build.ps1 -Targets linux/amd64       # selected targets
#   .\build.ps1 -Version v1.2.0            # explicit version
param(
    [ValidateSet("windows/amd64", "linux/amd64")]
    [string[]]$Targets = @("windows/amd64", "linux/amd64"),
    [string]$Version,
    [string]$OutDir = "dist"
)

$ErrorActionPreference = "Stop"
$name = "image-splitter"
$root = $PSScriptRoot

function Invoke-Native {
    param([scriptblock]$Command, [string]$What)
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "$What failed (exit code $LASTEXITCODE)" }
}

if (-not $Version) {
    $ErrorActionPreference = "Continue" # git writes to stderr when there are no tags
    $Version = git -C $root describe --tags --always --dirty 2>$null
    $ErrorActionPreference = "Stop"
    if ($LASTEXITCODE -ne 0 -or -not $Version) { $Version = "dev" }
}
$ldflags = "-s -w -X $name/cmd.version=$Version"

# OpenCV version pinned by the GoCV module in go.mod.
Push-Location $root
try {
    $gocvDir = go list -m -f "{{.Dir}}" gocv.io/x/gocv
    if ($LASTEXITCODE -ne 0) { Invoke-Native { go mod download gocv.io/x/gocv } "go mod download"; $gocvDir = go list -m -f "{{.Dir}}" gocv.io/x/gocv }
}
finally { Pop-Location }
$opencvVersion = (Select-String -Path "$gocvDir\Makefile" -Pattern '^OPENCV_VERSION\?=(\S+)').Matches[0].Groups[1].Value

$dist = Join-Path $root $OutDir
New-Item -ItemType Directory -Force $dist | Out-Null
Write-Host "Building $name $Version (OpenCV $opencvVersion)"

# Recursively resolves the DLLs an executable needs from the given folders;
# system DLLs (not found there) are left out.
function Get-DllDependencies {
    param([string]$Binary, [string[]]$SearchDirs)
    $found = @{}
    $queue = [System.Collections.Generic.Queue[string]]::new()
    $queue.Enqueue($Binary)
    while ($queue.Count -gt 0) {
        $file = $queue.Dequeue()
        $imports = objdump -p $file | Select-String -Pattern 'DLL Name:\s*(\S+)' | ForEach-Object { $_.Matches[0].Groups[1].Value }
        foreach ($dll in $imports) {
            if ($found.ContainsKey($dll.ToLower())) { continue }
            foreach ($dir in $SearchDirs) {
                $path = Join-Path $dir $dll
                if (Test-Path $path) {
                    $found[$dll.ToLower()] = $path
                    $queue.Enqueue($path)
                    break
                }
            }
        }
    }
    $found.Values
}

function Build-Windows {
    $opencvBin = "C:\opencv\build\install\x64\mingw\bin"
    if (-not (Test-Path "$opencvBin\libopencv_core$($opencvVersion -replace '\.', '').dll")) {
        throw "OpenCV $opencvVersion not found in C:\opencv. Run scripts\install-opencv.ps1 first."
    }
    $gcc = Get-Command gcc -ErrorAction SilentlyContinue
    if (-not $gcc -or -not (Get-Command objdump -ErrorAction SilentlyContinue)) {
        throw "MinGW (gcc, objdump) must be on PATH. Run scripts\install-opencv.ps1 first."
    }

    $pkg = Join-Path $dist "$name-$Version-windows-amd64"
    if (Test-Path $pkg) { Remove-Item -Recurse -Force $pkg }
    New-Item -ItemType Directory $pkg | Out-Null
    $exe = Join-Path $pkg "$name.exe"

    $saved = @{ GOOS = $env:GOOS; GOARCH = $env:GOARCH; CGO_ENABLED = $env:CGO_ENABLED }
    try {
        $env:GOOS, $env:GOARCH, $env:CGO_ENABLED = "windows", "amd64", "1"
        Push-Location $root
        try { Invoke-Native { go build -trimpath -ldflags $ldflags -o $exe . } "windows/amd64 build" }
        finally { Pop-Location }
    }
    finally {
        foreach ($k in $saved.Keys) { Set-Item "env:$k" $saved[$k] }
    }

    $dlls = Get-DllDependencies $exe @($opencvBin, (Split-Path $gcc.Source))
    $dlls | ForEach-Object { Copy-Item $_ $pkg }
    Copy-Item (Join-Path $root "README.md") $pkg

    $zip = "$pkg.zip"
    if (Test-Path $zip) { Remove-Item -Force $zip }
    Compress-Archive -Path "$pkg\*" -DestinationPath $zip
    Write-Host "  -> $zip ($($dlls.Count) DLLs bundled)"
}

function Build-Linux {
    $engine = @("podman", "docker") | Where-Object { Get-Command $_ -ErrorAction SilentlyContinue } | Select-Object -First 1
    if (-not $engine) { throw "linux/amd64 builds need Podman or Docker" }
    $out = "$name-$Version-linux-amd64"
    $src = $root -replace '\\', '/'
    Invoke-Native {
        & $engine run --rm `
            -v "${src}:/src" -w /src `
            -v "$name-gomod:/go/pkg/mod" -v "$name-gocache:/root/.cache/go-build" `
            -e GOTOOLCHAIN=auto -e GOFLAGS=-buildvcs=false `
            "docker.io/gocv/opencv:$opencvVersion" `
            go build -trimpath -ldflags $ldflags -o "/src/$OutDir/$out" .
    } "linux/amd64 build"
    Write-Host "  -> $(Join-Path $dist $out) (needs OpenCV $opencvVersion on the target)"
}

foreach ($target in $Targets) {
    Write-Host "[$target]"
    switch ($target) {
        "windows/amd64" { Build-Windows }
        "linux/amd64" { Build-Linux }
    }
}
