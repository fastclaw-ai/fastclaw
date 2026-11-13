package setup

import (
	"archive/zip"
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/auth"
	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
	"github.com/fastclaw-ai/fastclaw/internal/users"
)

// --- My skills: the caller's own skills, end users included ---
//
// A person's skills live in their personal layer
// (~/.fastclaw/users/<uid>/skills, object-store owner _user_<uid>), which
// the agent loop loads for every turn that person chats in, with any agent
// (agent.SkillsLoader.WithUserID with the chatter). So uploading here makes
// a skill usable in their own conversations only.
//
// Uploads are instructions only: SKILL.md plus text reference files. A
// script would run in the chatter's sandbox, which personal skills don't
// reach, and isn't something to take from anyone who can sign up.
//
//   GET    /api/me/skills          → {skills:[{name,description,files,bytes,updatedAt}], max}
//   POST   /api/me/skills          multipart `file`: a .zip of the skill folder, or a SKILL.md
//   DELETE /api/me/skills/{name}

const (
	maxMySkills       = 20
	maxMySkillUpload  = 2 << 20 // request body
	maxMySkillBytes   = 1 << 20 // all files, uncompressed
	maxMySkillFiles   = 50
	maxMySkillMDBytes = 100 << 10
)

// mySkillTextExts are the reference files a skill may carry.
var mySkillTextExts = map[string]bool{
	".md": true, ".markdown": true, ".txt": true, ".json": true,
	".yaml": true, ".yml": true, ".csv": true, ".tsv": true,
}

var mySkillNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}$`)

func (s *Server) registerMySkillRoutes(mux *http.ServeMux, auth func(http.HandlerFunc) http.HandlerFunc) {
	mux.HandleFunc("GET /api/me/skills", auth(s.handleListMySkills))
	mux.HandleFunc("POST /api/me/skills", auth(s.handleUploadMySkill))
	mux.HandleFunc("DELETE /api/me/skills/{name}", auth(s.handleDeleteMySkill))
}

func mySkillError(w http.ResponseWriter, status int, code, detail string) {
	body := map[string]any{"ok": false, "error": code, "code": code}
	if detail != "" {
		body["detail"] = detail
	}
	jsonResponse(w, status, body)
}

// mySkillsCaller is the person whose skills a request manages: the signed-in
// user, or the end user a product acts for. Channel accounts have none.
func (s *Server) mySkillsCaller(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	ident, ok := auth.FromContext(r.Context())
	uid := ident.EffectiveUserID()
	if !ok || uid == "" {
		mySkillError(w, http.StatusUnauthorized, "unauthorized", "")
		return "", "", false
	}
	if ident.Role == users.RoleChannelUser {
		mySkillError(w, http.StatusForbidden, "forbidden", "")
		return "", "", false
	}
	dir, err := userSkillsDir(uid)
	if err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return "", "", false
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return "", "", false
	}
	// Another replica may have the latest copy.
	if s.workspaceStore != nil {
		if err := skills.HydrateSkillsDown(r.Context(), s.workspaceStore, skills.UserSkillOwner(uid), dir); err != nil {
			slog.Warn("my skills: hydrate", "user", uid, "error", err)
		}
	}
	return uid, dir, true
}

type mySkillView struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Files       int    `json:"files"`
	Bytes       int64  `json:"bytes"`
	UpdatedAt   int64  `json:"updatedAt"`
}

func readMySkills(dir string) []mySkillView {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []mySkillView
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		root := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(filepath.Join(root, "SKILL.md"))
		if err != nil {
			continue
		}
		v := mySkillView{Name: e.Name()}
		if fm, _ := agent.SplitSkillFrontmatter(data); fm != nil {
			v.Description = fm.Description
		}
		var latest time.Time
		_ = filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			if info, err := d.Info(); err == nil {
				v.Files++
				v.Bytes += info.Size()
				if info.ModTime().After(latest) {
					latest = info.ModTime()
				}
			}
			return nil
		})
		v.UpdatedAt = latest.UnixMilli()
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (s *Server) handleListMySkills(w http.ResponseWriter, r *http.Request) {
	_, dir, ok := s.mySkillsCaller(w, r)
	if !ok {
		return
	}
	list := readMySkills(dir)
	if list == nil {
		list = []mySkillView{}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"skills": list, "max": maxMySkills})
}

// mySkillUpload is a checked upload: relative path → content.
type mySkillUpload struct {
	name  string
	files map[string][]byte
}

// parseMySkillUpload reads a zip of one skill folder (or a bare SKILL.md)
// and checks it. The returned code names what's wrong.
func parseMySkillUpload(filename string, data []byte) (*mySkillUpload, string, string) {
	files := map[string][]byte{}
	top := ""
	if strings.EqualFold(path.Ext(filename), ".md") {
		files["SKILL.md"] = data
		top = strings.TrimSuffix(path.Base(filename), path.Ext(filename))
	} else {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, "invalid_file", ""
		}
		top = detectCommonTopDir(zr.File)
		var total int64
		for _, f := range zr.File {
			name := f.Name
			if f.FileInfo().IsDir() || strings.HasPrefix(name, "__MACOSX/") {
				continue
			}
			if top != "" {
				name = strings.TrimPrefix(name, top+"/")
			}
			base := path.Base(name)
			if base == ".DS_Store" || strings.HasPrefix(base, "._") {
				continue
			}
			clean := path.Clean(name)
			if clean == "." || strings.HasPrefix(clean, "../") || clean == ".." || path.IsAbs(clean) ||
				f.Mode()&os.ModeSymlink != 0 || strings.HasPrefix(base, ".") {
				return nil, "file_not_allowed", name
			}
			if !mySkillTextExts[strings.ToLower(path.Ext(clean))] {
				return nil, "file_not_allowed", clean
			}
			if len(files) >= maxMySkillFiles {
				return nil, "too_many_files", ""
			}
			total += int64(f.UncompressedSize64)
			if total > maxMySkillBytes {
				return nil, "too_large", ""
			}
			rc, err := f.Open()
			if err != nil {
				return nil, "invalid_file", ""
			}
			content, err := io.ReadAll(io.LimitReader(rc, maxMySkillBytes+1))
			rc.Close()
			if err != nil || int64(len(content)) > maxMySkillBytes {
				return nil, "too_large", ""
			}
			files[clean] = content
		}
	}

	var total int
	for p, content := range files {
		total += len(content)
		if !utf8.Valid(content) {
			return nil, "file_not_allowed", p
		}
	}
	if total > maxMySkillBytes {
		return nil, "too_large", ""
	}
	md, ok := files["SKILL.md"]
	if !ok {
		return nil, "no_skill_md", ""
	}
	if len(md) > maxMySkillMDBytes {
		return nil, "too_large", "SKILL.md"
	}
	fm, _ := agent.SplitSkillFrontmatter(md)
	if fm == nil || strings.TrimSpace(fm.Description) == "" {
		return nil, "invalid_frontmatter", ""
	}
	name := ""
	for _, candidate := range []string{fm.Name, top} {
		c := strings.ToLower(strings.TrimSpace(candidate))
		if mySkillNameRE.MatchString(c) {
			name = c
			break
		}
	}
	if name == "" {
		return nil, "invalid_name", ""
	}
	return &mySkillUpload{name: name, files: files}, "", ""
}

// reservedSkillName: the deployment's own skills keep their names, so a
// person's upload can't stand in for one (and pick up its keys).
func reservedSkillName(name string) bool {
	home, err := config.HomeDir()
	if err != nil {
		return true
	}
	if _, err := os.Stat(filepath.Join(home, "skills", name)); err == nil {
		return true
	}
	return false
}

func (s *Server) handleUploadMySkill(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	uid, dir, ok := s.mySkillsCaller(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMySkillUpload)
	if err := r.ParseMultipartForm(maxMySkillUpload); err != nil {
		mySkillError(w, http.StatusBadRequest, "too_large", "")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		mySkillError(w, http.StatusBadRequest, "invalid_file", "")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxMySkillUpload+1))
	if err != nil || len(data) > maxMySkillUpload {
		mySkillError(w, http.StatusBadRequest, "too_large", "")
		return
	}
	up, code, detail := parseMySkillUpload(hdr.Filename, data)
	if code != "" {
		mySkillError(w, http.StatusBadRequest, code, detail)
		return
	}
	if reservedSkillName(up.name) {
		mySkillError(w, http.StatusBadRequest, "name_reserved", up.name)
		return
	}
	existing := readMySkills(dir)
	replacing := false
	for _, e := range existing {
		if e.Name == up.name {
			replacing = true
		}
	}
	if !replacing && len(existing) >= maxMySkills {
		mySkillError(w, http.StatusBadRequest, "too_many", "")
		return
	}

	// Write beside the skills folder, then swap it in whole.
	tmp, err := os.MkdirTemp(filepath.Dir(dir), ".skill-upload-")
	if err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	defer os.RemoveAll(tmp)
	for rel, content := range up.files {
		dest := filepath.Join(tmp, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			mySkillError(w, http.StatusInternalServerError, "internal", "")
			return
		}
		if err := os.WriteFile(dest, content, 0o644); err != nil {
			mySkillError(w, http.StatusInternalServerError, "internal", "")
			return
		}
	}
	final := filepath.Join(dir, up.name)
	if err := os.RemoveAll(final); err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if err := os.Rename(tmp, final); err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}

	if s.workspaceStore != nil {
		owner := skills.UserSkillOwner(uid)
		if replacing {
			if err := skills.DeleteSkillUp(r.Context(), s.workspaceStore, owner, up.name); err != nil {
				slog.Warn("my skills: drop old copy", "user", uid, "skill", up.name, "error", err)
			}
		}
		if err := skills.SyncSkillUp(r.Context(), s.workspaceStore, owner, up.name, dir); err != nil {
			slog.Warn("my skills: mirror", "user", uid, "skill", up.name, "error", err)
		}
	}
	slog.Info("my skill uploaded", "user", uid, "skill", up.name, "files", len(up.files), "replaced", replacing)
	for _, v := range readMySkills(dir) {
		if v.Name == up.name {
			jsonResponse(w, http.StatusOK, map[string]any{"skill": v, "replaced": replacing})
			return
		}
	}
	mySkillError(w, http.StatusInternalServerError, "internal", "")
}

func (s *Server) handleDeleteMySkill(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	name := r.PathValue("name")
	if !mySkillNameRE.MatchString(name) && sanitizeSkillName(name) != name {
		mySkillError(w, http.StatusBadRequest, "invalid_name", "")
		return
	}
	uid, dir, ok := s.mySkillsCaller(w, r)
	if !ok {
		return
	}
	target := filepath.Join(dir, name)
	if _, err := os.Stat(filepath.Join(target, "SKILL.md")); err != nil {
		mySkillError(w, http.StatusNotFound, "not_found", "")
		return
	}
	if err := os.RemoveAll(target); err != nil {
		mySkillError(w, http.StatusInternalServerError, "internal", "")
		return
	}
	if s.workspaceStore != nil {
		if err := skills.DeleteSkillUp(r.Context(), s.workspaceStore, skills.UserSkillOwner(uid), name); err != nil {
			slog.Warn("my skills: delete remote", "user", uid, "skill", name, "error", err)
		}
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true})
}
