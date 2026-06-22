package provider

import (
	"context"
	"fmt"
	"testing"
)

// The fmt.Errorf sites APIError replaced produced exactly
// "API error %d: %s" — logs, the chat-UI error bubble, and the stream-
// fallback warnings all surface that string. Pin it so the move to a
// typed error stays invisible to those consumers.
func TestAPIErrorPreservesLegacyFormat(t *testing.T) {
	err := &APIError{StatusCode: 503, Body: "body"}
	if got, want := err.Error(), "API error 503: body"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}

// IsAvailabilityError is the failover gate for the model fallback
// chain: provider-down signals (5xx / 429 / transport) pass, request
// rejections (other 4xx) and caller cancellation do not.
func TestIsAvailabilityError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"api 503", &APIError{StatusCode: 503, Body: "overloaded"}, true},
		{"api 500", &APIError{StatusCode: 500, Body: "boom"}, true},
		{"api 429", &APIError{StatusCode: 429, Body: "rate limited"}, true},
		{"api 400", &APIError{StatusCode: 400, Body: "bad request"}, false},
		{"api 401", &APIError{StatusCode: 401, Body: "bad key"}, false},
		{"wrapped api 503", fmt.Errorf("send request: %w", &APIError{StatusCode: 503}), true},
		{"wrapped api 400", fmt.Errorf("send request: %w", &APIError{StatusCode: 400}), false},
		{"transport", fmt.Errorf("connection refused"), true},
		{"caller cancel", context.Canceled, false},
		{"caller deadline", context.DeadlineExceeded, false},
		{"wrapped cancel", fmt.Errorf("send request: %w", context.Canceled), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAvailabilityError(tc.err); got != tc.want {
				t.Fatalf("IsAvailabilityError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
