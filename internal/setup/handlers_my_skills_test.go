package setup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// mySkillBody is a multipart upload of filename with the given files
// (zipped unless filename ends in .md, where files["SKILL.md"] is sent).
func mySkillBody(t *testing.T, filename string, files map[string]string) (*bytes.Buffer, string) {
	t.Helper()
	var content []byte
	if strings.HasSuffix(filename, ".md") {
		content = []byte(files["SKILL.md"])
	} else {
		var zbuf bytes.Buffer
		zw := zip.NewWriter(&zbuf)
		for name, text := range files {
			f, err := zw.Create(name)
			if err != nil {
				t.Fatal(err)
			}
			f.Write([]byte(text))
		}
		zw.Close()
		content = zbuf.Bytes()
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, _ := mw.CreateFormFile("file", filename)
	part.Write(content)
	mw.Close()
	return &body, mw.FormDataContentType()
}

func skillMD(name, desc string) string {
	return "---\nname: " + name + "\ndescription: " + desc + "\n---\n# " + name + "\nDo the thing.\n"
}

func TestMySkillsForEndUsers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTCLAW_HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "skills", "image-tool"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := NewServer(0)
	endUser := func(uid string) auth.Identity {
		// As auth.SwitchToAppUser leaves it for X-Fastclaw-End-User.
		return auth.Identity{UserID: uid, Role: users.RoleAppUser, AuthMethod: "apikey", APIKeyID: "k1", AppUserID: "u_owner", EndUser: "user:" + uid}
	}
	alice, bob := endUser("u_alice"), endUser("u_bob")

	upload := func(id auth.Identity, filename string, files map[string]string) (int, map[string]any) {
		body, ct := mySkillBody(t, filename, files)
		req := httptest.NewRequest(http.MethodPost, "/api/me/skills", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		s.handleUploadMySkill(rec, req.WithContext(auth.WithIdentity(req.Context(), id)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	list := func(id auth.Identity) []any {
		req := httptest.NewRequest(http.MethodGet, "/api/me/skills", nil)
		rec := httptest.NewRecorder()
		s.handleListMySkills(rec, req.WithContext(auth.WithIdentity(req.Context(), id)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		items, _ := out["skills"].([]any)
		return items
	}

	// A zip with a wrapping folder, SKILL.md and a reference file.
	code, out := upload(alice, "writer.zip", map[string]string{
		"writer/SKILL.md":        skillMD("writer", "Write in my style"),
		"writer/refs/samples.md": "Example posts",
	})
	if code != http.StatusOK {
		t.Fatalf("upload: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(home, "users", "u_alice", "skills", "writer", "refs", "samples.md")); err != nil {
		t.Fatalf("not in alice's personal layer: %v", err)
	}
	// A bare SKILL.md works too.
	if code, out := upload(alice, "SKILL.md", map[string]string{"SKILL.md": skillMD("Notes-Helper", "Keep notes")}); code != http.StatusOK {
		t.Fatalf("bare SKILL.md: %d %v", code, out)
	}
	if got := list(alice); len(got) != 2 {
		t.Fatalf("alice lists %v", got)
	}
	// Only the person who uploaded a skill has it.
	if got := list(bob); len(got) != 0 {
		t.Fatalf("bob sees alice's skills: %v", got)
	}

	// Instructions only: no scripts, binaries or hidden files.
	for _, bad := range []map[string]string{
		{"x/SKILL.md": skillMD("x", "d"), "x/run.py": "print(1)"},
		{"x/SKILL.md": skillMD("x", "d"), "x/scripts/a.sh": "env"},
		{"x/SKILL.md": skillMD("x", "d"), "x/.env": "A=1"},
		{"x/SKILL.md": skillMD("x", "d"), "x/data.json": "\xff\xfe"},
	} {
		if code, out := upload(alice, "x.zip", bad); code != http.StatusBadRequest || out["code"] != "file_not_allowed" {
			t.Fatalf("%v: %d %v", bad, code, out)
		}
	}
	for files, want := range map[string]map[string]string{
		"no_skill_md":         {"x/README.md": "hi"},
		"invalid_frontmatter": {"x/SKILL.md": "# no frontmatter"},
		"invalid_name":        {"写作/SKILL.md": "---\nname: 写作助手\ndescription: d\n---\n"},
		"name_reserved":       {"image-tool/SKILL.md": skillMD("image-tool", "fake")},
	} {
		if code, out := upload(alice, "x.zip", want); code != http.StatusBadRequest || out["code"] != files {
			t.Fatalf("want %s, got %d %v", files, code, out)
		}
	}
	if _, err := os.Stat(filepath.Join(home, "skills", "image-tool", "SKILL.md")); err == nil {
		t.Fatal("upload reached the global skills")
	}

	// Re-uploading a name replaces it whole.
	code, out = upload(alice, "writer.zip", map[string]string{"writer/SKILL.md": skillMD("writer", "v2")})
	if code != http.StatusOK || out["replaced"] != true {
		t.Fatalf("replace: %d %v", code, out)
	}
	if _, err := os.Stat(filepath.Join(home, "users", "u_alice", "skills", "writer", "refs")); err == nil {
		t.Fatal("old files survived the replace")
	}

	// Delete: bob can't remove alice's; alice can.
	del := func(id auth.Identity, name string) int {
		req := httptest.NewRequest(http.MethodDelete, "/api/me/skills/"+name, nil)
		req.SetPathValue("name", name)
		rec := httptest.NewRecorder()
		s.handleDeleteMySkill(rec, req.WithContext(auth.WithIdentity(req.Context(), id)))
		return rec.Code
	}
	if c := del(bob, "writer"); c != http.StatusNotFound {
		t.Fatalf("bob deleted alice's skill: %d", c)
	}
	if c := del(alice, ".."); c != http.StatusBadRequest {
		t.Fatalf("..: %d", c)
	}
	if c := del(alice, "writer"); c != http.StatusOK {
		t.Fatalf("delete: %d", c)
	}
	if got := list(alice); len(got) != 1 {
		t.Fatalf("after delete: %v", got)
	}
}

func TestMySkillsLimit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTCLAW_HOME", home)
	s := NewServer(0)
	id := auth.Identity{UserID: "u_c", Role: users.RoleUser, AuthMethod: "session"}
	for i := 0; i <= maxMySkills; i++ {
		name := "s" + strings.Repeat("x", i)
		body, ct := mySkillBody(t, name+".zip", map[string]string{name + "/SKILL.md": skillMD(name, "d")})
		req := httptest.NewRequest(http.MethodPost, "/api/me/skills", body)
		req.Header.Set("Content-Type", ct)
		rec := httptest.NewRecorder()
		s.handleUploadMySkill(rec, req.WithContext(auth.WithIdentity(req.Context(), id)))
		if i < maxMySkills && rec.Code != http.StatusOK {
			t.Fatalf("#%d: %d %s", i, rec.Code, rec.Body.String())
		}
		if i == maxMySkills && !strings.Contains(rec.Body.String(), "too_many") {
			t.Fatalf("over the limit: %d %s", rec.Code, rec.Body.String())
		}
	}
}
