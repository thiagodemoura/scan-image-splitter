package imageio

import (
	"math"
	"os"

	"gocv.io/x/gocv"
)

// OpenCV IMWRITE_TIFF_* parameter ids and libtiff values (not exported by GoCV).
const (
	tiffResUnit     = 256
	tiffXDPI        = 257
	tiffYDPI        = 258
	tiffCompression = 259
	tiffPredictor   = 317

	compressionLZW      = 5
	predictorHorizontal = 2
	resUnitInch         = 2
)

// WriteTIFF saves m as a lossless LZW-compressed TIFF, recording dpi when > 0.
func WriteTIFF(path string, m gocv.Mat, dpi float64) error {
	params := []int{tiffCompression, compressionLZW, tiffPredictor, predictorHorizontal}
	if dpi > 0 {
		d := int(math.Round(dpi))
		params = append(params, tiffResUnit, resUnitInch, tiffXDPI, d, tiffYDPI, d)
	}
	return encode(path, ".tif", m, params)
}

// WritePNG saves m as PNG (used for diagnostic masks).
func WritePNG(path string, m gocv.Mat) error {
	return encode(path, gocv.PNGFileExt, m, []int{gocv.IMWritePngCompression, 6})
}

// WriteJPEG saves m as JPEG (used for review previews only, never for crops).
func WriteJPEG(path string, m gocv.Mat, quality int) error {
	return encode(path, gocv.JPEGFileExt, m, []int{gocv.IMWriteJpegQuality, quality})
}

// encode goes through memory so non-ASCII output paths work on Windows.
func encode(path string, ext gocv.FileExt, m gocv.Mat, params []int) error {
	buf, err := gocv.IMEncodeWithParams(ext, m, params)
	if err != nil {
		return err
	}
	defer buf.Close()
	return os.WriteFile(path, buf.GetBytes(), 0o644)
}
