package provider

import (
	"context"
	"errors"
	"fmt"
)

// APIError is a non-2xx HTTP response from an LLM provider API. It
// replaces the fmt.Errorf("API error %d: %s", ...) strings the Chat /
// ChatStream sites used to return, keeping the status code machine-
// readable so callers (agent model fallback) can tell "the provider is
// down" apart from "the request is wrong" without string matching.
type APIError struct {
	StatusCode int
	Body       string
}

// Error preserves the exact legacy format — logs, the chat-UI error
// surfaces, and anything keyed on the old string see no change.
func (e *APIError) Error() string {
	return fmt.Sprintf("API error %d: %s", e.StatusCode, e.Body)
}

// IsAvailabilityError reports whether err means the provider was
// unavailable, as opposed to the request itself being rejected. This is
// the failover gate for the agent's model fallback chain: only errors a
// *different* provider could plausibly fix should trigger it.
//
//   - *APIError 5xx / 429: the provider is overloaded, broken, or rate
//     limiting us — retrying elsewhere can help.
//   - *APIError other 4xx: bad request, bad auth, oversized context —
//     replaying the same payload at another provider just burns quota
//     on a second rejection (or worse, a different model "fixes" a
//     malformed request by silently accepting it).
//   - Non-APIError, non-nil: the provider never answered at all
//     (connection refused, DNS failure, TLS error, net/http timeout).
//     Request-shaped problems always come back as *APIError, so
//     anything else is transport-level and worth a failover.
//   - context.Canceled / DeadlineExceeded: the CALLER gave up, not the
//     provider — firing the same doomed request at the next provider
//     would only waste tokens. Checked first because net/http wraps
//     ctx errors and they would otherwise fall into the transport case.
func IsAvailabilityError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.StatusCode == 429 || apiErr.StatusCode >= 500
	}
	return true
}
