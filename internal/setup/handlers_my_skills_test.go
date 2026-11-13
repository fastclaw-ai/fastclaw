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
	"reflect"
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

	// The whole skill folder goes up: scripts, templates and images too.
	if code, out := upload(alice, "ppt.zip", map[string]string{
		"ppt-main/SKILL.md":            skillMD("ppt", "Make slides"),
		"ppt-main/.gitignore":          "node_modules",
		"ppt-main/scripts/check.mjs":   "console.log(1)",
		"ppt-main/assets/bg.webp":      "\xff\xfe\x00",
		"ppt-main/.git/config":         "[core]",
		"__MACOSX/ppt-main/._SKILL.md": "junk",
	}); code != http.StatusOK {
		t.Fatalf("full folder: %d %v", code, out)
	}
	pptDir := filepath.Join(home, "users", "u_alice", "skills", "ppt")
	for _, f := range []string{"scripts/check.mjs", "assets/bg.webp", ".gitignore"} {
		if _, err := os.Stat(filepath.Join(pptDir, f)); err != nil {
			t.Fatalf("%s not kept: %v", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(pptDir, ".git")); err == nil {
		t.Fatal(".git was kept")
	}
	// SKILL.md in a subfolder: that folder is the skill, the rest is left out.
	if code, out := upload(alice, "repo.zip", map[string]string{
		"repo/README.md":                 "about",
		"repo/skills/deep/SKILL.md":      skillMD("deep", "Nested"),
		"repo/skills/deep/refs/notes.md": "n",
	}); code != http.StatusOK {
		t.Fatalf("nested: %d %v", code, out)
	}
	deepDir := filepath.Join(home, "users", "u_alice", "skills", "deep")
	if _, err := os.Stat(filepath.Join(deepDir, "refs", "notes.md")); err != nil {
		t.Fatalf("nested files: %v", err)
	}
	if _, err := os.Stat(filepath.Join(deepDir, "README.md")); err == nil {
		t.Fatal("files outside the skill folder were kept")
	}
	for _, name := range []string{"ppt", "deep"} {
		del := httptest.NewRequest(http.MethodDelete, "/api/me/skills/"+name, nil)
		del.SetPathValue("name", name)
		s.handleDeleteMySkill(httptest.NewRecorder(), del.WithContext(auth.WithIdentity(del.Context(), alice)))
	}
	if code, out := upload(alice, "x.zip", map[string]string{"SKILL.md": skillMD("x", "d"), "../evil.md": "x"}); code != http.StatusBadRequest || out["code"] != "file_not_allowed" {
		t.Fatalf("traversal: %d %v", code, out)
	}

	for files, want := range map[string]map[string]string{
		"no_skill_md":         {"x/README.md": "hi"},
		"multiple_skills":     {"a/SKILL.md": skillMD("a", "d"), "b/SKILL.md": skillMD("b", "d")},
		"invalid_frontmatter": {"x/SKILL.md": "# no frontmatter"},
		"invalid_name":        {"写作/SKILL.md": "---\nname: 写作助手\ndescription: d\n---\n"},
	} {
		if code, out := upload(alice, "x.zip", want); code != http.StatusBadRequest || out["code"] != files {
			t.Fatalf("want %s, got %d %v", files, code, out)
		}
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

// GET /api/me/skills walks the chain: mine > product (the api key's owner)
// > system, one entry per name. A personal skill may take a global name.
func TestMySkillsListMergesLayers(t *testing.T) {
	home := t.TempDir()
	t.Setenv("FASTCLAW_HOME", home)
	write := func(dir, name, desc string) {
		if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatal(err)
		}
		os.WriteFile(filepath.Join(dir, name, "SKILL.md"), []byte(skillMD(name, desc)), 0o644)
	}
	global := filepath.Join(home, "skills")
	product := filepath.Join(home, "users", "u_owner", "skills")
	write(global, "image-tool", "system")
	write(global, "ppt", "system")
	write(product, "ppt", "product")
	write(product, "brand", "product")
	s := NewServer(0)
	alice := auth.Identity{UserID: "u_alice", Role: users.RoleAppUser, AuthMethod: "apikey", APIKeyID: "k1", AppUserID: "u_owner", EndUser: "user:u_alice"}
	owner := auth.Identity{UserID: "u_owner", Role: users.RoleUser, AuthMethod: "apikey", APIKeyID: "k1"}

	body, ct := mySkillBody(t, "x.zip", map[string]string{"image-tool/SKILL.md": skillMD("image-tool", "mine")})
	req := httptest.NewRequest(http.MethodPost, "/api/me/skills", body)
	req.Header.Set("Content-Type", ct)
	rec := httptest.NewRecorder()
	s.handleUploadMySkill(rec, req.WithContext(auth.WithIdentity(req.Context(), alice)))
	if rec.Code != http.StatusOK {
		t.Fatalf("override a global name: %d %s", rec.Code, rec.Body.String())
	}
	if data, _ := os.ReadFile(filepath.Join(global, "image-tool", "SKILL.md")); !strings.Contains(string(data), "system") {
		t.Fatal("upload touched the global skill")
	}

	list := func(id auth.Identity) (map[string]string, map[string]any) {
		req := httptest.NewRequest(http.MethodGet, "/api/me/skills", nil)
		rec := httptest.NewRecorder()
		s.handleListMySkills(rec, req.WithContext(auth.WithIdentity(req.Context(), id)))
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		got := map[string]string{}
		items, _ := out["skills"].([]any)
		for _, it := range items {
			m := it.(map[string]any)
			if _, dup := got[m["name"].(string)]; dup {
				t.Fatalf("%v listed twice", m["name"])
			}
			got[m["name"].(string)] = m["source"].(string) + ":" + m["description"].(string)
		}
		return got, out
	}
	got, out := list(alice)
	want := map[string]string{"image-tool": "mine:mine", "ppt": "product:product", "brand": "product:product"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("alice: %v", got)
	}
	if out["mine"] != float64(1) {
		t.Fatalf("mine count: %v", out["mine"])
	}
	// The owner's own skills are theirs ("mine"), over the global ones.
	if got, _ := list(owner); got["ppt"] != "mine:product" || got["image-tool"] != "system:system" {
		t.Fatalf("owner: %v", got)
	}
}
