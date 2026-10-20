package imageio

import (
	"bytes"
	"image"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"gocv.io/x/gocv"

	"image-splitter/internal/scantest"
)

func collect(t *testing.T, path string, dpi int) []Page {
	t.Helper()
	var pages []Page
	for p, err := range Pages(path, dpi) {
		if err != nil {
			t.Fatal(err)
		}
		pages = append(pages, p)
	}
	t.Cleanup(func() {
		for _, p := range pages {
			p.Mat.Close()
		}
	})
	return pages
}

func solid(w, h int, mt gocv.MatType) gocv.Mat {
	return gocv.NewMatWithSizeFromScalar(gocv.NewScalar(40, 120, 200, 0), h, w, mt)
}

func TestSupported(t *testing.T) {
	for path, want := range map[string]bool{
		"a.tif": true, "a.TIFF": true, "a.jpg": true, "a.JPEG": true, "a.png": true, "a.pdf": true,
		"a.bmp": false, "a": false, "a.tif.txt": false,
	} {
		if Supported(path) != want {
			t.Errorf("Supported(%q) = %v", path, !want)
		}
	}
}

func TestMultiPageTIFF(t *testing.T) {
	// Non-ASCII name: OpenCV cannot open such paths directly on Windows.
	path := filepath.Join(t.TempDir(), "digitalização.tif")
	gray, color := solid(30, 20, gocv.MatTypeCV8UC1), solid(40, 10, gocv.MatTypeCV8UC3)
	defer gray.Close()
	defer color.Close()
	if err := scantest.WriteTIFF(path, []gocv.Mat{gray, color, gray}, 600); err != nil {
		t.Fatal(err)
	}

	pages := collect(t, path, 0)
	want := []struct{ w, h, ch int }{{30, 20, 1}, {40, 10, 3}, {30, 20, 1}}
	if len(pages) != len(want) {
		t.Fatalf("got %d pages, want %d", len(pages), len(want))
	}
	for i, p := range pages {
		w := want[i]
		if p.Number != i+1 || p.Mat.Cols() != w.w || p.Mat.Rows() != w.h || p.Mat.Channels() != w.ch || p.DPI != 600 {
			t.Errorf("page %d: number %d, %dx%d, %d channels, dpi %v", i, p.Number, p.Mat.Cols(), p.Mat.Rows(), p.Mat.Channels(), p.DPI)
		}
	}
	if !bytes.Equal(pages[1].Mat.ToBytes(), color.ToBytes()) {
		t.Error("colour page pixels differ")
	}
}

func TestWriteTIFFIsLosslessLZW(t *testing.T) {
	rng := rand.New(rand.NewPCG(3, 4))
	for _, mt := range []gocv.MatType{gocv.MatTypeCV8UC1, gocv.MatTypeCV8UC3, gocv.MatTypeCV16UC3} {
		size := 64 * 48 * map[gocv.MatType]int{gocv.MatTypeCV8UC1: 1, gocv.MatTypeCV8UC3: 3, gocv.MatTypeCV16UC3: 6}[mt]
		data := make([]byte, size)
		for i := range data {
			data[i] = byte(rng.IntN(256))
		}
		src, err := gocv.NewMatFromBytes(48, 64, mt, data)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(t.TempDir(), "crop.tif")
		if err := WriteTIFF(path, src, 600); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		tags, err := ReadTIFFTags(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		if tags[259] != compressionLZW || tags[282] != 600 || tags[296] != resUnitInch {
			t.Errorf("%v: compression %v, resolution %v, unit %v", mt, tags[259], tags[282], tags[296])
		}
		pages := collect(t, path, 0)
		if len(pages) != 1 || pages[0].Mat.Type() != mt || !bytes.Equal(pages[0].Mat.ToBytes(), src.ToBytes()) {
			t.Errorf("%v: pixels or type changed after the TIFF round trip", mt)
		}
		src.Close()
	}
}

func TestWriteTIFFWithoutDPI(t *testing.T) {
	m := solid(8, 8, gocv.MatTypeCV8UC3)
	defer m.Close()
	path := filepath.Join(t.TempDir(), "nodpi.tif")
	if err := WriteTIFF(path, m, 0); err != nil {
		t.Fatal(err)
	}
	if dpi := readDPI(path); dpi != 0 {
		t.Errorf("dpi %v, want 0 (unset)", dpi)
	}
}

func TestWritePNGAndJPEG(t *testing.T) {
	m := solid(32, 16, gocv.MatTypeCV8UC3)
	defer m.Close()
	dir := t.TempDir()
	for name, write := range map[string]func(string) error{
		"mask.png":    func(p string) error { return WritePNG(p, m) },
		"review.jpg":  func(p string) error { return WriteJPEG(p, m, 90) },
		"revisão.jpg": func(p string) error { return WriteJPEG(p, m, 90) },
	} {
		path := filepath.Join(dir, name)
		if err := write(path); err != nil {
			t.Fatal(err)
		}
		pages := collect(t, path, 0)
		if len(pages) != 1 || pages[0].Mat.Cols() != 32 || pages[0].Mat.Rows() != 16 {
			t.Errorf("%s: read back %d pages", name, len(pages))
		}
	}
}

func TestJPEGInput(t *testing.T) {
	m := solid(80, 50, gocv.MatTypeCV8UC3)
	defer m.Close()
	path := filepath.Join(t.TempDir(), "foto.jpeg")
	if err := scantest.WriteJPEG(path, m); err != nil {
		t.Fatal(err)
	}
	pages := collect(t, path, 0)
	if len(pages) != 1 || pages[0].Number != 1 || pages[0].Mat.Cols() != 80 || pages[0].Mat.Rows() != 50 {
		t.Fatalf("unexpected pages %+v", pages)
	}
}

func TestPDFInput(t *testing.T) {
	if _, err := exec.LookPath("pdftoppm"); err != nil {
		t.Skip("pdftoppm not installed")
	}
	m := solid(80, 50, gocv.MatTypeCV8UC3)
	defer m.Close()
	jpg, err := scantest.JPEGBytes(m)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "scan.pdf")
	sizes := []image.Point{{150, 150}, {300, 150}}
	if err := scantest.WritePDF(path, [][]byte{jpg, jpg}, sizes, 150); err != nil {
		t.Fatal(err)
	}
	pages := collect(t, path, 150)
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2", len(pages))
	}
	for i, p := range pages {
		if p.Number != i+1 || p.Mat.Cols() != sizes[i].X || p.Mat.Rows() != sizes[i].Y || p.DPI != 150 {
			t.Errorf("page %d: %dx%d at %v dpi, want %v at 150", p.Number, p.Mat.Cols(), p.Mat.Rows(), p.DPI, sizes[i])
		}
	}
}

