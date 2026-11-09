package agent

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// countingPool counts sandbox creations and writes.
type countingPool struct {
	gets   atomic.Int32
	writes atomic.Int32
}

func (p *countingPool) Get(context.Context, string, string, string) (sandbox.Executor, error) {
	p.gets.Add(1)
	return &countingExecutor{pool: p}, nil
}
func (p *countingPool) Release(string, string, string) error { return nil }
func (p *countingPool) CloseAll()                            {}
func (p *countingPool) Backend() string                      { return "fake" }

type countingExecutor struct{ pool *countingPool }

func (e *countingExecutor) Exec(context.Context, string, time.Duration) (string, error) {
	return "", nil
}
func (e *countingExecutor) ReadFile(context.Context, string) (string, error) { return "", nil }
func (e *countingExecutor) WriteFile(context.Context, string, string) (string, error) {
	e.pool.writes.Add(1)
	return "", nil
}
func (e *countingExecutor) ListDir(context.Context, string) (string, error) { return "", nil }
func (e *countingExecutor) Backend() string                                 { return "fake" }
func (e *countingExecutor) Close() error                                    { return nil }

const pngDataURL = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// An attachment must not start a sandbox: the store has it, and a
// sandbox created later hydrates it from there.
func TestWriteSessionAttachmentsSkipsColdSandbox(t *testing.T) {
	inner := &countingPool{}
	pool := sandbox.NewLifecyclePool(inner, 0, 0)
	ws := workspace.NewLocalFS(t.TempDir())
	a := &Agent{name: "a", agentID: "a", workspaceStore: ws, sandboxPool: pool}

	ctx := context.Background()
	names := a.WriteSessionAttachments(ctx, "s1", "", []Attachment{{URL: pngDataURL, Name: "pic.png"}})
	if len(names) != 1 {
		t.Fatalf("names = %v", names)
	}
	if n := inner.gets.Load(); n != 0 {
		t.Fatalf("sandbox created %d times for an attachment", n)
	}
	if _, err := ws.Stat(ctx, "a", "", "s1", names[0]); err != nil {
		t.Fatalf("attachment not in store: %v", err)
	}

	// Once the sandbox is live, attachments go straight into it.
	ex, _ := pool.Get(ctx, "a", "", "s1")
	if _, err := ex.Exec(ctx, "true", time.Second); err != nil {
		t.Fatal(err)
	}
	before := inner.writes.Load()
	a.WriteSessionAttachments(ctx, "s1", "", []Attachment{{URL: pngDataURL, Name: "pic2.png"}})
	if inner.writes.Load() == before {
		t.Fatal("attachment not written into the live sandbox")
	}
}

// Without a store the sandbox is the only place the file can go.
func TestWriteSessionAttachmentsNoStoreWritesSandbox(t *testing.T) {
	inner := &countingPool{}
	a := &Agent{name: "a", sandboxPool: sandbox.NewLifecyclePool(inner, 0, 0)}
	a.WriteSessionAttachments(context.Background(), "s1", "", []Attachment{{URL: pngDataURL, Name: "pic.png"}})
	if inner.writes.Load() == 0 {
		t.Fatal("attachment not written to the sandbox")
	}
}
