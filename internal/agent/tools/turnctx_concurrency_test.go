package tools

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// recordingStore is a minimal workspace.Store that records every Put's
// resolved (projectID, sessionID, path) + content. Used by the concurrency
// tests to prove per-turn scoping doesn't race across concurrent turns.
type recordingStore struct {
	mu   sync.Mutex
	puts []recordedPut
}

type recordedPut struct {
	projectID string
	sessionID string
	path      string
	content   string
}

func (s *recordingStore) Put(_ context.Context, _, projectID, sessionID, path string, r io.Reader, _ int64, _ string) error {
	b, _ := io.ReadAll(r)
	s.mu.Lock()
	s.puts = append(s.puts, recordedPut{projectID: projectID, sessionID: sessionID, path: path, content: string(b)})
	s.mu.Unlock()
	return nil
}
func (s *recordingStore) Get(context.Context, string, string, string, string) (io.ReadCloser, error) {
	return nil, nil
}
func (s *recordingStore) Stat(context.Context, string, string, string, string) ( *workspace.ObjectInfo, error) {
	return nil, nil
}
func (s *recordingStore) List(context.Context, string, string, string) ([]workspace.ObjectInfo, error) {
	return nil, nil
}
func (s *recordingStore) Delete(context.Context, string, string, string, string) error { return nil }
func (s *recordingStore) Move(context.Context, string, string, string, string, string) error {
	return nil
}
func (s *recordingStore) SignedURL(context.Context, string, string, string, string, time.Duration) (string, error) {
	return "", nil
}

// TestWriteFile_ConcurrentTurnsNoCrossContamination is the headline P2
// regression test. Before the TurnContext refactor, write_file read its
// session scoping off shared Registry fields that bindSession mutated
// per turn — so two concurrent turns on one agent could interleave and
// land a chatter's file in another chatter's session scope. Now each
// turn attaches its own TurnContext to ctx, and every scope resolution
// reads from ctx. This test runs N concurrent writes with distinct
// TurnContexts through ONE shared Registry and asserts every Put landed
// in its own scope.
//
// Run with -race to also catch any data race on shared state.
func TestWriteFile_ConcurrentTurnsNoCrossContamination(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	r.agentID = "ag_shared"
	store := &recordingStore{}
	r.SetWorkspaceStore(store, "ag_shared")

	// Each "turn" is a distinct chatter + session. The shared Registry
	// is the same instance for all of them — exactly the public-agent /
	// multi-session scenario that used to race.
	turns := []struct {
		chatter, session, project, path, content string
	}{
		{"u_alice", "s-aaa", "", "notes.md", "alice's notes"},
		{"u_bob", "s-bbb", "", "notes.md", "bob's notes"},
		{"u_carol", "s-ccc", "proj-1", "report.md", "carol's report"},
		{"u_dave", "s-ddd", "", "draft.md", "dave's draft"},
		{"u_eve", "s-eee", "proj-2", "spec.md", "eve's spec"},
	}

	var wg sync.WaitGroup
	for _, tn := range turns {
		wg.Add(1)
		go func(tn struct {
			chatter, session, project, path, content string
		}) {
			defer wg.Done()
			ctx := WithTurnContext(context.Background(), &TurnContext{
				ChatterUserID: tn.chatter,
				SessionID:     tn.session,
				ProjectID:     tn.project,
			})
			args := `{"path":"` + tn.path + `","content":"` + tn.content + `"}`
			if _, err := r.Execute(ctx, "write_file", args); err != nil {
				t.Errorf("write_file for %s: %v", tn.chatter, err)
			}
		}(tn)
	}
	wg.Wait()

	// Every recorded Put must carry its own turn's scope — no leakage.
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.puts) != len(turns) {
		t.Fatalf("got %d puts, want %d", len(store.puts), len(turns))
	}
	seen := map[string]string{} // "project|session|path" → content
	for _, p := range store.puts {
		key := p.projectID + "|" + p.sessionID + "|" + p.path
		if prev, dup := seen[key]; dup {
			t.Errorf("duplicate scope key %q: %q then %q — concurrent turns collided", key, prev, p.content)
		}
		seen[key] = p.content
	}
	// Each turn's content must land under its own session/project.
	for _, tn := range turns {
		key := tn.project + "|" + tn.session + "|" + tn.path
		got, ok := seen[key]
		if !ok {
			t.Errorf("turn %s: no Put recorded at its own scope %q — write lost", tn.chatter, key)
			continue
		}
		if got != tn.content {
			t.Errorf("turn %s: content at %q = %q, want %q — cross-chatter contamination",
				tn.chatter, key, got, tn.content)
		}
	}
}

// TestIdentityFileGate_ConcurrentAdminFlags proves the admin gate no
// longer races: an admin turn and a non-admin turn running concurrently
// must each see their OWN callerIsAdmin, so a non-admin chatter can't
// transiently inherit an admin's flag (which would let them read SOUL.md).
//
// The concurrent goroutines hammer the gate from many turns at once (to
// surface any data race under -race); the correctness assertion is that
// every admin ctx resolves to "allowed" and every non-admin ctx to
// "blocked", regardless of interleaving.
func TestIdentityFileGate_ConcurrentAdminFlags(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())

	const goroutines = 50
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		// Admin turn: SOUL.md read must be allowed.
		go func() {
			defer wg.Done()
			ctx := WithTurnContext(context.Background(), &TurnContext{CallerIsAdmin: true})
			if r.identityFileBlocked(ctx, "SOUL.md") {
				t.Errorf("admin turn: SOUL.md read wrongly blocked")
			}
		}()
		// Non-admin turn: SOUL.md read must be blocked. The critical
		// assertion is that this NEVER flips to allowed because of a
		// racing admin turn that used to share the registry field.
		go func() {
			defer wg.Done()
			ctx := WithTurnContext(context.Background(), &TurnContext{CallerIsAdmin: false})
			if !r.identityFileBlocked(ctx, "SOUL.md") {
				t.Errorf("SECURITY: non-admin turn: SOUL.md read allowed — admin flag raced")
			}
		}()
	}
	wg.Wait()
}
