package daemon

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Paths returns the daemon directory paths under ~/.fastclaw.
func Paths() (pidFile, logFile, logDir string, err error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", "", err
	}
	base := filepath.Join(home, ".fastclaw")
	logDir = filepath.Join(base, "logs")
	pidFile = filepath.Join(base, "fastclaw.pid")
	logFile = filepath.Join(logDir, "gateway.log")
	return
}

// Status represents the current daemon state.
type Status struct {
	Running bool
	PID     int
	Uptime  time.Duration
}

// GetStatus checks if the daemon is running.
func GetStatus() (*Status, error) {
	pidFile, _, _, err := Paths()
	if err != nil {
		return nil, err
	}

	pid, err := readPID(pidFile)
	if err != nil {
		return &Status{Running: false}, nil
	}

	if !isProcessAlive(pid) {
		// Stale PID file
		os.Remove(pidFile)
		return &Status{Running: false}, nil
	}

	// Estimate uptime from PID file mtime
	info, err := os.Stat(pidFile)
	var uptime time.Duration
	if err == nil {
		uptime = time.Since(info.ModTime())
	}

	return &Status{Running: true, PID: pid, Uptime: uptime}, nil
}

// Start launches the gateway as a background daemon with auto-restart.
func Start(addr string, port int) error {
	st, _ := GetStatus()
	if st != nil && st.Running {
		return fmt.Errorf("daemon already running (PID %d)", st.PID)
	}

	pidFile, logFile, logDir, err := Paths()
	if err != nil {
		return err
	}

	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	// Find our own binary
	bin, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find executable: %w", err)
	}
	bin, _ = filepath.EvalSymlinks(bin)

	// Open log file for stdout/stderr
	lf, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	// Launch the daemon wrapper process
	args := []string{"daemon", "__run", "--port", strconv.Itoa(port), "--addr", addr}
	cmd := exec.Command(bin, args...)
	cmd.Stdout = lf
	cmd.Stderr = lf
	setSysProcAttr(cmd) // platform-specific detach

	if err := cmd.Start(); err != nil {
		lf.Close()
		return fmt.Errorf("start daemon: %w", err)
	}

	// Write PID file
	if err := writePID(pidFile, cmd.Process.Pid); err != nil {
		lf.Close()
		return fmt.Errorf("write PID file: %w", err)
	}

	lf.Close()

	fmt.Printf("Daemon started (PID %d)\n", cmd.Process.Pid)
	fmt.Printf("Logs: %s\n", logFile)
	return nil
}

// RunLoop is the daemon wrapper that auto-restarts the gateway on crash.
// This is called internally by 'daemon __run'.
func RunLoop(addr string, port int) error {
	pidFile, logFile, _, err := Paths()
	if err != nil {
		return err
	}

	// Write our own PID (the wrapper)
	if err := writePID(pidFile, os.Getpid()); err != nil {
		return err
	}
	defer os.Remove(pidFile)

	bin, err := os.Executable()
	if err != nil {
		return err
	}
	bin, _ = filepath.EvalSymlinks(bin)

	const maxRestarts = 10
	const maxBackoff = 30 * time.Second
	const stableThreshold = 60 * time.Second

	consecutiveCrashes := 0
	backoff := time.Second

	for {
		startTime := time.Now()

		fmt.Fprintf(os.Stderr, "[daemon] starting gateway (addr %s, port %d) at %s\n", addr, port, startTime.Format(time.RFC3339))

		cmd := exec.Command(bin, "gateway", "--port", strconv.Itoa(port), "--addr", addr)
		// Inherit stdout/stderr (already redirected to log file)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr

		// Open log file for appending context
		lf, _ := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if lf != nil {
			cmd.Stdout = lf
			cmd.Stderr = lf
		}

		err := cmd.Run()

		if lf != nil {
			lf.Close()
		}

		elapsed := time.Since(startTime)

		if err == nil {
			// Clean exit
			fmt.Fprintf(os.Stderr, "[daemon] gateway exited cleanly\n")
			return nil
		}

		// Check if we were signaled to stop
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			if isCleanShutdown(exitErr) {
				fmt.Fprintln(os.Stderr, "[daemon] gateway received shutdown signal, stopping")
				return nil
			}
		}

		// Gateway crashed
		if elapsed >= stableThreshold {
			// Was stable long enough, reset backoff
			consecutiveCrashes = 0
			backoff = time.Second
		}

		consecutiveCrashes++
		if consecutiveCrashes >= maxRestarts {
			return fmt.Errorf("gateway crashed %d consecutive times, giving up", maxRestarts)
		}

		fmt.Fprintf(os.Stderr, "[daemon] gateway crashed after %s (attempt %d/%d), restarting in %s\n",
			elapsed.Round(time.Second), consecutiveCrashes, maxRestarts, backoff)

		time.Sleep(backoff)

		// Exponential backoff: 1s, 2s, 4s, 8s, 16s, 30s, 30s...
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// Stop sends SIGTERM to the daemon, waits up to 5s, then SIGKILL.
func Stop() error {
	pidFile, _, _, err := Paths()
	if err != nil {
		return err
	}

	pid, err := readPID(pidFile)
	if err != nil {
		return fmt.Errorf("daemon not running (no PID file)")
	}

	if !isProcessAlive(pid) {
		os.Remove(pidFile)
		return fmt.Errorf("daemon not running (stale PID file, cleaned up)")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}

	// Send SIGTERM
	if err := signalProcess(proc, "TERM"); err != nil {
		return fmt.Errorf("signal process %d: %w", pid, err)
	}

	// Wait up to 5s for clean shutdown
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessAlive(pid) {
			os.Remove(pidFile)
			fmt.Printf("Daemon stopped (PID %d)\n", pid)
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Force kill
	if err := signalProcess(proc, "KILL"); err != nil {
		// Might have already died
		if !isProcessAlive(pid) {
			os.Remove(pidFile)
			fmt.Printf("Daemon stopped (PID %d)\n", pid)
			return nil
		}
		return fmt.Errorf("kill process %d: %w", pid, err)
	}

	os.Remove(pidFile)
	fmt.Printf("Daemon killed (PID %d)\n", pid)
	return nil
}

// WritePIDFile writes the current process PID. Called from runGateway.
func WritePIDFile() error {
	pidFile, _, _, err := Paths()
	if err != nil {
		return err
	}
	return writePID(pidFile, os.Getpid())
}

// RemovePIDFile removes the PID file. Called on clean shutdown.
func RemovePIDFile() {
	pidFile, _, _, _ := Paths()
	if pidFile != "" {
		os.Remove(pidFile)
	}
}

func writePID(path string, pid int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Atomic write via temp file
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readPID(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks if process exists without actually signaling
	err = signalProcess(proc, "CHECK")
	return err == nil
}
