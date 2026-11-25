//go:build windows

package tools

import (
	"context"
	"os/exec"
)

func hostShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "cmd.exe", "/d", "/s", "/c", command)
}
