//go:build unix

package tools

import (
	"context"
	"os/exec"
)

func hostShellCommand(ctx context.Context, command string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", command)
}
