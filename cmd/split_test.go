package cmd

import (
	"bytes"
	"encoding/json"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"gocv.io/x/gocv"

	"image-splitter/internal/imageio"
	"image-splitter/internal/scantest"
)

// run executes the CLI with args after restoring every flag to its default.
func run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	for _, c := range []*pflag.FlagSet{splitCmd.Flags(), rootCmd.PersistentFlags()} {
		c.VisitAll(func(f *pflag.Flag) {
			f.Value.Set(f.DefValue)
			f.Changed = false
		})
	}
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	return out.String(), err
}

func writePage(t *testing.T, path string, photos ...scantest.Photo) {
	t.Helper()
	page := scantest.Page(photos)
	defer page.Close()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var err error
	switch filepath.Ext(path) {
	case ".jpg":
		err = scantest.WriteJPEG(path, page)
	case ".pdf":
		var jpg []byte
		if jpg, err = scantest.JPEGBytes(page); err == nil {
			err = scantest.WritePDF(path, [][]byte{jpg}, []image.Point{{page.Cols(), page.Rows()}}, 300)
		}
	default:
		t.Fatalf("unsupported %s", path)
	}
	if err != nil {
		t.Fatal(err)
	}
}

func tiffDPIAndCompression(t *testing.T, path string) (dpi, compression float64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	tags, err := imageio.ReadTIFFTags(f)
	if err != nil {
		t.Fatal(err)
	}
	return tags[282], tags[259]
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

var (
	twoPhotos = []scantest.Photo{
		{CX: 700, CY: 900, W: 1000, H: 700, Angle: 5},
		{CX: 1800, CY: 2400, W: 800, H: 1000, Angle: -7},
	}
	onePhoto = scantest.Photo{CX: 1200, CY: 1700, W: 1100, H: 800, Angle: 3}
)

func TestSplitEndToEnd(t *testing.T) {
	root := t.TempDir()
	in, out, dbg := filepath.Join(root, "scans"), filepath.Join(root, "photos"), filepath.Join(root, "debug")

	// Two-page TIFF at 300 DPI.
	p1, p2 := scantest.Page(twoPhotos), scantest.Page([]scantest.Photo{onePhoto})
	os.MkdirAll(in, 0o755)
	err := scantest.WriteTIFF(filepath.Join(in, "album.tif"), []gocv.Mat{p1, p2}, 300)
	p1.Close()
	p2.Close()
	if err != nil {
		t.Fatal(err)
	}
	// JPEG without a recorded resolution, in a subfolder.
	writePage(t, filepath.Join(in, "sub", "loose.jpg"), onePhoto)
	_, havePoppler := exec.LookPath("pdftoppm")
	if havePoppler == nil {
		writePage(t, filepath.Join(in, "digitalização.pdf"), twoPhotos...)
	}

	stdout, err := run(t, "split", "-i", in, "-o", out, "--debug-dir", dbg, "--pdf-dpi", "300", "--dpi", "200")
	if err != nil {
		t.Fatalf("split: %v\n%s", err, stdout)
	}

	want := map[string]float64{ // photo -> expected DPI
		"album_p001_photo01.tif": 300, "album_p001_photo02.tif": 300,
		"album_p002_photo01.tif": 300,
		"loose_p001_photo01.tif": 200, // --dpi fallback
	}
	if havePoppler == nil {
		want["digitaliza__o_p001_photo01.tif"] = 300
		want["digitaliza__o_p001_photo02.tif"] = 300
	} else {
		t.Log("pdftoppm not installed: PDF input not covered")
	}
	files := listDir(t, out)
	for name, wantDPI := range want {
		if !slices.Contains(files, name) {
			t.Errorf("missing output %s (have %v)", name, files)
			continue
		}
		dpi, compression := tiffDPIAndCompression(t, filepath.Join(out, name))
		if dpi != wantDPI || compression != 5 {
			t.Errorf("%s: dpi %v compression %v, want %v and LZW (5)", name, dpi, compression, wantDPI)
		}
	}
	if got := len(files) - 1; got != len(want) { // minus the review folder
		t.Errorf("got %d outputs, want %d: %v", got, len(want), files)
	}

	reviews := listDir(t, filepath.Join(out, "review"))
	pages := len(want) - 1 - map[bool]int{true: 1}[havePoppler == nil] // album p1, p2, loose (+ pdf)
	if len(reviews) != pages {
		t.Errorf("got review images %v, want %d", reviews, pages)
	}

	var report []pageReport
	data, err := os.ReadFile(filepath.Join(dbg, "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &report); err != nil {
		t.Fatal(err)
	}
	if len(report) != pages {
		t.Fatalf("report has %d pages, want %d", len(report), pages)
	}
	for _, p := range report {
		if p.Error != "" || p.Stats == nil || p.Review == "" {
			t.Errorf("page %s #%d: %+v", p.File, p.Page, p)
		}
		for _, ph := range p.Photos {
			if ph.Method == "" || ph.Width == 0 || filepath.Dir(ph.Output) != out {
				t.Errorf("photo %+v", ph)
			}
		}
	}
	if !slices.Contains(listDir(t, dbg), "album_p001_mask_final.png") {
		t.Error("missing debug masks")
	}
	if !strings.Contains(stdout, "Exported") {
		t.Errorf("summary missing from output:\n%s", stdout)
	}
}

func TestSplitNonRecursiveAndNoReview(t *testing.T) {
	root := t.TempDir()
	in, out := filepath.Join(root, "scans"), filepath.Join(root, "photos")
	writePage(t, filepath.Join(in, "top.jpg"), onePhoto)
	writePage(t, filepath.Join(in, "sub", "nested.jpg"), onePhoto)

	if stdout, err := run(t, "split", "-i", in, "-o", out, "--recursive=false", "--review=false"); err != nil {
		t.Fatalf("split: %v\n%s", err, stdout)
	}
	if files := listDir(t, out); !slices.Equal(files, []string{"top_p001_photo01.tif"}) {
		t.Errorf("outputs %v, want only the top-level scan and no review folder", files)
	}
}

func TestSplitIgnoresOutputInsideInput(t *testing.T) {
	in := t.TempDir()
	out := filepath.Join(in, "photos")
	writePage(t, filepath.Join(in, "scan.jpg"), onePhoto)
	for range 2 {
		if stdout, err := run(t, "split", "-i", in, "-o", out); err != nil {
			t.Fatalf("split: %v\n%s", err, stdout)
		}
	}
	if files := listDir(t, out); !slices.Equal(files, []string{"review", "scan_p001_photo01.tif"}) {
		t.Errorf("outputs %v: earlier results were processed as input", files)
	}
}

func TestSplitReportsBadFilesAndContinues(t *testing.T) {
	root := t.TempDir()
	in, out := filepath.Join(root, "scans"), filepath.Join(root, "photos")
	writePage(t, filepath.Join(in, "good.jpg"), onePhoto)
	os.WriteFile(filepath.Join(in, "broken.tif"), []byte("not a tiff"), 0o644)
	os.WriteFile(filepath.Join(in, "notes.txt"), []byte("ignored"), 0o644)

	_, err := run(t, "split", "-i", in, "-o", out)
	if err == nil || !strings.Contains(err.Error(), "1 error") {
		t.Fatalf("want one error, got %v", err)
	}
	if !slices.Contains(listDir(t, out), "good_p001_photo01.tif") {
		t.Error("the good scan was not processed")
	}
}

func TestSplitEmptyPage(t *testing.T) {
	root := t.TempDir()
	in, out := filepath.Join(root, "scans"), filepath.Join(root, "photos")
	writePage(t, filepath.Join(in, "blank.jpg"))
	stdout, err := run(t, "split", "-i", in, "-o", out)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "No photos detected") {
		t.Errorf("output does not report the empty page:\n%s", stdout)
	}
}

func TestSplitValidation(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "file.jpg")
	os.WriteFile(file, nil, 0o644)
	empty := filepath.Join(root, "empty")
	os.MkdirAll(empty, 0o755)
	out := filepath.Join(root, "out")

	for _, c := range []struct {
		args []string
		want string
	}{
		{[]string{"split", "-o", out}, `required flag(s) "input"`},
		{[]string{"split", "-i", empty}, `required flag(s) "output"`},
		{[]string{"split", "-i", empty, "-o", out, "--threshold", "0"}, "threshold"},
		{[]string{"split", "-i", empty, "-o", out, "--threshold", "300"}, "threshold"},
		{[]string{"split", "-i", empty, "-o", out, "--max-page-fraction", "1.5"}, "max-page-fraction"},
		{[]string{"split", "-i", empty, "-o", out, "--min-fill", "-1"}, "min-fill"},
		{[]string{"split", "-i", empty, "-o", out, "--pdf-dpi", "50"}, "pdf-dpi"},
		{[]string{"split", "-i", empty, "-o", out, "--padding", "-2"}, "padding"},
		{[]string{"split", "-i", filepath.Join(root, "missing"), "-o", out}, "input"},
		{[]string{"split", "-i", file, "-o", out}, "must be a directory"},
		{[]string{"split", "-i", empty, "-o", out}, "no TIFF, JPEG, PNG or PDF files"},
	} {
		_, err := run(t, c.args...)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%v: got %v, want an error containing %q", c.args[1:], err, c.want)
		}
	}
}

func TestVersion(t *testing.T) {
	out, err := run(t, "version")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "image-splitter "+version) || !strings.Contains(out, "OpenCV "+gocv.OpenCVVersion()) {
		t.Errorf("version output:\n%s", out)
	}
	if out, err = run(t, "--version"); err != nil || !strings.Contains(out, version) {
		t.Errorf("--version: %v\n%s", err, out)
	}
}

func TestSafeName(t *testing.T) {
	for in, want := range map[string]string{
		"album 1": "album_1", "digitalização": "digitaliza__o", "a-b_c": "a-b_c", "": "scan",
	} {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}
