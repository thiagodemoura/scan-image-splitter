package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
	"gocv.io/x/gocv"
)

// version is set at build time with -ldflags "-X image-splitter/cmd.version=v1.2.3".
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version and the linked OpenCV version",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintf(cmd.OutOrStdout(), "image-splitter %s (%s, %s/%s)\nGoCV %s, OpenCV %s\n",
			version, runtime.Version(), runtime.GOOS, runtime.GOARCH, gocv.Version(), gocv.OpenCVVersion())
	},
}

func init() {
	rootCmd.Version = version
	rootCmd.AddCommand(versionCmd)
}
