// Package redact hides secrets and the service's own network identity
// from text that reaches a regular chatter (and the model working for
// them). It doesn't depend on the model following its prompt: tool
// results are filtered before the model sees them, and replies before
// the chatter does.
//
// Two kinds of match:
//   - exact values registered at runtime (provider and skill keys, the
//     daemon's secret env vars, the egress IP);
//   - common key shapes (sk-…, ghp_…, AKIA…, JWTs, private-key blocks,
//     passwords inside connection URLs).
package redact

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Mask replaces every hidden value.
const Mask = "[hidden]"

// minValueLen keeps short, common strings ("admin", "true") from being
// registered: a false positive there would garble ordinary text.
const minValueLen = 8

var (
	mu     sync.RWMutex
	values = map[string]struct{}{}
	sorted []string // longest first, so a key containing another wins
)

// Register adds exact values to hide. Empty and short values are ignored.
func Register(vals ...string) {
	added := false
	mu.Lock()
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if len(v) < minValueLen {
			continue
		}
		if _, ok := values[v]; ok {
			continue
		}
		values[v] = struct{}{}
		added = true
	}
	if added {
		sorted = sorted[:0]
		for v := range values {
			sorted = append(sorted, v)
		}
		sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	}
	mu.Unlock()
}

// RegisterIP hides an IP address. IPs are shorter than minValueLen at
// times (1.2.3.4), so they bypass the length floor; only parseable,
// public addresses are taken.
func RegisterIP(ip string) {
	ip = strings.TrimSpace(ip)
	p := net.ParseIP(ip)
	if p == nil || p.IsLoopback() || p.IsPrivate() || p.IsUnspecified() {
		return
	}
	mu.Lock()
	if _, ok := values[ip]; !ok {
		values[ip] = struct{}{}
		sorted = append(sorted, ip)
		sort.Slice(sorted, func(i, j int) bool { return len(sorted[i]) > len(sorted[j]) })
	}
	mu.Unlock()
}

var patterns = []*regexp.Regexp{
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
	regexp.MustCompile(`\bsk-(?:ant-|proj-|or-)?[A-Za-z0-9_-]{20,}`),
	regexp.MustCompile(`\b(?:ghp|gho|ghs|ghu|ghr)_[A-Za-z0-9]{30,}\b`),
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}\b`),
	regexp.MustCompile(`\b(?:AKIA|ASIA)[0-9A-Z]{16}\b`),
	regexp.MustCompile(`\bLTAI[0-9A-Za-z]{12,24}\b`),
	regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{35}\b`),
	regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
	regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}`),
	regexp.MustCompile(`(?i)\bbearer\s+[A-Za-z0-9._~+/=-]{20,}`),
}

// userinfo password in scheme://user:password@host
var urlPassword = regexp.MustCompile(`\b([a-zA-Z][a-zA-Z0-9+.-]*://[^\s:/@]+:)[^\s@/]+@`)

// Text returns s with every registered value and secret-shaped token
// replaced by Mask.
func Text(s string) string {
	if s == "" {
		return s
	}
	mu.RLock()
	for _, v := range sorted {
		if strings.Contains(s, v) {
			s = strings.ReplaceAll(s, v, Mask)
		}
	}
	mu.RUnlock()
	for _, re := range patterns {
		s = re.ReplaceAllString(s, Mask)
	}
	return urlPassword.ReplaceAllString(s, "${1}"+Mask+"@")
}

// LoadEnv registers the values of env vars whose names isSecret marks,
// plus FASTCLAW_REDACT_EXTRA: a comma-separated list of extra values or
// IPs the operator wants hidden (e.g. the sandbox provider's egress IPs).
func LoadEnv(isSecret func(name string) bool) {
	for _, kv := range os.Environ() {
		name, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if isSecret != nil && isSecret(name) {
			Register(val)
		}
	}
	for _, v := range strings.Split(os.Getenv("FASTCLAW_REDACT_EXTRA"), ",") {
		if net.ParseIP(strings.TrimSpace(v)) != nil {
			RegisterIP(v)
		} else {
			Register(v)
		}
	}
}

// DiscoverEgressIP looks up the public IP this process reaches the
// internet from and hides it. Best effort: a failure leaves things as
// they were.
func DiscoverEgressIP(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	for _, u := range []string{"https://api.ipify.org", "https://checkip.amazonaws.com", "https://ifconfig.me/ip"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			continue
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if ip := strings.TrimSpace(string(body)); net.ParseIP(ip) != nil {
			RegisterIP(ip)
			return
		}
	}
}
