package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestHostShellCommandUsesPlatformShell(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := hostShellCommand(ctx, "echo host-shell-ok").CombinedOutput()
	if err != nil {
		t.Fatalf("host shell command failed: %v (%q)", err, out)
	}
	if !strings.Contains(string(out), "host-shell-ok") {
		t.Fatalf("host shell output = %q, want marker", out)
	}
}
