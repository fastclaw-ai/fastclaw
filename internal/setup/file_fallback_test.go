package setup

import "testing"

func TestLegacyRootFallback(t *testing.T) {
	cases := map[string]string{
		"sessions/s-1/beauty.png":     "beauty.png",
		"sessions/s-1/sub/beauty.png": "",
		"sessions/s-1/..":             "",
		"sessions/s-1/":               "",
		"beauty.png":                  "",
		"projects/p/s-1/beauty.png":   "",
	}
	for in, want := range cases {
		if got := legacyRootFallback(in); got != want {
			t.Errorf("legacyRootFallback(%q) = %q, want %q", in, got, want)
		}
	}
}
