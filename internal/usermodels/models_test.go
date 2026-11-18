package usermodels

import (
	"context"
	"encoding/json"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"strings"
	"testing"
)

func TestBYOKIsolationAndSwitch(t *testing.T) {
	t.Setenv("CONFIG_ENCRYPTION_KEY", "test-only-encryption-secret")
	ctx := context.Background()
	st, err := store.NewDBStore("sqlite", "file:"+t.TempDir()+"/models.db")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	c := Config{Providers: []Provider{{ID: "mine", Name: "Mine", APIBase: "https://api.example.com/v1", APIType: "openai-completions", APIKey: "test-secret-key", Models: []string{"org/model"}}}, DefaultModel: "mine/org/model"}
	if err = Save(ctx, st, "alice", c); err != nil {
		t.Fatal(err)
	}
	row, err := st.GetConfigByName(ctx, store.KindSetting, "alice", "", Namespace)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(row.Data)
	if strings.Contains(string(raw), "test-secret-key") {
		t.Fatal("plaintext key stored")
	}
	view := View(c)
	b, _ := json.Marshal(view)
	if strings.Contains(string(b), "test-secret") || !view.Providers[0].HasKey {
		t.Fatal("key leaked or key flag missing")
	}
	rc := config.ResolvedAgent{Model: "system/model"}
	if err = Apply(ctx, st, "bob", &rc); err != nil || rc.Model != "system/model" {
		t.Fatalf("other user affected: %+v %v", rc, err)
	}
	if err = Apply(ctx, st, "alice", &rc); err != nil || rc.Model != "byok-mine/org/model" || !rc.Providers["byok-mine"].BYOK {
		t.Fatalf("override failed: %+v %v", rc, err)
	}
	if err = Save(ctx, st, "alice", view); err != nil {
		t.Fatal(err)
	}
	saved, _ := Load(ctx, st, "alice")
	if saved.Providers[0].APIKey != "test-secret-key" {
		t.Fatal("blank key did not preserve saved secret")
	}
	view.Providers[0].APIBase = "https://changed.example.com"
	if err = Save(ctx, st, "alice", view); err == nil {
		t.Fatal("secret copied to changed endpoint")
	}
	c.DefaultModel = "system/model"
	if err = Save(ctx, st, "alice", c); err == nil {
		t.Fatal("selected system key through BYOK")
	}
	c.DefaultModel = ""
	if err = Save(ctx, st, "alice", c); err != nil {
		t.Fatal(err)
	}
	rc = config.ResolvedAgent{Model: "system/model"}
	if err = Apply(ctx, st, "alice", &rc); err != nil || rc.Model != "system/model" {
		t.Fatal("switch to system failed")
	}
}
