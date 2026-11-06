package agl

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCompareVersions(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.3.3", "0.3.5", -1}, {"0.3.10", "0.3.9", 1}, {"0.3.5", "0.3.5", 0},
		{"agl 0.3.5", "0.3.5", 0}, {"0.4", "0.3.9", 1}, {"1.0.0", "1", 0},
	} {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
	if !UpdateAvailable("0.3.3", "0.3.5") || UpdateAvailable("0.3.5", "0.3.5") || UpdateAvailable("", "0.3.5") || UpdateAvailable("0.3.3", "") {
		t.Fatal("UpdateAvailable")
	}
	if RemoteUpdateSupported("0.3.4") || !RemoteUpdateSupported("0.3.5") || !RemoteUpdateSupported("0.4.0") {
		t.Fatal("RemoteUpdateSupported")
	}
}

func TestLatestReleaseFromRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://github.com/thinkany-ai/agl/releases/tag/v0.3.5", http.StatusFound)
	}))
	defer srv.Close()
	l := newLatestRelease(srv.URL)
	if v := l.get(); v != "" {
		t.Fatalf("first read waits for nothing: %q", v)
	}
	waitFor(t, "latest version", func() bool { return l.get() == "0.3.5" })
	l.mu.Lock()
	l.checked = time.Now()
	l.mu.Unlock()
}
