package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// spinnerFrames are the braille animation frames for the spinner.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Spinner displays a "⠋ Thinking..." animation on a single line.
// Uses goroutine + \r overwrites, no Bubble Tea dependency.
type Spinner struct {
	message string
	w       io.Writer
	stop    chan struct{}
	done    chan struct{}
	mu      sync.Mutex
	running bool
}

// NewSpinner creates a spinner that writes to the given writer.
func NewSpinner(w io.Writer, message string) *Spinner {
	if message == "" {
		message = "Thinking..."
	}
	return &Spinner{
		message: message,
		w:       w,
		stop:    make(chan struct{}),
		done:    make(chan struct{}),
	}
}

// Start begins the spinner animation in a background goroutine.
// Does nothing if the spinner is already running or if output is not a TTY.
func (s *Spinner) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running || !IsTerminal() {
		return
	}
	s.running = true

	go func() {
		defer close(s.done)
		frame := 0
		ticker := time.NewTicker(80 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-s.stop:
				// Clear the spinner line.
				fmt.Fprintf(s.w, "\r\033[K")
				return
			case <-ticker.C:
				text := fmt.Sprintf("%s %s", spinnerFrames[frame%len(spinnerFrames)], s.message)
				if ColorEnabled() {
					text = DimStyle.Render(text)
				}
				fmt.Fprintf(s.w, "\r\033[K%s", text)
				frame++
			}
		}
	}()
}

// Stop halts the spinner and clears its line. Safe to call multiple times.
func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return
	}
	s.running = false
	close(s.stop)
	<-s.done
}
