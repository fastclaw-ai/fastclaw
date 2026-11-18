package usermodels

import (
	"context"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type probeModels struct{ fail string }

func (p probeModels) Chat(_ context.Context, _ []provider.Message, _ []provider.Tool, model string, _ int, _ float64) (*provider.Response, error) {
	if provider.StripProviderPrefix(model) == p.fail {
		return &provider.Response{}, nil
	}
	return &provider.Response{Content: "OK"}, nil
}

func TestBYOKRequiresVerificationOfEveryModel(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewDBStore("sqlite", "file:"+t.TempDir()+"/models.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	p := Provider{ID: "mine", Name: "Mine", APIBase: "https://api.example.com/v1",
		APIType: "openai-completions", APIKey: "test-key", Models: []string{"first", "org/second"}}
	c := Config{Providers: []Provider{p}}
	run := func(fail string) string {
		t.Helper()
		failed, err := testConnection(ctx, st, "alice", p, func(Provider) modelChat {
			return probeModels{fail: fail}
		})
		if err != nil {
			t.Fatal(err)
		}
		return failed
	}
	if failed := run("org/second"); failed != "org/second" {
		t.Fatalf("failed model = %q", failed)
	}
	if err := SaveVerified(ctx, st, "alice", c); err == nil {
		t.Fatal("saved after partial verification")
	}
	if failed := run(""); failed != "" {
		t.Fatalf("unexpected failed model: %s", failed)
	}
	if err := SaveVerified(ctx, st, "bob", c); err == nil {
		t.Fatal("used another user's verification")
	}
	c.Providers[0].APIKey = "different-test-key"
	if err := SaveVerified(ctx, st, "alice", c); err == nil {
		t.Fatal("saved with a different key")
	}
	c.Providers[0] = p
	if err := SaveVerified(ctx, st, "alice", c); err != nil {
		t.Fatal(err)
	}
	c = View(c)
	c.DefaultModel = "mine/org/second"
	if err := SaveVerified(ctx, st, "alice", c); err != nil {
		t.Fatalf("changing the active model should not require retesting: %v", err)
	}
}
