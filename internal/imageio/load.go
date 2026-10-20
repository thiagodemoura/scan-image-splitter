// Package imageio loads scanned pages (TIFF, JPEG, PNG, PDF) into OpenCV
// matrices and writes lossless results.
package imageio

import (
	"fmt"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"gocv.io/x/gocv"
)

// Page is one decoded page. The caller owns Mat and must Close it.
type Page struct {
	Mat    gocv.Mat
	Number int     // 1-based page number within the source file
	DPI    float64 // 0 when the source does not record a resolution
}

// readFlags keeps the source bit depth (8/16-bit) and channel count (gray/BGR/BGRA)
// so crops are exported without any loss.
const readFlags = gocv.IMReadAnyDepth | gocv.IMReadAnyColor

// Supported reports whether path has an extension Pages can read.
func Supported(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tif", ".tiff", ".jpg", ".jpeg", ".png", ".pdf":
		return true
	}
	return false
}

// Pages yields every page of a TIFF (multi-page aware), JPEG, PNG or PDF file.
// PDF pages are rasterized losslessly at pdfDPI with Poppler's pdftoppm.
func Pages(path string, pdfDPI int) iter.Seq2[Page, error] {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tif", ".tiff":
		return tiffPages(path)
	case ".pdf":
		return pdfPages(path, pdfDPI)
	default:
		return func(yield func(Page, error) bool) {
			m, err := decodeFile(path)
			if err != nil {
				yield(Page{}, err)
				return
			}
			yield(Page{Mat: m, Number: 1, DPI: readDPI(path)}, nil)
		}
	}
}

func tiffPages(path string) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		// OpenCV cannot open non-ASCII paths on Windows; read through an ASCII temp copy.
		readPath, cleanup, err := asciiPath(path)
		if err != nil {
			yield(Page{}, err)
			return
		}
		defer cleanup()
		dpi := readDPI(path)
		for i := 0; ; i++ {
			mats := gocv.IMReadMulti_WithParams(readPath, i, 1, readFlags)
			if len(mats) == 0 || mats[0].Empty() {
				for _, m := range mats {
					m.Close()
				}
				if i == 0 {
					yield(Page{}, fmt.Errorf("cannot decode TIFF %s", path))
				}
				return
			}
			for _, m := range mats[1:] {
				m.Close()
			}
			if !yield(Page{Mat: mats[0], Number: i + 1, DPI: dpi}, nil) {
				return
			}
		}
	}
}

func pdfPages(path string, dpi int) iter.Seq2[Page, error] {
	return func(yield func(Page, error) bool) {
		if _, err := exec.LookPath("pdftoppm"); err != nil {
			yield(Page{}, fmt.Errorf("PDF input requires Poppler's pdftoppm on PATH"))
			return
		}
		tmp, err := os.MkdirTemp("", "image-splitter-pdf-*")
		if err != nil {
			yield(Page{}, err)
			return
		}
		defer os.RemoveAll(tmp)

		// PNG keeps the rasterized pages lossless.
		cmd := exec.Command("pdftoppm", "-r", fmt.Sprint(dpi), "-png", path, filepath.Join(tmp, "page"))
		if out, err := cmd.CombinedOutput(); err != nil {
			yield(Page{}, fmt.Errorf("pdftoppm: %w: %s", err, strings.TrimSpace(string(out))))
			return
		}
		names, err := filepath.Glob(filepath.Join(tmp, "page*.png"))
		if err != nil {
			yield(Page{}, err)
			return
		}
		sort.Strings(names) // pdftoppm zero-pads page numbers
		for i, name := range names {
			m, err := decodeFile(name)
			if !yield(Page{Mat: m, Number: i + 1, DPI: float64(dpi)}, err) || err != nil {
				return
			}
		}
	}
}

func decodeFile(path string) (gocv.Mat, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return gocv.Mat{}, err
	}
	m, err := gocv.IMDecode(data, readFlags)
	if err == nil && m.Empty() {
		m.Close()
		err = fmt.Errorf("cannot decode %s", path)
	}
	return m, err
}

func asciiPath(path string) (string, func(), error) {
	isASCII := true
	for _, r := range path {
		if r > 127 {
			isASCII = false
			break
		}
	}
	if isASCII {
		return path, func() {}, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	f, err := os.CreateTemp("", "image-splitter-*"+filepath.Ext(path))
	if err != nil {
		return "", nil, err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", nil, err
	}
	return f.Name(), func() { os.Remove(f.Name()) }, nil
}
