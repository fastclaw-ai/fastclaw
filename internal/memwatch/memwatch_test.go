package memwatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDumpWritesProfilesAndPrunes(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 11, 16, 4, 0, 0, 0, time.UTC)
	for i := 0; i < keep+2; i++ {
		if err := dump(dir, base.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
	for _, prefix := range []string{"heap-", "goroutine-"} {
		m, _ := filepath.Glob(filepath.Join(dir, prefix+"*.pprof"))
		if len(m) != keep {
			t.Fatalf("%s files = %d, want %d", prefix, len(m), keep)
		}
		if filepath.Base(m[0]) != prefix+"20261116-040200.pprof" {
			t.Errorf("oldest kept %s = %s", prefix, filepath.Base(m[0]))
		}
		st, err := os.Stat(m[len(m)-1])
		if err != nil || st.Size() == 0 {
			t.Errorf("%s profile empty", prefix)
		}
	}
}
