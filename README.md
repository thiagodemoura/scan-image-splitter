# image-splitter

A Go CLI that finds photographs placed freely on scanned pages and exports each one as its own lossless TIFF. It reads TIFF (including multi-page), JPEG, PNG and PDF scans. Photos can be in any position, size and rotation. Detection and geometry use OpenCV through [GoCV](https://gocv.io).

## How it works

| Stage | What happens |
|---|---|
| Input | Every page of every `.tif/.tiff` (multi-page), `.jpg/.jpeg`, `.png` and `.pdf` in the input folder. The bit depth (8/16-bit) and channels (gray/colour) are kept. PDF pages are rasterized losslessly (PNG) with Poppler's `pdftoppm`. |
| Pre-processing | Detection runs on a downscaled copy of the page: median denoising, scanner-bed colour estimated from the page border, and flattening of uneven illumination. |
| Detection | Three masks are combined: a fixed colour-distance threshold, Otsu's threshold (only when it clearly separates photos from the bed), and Canny edges on lightness and colour (these find white-bordered and pale photos). The result is cleaned with closing, hole filling and opening sized to the page, so it behaves the same at any DPI. |
| Separation | Small closing keeps photos about 1 mm apart separate. Photos that touch are split with a distance transform. Duplicate detections are suppressed, and nearby photos are never merged. |
| Geometry | Each photo gets a robust rotated rectangle. It is deskewed by rotation, or by a four-corner perspective warp when the corners are reliable and visibly off square. Rotations under `--min-angle` are cropped directly, without resampling. |
| Export | One LZW-compressed TIFF per photo (lossless, with the source DPI kept), plus a review image per page with the numbered detections. With `--debug-dir`, the segmentation masks and a JSON report are written too. |

Photos are numbered top to bottom, then left to right (row by row).

**Scanning tips:** scan at 600 DPI into TIFF if your scanner allows it. The exported crops are lossless TIFF, so they keep everything the scan has. A scan that is already JPEG cannot regain the detail its compression lost, even after conversion to TIFF.

## Requirements

- Go 1.26.4 or newer (only for building).
- OpenCV 4.13.0, the version pinned by GoCV v0.43.0.
- Poppler's `pdftoppm` on `PATH`, for PDF input only.

### OpenCV

These scripts install OpenCV only if it is missing or the wrong version. They then verify the install with GoCV's version check. Building OpenCV from source takes 30–90 minutes. Pass `-Force` / `--force` to reinstall.

```powershell
# Windows: builds OpenCV with MinGW into C:\opencv and adds its bin folder to the user PATH.
# Run from an elevated PowerShell if MinGW/CMake still have to be installed (via Chocolatey).
.\scripts\install-opencv.ps1
```

```sh
# Linux: builds from source with GoCV's Makefile (uses sudo). macOS: Homebrew.
./scripts/install-opencv.sh
```

### Poppler (PDF only)

- Windows: install a Poppler for Windows build and add its `Library\bin` folder to `PATH`.
- macOS: `brew install poppler`
- Debian/Ubuntu: `sudo apt install poppler-utils`

Check with `pdftoppm -v`.

## Build

```powershell
go build -o image-splitter.exe .
```

Release packages go to `dist/`:

```powershell
.\build.ps1                         # windows/amd64 and linux/amd64; version from git describe
.\build.ps1 -Targets linux/amd64    # only Linux
.\build.ps1 -Version v1.2.0         # explicit version
```

- **windows/amd64** builds natively and produces a zip with the exe plus the OpenCV and MinGW DLLs it uses, so it runs on PCs without OpenCV installed.
- **linux/amd64** builds in the `gocv/opencv` container (Podman or Docker). The target machine needs OpenCV 4.13.0.
- **macOS:** run `./scripts/install-opencv.sh` and `go build` on a Mac. GoCV uses cgo, so it can't be cross-compiled.

`image-splitter version` prints the program, GoCV and OpenCV versions.

## Usage

```powershell
# Process every scan in a folder (subfolders included)
.\image-splitter.exe split -i .\scans -o .\photos

# Also write segmentation masks and report.json
.\image-splitter.exe split -i .\scans -o .\photos --debug-dir .\debug

# More sensitive detection, 10 px margin around each photo
.\image-splitter.exe split -i .\scans -o .\photos --threshold 20 --padding 10
```

Output for `scans\album.tif`, page 1:

```
photos\album_p001_photo01.tif     photo 1 (LZW TIFF)
photos\album_p001_photo02.tif     photo 2
photos\review\album_p001.jpg      page preview with the numbered detections
debug\album_p001_mask_*.png       fixed, otsu, edges, combined and final masks
debug\report.json                 every page, its statistics and detections
```

Review image colours: green means cropped as is, blue means deskewed by rotation, orange means perspective corrected.

### Options

| Flag | Default | Meaning |
|---|---|---|
| `-i, --input` | (required) | Folder with the scans |
| `-o, --output` | (required) | Folder for the photos. Skipped when it is inside the input folder. |
| `-r, --recursive` | `true` | Include subfolders |
| `--review` | `true` | Write review images to `<output>/review` |
| `--debug-dir` | | Write masks and `report.json` |
| `--threshold` | `28` | Colour distance from the scanner bed counted as photo (0–255). Lower is more sensitive. |
| `--min-area` | `40000` | Minimum photo area, in page pixels |
| `--min-width`, `--min-height` | `150` | Minimum photo size, in page pixels, in either orientation |
| `--max-page-fraction` | `0.95` | Reject regions covering more of the page than this |
| `--min-fill` | `0.6` | Reject regions that fill less of their rotated box than this |
| `--work-size` | `2000` | Long side of the downscaled image used for detection |
| `--edges` | `true` | Edge detection, needed for white-bordered and pale photos |
| `--split-touching` | `true` | Separate photos that touch |
| `--deskew` | `true` | Straighten rotated photos |
| `--perspective` | `true` | Correct perspective when four reliable corners are found |
| `--min-angle` | `0.3` | Rotations below this (degrees) are cropped without resampling |
| `--padding` | `0` | Extra page pixels around each photo |
| `--pdf-dpi` | `600` | Resolution for rasterizing PDF pages |
| `--dpi` | `0` | DPI written to output TIFFs when the source records none (0 leaves it unset) |

## Tests

The tests generate synthetic A4 scans with uneven lighting and noise. The scans include rotated, white-bordered, pale-edged, touching, nearby and perspective-distorted photos. The tests run the full pipeline on TIFF, JPEG and PDF input. They need OpenCV, and Poppler for the PDF cases (otherwise those are skipped). Without a local install, run them in a container:

```sh
podman run --rm -v "$PWD:/src" -w /src -e GOTOOLCHAIN=auto docker.io/gocv/opencv:4.13.0 \
  sh -c 'apt-get update -qq && apt-get install -y -qq poppler-utils >/dev/null && go test -cover ./...'
```

## Limitations

- The scanner bed is assumed to be fairly uniform and visible along the page border. Dark or textured backgrounds need a different `--threshold`.
- Photos overlapping each other cannot be separated.
- A photo whose edge has the same colour as the bed and casts no shadow can be cropped short on that side. Use `--padding`, and check the review images.
- Perspective correction needs four clear corners. Otherwise the photo is only deskewed.

Always check the review images before deleting the original scans.
