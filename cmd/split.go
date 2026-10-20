package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"gocv.io/x/gocv"

	"image-splitter/internal/detect"
	"image-splitter/internal/imageio"
)

var (
	inputDir, outputDir, debugDir string
	recursive, review             bool
	padding, pdfDPI               int
	fallbackDPI                   float64
	cfg                           = detect.DefaultConfig()

	stdout, stderr io.Writer = os.Stdout, os.Stderr
)

var splitCmd = &cobra.Command{
	Use:   "split",
	Short: "Detect photos on scanned pages and export each one as a lossless TIFF",
	Example: "  image-splitter split -i ./scans -o ./photos\n" +
		"  image-splitter split -i ./scans -o ./photos --debug-dir ./debug\n" +
		"  image-splitter split -i ./scans -o ./photos --threshold 20 --padding 0",
	RunE: func(cmd *cobra.Command, args []string) error {
		stdout, stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
		return runSplit()
	},
}

func init() {
	rootCmd.AddCommand(splitCmd)
	f := splitCmd.Flags()
	f.StringVarP(&inputDir, "input", "i", "", "Input folder with .tif, .tiff, .jpg, .jpeg, .png and .pdf scans (required)")
	f.StringVarP(&outputDir, "output", "o", "", "Output folder for the extracted photos (required)")
	f.BoolVarP(&recursive, "recursive", "r", true, "Scan subfolders")
	f.BoolVar(&review, "review", true, "Write a review image per page with the numbered detections to <output>/review")
	f.StringVar(&debugDir, "debug-dir", "", "Write segmentation masks and report.json to this folder")

	f.Float64Var(&cfg.Threshold, "threshold", cfg.Threshold, "Colour distance from the scanner bed counted as photo (0-255); lower is more sensitive")
	f.Float64Var(&cfg.MinArea, "min-area", cfg.MinArea, "Minimum photo area in page pixels")
	f.Float64Var(&cfg.MinWidth, "min-width", cfg.MinWidth, "Minimum photo width in page pixels")
	f.Float64Var(&cfg.MinHeight, "min-height", cfg.MinHeight, "Minimum photo height in page pixels")
	f.Float64Var(&cfg.MaxPageFraction, "max-page-fraction", cfg.MaxPageFraction, "Reject regions covering more than this fraction of the page")
	f.Float64Var(&cfg.MinFill, "min-fill", cfg.MinFill, "Reject regions filling less than this fraction of their rotated bounding box")
	f.IntVar(&cfg.WorkSize, "work-size", cfg.WorkSize, "Long side in pixels of the downscaled image used for detection")
	f.BoolVar(&cfg.Edges, "edges", cfg.Edges, "Use edge detection (finds photos with white borders)")
	f.BoolVar(&cfg.SplitTouching, "split-touching", cfg.SplitTouching, "Try to separate photos that touch each other")
	f.BoolVar(&cfg.Deskew, "deskew", cfg.Deskew, "Straighten rotated photos")
	f.BoolVar(&cfg.Perspective, "perspective", cfg.Perspective, "Correct perspective when four reliable corners are found")
	f.Float64Var(&cfg.MinAngle, "min-angle", cfg.MinAngle, "Rotations below this many degrees are cropped without resampling")

	f.IntVar(&padding, "padding", 0, "Extra page pixels around each photo")
	f.IntVar(&pdfDPI, "pdf-dpi", 600, "Resolution used to rasterize PDF pages (requires Poppler's pdftoppm)")
	f.Float64Var(&fallbackDPI, "dpi", 0, "Resolution written to output TIFFs when the source records none (0 = leave unset)")
	_ = splitCmd.MarkFlagRequired("input")
	_ = splitCmd.MarkFlagRequired("output")
}

type pageReport struct {
	File   string        `json:"file"`
	Page   int           `json:"page"`
	Width  int           `json:"width,omitempty"`
	Height int           `json:"height,omitempty"`
	DPI    float64       `json:"dpi,omitempty"`
	Review string        `json:"review,omitempty"`
	Stats  *detect.Stats `json:"stats,omitempty"`
	Photos []photoReport `json:"photos"`
	Error  string        `json:"error,omitempty"`
}

