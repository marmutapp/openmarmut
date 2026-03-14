package ui

import (
	"os"
	"sync"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-isatty"
	"github.com/muesli/termenv"
)

var (
	initOnce    sync.Once
	isTTY       bool
	colorForced bool
)

func init() {
	detect()
}

// detect runs TTY and color detection once.
func detect() {
	initOnce.Do(func() {
		isTTY = isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
		colorForced = false

		// NO_COLOR convention: https://no-color.org/
		if _, ok := os.LookupEnv("NO_COLOR"); ok {
			isTTY = false
		}

		// FORCE_COLOR overrides NO_COLOR for tools that need it.
		if _, ok := os.LookupEnv("FORCE_COLOR"); ok {
			colorForced = true
			isTTY = true
		}

		syncLipglossProfile()
	})
}

// syncLipglossProfile sets the lipgloss renderer color profile to match our detection.
func syncLipglossProfile() {
	if isTTY || colorForced {
		lipgloss.SetColorProfile(termenv.TrueColor)
	} else {
		lipgloss.SetColorProfile(termenv.Ascii)
	}
}

// IsTerminal reports whether stdout is connected to an interactive terminal.
// Returns false if NO_COLOR env var is set.
func IsTerminal() bool {
	return isTTY
}

// ColorEnabled reports whether styled output should be used.
func ColorEnabled() bool {
	return isTTY || colorForced
}

// overrideTTY is used only by tests to override TTY detection.
func overrideTTY(val bool) {
	isTTY = val
	colorForced = val
	syncLipglossProfile()
}
