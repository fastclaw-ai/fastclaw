package setup

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/users"
	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

// The design page's manual edits are relayed into <design>/scene/edits.json
// — and that is the only file this endpoint writes.
func TestSaveSceneEdits(t *testing.T) {
	ctx := context.Background()
	st, err := store.NewDBStore("sqlite", "file:"+filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveAgent(ctx, &store.AgentRecord{ID: "agt_1", UserID: "u_alice", Name: "a"}); err != nil {
		t.Fatal(err)
	}
	ws := workspace.NewLocalFS(t.TempDir())
	scene := "sessions/s-1/poster-editable/scene/scene.json"
	if err := ws.Put(ctx, "agt_1", "", "", scene, strings.NewReader("{}"), 2, ""); err != nil {
		t.Fatal(err)
	}
	s := &Server{dataStore: st, workspaceStore: ws}
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/agents/{id}/scene-edits/{path...}", s.handleSaveSceneEdits)

	put := func(user, path, body string) int {
		r := httptest.NewRequest(http.MethodPut, "/api/agents/agt_1/scene-edits/"+path, strings.NewReader(body))
		r = r.WithContext(auth.WithIdentity(r.Context(), auth.Identity{UserID: user, Role: users.RoleUser, AuthMethod: "session"}))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w.Code
	}
	edits := "sessions/s-1/poster-editable/scene/edits.json"
	cases := []struct {
		name, user, path, body string
		want                   int
	}{
		{"owner saves edits", "u_alice", edits, `{"changed":{}}`, http.StatusOK},
		{"not the owner", "u_bob", edits, `{}`, http.StatusForbidden},
		{"other file name", "u_alice", "sessions/s-1/poster-editable/scene/scene.json", `{}`, http.StatusBadRequest},
		{"not in a scene folder", "u_alice", "sessions/s-1/edits.json", `{}`, http.StatusBadRequest},
		{"no design there", "u_alice", "sessions/s-2/x-editable/scene/edits.json", `{}`, http.StatusNotFound},
		{"not JSON", "u_alice", edits, `nope`, http.StatusBadRequest},
		// ServeMux redirects ".." paths before the handler runs.
		{"escape attempt", "u_alice", "../../agt_2/scene/edits.json", `{}`, http.StatusMovedPermanently},
	}
	for _, c := range cases {
		if got := put(c.user, c.path, c.body); got != c.want {
			t.Errorf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
	rc, err := ws.Get(ctx, "agt_1", "", "", edits)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if b, _ := io.ReadAll(rc); string(b) != `{"changed":{}}` {
		t.Errorf("saved %q", b)
	}
}
