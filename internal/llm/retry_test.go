package llm

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var silentLogger = slog.New(slog.NewTextHandler(io.Discard, nil))

// --- Mock provider for retry tests ---

type mockRetryProvider struct {
	name      string
	model     string
	responses []mockRetryResult
	callIdx   int
}

type mockRetryResult struct {
	resp *Response
	err  error
}

func (m *mockRetryProvider) Name() string  { return m.name }
func (m *mockRetryProvider) Model() string { return m.model }

func (m *mockRetryProvider) Complete(_ context.Context, _ Request, cb StreamCallback) (*Response, error) {
	if m.callIdx >= len(m.responses) {
		return nil, fmt.Errorf("mock: no more responses (call %d)", m.callIdx)
	}
	r := m.responses[m.callIdx]
	m.callIdx++

	if r.err != nil {
		return nil, r.err
	}

	if cb != nil && r.resp.Content != "" {
		if err := cb(r.resp.Content); err != nil {
			return nil, err
		}
	}
	return r.resp, nil
}

// --- Tests ---

func TestRetryProvider_SuccessNoRetry(t *testing.T) {
	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{resp: &Response{Content: "ok"}},
		},
	}

	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)
	resp, err := rp.Complete(context.Background(), Request{}, nil)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 1, inner.callIdx)
}

func TestRetryProvider_RetryOnRateLimit(t *testing.T) {
	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{err: fmt.Errorf("provider: %w: slow down", ErrRateLimited)},
			{err: fmt.Errorf("provider: %w: slow down", ErrRateLimited)},
			{resp: &Response{Content: "ok"}},
		},
	}

	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)
	resp, err := rp.Complete(context.Background(), Request{}, nil)

	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Content)
	assert.Equal(t, 3, inner.callIdx)
}

func TestRetryProvider_RetryOnServerError(t *testing.T) {
	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{err: fmt.Errorf("provider: %w: HTTP 500: internal", ErrServerError)},
			{resp: &Response{Content: "recovered"}},
		},
	}

	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)
	resp, err := rp.Complete(context.Background(), Request{}, nil)

	require.NoError(t, err)
	assert.Equal(t, "recovered", resp.Content)
	assert.Equal(t, 2, inner.callIdx)
}

func TestRetryProvider_NoRetryOnAuthError(t *testing.T) {
	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{err: fmt.Errorf("provider: %w: bad key", ErrAuthFailed)},
		},
	}

	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)
	_, err := rp.Complete(context.Background(), Request{}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrAuthFailed)
	assert.Equal(t, 1, inner.callIdx)
}

func TestRetryProvider_NoRetryOnModelNotFound(t *testing.T) {
	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{err: fmt.Errorf("provider: %w: no such model", ErrModelNotFound)},
		},
	}

	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)
	_, err := rp.Complete(context.Background(), Request{}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrModelNotFound)
	assert.Equal(t, 1, inner.callIdx)
}

func TestRetryProvider_ExhaustsRetries(t *testing.T) {
	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{err: fmt.Errorf("provider: %w: 1", ErrRateLimited)},
			{err: fmt.Errorf("provider: %w: 2", ErrRateLimited)},
			{err: fmt.Errorf("provider: %w: 3", ErrRateLimited)},
			{err: fmt.Errorf("provider: %w: 4", ErrRateLimited)},
		},
	}

	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)
	_, err := rp.Complete(context.Background(), Request{}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrRateLimited)
	// 1 initial + 3 retries = 4 calls.
	assert.Equal(t, 4, inner.callIdx)
}

func TestRetryProvider_RespectsRetryAfter(t *testing.T) {
	retryAfterErr := &RetryableError{
		Err:        fmt.Errorf("provider: %w: slow down", ErrRateLimited),
		RetryAfter: 5 * time.Second,
	}

	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{err: retryAfterErr},
			{resp: &Response{Content: "ok"}},
		},
	}

	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)

	// Verify the backoff respects Retry-After.
	delay := rp.backoff(1, retryAfterErr)
	assert.Equal(t, 5*time.Second, delay)
}

