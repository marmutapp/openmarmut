package ui

import (
	"bytes"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestSpinner_StartStop(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	var buf bytes.Buffer
	s := NewSpinner(&buf, "Working...")
	s.Start()
	time.Sleep(200 * time.Millisecond) // Let a few frames render.
	s.Stop()

	output := buf.String()
	assert.Contains(t, output, "Working...")
}

func TestSpinner_StopIdempotent(t *testing.T) {
	overrideTTY(true)
	defer overrideTTY(false)

	var buf bytes.Buffer
	s := NewSpinner(&buf, "")
	s.Start()
	time.Sleep(100 * time.Millisecond)
	s.Stop()
	s.Stop() // Second stop should not panic.
}

func TestSpinner_NoTTY(t *testing.T) {
	overrideTTY(false)
	defer overrideTTY(false)

	var buf bytes.Buffer
	s := NewSpinner(&buf, "test")
	s.Start()
	time.Sleep(100 * time.Millisecond)
	s.Stop()

	// Should produce no output when not a TTY.
	assert.Empty(t, buf.String())
}

func TestSpinner_DefaultMessage(t *testing.T) {
	s := NewSpinner(nil, "")
	assert.Equal(t, "Thinking...", s.message)
}