type photoReport struct {
	detect.Detection
	Output string `json:"output"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

func runSplit() error {
	if cfg.Threshold <= 0 || cfg.Threshold > 255 {
		return fmt.Errorf("threshold must be between 0 and 255")
	}
	if cfg.MaxPageFraction <= 0 || cfg.MaxPageFraction > 1 || cfg.MinFill < 0 || cfg.MinFill > 1 {
		return fmt.Errorf("max-page-fraction and min-fill must be between 0 and 1")
	}
	if pdfDPI < 72 || pdfDPI > 1200 {
		return fmt.Errorf("pdf-dpi must be between 72 and 1200")
	}
	if padding < 0 {
		return fmt.Errorf("padding must not be negative")
	}
	info, err := os.Stat(inputDir)
	if err != nil {
		return fmt.Errorf("input: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("input must be a directory")
	}
	reviewDir := filepath.Join(outputDir, "review")
	dirs := []string{outputDir, debugDir}
	if review {
		dirs = append(dirs, reviewDir)
	}
	for _, dir := range dirs {
		if dir != "" {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
		}
	}

	files, err := findInputs()
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no TIFF, JPEG, PNG or PDF files found in %s", inputDir)
	}

	var reports []pageReport
	failed, exported := 0, 0
	for _, path := range files {
		fmt.Fprintf(stdout, "Processing %s\n", path)
		base := safeName(strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)))
		for page, err := range imageio.Pages(path, pdfDPI) {
			if err != nil {
				fmt.Fprintf(stderr, "  ERROR: %v\n", err)
				reports = append(reports, pageReport{File: path, Page: page.Number, Error: err.Error()})
				failed++
				continue
			}
			rep := processPage(page, path, fmt.Sprintf("%s_p%03d", base, page.Number), reviewDir)
			page.Mat.Close()
			if rep.Error != "" {
				failed++
			}
			exported += len(rep.Photos)
			reports = append(reports, rep)
		}
	}

	if debugDir != "" {
		data, err := json.MarshalIndent(reports, "", "  ")
		if err == nil {
			err = os.WriteFile(filepath.Join(debugDir, "report.json"), data, 0o644)
		}
		if err != nil {
			fmt.Fprintf(stderr, "ERROR writing report: %v\n", err)
			failed++
		}
	}
	fmt.Fprintf(stdout, "Exported %d photo(s) from %d file(s)\n", exported, len(files))
	if failed > 0 {
		return fmt.Errorf("finished with %d error(s)", failed)
	}
	return nil
}

func processPage(page imageio.Page, path, label, reviewDir string) pageReport {
	rep := pageReport{File: path, Page: page.Number, Width: page.Mat.Cols(), Height: page.Mat.Rows(), DPI: page.DPI, Photos: []photoReport{}}
	dpi := page.DPI
	if dpi == 0 {
		dpi = fallbackDPI
	}

	res, err := detect.Detect(page.Mat, cfg, debugDir != "")
	if err != nil {
		fmt.Fprintf(stderr, "  ERROR page %d: %v\n", page.Number, err)
		rep.Error = err.Error()
		return rep
	}
	defer res.Close()
	rep.Stats = &res.Stats

	for name, m := range res.Masks {
		if err := imageio.WritePNG(filepath.Join(debugDir, fmt.Sprintf("%s_mask_%s.png", label, name)), m); err != nil {
			fmt.Fprintf(stderr, "  ERROR writing mask: %v\n", err)
		}
	}

	var errs []string
	for _, d := range res.Detections {
		out := filepath.Join(outputDir, fmt.Sprintf("%s_photo%02d.tif", label, d.Index))
		crop, err := detect.Extract(page.Mat, d, padding)
		if err == nil {
			err = imageio.WriteTIFF(out, crop, dpi)
			rep.Photos = append(rep.Photos, photoReport{Detection: d, Output: out, Width: crop.Cols(), Height: crop.Rows()})
			crop.Close()
		}
		if err != nil {
			fmt.Fprintf(stderr, "  ERROR writing %s: %v\n", out, err)
			errs = append(errs, err.Error())
			continue
		}
		fmt.Fprintf(stdout, "  -> %s (%s, %.1f°)\n", out, d.Method, d.Angle)
	}
	if len(res.Detections) == 0 {
		fmt.Fprintf(stdout, "  No photos detected on page %d\n", page.Number)
	}

	if review {
		rep.Review = filepath.Join(reviewDir, label+".jpg")
		if err := writeReview(rep.Review, page.Mat, res.Detections); err != nil {
			fmt.Fprintf(stderr, "  ERROR writing review: %v\n", err)
			errs = append(errs, err.Error())
		}
	}
	rep.Error = strings.Join(errs, "; ")
	return rep
}

func writeReview(path string, page gocv.Mat, ds []detect.Detection) error {
	img, err := detect.Review(page, ds, 2000)
	if err != nil {
		return err
	}
	defer img.Close()
	return imageio.WriteJPEG(path, img, 90)
}

func findInputs() ([]string, error) {
	var files []string
	err := filepath.WalkDir(inputDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != inputDir && (!recursive || samePath(path, outputDir) || samePath(path, debugDir)) {
				return filepath.SkipDir
			}
			return nil
		}
		if imageio.Supported(path) {
			files = append(files, path)
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}

func samePath(a, b string) bool {
	if b == "" {
		return false
	}
	pa, err1 := filepath.Abs(a)
	pb, err2 := filepath.Abs(b)
	return err1 == nil && err2 == nil && strings.EqualFold(pa, pb)
}

func safeName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
	if s == "" {
		return "scan"
	}
	return s
}
