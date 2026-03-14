package main

import (
	"fmt"
	"os"

	"github.com/gajaai/openmarmut-go/internal/cli"
	"github.com/gajaai/openmarmut-go/internal/ui"
)

func main() {
	if err := cli.NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, ui.FormatError(err.Error()))
		os.Exit(1)
	}
}
