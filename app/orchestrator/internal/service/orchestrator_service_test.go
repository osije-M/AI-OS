package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestIsRetryable(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"unavailable", status.Error(codes.Unavailable, "unavailable"), true},
		{"deadline_exceeded", status.Error(codes.DeadlineExceeded, "timeout"), true},
		{"not_found", status.Error(codes.NotFound, "not found"), false},
		{"invalid_argument", status.Error(codes.InvalidArgument, "bad arg"), false},
		{"internal", status.Error(codes.Internal, "boom"), false},
		{"nil_error", nil, false},
		// A plain (non-status) error: status.FromError falls back to codes.Unknown,
		// which is not Unavailable/DeadlineExceeded -> not retryable.
		{"plain_error", errors.New("some generic error"), false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isRetryable(tt.err)
			if got != tt.want {
				t.Errorf("isRetryable(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestRetryBackoffRespectsContextDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()

	start := time.Now()
	retryBackoff(ctx, 0, 10000) // huge backoff, should be clamped by ctx deadline
	elapsed := time.Since(start)

	if elapsed > 200*time.Millisecond {
		t.Errorf("retryBackoff took %v, expected to return quickly due to context deadline clamp", elapsed)
	}
}

func TestRetryBackoffIncreasesWithAttempt(t *testing.T) {
	// No deadline on this context, so retryBackoff sleeps the full computed backoff.
	// attempt=0 -> base backoffMs*2^0; attempt=2 -> base backoffMs*2^2 (much larger).
	ctx := context.Background()

	start := time.Now()
	retryBackoff(ctx, 0, 5)
	shortElapsed := time.Since(start)

	start = time.Now()
	retryBackoff(ctx, 3, 5)
	longElapsed := time.Since(start)

	if longElapsed <= shortElapsed {
		t.Errorf("expected backoff at higher attempt to take longer: attempt0=%v attempt3=%v", shortElapsed, longElapsed)
	}
}
