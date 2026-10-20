package cmd

import (
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	_ "golang.org/x/image/tiff"
	"tiffsplit-agnostic/internal/segment"
)

var (
	inputDir, outputDir, debugDir string
	recursive bool
	threshold, minArea, minWidth, minHeight, padding, pdfDPI int
	backgroundTolerance int
)

var splitCmd = &cobra.Command{
	Use:   "split",
	Short: "Segment scanned pages into photo crops",
	Example: "  tiffsplit split -i ./scans -o ./photos\n  tiffsplit split -i ./scans -o ./photos --pdf-dpi 220 --debug-dir ./debug",
	RunE: func(cmd *cobra.Command, args []string) error { return runSplit() },
}

func init() {
	rootCmd.AddCommand(splitCmd)
	f := splitCmd.Flags()
	f.StringVarP(&inputDir, "input", "i", "", "Input folder containing .tif, .tiff, .jpg, .jpeg, and .pdf files (required)")
	f.StringVarP(&outputDir, "output", "o", "", "Output folder for extracted JPEGs (required)")
	f.BoolVarP(&recursive, "recursive", "r", true, "Recursively scan input folder")
	f.IntVar(&threshold, "threshold", 35, "Foreground color-distance threshold (0-255); lower is more sensitive")
	f.IntVar(&backgroundTolerance, "background-tolerance", 24, "Tolerance around estimated page background (0-255)")
	f.IntVar(&minArea, "min-area", 12000, "Minimum detected region area in pixels")
	f.IntVar(&minWidth, "min-width", 80, "Minimum crop width in pixels")
	f.IntVar(&minHeight, "min-height", 80, "Minimum crop height in pixels")
	f.IntVar(&padding, "padding", 8, "Padding around each crop in pixels")
	f.IntVar(&pdfDPI, "pdf-dpi", 200, "DPI used to rasterize PDF pages (requires pdftoppm)")
	f.StringVar(&debugDir, "debug-dir", "", "Optional folder for page previews and segmentation masks")
	_ = splitCmd.MarkFlagRequired("input")
	_ = splitCmd.MarkFlagRequired("output")
}

func runSplit() error {
	if threshold < 0 || threshold > 255 || backgroundTolerance < 0 || backgroundTolerance > 255 {
		return fmt.Errorf("threshold and background-tolerance must be between 0 and 255")
	}
	if pdfDPI < 72 || pdfDPI > 600 { return fmt.Errorf("pdf-dpi must be between 72 and 600") }
	info, err := os.Stat(inputDir); if err != nil { return fmt.Errorf("input: %w", err) }; if !info.IsDir() { return fmt.Errorf("input must be a directory") }
	if err := os.MkdirAll(outputDir, 0755); err != nil { return err }
	if debugDir != "" { if err := os.MkdirAll(debugDir, 0755); err != nil { return err } }

	var files []string
	walkErr := filepath.WalkDir(inputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil { return err }; if d.IsDir() { if path != inputDir && !recursive { return filepath.SkipDir }; return nil }
		ext := strings.ToLower(filepath.Ext(path)); if ext == ".tif" || ext == ".tiff" || ext == ".jpg" || ext == ".jpeg" || ext == ".pdf" { files = append(files, path) }; return nil
	})
	if walkErr != nil { return walkErr }; sort.Strings(files)
	if len(files) == 0 { return fmt.Errorf("no TIFF, JPEG, or PDF files found in %s", inputDir) }

	failed := 0
	for _, path := range files {
		fmt.Printf("Processing %s\n", path)
		pages, cleanup, err := loadPages(path)
		if err != nil { fmt.Fprintf(os.Stderr, "  ERROR: %v\n", err); failed++; continue }
		for pageIndex, img := range pages {
			base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
			pageLabel := fmt.Sprintf("%s_p%03d", safeName(base), pageIndex+1)
			cfg := segment.Config{Threshold: threshold, BackgroundTolerance: backgroundTolerance, MinArea: minArea, MinWidth: minWidth, MinHeight: minHeight, Padding: padding}
			regions, mask := segment.Find(img, cfg)
			if debugDir != "" { _ = savePNG(filepath.Join(debugDir, pageLabel+"_mask.png"), mask) }
			for i, r := range regions {
				cropRect := r.Rect.Intersect(img.Bounds()); if cropRect.Empty() { continue }
				crop := img.(interface{ SubImage(image.Rectangle) image.Image }).SubImage(cropRect)
				out := filepath.Join(outputDir, fmt.Sprintf("%s_photo%03d.jpg", pageLabel, i+1))
				if err := saveJPEG(out, crop); err != nil { fmt.Fprintf(os.Stderr, "  ERROR writing %s: %v\n", out, err); failed++; continue }
				fmt.Printf("  -> %s (%dx%d)\n", out, cropRect.Dx(), cropRect.Dy())
			}
			if len(regions) == 0 { fmt.Printf("  No regions detected on page %d\n", pageIndex+1) }
		}
		cleanup()
	}
	if failed > 0 { return fmt.Errorf("finished with %d file or output error(s)", failed) }
	return nil
}

// JPEG and TIFF are decoded by Go's image decoders. PDFs use Poppler's pdftoppm.
func loadPages(path string) ([]image.Image, func(), error) {
	if strings.EqualFold(filepath.Ext(path), ".pdf") {
		if _, err := exec.LookPath("pdftoppm"); err != nil { return nil, func(){}, fmt.Errorf("PDF input requires Poppler pdftoppm in PATH; see README") }
		tmp, err := os.MkdirTemp("", "tiffsplit-pdf-*"); if err != nil { return nil, func(){}, err }
		prefix := filepath.Join(tmp, "page")
		cmd := exec.Command("pdftoppm", "-r", fmt.Sprint(pdfDPI), "-jpeg", path, prefix)
		if output, err := cmd.CombinedOutput(); err != nil { os.RemoveAll(tmp); return nil, func(){}, fmt.Errorf("pdftoppm: %w: %s", err, strings.TrimSpace(string(output))) }
		entries, err := os.ReadDir(tmp); if err != nil { os.RemoveAll(tmp); return nil, func(){}, err }
		var names []string; for _, e := range entries { if strings.HasSuffix(strings.ToLower(e.Name()), ".jpg") || strings.HasSuffix(strings.ToLower(e.Name()), ".jpeg") { names = append(names, filepath.Join(tmp, e.Name())) } }; sort.Strings(names)
		pages := make([]image.Image, 0, len(names)); for _, name := range names { f, e := os.Open(name); if e != nil { os.RemoveAll(tmp); return nil, func(){}, e }; im, _, e := image.Decode(f); f.Close(); if e != nil { os.RemoveAll(tmp); return nil, func(){}, e }; pages = append(pages, im) }
		return pages, func(){ _ = os.RemoveAll(tmp) }, nil
	}
	f, err := os.Open(path); if err != nil { return nil, func(){}, err }; defer f.Close()
	img, _, err := image.Decode(f); if err != nil { return nil, func(){}, fmt.Errorf("decode image: %w", err) }
	return []image.Image{img}, func(){}, nil
}

func saveJPEG(path string, img image.Image) error { f, err := os.Create(path); if err != nil { return err }; defer f.Close(); return jpeg.Encode(f, img, &jpeg.Options{Quality: 94}) }
func savePNG(path string, img image.Image) error { f, err := os.Create(path); if err != nil { return err }; defer f.Close(); return png.Encode(f, img) }
func safeName(s string) string { s = strings.Map(func(r rune) rune { if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' { return r }; return '_' }, s); if s == "" { return "scan" }; return s }
var _ color.Color
