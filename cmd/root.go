package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "tiffsplit",
	Short: "Find and export individual photographs from TIFF, JPEG, and PDF scans",
	Long:  "A format-agnostic scan processor. JPEG and TIFF are decoded in Go; PDF pages are rasterized using Poppler's pdftoppm command.",
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func init() { rootCmd.SilenceUsage = true }
