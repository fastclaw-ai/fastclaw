package connectors

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestPublicClientRefusesLocalAddresses(t *testing.T) {
	for _, u := range []string{"http://localhost:8080", "http://example.com", "ftp://example.com", "https://example.com/path"} {
		if _, err := NewPublicClient(u, "k"); err == nil {
			t.Errorf("%s accepted", u)
		}
	}
	if _, err := NewPublicClient("https://connany.example.com", "k"); err != nil {
		t.Errorf("https origin refused: %v", err)
	}
	// Resolves to loopback: refused at dial time.
	for _, u := range []string{"https://127.0.0.1:9", "https://localhost:9", "https://10.0.0.1:9"} {
		if err := ProbePublic(context.Background(), u, "k"); Code(err) != "unreachable" {
			t.Errorf("%s → %v, want unreachable", u, err)
		}
	}
}

func TestForPrefersTheUsersOwnService(t *testing.T) {
	_, srv := newFake(t)
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	old := newUserService
	newUserService = func(ctx context.Context, baseURL, apiKey string, db DB) (*Service, error) {
		return newService(ctx, baseURL, apiKey, db, false) // the fake is on localhost
	}
	t.Cleanup(func() {
		newUserService = old
		_ = Configure(ctx, "", "", nil)
		userMu.Lock()
		userSvc = map[string]userEntry{}
		userMu.Unlock()
	})

	if _, err := Apply(ctx, st); err != nil {
		t.Fatal(err)
	}
	if svc, src := ForWithSource(ctx, "u1"); svc != nil || src != "" {
		t.Fatalf("nothing configured → %v %q", svc, src)
	}

	// Their own key works with no deployment service at all.
	if err := SaveUserSettings(ctx, st, "u1", Settings{URL: srv.URL, APIKey: "key"}); err != nil {
		t.Fatal(err)
	}
	own, src := ForWithSource(ctx, "u1")
	if own == nil || src != "user" {
		t.Fatalf("own key → %v %q", own, src)
	}
	if _, _, err := own.Catalog(ctx, "en", true); err != nil {
		t.Fatalf("catalog through own key: %v", err)
	}
	if svc := For(ctx, "u2"); svc != nil {
		t.Fatal("another user got u1's service")
	}

	// The deployment's service is the fallback; the user's own wins.
	if err := Configure(ctx, srv.URL, "key", st); err != nil {
		t.Fatal(err)
	}
	if _, src := ForWithSource(ctx, "u2"); src != "system" {
		t.Fatalf("u2 → %q, want system", src)
	}
	if svc, src := ForWithSource(ctx, "u1"); src != "user" || svc != own {
		t.Fatalf("u1 → %q (same service %v), want own", src, svc == own)
	}

	// A product's end user runs on the product account's Connany, never
	// their own key; a chatter (also owner-linked) is not an end user.
	for _, u := range []*store.UserRecord{
		{ID: "u1", Username: "u1", Email: "u1@x", Role: "user", Status: "active"},
		{ID: "eu1", Username: "eu1", Email: "eu1@x", Role: "app_user", Status: "active", OwnerUserID: "u1", ExternalID: "user:1"},
		{ID: "ch1", Username: "ch1", Email: "ch1@x", Role: "chatter", Status: "active", OwnerUserID: "u1"},
	} {
		if err := st.CreateUser(ctx, u); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveUserSettings(ctx, st, "eu1", Settings{URL: srv.URL, APIKey: "other"}); err != nil {
		t.Fatal(err)
	}
	if svc, src := ForWithSource(ctx, "eu1"); src != "account" || svc != own {
		t.Fatalf("end user → %q (account's service %v), want account", src, svc == own)
	}
	if _, src := ForWithSource(ctx, "ch1"); src != "system" {
		t.Fatalf("chatter → %q, want system", src)
	}

	if err := ClearUserSettings(ctx, st, "u1"); err != nil {
		t.Fatal(err)
	}
	if _, src := ForWithSource(ctx, "u1"); src != "system" {
		t.Fatalf("after clear → %q, want system", src)
	}
	if _, src := ForWithSource(ctx, "eu1"); src != "system" {
		t.Fatalf("end user after the account cleared → %q, want system", src)
	}
	// The system row is untouched by a user's save / clear.
	if _, ok, _ := LoadSettings(ctx, st); ok {
		t.Fatal("user settings leaked into the system row")
	}
}
