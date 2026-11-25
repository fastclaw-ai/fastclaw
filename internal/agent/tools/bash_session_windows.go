//go:build windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

// Windows has no Setpgid analogue exposed via syscall.SysProcAttr — job
// objects exist but require non-trivial wiring. taskkill provides the
// process-tree semantics this package needs without adding a job-object
// implementation.
func setProcessGroup(cmd *exec.Cmd) {}

func killProcessGroup(groupLeaderPid int) error {
	if groupLeaderPid <= 0 {
		return nil
	}
	if err := exec.Command(
		"taskkill",
		"/PID",
		strconv.Itoa(groupLeaderPid),
		"/T",
		"/F",
	).Run(); err == nil {
		return nil
	}

	// Fall back to the direct child when taskkill is unavailable or the
	// process exited between the command and the lookup.
	process, err := os.FindProcess(groupLeaderPid)
	if err != nil {
		return err
	}
	if err := process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		return err
	}
	return nil
}
