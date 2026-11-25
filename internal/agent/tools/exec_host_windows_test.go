//go:build windows

package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunHostCommandTimeoutCancelsWindowsShell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := runHostCommand(
		ctx,
		"ping -n 10 127.0.0.1 >NUL 2>NUL",
		"",
		buildSubprocessEnv(nil),
		500*time.Millisecond,
	)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected a timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected an actionable timeout error, got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("runHostCommand blocked %s after its 500ms deadline", elapsed)
	}
}
