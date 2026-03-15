package main

import (
	"fmt"
	"os"

	"github.com/marmutapp/openmarmut/internal/cli"
	"github.com/marmutapp/openmarmut/internal/ui"
)

// Set by goreleaser ldflags at build time.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	cli.SetVersionInfo(version, commit, date)
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, ui.FormatError(err.Error()))
		os.Exit(1)
	}
}
