package llm

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"strconv"
	"strings"
	"time"
)

// RetryableError wraps a provider error with an optional Retry-After duration.
// Providers should return this for 429 and 5xx responses when a Retry-After
// header is present.
type RetryableError struct {
	Err        error
	RetryAfter time.Duration
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// ParseRetryAfter parses a Retry-After header value (seconds as integer string).
// Returns 0 if the header is empty or unparseable.
func ParseRetryAfter(header string) time.Duration {
	if header == "" {
		return 0
	}
	secs, err := strconv.Atoi(header)
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// RetryConfig controls retry behavior.
type RetryConfig struct {
	MaxRetries int           // Maximum number of retries (default 3).
	BaseDelay  time.Duration // Initial backoff delay (default 1s).
}

// RetryProvider wraps a Provider with exponential backoff retry logic
// for rate limit errors (ErrRateLimited) and server errors (ErrServerError).
type RetryProvider struct {
	inner  Provider
	config RetryConfig
	logger *slog.Logger
	sleep  func(time.Duration) // injectable for testing
}

// NewRetryProvider wraps a Provider with retry logic.
func NewRetryProvider(inner Provider, cfg RetryConfig, logger *slog.Logger) *RetryProvider {
	if cfg.MaxRetries <= 0 {
		cfg.MaxRetries = 3
	}
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = time.Second
	}
	return &RetryProvider{
		inner:  inner,
		config: cfg,
		logger: logger,
		sleep:  time.Sleep,
	}
}

func (r *RetryProvider) Name() string  { return r.inner.Name() }
func (r *RetryProvider) Model() string { return r.inner.Model() }

// Complete delegates to the inner provider, retrying on retryable errors.
func (r *RetryProvider) Complete(ctx context.Context, req Request, cb StreamCallback) (*Response, error) {
	var lastErr error

	for attempt := 0; attempt <= r.config.MaxRetries; attempt++ {
		if attempt > 0 {
			delay := r.backoff(attempt, lastErr)
			r.logger.Info("retrying provider call",
				"attempt", attempt,
				"delay", delay,
				"error", lastErr,
			)

			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-r.timerChan(delay):
			}
		}

		resp, err := r.inner.Complete(ctx, req, cb)
		if err == nil {
			return resp, nil
		}

		if !isRetryable(err) {
			return nil, err
		}

		lastErr = err
	}

	return nil, lastErr
}

// backoff calculates the delay for a given attempt, respecting Retry-After.
func (r *RetryProvider) backoff(attempt int, err error) time.Duration {
	// Check for Retry-After from provider.
	var re *RetryableError
	if errors.As(err, &re) && re.RetryAfter > 0 {
		return re.RetryAfter
	}

	// Exponential backoff: baseDelay * 2^(attempt-1).
	multiplier := math.Pow(2, float64(attempt-1))
	return time.Duration(float64(r.config.BaseDelay) * multiplier)
}

// timerChan returns a channel that fires after the given duration.
func (r *RetryProvider) timerChan(d time.Duration) <-chan time.Time {
	return time.After(d)
}

// isRetryable returns true for errors that warrant a retry.
// This includes rate limits, server errors, and transient network errors
// (connection reset, connection refused, timeouts, EOF).
func isRetryable(err error) bool {
	if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrServerError) {
		return true
	}

	// EOF — connection dropped mid-response.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}

	// net.Error covers timeouts and temporary network failures.
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	// net.OpError covers connection refused, connection reset, etc.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return true
	}

	// Fallback: check error message for common network error strings
	// that may be wrapped in ways that don't implement net.Error.
	msg := err.Error()
	if strings.Contains(msg, "connection reset") ||
		strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "i/o timeout") ||
		strings.Contains(msg, "no such host") {
		return true
	}

	return false
}
