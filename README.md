# image-splitter-agnostic

A Go CLI that scans a folder for TIFF, JPEG, and PDF documents and exports detected photo regions as JPEG crops. The image segmentation engine is format-independent once a page is decoded to Go's `image.Image` interface.

## Supported inputs

- `.jpg`, `.jpeg`: decoded with Go's standard library.
- `.tif`, `.tiff`: decoded with `golang.org/x/image/tiff`.
- `.pdf`: every page is rasterized to JPEG with Poppler's `pdftoppm` command. Install Poppler and ensure `pdftoppm` is on `PATH`.

## Build

Requires Go 1.26.4 or newer.

```powershell
go mod tidy
go build -o image-splitter.exe .
```

Cross-platform binaries (Windows, Linux, macOS; amd64 and arm64) are written to `dist/`:

```powershell
.\build.ps1
.\build.ps1 -Targets linux/amd64,darwin/arm64 -Version 1.0.0
```

### OpenCV (GoCV)

GoCV needs the native OpenCV libraries, matching the version pinned by the GoCV module. These scripts install them only if they are missing or the wrong version, then verify with GoCV's version check:

```powershell
# Windows (elevated PowerShell if MinGW/CMake must be installed via Chocolatey).
# Builds OpenCV with MinGW into C:\opencv and adds its bin dir to the user PATH.
.\scripts\install-opencv.ps1
```

```sh
# Linux: builds from source via GoCV's Makefile (uses sudo). macOS: Homebrew.
./scripts/install-opencv.sh
```

Building from source takes 30–90 minutes. Pass `-Force` / `--force` to reinstall.

PDF support additionally requires Poppler:
- Windows: install a Poppler for Windows distribution and add its `Library/bin` (or equivalent `bin`) directory to `PATH`.
- macOS: `brew install poppler`
- Debian/Ubuntu: `sudo apt install poppler-utils`

Check it is available with `pdftoppm -v`.

## Usage

```powershell
# Process TIFF/JPEG/PDF files in a folder
.\image-splitter.exe split --input .\scans --output .\photos

# Include diagnostics masks and increase raster resolution for PDF input
.\image-splitter.exe split -i .\scans -o .\photos --pdf-dpi 250 --debug-dir .\debug

# Tune the segmentation
.\image-splitter.exe split -i .\scans -o .\photos --threshold 30 --min-area 18000 --padding 12

# Show available flags
.\image-splitter.exe split --help
```

Options:

- `--threshold`: RGB distance from estimated border background needed to count as foreground.
- `--background-tolerance`: currently contributes a small adjustment to the foreground threshold.
- `--min-area`, `--min-width`, `--min-height`: discard small components.
- `--padding`: add pixels around crop boundaries.
- `--pdf-dpi`: rasterization resolution for PDF pages (72–600).
- `--debug-dir`: write grayscale segmentation masks for review.
- `--recursive`: walk subdirectories (true by default).

## Limitations

This is a practical baseline, not a production-grade computer-vision model. It estimates the background from page edges and uses connected components. It works best for photos with visible contrast against a light scanner bed. Dark pages, shadows, textured backgrounds, borderless photos, and photos touching/overlapping can lead to missed or merged regions. The algorithm does not yet use perspective correction, watershed splitting, ML detection, or human review UI. Multi-page TIFF decoding depends on the decoder and this baseline reads the first decoded image only; PDF is explicitly processed page by page. Output is JPEG, so crops are recompressed.

Always inspect output and debug masks before deleting source scans.
