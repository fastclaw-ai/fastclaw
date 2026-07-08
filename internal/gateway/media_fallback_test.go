package gateway

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// memWorkspace is a minimal in-memory workspace.Store for the media
// fallback tests. Unlike the fakeWorkspace in internal/sandbox, this one
// tracks ModTime per object so we can exercise both the path-diff primary
// path and the mtime fallback path. The curAgent/curProj/curSess fields
// are set per-test via withScope so the put()/setModTime() test helpers
// can scope themselves without the caller threading the tuple every call.
type memWorkspace struct {
	mu                         sync.Mutex
	objects                    map[string]map[string]wsObj // scopeKey → path → object
	curAgent, curProj, curSess string
}

type wsObj struct {
	data    []byte
	modTime time.Time
}

func newMemWorkspace() *memWorkspace {
	return &memWorkspace{objects: map[string]map[string]wsObj{}}
}

func wsScope(agentID, projectID, sessionID string) string {
	if projectID != "" {
		return agentID + "|p:" + projectID
	}
	return agentID + "|s:" + sessionID
}

// withScope pins the (agent, project, session) tuple the put/setModTime
// test helpers scope against. Returns the receiver for chaining.
func (w *memWorkspace) withScope(agentID, projectID, sessionID string) *memWorkspace {
	w.curAgent, w.curProj, w.curSess = agentID, projectID, sessionID
	return w
}

// put writes an object into the currently-scoped slot with a caller-
// supplied mtime (so tests can model "old file" vs "fresh file").
func (w *memWorkspace) put(path string, data []byte, modTime time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	sk := wsScope(w.curAgent, w.curProj, w.curSess)
	if _, ok := w.objects[sk]; !ok {
		w.objects[sk] = map[string]wsObj{}
	}
	w.objects[sk][path] = wsObj{data: data, modTime: modTime}
}

// setModTime lets a test pin an object's ModTime to simulate the sandbox
// sync rewriting an old artifact with a fresh mtime (the root cause of
// the "stray mp3 re-attaches to unrelated replies" bug).
func (w *memWorkspace) setModTime(path string, t time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o := w.objects[wsScope(w.curAgent, w.curProj, w.curSess)][path]
	o.modTime = t
	w.objects[wsScope(w.curAgent, w.curProj, w.curSess)][path] = o
}

func (w *memWorkspace) Put(ctx context.Context, agentID, projectID, sessionID, p string, r io.Reader, _ int64, _ string) error {
	buf, _ := io.ReadAll(r)
	w.mu.Lock()
	defer w.mu.Unlock()
	sk := wsScope(agentID, projectID, sessionID)
	if _, ok := w.objects[sk]; !ok {
		w.objects[sk] = map[string]wsObj{}
	}
	w.objects[sk][p] = wsObj{data: buf, modTime: time.Now()}
	return nil
}

func (w *memWorkspace) Get(ctx context.Context, agentID, projectID, sessionID, p string) (io.ReadCloser, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o, ok := w.objects[wsScope(agentID, projectID, sessionID)][p]
	if !ok {
		return nil, workspace.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(o.data)), nil
}

func (w *memWorkspace) Stat(ctx context.Context, agentID, projectID, sessionID, p string) (*workspace.ObjectInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	o, ok := w.objects[wsScope(agentID, projectID, sessionID)][p]
	if !ok {
		return nil, workspace.ErrNotFound
	}
	return &workspace.ObjectInfo{Path: p, Size: int64(len(o.data)), ModTime: o.modTime}, nil
}

func (w *memWorkspace) List(ctx context.Context, agentID, projectID, sessionID string) ([]workspace.ObjectInfo, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []workspace.ObjectInfo
	for p, o := range w.objects[wsScope(agentID, projectID, sessionID)] {
		out = append(out, workspace.ObjectInfo{Path: p, Size: int64(len(o.data)), ModTime: o.modTime})
	}
	return out, nil
}

func (w *memWorkspace) Delete(ctx context.Context, agentID, projectID, sessionID, p string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	delete(w.objects[wsScope(agentID, projectID, sessionID)], p)
	return nil
}

func (w *memWorkspace) Move(ctx context.Context, agentID, fromProjectID, fromSessionID, toProjectID, toSessionID string) error {
	return nil
}