func TestPDFWithoutPoppler(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, err := range Pages(filepath.Join(t.TempDir(), "scan.pdf"), 300) {
		if err == nil {
			t.Fatal("want an error without pdftoppm")
		}
		return
	}
	t.Fatal("no result")
}

func TestLoadErrors(t *testing.T) {
	dir := t.TempDir()
	garbage := []byte("not an image")
	for _, name := range []string{"bad.tif", "bad.jpg", "bad.pdf", "missing.png"} {
		path := filepath.Join(dir, name)
		if name != "missing.png" {
			os.WriteFile(path, garbage, 0o644)
		}
		if name == "bad.pdf" {
			if _, err := exec.LookPath("pdftoppm"); err != nil {
				continue
			}
		}
		gotErr := false
		for p, err := range Pages(path, 72) {
			if err != nil {
				gotErr = true
			} else {
				p.Mat.Close()
			}
		}
		if !gotErr {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestStopIterationEarly(t *testing.T) {
	m := solid(10, 10, gocv.MatTypeCV8UC1)
	defer m.Close()
	path := filepath.Join(t.TempDir(), "pages.tif")
	if err := scantest.WriteTIFF(path, []gocv.Mat{m, m, m}, 300); err != nil {
		t.Fatal(err)
	}
	n := 0
	for p := range Pages(path, 0) {
		p.Mat.Close()
		n++
		break
	}
	if n != 1 {
		t.Errorf("iterated %d pages after break", n)
	}
}

func TestDPIParsing(t *testing.T) {
	jfif := func(units byte, density uint16) []byte {
		return []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, units, byte(density >> 8), byte(density), byte(density >> 8), byte(density)}
	}
	for _, c := range []struct {
		data []byte
		want float64
	}{
		{jfif(1, 600), 600},
		{jfif(2, 100), 254},
		{jfif(0, 1), 0},
		{[]byte("short"), 0},
		{append([]byte{0xFF, 0xD8, 0xFF, 0xE1}, make([]byte, 20)...), 0}, // EXIF, not JFIF
	} {
		if got := jfifDPI(bytes.NewReader(c.data)); got != c.want {
			t.Errorf("jfifDPI(% x) = %v, want %v", c.data[:min(len(c.data), 16)], got, c.want)
		}
	}

	for _, c := range []struct{ res, unit, want float64 }{{300, 2, 300}, {300, 0, 300}, {100, 3, 254}, {72, 1, 0}} {
		if got := resolutionToDPI(c.res, c.unit); got != c.want {
			t.Errorf("resolutionToDPI(%v, %v) = %v, want %v", c.res, c.unit, got, c.want)
		}
	}

	for _, bad := range [][]byte{[]byte("XX*\x00\x08\x00\x00\x00"), []byte("II+\x00\x08\x00\x00\x00"), []byte("II")} {
		if _, err := ReadTIFFTags(bytes.NewReader(bad)); err == nil {
			t.Errorf("ReadTIFFTags(%q): want an error", bad)
		}
	}
	if readDPI(filepath.Join(t.TempDir(), "missing.tif")) != 0 {
		t.Error("missing file: want 0 dpi")
	}
}