func TestRetryProvider_ExponentialBackoff(t *testing.T) {
	baseErr := fmt.Errorf("provider: %w: error", ErrRateLimited)
	rp := NewRetryProvider(nil, RetryConfig{MaxRetries: 3, BaseDelay: time.Second}, silentLogger)

	assert.Equal(t, 1*time.Second, rp.backoff(1, baseErr))
	assert.Equal(t, 2*time.Second, rp.backoff(2, baseErr))
	assert.Equal(t, 4*time.Second, rp.backoff(3, baseErr))
}

func TestRetryProvider_ContextCancellation(t *testing.T) {
	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{err: fmt.Errorf("provider: %w: slow", ErrRateLimited)},
			{resp: &Response{Content: "ok"}},
		},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)
	_, err := rp.Complete(ctx, Request{}, nil)

	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestRetryProvider_DelegatesNameModel(t *testing.T) {
	inner := &mockRetryProvider{name: "my-provider", model: "gpt-4"}
	rp := NewRetryProvider(inner, RetryConfig{}, silentLogger)

	assert.Equal(t, "my-provider", rp.Name())
	assert.Equal(t, "gpt-4", rp.Model())
}

func TestRetryProvider_StreamCallbackPassedThrough(t *testing.T) {
	inner := &mockRetryProvider{
		name:  "test",
		model: "m",
		responses: []mockRetryResult{
			{resp: &Response{Content: "streamed"}},
		},
	}

	var streamed string
	rp := NewRetryProvider(inner, RetryConfig{MaxRetries: 3, BaseDelay: time.Millisecond}, silentLogger)
	resp, err := rp.Complete(context.Background(), Request{}, func(text string) error {
		streamed += text
		return nil
	})

	require.NoError(t, err)
	assert.Equal(t, "streamed", resp.Content)
	assert.Equal(t, "streamed", streamed)
}

func TestRetryProvider_DefaultConfig(t *testing.T) {
	rp := NewRetryProvider(nil, RetryConfig{}, silentLogger)
	assert.Equal(t, 3, rp.config.MaxRetries)
	assert.Equal(t, time.Second, rp.config.BaseDelay)
}

func TestParseRetryAfter_ValidSeconds(t *testing.T) {
	assert.Equal(t, 5*time.Second, ParseRetryAfter("5"))
}

func TestParseRetryAfter_Empty(t *testing.T) {
	assert.Equal(t, time.Duration(0), ParseRetryAfter(""))
}

func TestParseRetryAfter_Invalid(t *testing.T) {
	assert.Equal(t, time.Duration(0), ParseRetryAfter("abc"))
}

func TestParseRetryAfter_Zero(t *testing.T) {
	assert.Equal(t, time.Duration(0), ParseRetryAfter("0"))
}

func TestParseRetryAfter_Negative(t *testing.T) {
	assert.Equal(t, time.Duration(0), ParseRetryAfter("-1"))
}

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{"rate limited", fmt.Errorf("wrap: %w", ErrRateLimited), true},
		{"server error", fmt.Errorf("wrap: %w", ErrServerError), true},
		{"auth failed", fmt.Errorf("wrap: %w", ErrAuthFailed), false},
		{"model not found", fmt.Errorf("wrap: %w", ErrModelNotFound), false},
		{"context too long", fmt.Errorf("wrap: %w", ErrContextTooLong), false},
		{"stream aborted", fmt.Errorf("wrap: %w", ErrStreamAborted), false},
		{"generic error", fmt.Errorf("something broke"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, isRetryable(tt.err))
		})
	}
}

func TestRetryableError_Unwrap(t *testing.T) {
	inner := fmt.Errorf("provider: %w: slow", ErrRateLimited)
	re := &RetryableError{Err: inner, RetryAfter: 3 * time.Second}

	assert.ErrorIs(t, re, ErrRateLimited)
	assert.Equal(t, inner.Error(), re.Error())
}
