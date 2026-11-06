package agl

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Keeping linked machines' agl current: the newest release (checked now
// and then), and daemon.update, which a hub may call on any link from agl
// 0.3.5 (it installs only the official release; below a full link it
// waits until the hub's sessions are idle).

const (
	// DefaultLatestURL redirects to …/releases/tag/v<version>.
	DefaultLatestURL = "https://github.com/thinkany-ai/agl/releases/latest"
	// MinRemoteUpdateVersion is the first agl that takes daemon.update
	// from a link below `full` (the links FastClaw's install command makes).
	MinRemoteUpdateVersion = "0.3.5"

	latestTTL      = time.Hour
	latestRetry    = 5 * time.Minute
	updateTimeout  = 5 * time.Minute
	latestFetchTTL = 15 * time.Second
)

var versionRe = regexp.MustCompile(`\d+(?:\.\d+)*`)

// latestRelease caches the newest agl version; reads never wait for it.
type latestRelease struct {
	url    string
	client *http.Client

	mu       sync.Mutex
	version  string
	checked  time.Time
	fetching bool
}

func newLatestRelease(url string) *latestRelease {
	return &latestRelease{url: url, client: &http.Client{
		Timeout: latestFetchTTL,
		// The answer is the redirect itself.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// get returns the cached version ("" until known) and refreshes it in the
// background when stale.
func (l *latestRelease) get() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	ttl := latestTTL
	if l.version == "" {
		ttl = latestRetry
	}
	if !l.fetching && time.Since(l.checked) > ttl {
		l.fetching = true
		go l.fetch()
	}
	return l.version
}

func (l *latestRelease) fetch() {
	v, err := l.lookup()
	l.mu.Lock()
	defer l.mu.Unlock()
	l.fetching = false
	l.checked = time.Now()
	if err == nil && v != "" {
		l.version = v
	}
}

func (l *latestRelease) lookup() (string, error) {
	resp, err := l.client.Get(l.url)
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	if i := strings.LastIndex(loc, "/tag/"); i >= 0 {
		if v := versionRe.FindString(loc[i:]); v != "" {
			return v, nil
		}
	}
	return "", errors.New("no release tag in redirect")
}

// SetLatestURL changes where the newest release is looked up; "" turns
// the lookup off (tests, air-gapped installs).
func (h *Hub) SetLatestURL(url string) {
	if url == "" {
		h.latest = nil
		return
	}
	h.latest = newLatestRelease(url)
}

// LatestVersion is the newest agl release ("" while unknown).
func (h *Hub) LatestVersion() string {
	if h == nil || h.latest == nil {
		return ""
	}
	return h.latest.get()
}

// CompareVersions compares the dotted numbers in a and b (e.g. "0.3.5",
// "agl 0.3.10"): -1, 0 or 1. Missing parts count as 0.
func CompareVersions(a, b string) int {
	pa, pb := versionParts(a), versionParts(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

func versionParts(v string) []int {
	var out []int
	for _, p := range strings.Split(versionRe.FindString(v), ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return out
		}
		out = append(out, n)
	}
	return out
}

// UpdateAvailable reports whether a machine running version is behind latest.
func UpdateAvailable(version, latest string) bool {
	return version != "" && latest != "" && CompareVersions(version, latest) < 0
}

// RemoteUpdateSupported reports whether an agl at version takes
// daemon.update over FastClaw's link.
func RemoteUpdateSupported(version string) bool {
	return version != "" && CompareVersions(version, MinRemoteUpdateVersion) >= 0
}

// AglUpdate is the answer to daemon.update.
type AglUpdate struct {
	Updated bool   `json:"updated"`
	Current string `json:"current"`
	Latest  string `json:"latest"`
	// Busy > 0: not updated because sessions were running a turn.
	Busy int `json:"busy,omitempty"`
}

// UpdateAgl asks machineID's agl to update itself to the newest release.
// It never ends a running turn (if_idle); the link drops and comes back
// on the new version.
func (h *Hub) UpdateAgl(ctx context.Context, machineID string) (AglUpdate, error) {
	ctx, cancel := context.WithTimeout(ctx, updateTimeout)
	defer cancel()
	raw, err := h.Request(ctx, machineID, "daemon.update", map[string]any{"if_idle": true})
	if err != nil {
		return AglUpdate{}, err
	}
	var out AglUpdate
	if err := json.Unmarshal(raw, &out); err != nil {
		return AglUpdate{}, errors.New("bad daemon.update response")
	}
	return out, nil
}