func (w *memWorkspace) SignedURL(ctx context.Context, agentID, projectID, sessionID, p string, ttl time.Duration) (string, error) {
	return "", workspace.ErrSignedURLUnsupported
}

// --- Tests ---

const (
	mfaAgent = "agt_test"
	mfaSess  = "wx_user_openid"
)

// TestAppendRecentWorkspaceMedia_StaleMtimeNotAttached is the regression
// test for the reported bug: an mp3 generated days ago sits in the
// session workspace; the sandbox sync rewrites it and stamps a fresh
// mtime ~= turnStart. With the path-diff signal (preTurnFiles) the old
// file must NOT be attached, even though its mtime now falls inside the
// turn window. The pre-fix mtime-only heuristic would attach it.
func TestAppendRecentWorkspaceMedia_StaleMtimeNotAttached(t *testing.T) {
	ws := newMemWorkspace().withScope(mfaAgent, "", mfaSess)
	ws.put("weekend_greeting.mp3", []byte("audio-bytes"), time.Now().Add(-4*24*time.Hour))

	turnStart := time.Now()
	// Pre-turn snapshot taken here, BEFORE the sandbox sync refreshes mtime.
	preTurn := snapshotWorkspaceFiles(context.Background(), ws, mfaAgent, "", mfaSess)
	if !preTurn["weekend_greeting.mp3"] {
		t.Fatalf("pre-turn snapshot should contain the mp3")
	}
	// Simulate the sandbox sync rewriting the old file with a fresh mtime.
	ws.setModTime("weekend_greeting.mp3", turnStart.Add(50*time.Millisecond))

	got := appendRecentWorkspaceMedia(context.Background(), ws, mfaAgent, "", mfaSess, turnStart, preTurn, nil)
	if len(got) != 0 {
		t.Fatalf("expected 0 attachments for stale file with refreshed mtime, got %d: %+v", len(got), got)
	}
}

// TestAppendRecentWorkspaceMedia_NewFileAttached verifies a file that's
// genuinely new this turn (absent from preTurnFiles) is attached.
func TestAppendRecentWorkspaceMedia_NewFileAttached(t *testing.T) {
	ws := newMemWorkspace().withScope(mfaAgent, "", mfaSess)
	turnStart := time.Now()
	// Realistic ordering: the turn produces a new file AFTER turnStart,
	// so the pre-turn snapshot is empty.
	preTurn := map[string]bool{}
	ws.put("cover.png", []byte("png-bytes"), turnStart.Add(100*time.Millisecond))

	got := appendRecentWorkspaceMedia(context.Background(), ws, mfaAgent, "", mfaSess, turnStart, preTurn, nil)
	if len(got) != 1 || got[0].Filename != "cover.png" {
		t.Fatalf("expected 1 attachment (cover.png), got %+v", got)
	}
}

// TestAppendRecentWorkspaceMedia_MtimeFallback verifies the legacy
// mtime-based heuristic still works when preTurnFiles is nil (the
// pre-turn List failed). A file with a fresh mtime should still attach.
func TestAppendRecentWorkspaceMedia_MtimeFallback(t *testing.T) {
	ws := newMemWorkspace().withScope(mfaAgent, "", mfaSess)
	turnStart := time.Now()
	ws.put("fresh.png", []byte("png-bytes"), turnStart.Add(100*time.Millisecond))

	// nil preTurnFiles → path-diff unavailable → mtime fallback engages.
	got := appendRecentWorkspaceMedia(context.Background(), ws, mfaAgent, "", mfaSess, turnStart, nil, nil)
	if len(got) != 1 || got[0].Filename != "fresh.png" {
		t.Fatalf("mtime fallback should attach fresh.png, got %+v", got)
	}
}

// TestAppendRecentWorkspaceMedia_NonShippableSkipped verifies the
// allowlist still excludes scratchpads (.md) even when the file is new.
func TestAppendRecentWorkspaceMedia_NonShippableSkipped(t *testing.T) {
	ws := newMemWorkspace().withScope(mfaAgent, "", mfaSess)
	turnStart := time.Now()
	preTurn := map[string]bool{}
	ws.put("todo.md", []byte("# todos"), turnStart.Add(100*time.Millisecond))

	got := appendRecentWorkspaceMedia(context.Background(), ws, mfaAgent, "", mfaSess, turnStart, preTurn, nil)
	if len(got) != 0 {
		t.Fatalf("non-shippable .md must not be attached, got %+v", got)
	}
}
