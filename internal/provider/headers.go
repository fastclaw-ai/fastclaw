package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
)

// SessionPlaceholder in a provider header value is replaced with a
// stable per-conversation ID. Some gateways require one to route and
// prompt-cache per conversation — OpenCode Go rejects requests without
// `x-opencode-session`.
const SessionPlaceholder = "{{session}}"

type sessionKeyCtxKey struct{}

// WithSessionKey tags ctx with the durable session key of the current
// turn so provider headers can derive a per-conversation ID from it.
func WithSessionKey(ctx context.Context, sessionKey string) context.Context {
	return context.WithValue(ctx, sessionKeyCtxKey{}, sessionKey)
}

// processSessionID backs SessionPlaceholder for calls made outside a
// chat turn (e.g. background compaction), so a required header is never
// sent empty.
var processSessionID = func() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}()

// sessionHeaderID returns the value substituted for SessionPlaceholder.
// The session key is hashed so internal chat identifiers (channel, chat
// ID) never leave the process.
func sessionHeaderID(ctx context.Context) string {
	key, _ := ctx.Value(sessionKeyCtxKey{}).(string)
	if key == "" {
		return processSessionID
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

// ApplyCustomHeaders sets operator-configured provider headers on an
// outbound LLM request, expanding SessionPlaceholder. Applied after the
// built-in headers, so an operator can override them deliberately.
func ApplyCustomHeaders(ctx context.Context, h http.Header, headers map[string]string) {
	for name, value := range headers {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if strings.Contains(value, SessionPlaceholder) {
			value = strings.ReplaceAll(value, SessionPlaceholder, sessionHeaderID(ctx))
		}
		h.Set(name, value)
	}
}
