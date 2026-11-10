package setup

import "testing"

func TestValidReturnURL(t *testing.T) {
	for raw, want := range map[string]bool{
		"https://weclaw.im/devices":     true,
		"http://localhost:3000/devices": true,
		"http://127.0.0.1:3000/x":       true,
		"http://weclaw.im/devices":      false,
		"https://weclaw.im/devices#x":   false,
		"https://u:p@weclaw.im/":        false,
		"myapp://cb":                    false,
		"/devices":                      false,
	} {
		if got := validReturnURL(raw); got != want {
			t.Errorf("validReturnURL(%q) = %v, want %v", raw, got, want)
		}
	}
}
