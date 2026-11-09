package sandbox

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type memEvents struct {
	mu     sync.Mutex
	events []store.MeteredEvent
	target store.BillingTarget
}

func (m *memEvents) InsertMeteredEvent(_ context.Context, e *store.MeteredEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, *e)
	return nil
}

func (m *memEvents) BillingTarget(_ context.Context, uid string) (store.BillingTarget, error) {
	t := m.target
	t.AccountID = uid
	return t, nil
}

func TestSandboxOpsAreMetered(t *testing.T) {
	st := &memEvents{}
	turn := billing.NewService(st).NewTurn("u_end", "agt_1", "sess", "wechat")
	ctx := billing.WithTurn(context.Background(), turn)

	lp := NewLifecyclePool(newFakePool(), 0, 0)
	lp.Start()
	defer lp.CloseAll()
	ex, err := lp.Get(ctx, "agt_1", "", "sess")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ex.Exec(ctx, "echo hi", time.Second); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.WriteFile(ctx, "/workspace/a.txt", "x"); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.ReadFile(ctx, "/workspace/a.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := ex.ListDir(ctx, "/workspace"); err != nil {
		t.Fatal(err)
	}
	if len(st.events) != 4 {
		t.Fatalf("events = %+v", st.events)
	}
	for i, op := range []string{"exec", "write_file", "read_file", "list_dir"} {
		e := st.events[i]
		if e.Kind != "sandbox" || e.Provider != "fake" || e.Model != "" || e.Unit != "second" || e.Units < 0 ||
			e.Detail["op"] != op || e.TurnID != turn.ID || e.Channel != "wechat" {
			t.Fatalf("event %d = %+v", i, e)
		}
	}
}

func TestSandboxExecDeniedByHook(t *testing.T) {
	t.Setenv(billing.EnvAllowPrivate, "true")
	var body string
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		_, _ = io.WriteString(w, `{"allow": false, "message": "Sandbox time is used up."}`)
	}))
	defer hook.Close()
	st := &memEvents{target: store.BillingTarget{HookURL: hook.URL, HookSecret: "x"}}
	ctx := billing.WithTurn(context.Background(), billing.NewService(st).NewTurn("u", "agt_1", "s", "web"))

	inner := newFakePool()
	lp := NewLifecyclePool(inner, 0, 0)
	lp.Start()
	defer lp.CloseAll()
	ex, _ := lp.Get(ctx, "agt_1", "", "s")
	if _, err := ex.Exec(ctx, "make build", 30*time.Second); err == nil || err.Error() != "Sandbox time is used up." {
		t.Fatalf("exec = %v", err)
	}
	if atomic.LoadInt32(&inner.creates) != 0 || len(st.events) != 0 {
		t.Fatal("a denied command must not start the sandbox or record usage")
	}
	if !strings.Contains(body, `"stage":"tool","kind":"sandbox","provider":"fake"`) || !strings.Contains(body, `"estimate":{"units":30,"unit":"second"}`) {
		t.Fatalf("authorize body = %s", body)
	}
}
