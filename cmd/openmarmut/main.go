package main

import (
	"fmt"
	"os"

	"github.com/marmutapp/openmarmut/internal/cli"
	"github.com/marmutapp/openmarmut/internal/ui"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, ui.FormatError(err.Error()))
		os.Exit(1)
	}
}
