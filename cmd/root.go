package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "image-splitter",
	Short: "Find and export individual photographs from TIFF, JPEG, and PDF scans",
	Long: "Detects photos placed freely on scanned pages (TIFF, including multi-page, JPEG, PNG and PDF), " +
		"straightens them and exports each one as a lossless LZW-compressed TIFF. Uses OpenCV via GoCV; " +
		"PDF pages are rasterized with Poppler's pdftoppm.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func init() { rootCmd.SilenceUsage = true }
