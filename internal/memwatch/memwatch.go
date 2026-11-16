// Package memwatch keeps evidence of memory spikes. The cloud pod was
// OOM-killed twice in four minutes (heap 74 MiB to 512 MiB in under a
// minute) with nothing in the logs to say what grew. When the heap
// passes a threshold, a heap profile and a goroutine dump go to disk,
// which outlives the process, at most once per cooldown.
//
// Settings (env):
//
//	FASTCLAW_MEMWATCH_MB     heap threshold in MiB (default 600; 0 = off)
//	FASTCLAW_PPROF_ADDR      serve net/http/pprof here, e.g. 127.0.0.1:6060
//	                         (off by default; reach it with kubectl port-forward)
package memwatch

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	rpprof "runtime/pprof"
	"strconv"
	"strings"
	"time"
)

const (
	interval = 10 * time.Second
	cooldown = 10 * time.Minute
	keep     = 6 // newest profiles kept on disk
)

// Start runs the watcher (and pprof, when configured) until ctx ends.
// dir is where profiles go; it should be on persistent storage.
func Start(ctx context.Context, dir string) {
	if addr := strings.TrimSpace(os.Getenv("FASTCLAW_PPROF_ADDR")); addr != "" {
		go servePprof(addr)
	}
	limitMB := 600
	if v := strings.TrimSpace(os.Getenv("FASTCLAW_MEMWATCH_MB")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limitMB = n
		}
	}
	if limitMB <= 0 || dir == "" {
		return
	}
	go watch(ctx, dir, uint64(limitMB)<<20)
}

func servePprof(addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	slog.Info("pprof listening", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Warn("pprof stopped", "error", err)
	}
}

func watch(ctx context.Context, dir string, limit uint64) {
	t := time.NewTicker(interval)
	defer t.Stop()
	var last time.Time
	var ms runtime.MemStats
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		runtime.ReadMemStats(&ms)
		if ms.HeapAlloc < limit || time.Since(last) < cooldown {
			continue
		}
		last = time.Now()
		slog.Warn("memory high: writing heap profile",
			"heap_mb", ms.HeapAlloc>>20, "sys_mb", ms.Sys>>20,
			"goroutines", runtime.NumGoroutine(), "dir", dir)
		if err := dump(dir, last); err != nil {
			slog.Warn("memwatch: dump failed", "error", err)
		}
	}
}

func dump(dir string, at time.Time) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	stamp := at.UTC().Format("20060102-150405")
	for _, p := range []struct{ name, file string }{
		{"heap", "heap-" + stamp + ".pprof"},
		{"goroutine", "goroutine-" + stamp + ".pprof"},
	} {
		f, err := os.Create(filepath.Join(dir, p.file))
		if err != nil {
			return err
		}
		werr := rpprof.Lookup(p.name).WriteTo(f, 0)
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return fmt.Errorf("%s: %w", p.name, werr)
		}
	}
	prune(dir)
	return nil
}

// prune keeps the newest `keep` dumps of each kind.
func prune(dir string) {
	for _, prefix := range []string{"heap-", "goroutine-"} {
		matches, _ := filepath.Glob(filepath.Join(dir, prefix+"*.pprof"))
		// Names sort by time (UTC stamp).
		for i := 0; i < len(matches)-keep; i++ {
			_ = os.Remove(matches[i])
		}
	}
}
