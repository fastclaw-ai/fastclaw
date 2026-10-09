package setup

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/skills"
)

// Agent configuration archives: a portable ZIP of an agent's identity
// files and its own skills, so an agent can be backed up, moved to
// another deployment or handed to someone else.
//
// Layout:
//
//	fastclaw-agent.json     manifest (format, profile, settings)
//	README.md               human note, ignored on import
//	SOUL.md, AGENTS.md, …   identity files (portableAgentFiles)
//	skills/<name>/SKILL.md  the agent's own skills, with their resources
//
// Deliberately left out: model / provider credentials, MCP servers (their
// env and headers carry secrets), channels, skill env values, chats, and
// per-user state (USER.md, MEMORY.md). Import accepts the same layout
// without a manifest too, so an OpenClaw-style workspace folder works.
const (
	agentArchiveFormat       = "fastclaw-agent"
	agentArchiveVersion      = 1
	agentArchiveManifestName = "fastclaw-agent.json"
	maxAgentArchiveBytes     = 64 << 20
	maxAgentArchiveFiles     = 2000
	maxAgentFileBytes        = 1 << 20
)

// portableAgentFiles is forkAgentFiles: the agent's identity, not any
// one chatter's state.
var portableAgentFiles = forkAgentFiles

type agentArchiveManifest struct {
	Format   string               `json:"format"`
	Version  int                  `json:"version"`
	Profile  agentArchiveProfile  `json:"profile"`
	Settings agentArchiveSettings `json:"settings,omitempty"`
	Files    []string             `json:"files"`
	Skills   []string             `json:"skills"`
}

type agentArchiveProfile struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type agentArchiveSettings struct {
	PromptMode   string `json:"promptMode,omitempty"`
	SplitReplies *bool  `json:"splitReplies,omitempty"`
	AutoPersist  *bool  `json:"autoPersist,omitempty"`
}

// parsedAgentArchive is an import candidate after validation.
type parsedAgentArchive struct {
	Manifest *agentArchiveManifest
	Files    map[string][]byte            // identity file → content
	Skills   map[string]map[string][]byte // skill name → relative path → bytes
	Ignored  []string
}

// GET /api/agents/{id}/archive — download the agent's configuration.
func (s *Server) handleExportAgentArchive(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	ctx := r.Context()

	manifest := agentArchiveManifest{
		Format:  agentArchiveFormat,
		Version: agentArchiveVersion,
		Profile: agentArchiveProfile{Name: rec.Name},
		Settings: agentArchiveSettings{
			PromptMode:   s.agentScopePromptMode(r, id),
			SplitReplies: s.agentScopeSplitReplies(r, id),
			AutoPersist:  s.agentScopeAutoPersist(r, id),
		},
		Files:  []string{},
		Skills: []string{},
	}
	manifest.Profile.Description, _ = rec.Config["description"].(string)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	put := func(name string, data []byte) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: time.Now()})
		if err != nil {
			return err
		}
		_, err = f.Write(data)
		return err
	}
	fail := func(err error) {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
	}

	for _, name := range portableAgentFiles {
		data, err := s.dataStore.GetAgentFileExact(ctx, id, rec.UserID, name)
		if err != nil {
			continue
		}
		if err := put(name, data); err != nil {
			fail(err)
			return
		}
		manifest.Files = append(manifest.Files, name)
	}

	if dir, err := agentSkillsDir(id); err == nil {
		if s.workspaceStore != nil {
			if err := skills.HydrateSkillsDown(ctx, s.workspaceStore, id, dir); err != nil {
				slog.Warn("agent archive: hydrate skills", "agent", id, "error", err)
			}
		}
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			root := filepath.Join(dir, e.Name())
			if _, err := os.Stat(filepath.Join(root, "SKILL.md")); err != nil {
				continue
			}
			err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
				if err != nil || d.IsDir() || !d.Type().IsRegular() {
					return err
				}
				rel, err := filepath.Rel(root, p)
				if err != nil {
					return err
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				return put("skills/"+e.Name()+"/"+filepath.ToSlash(rel), data)
			})
			if err != nil {
				fail(err)
				return
			}
			manifest.Skills = append(manifest.Skills, e.Name())
		}
	}

	blob, _ := json.MarshalIndent(manifest, "", "  ")
	if err := put(agentArchiveManifestName, blob); err != nil {
		fail(err)
		return
	}
	readme := "FastClaw agent configuration\n\n" +
		"Identity files (SOUL.md, AGENTS.md, …) live at the archive root; skills live in skills/<name>/.\n" +
		"Import it from Settings → Advanced on any agent. Import replaces that agent's identity files and skills.\n" +
		"Model credentials, MCP servers, channels, chats and personal memory are not included.\n"
	if err := put("README.md", []byte(readme)); err != nil {
		fail(err)
		return
	}
	if err := zw.Close(); err != nil {
		fail(err)
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": archiveFileStem(rec.Name, id) + ".zip"}))
	w.Header().Set("Content-Length", fmt.Sprint(buf.Len()))
	_, _ = w.Write(buf.Bytes())
}

// POST /api/agents/{id}/archive[?dryRun=1] — multipart `file` (ZIP or
// TAR.GZ). dryRun only validates and describes what would change.
func (s *Server) handleImportAgentArchive(w http.ResponseWriter, r *http.Request) {
	if !s.requireWritable(w, r) {
		return
	}
	id := r.PathValue("id")
	rec := s.requireAgentOwner(w, r, id)
	if rec == nil {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAgentArchiveBytes+1<<20)
	if err := r.ParseMultipartForm(maxAgentArchiveBytes); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "upload a ZIP or TAR.GZ up to 64 MB"})
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": "file field required"})
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxAgentArchiveBytes+1))
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	if len(data) > maxAgentArchiveBytes {
		jsonResponse(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "archive exceeds 64 MB"})
		return
	}
	entries, err := readArchiveEntries(hdr.Filename, data)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}
	parsed, err := parseAgentArchive(entries)
	if err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
		return
	}

	preview := parsed.preview()
	if r.URL.Query().Get("dryRun") != "" {
		jsonResponse(w, http.StatusOK, preview)
		return
	}
	if err := s.applyAgentArchive(r, rec.ID, rec.UserID, parsed); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
		return
	}
	slog.Info("agent archive imported", "agent", id, "files", len(parsed.Files), "skills", len(parsed.Skills))
	preview["ok"] = true
	jsonResponse(w, http.StatusOK, preview)
}

func (p *parsedAgentArchive) preview() map[string]any {
	files := make([]string, 0, len(p.Files))
	for _, name := range portableAgentFiles {
		if _, ok := p.Files[name]; ok {
			files = append(files, name)
		}
	}
	skillNames := make([]string, 0, len(p.Skills))
	for name := range p.Skills {
		skillNames = append(skillNames, name)
	}
	sort.Strings(skillNames)
	out := map[string]any{
		"format":  "workspace",
		"files":   files,
		"skills":  skillNames,
		"ignored": p.Ignored,
	}
	if m := p.Manifest; m != nil {
		out["format"] = agentArchiveFormat
		out["name"] = m.Profile.Name
		out["description"] = m.Profile.Description
		out["settings"] = m.Settings
	}
	return out
}

// applyAgentArchive replaces the agent's identity files and skills with
// the archive's. Files and skills the archive doesn't carry are removed,
// so the result matches the exported agent.
func (s *Server) applyAgentArchive(r *http.Request, agentID, ownerID string, p *parsedAgentArchive) error {
	ctx := r.Context()

	// Stage skills first: a bad write leaves the agent untouched.
	dir, err := agentSkillsDir(agentID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(filepath.Dir(dir), ".skills-import-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(staging)
	for name, files := range p.Skills {
		for rel, data := range files {
			dest := filepath.Join(staging, name, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(dest, data, 0o644); err != nil {
				return err
			}
		}
	}

	for _, name := range portableAgentFiles {
		if data, ok := p.Files[name]; ok {
			if err := s.dataStore.SaveAgentFile(ctx, agentID, ownerID, name, data); err != nil {
				return err
			}
			continue
		}
		_ = s.dataStore.DeleteAgentFile(ctx, agentID, ownerID, name)
	}

	if s.workspaceStore != nil {
		_ = skills.HydrateSkillsDown(ctx, s.workspaceStore, agentID, dir)
	}
	existing, _ := os.ReadDir(dir)
	for _, e := range existing {
		if !e.IsDir() {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
		if s.workspaceStore != nil {
			if err := skills.DeleteSkillUp(ctx, s.workspaceStore, agentID, e.Name()); err != nil {
				slog.Warn("agent archive: remove skill from object store", "agent", agentID, "skill", e.Name(), "error", err)
			}
		}
	}
	for name := range p.Skills {
		if err := os.Rename(filepath.Join(staging, name), filepath.Join(dir, name)); err != nil {
			return err
		}
		if s.workspaceStore != nil {
			if err := skills.SyncSkillUp(ctx, s.workspaceStore, agentID, name, dir); err != nil {
				slog.Warn("agent archive: mirror skill to object store", "agent", agentID, "skill", name, "error", err)
			}
		}
	}

	if m := p.Manifest; m != nil {
		patch := map[string]interface{}{}
		switch m.Settings.PromptMode {
		case config.PromptModeAgent, config.PromptModeChatbot, config.PromptModeCustomize:
			patch["promptMode"] = m.Settings.PromptMode
		case "":
			patch["promptMode"] = nil
		}
		patch["splitReplies"] = nil
		if m.Settings.SplitReplies != nil {
			patch["splitReplies"] = *m.Settings.SplitReplies
		}
		patch["autoPersist"] = nil
		if m.Settings.AutoPersist != nil {
			patch["autoPersist"] = *m.Settings.AutoPersist
		}
		if err := s.applyAgentScopeDefaultsPatch(r, agentID, patch); err != nil {
			return err
		}
	}

	s.invalidateUser(ownerID)
	s.invalidateAgent(agentID)
	if ag := s.resolveAgent(r, agentID); ag != nil {
		ag.ReloadWorkspaceFiles()
	}
	return nil
}

func agentSkillsDir(agentID string) (string, error) {
	home, err := config.AgentHomeDir(agentID)
	if err != nil {
		return "", err
	}
	return filepath.Join(home, "skills"), nil
}

// readArchiveEntries unpacks a ZIP or TAR.GZ into path → bytes, dropping
// directories, links and OS metadata, and enforcing the size limits.
func readArchiveEntries(filename string, data []byte) (map[string][]byte, error) {
	out := map[string][]byte{}
	total := 0
	add := func(name string, r io.Reader) error {
		name = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(name, "\\", "/")), "/")
		base := path.Base(name)
		if name == "" || name == "." || strings.HasPrefix(name, "__MACOSX/") || base == ".DS_Store" || strings.HasPrefix(base, "._") {
			return nil
		}
		if len(out) >= maxAgentArchiveFiles {
			return errors.New("archive contains more than 2,000 files")
		}
		b, err := io.ReadAll(io.LimitReader(r, int64(maxAgentArchiveBytes-total+1)))
		if err != nil {
			return err
		}
		total += len(b)
		if total > maxAgentArchiveBytes {
			return errors.New("archive contents exceed 64 MB")
		}
		out[name] = b
		return nil
	}

	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".tar.gz") || strings.HasSuffix(lower, ".tgz"):
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("not a valid TAR.GZ: %w", err)
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, fmt.Errorf("not a valid TAR.GZ: %w", err)
			}
			if h.Typeflag != tar.TypeReg {
				continue
			}
			if err := add(h.Name, tr); err != nil {
				return nil, err
			}
		}
	default:
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("not a valid ZIP: %w", err)
		}
		for _, f := range zr.File {
			if f.FileInfo().IsDir() || !f.Mode().IsRegular() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			err = add(f.Name, rc)
			rc.Close()
			if err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// parseAgentArchive finds the agent root (the archive root, or one
// wrapping folder) and validates identity files and skills under it.
func parseAgentArchive(entries map[string][]byte) (*parsedAgentArchive, error) {
	entries = stripCommonTopDir(entries)
	p := &parsedAgentArchive{Files: map[string][]byte{}, Skills: map[string]map[string][]byte{}}
	portable := map[string]bool{}
	for _, name := range portableAgentFiles {
		portable[name] = true
	}

	if raw, ok := entries[agentArchiveManifestName]; ok {
		var m agentArchiveManifest
		if err := json.Unmarshal(raw, &m); err != nil {
			return nil, fmt.Errorf("invalid %s: %w", agentArchiveManifestName, err)
		}
		if m.Format != agentArchiveFormat || m.Version < 1 || m.Version > agentArchiveVersion {
			return nil, errors.New("unsupported agent archive format or version")
		}
		p.Manifest = &m
	}

	for name, data := range entries {
		switch {
		case name == agentArchiveManifestName || name == "README.md":
		case portable[name]:
			if len(data) > maxAgentFileBytes || (name == "KNOWLEDGE.md" && len(data) > maxKnowledgeFileBytes) {
				return nil, fmt.Errorf("%s is too large", name)
			}
			if !utf8.Valid(data) {
				return nil, fmt.Errorf("%s is not UTF-8 text", name)
			}
			p.Files[name] = data
		case strings.HasPrefix(name, "skills/") && strings.Count(name, "/") >= 2:
			rest := strings.TrimPrefix(name, "skills/")
			i := strings.Index(rest, "/")
			skill, rel := sanitizeSkillName(rest[:i]), rest[i+1:]
			if skill == "" || skill != rest[:i] || strings.HasPrefix(skill, ".") {
				p.Ignored = append(p.Ignored, name)
				continue
			}
			if p.Skills[skill] == nil {
				p.Skills[skill] = map[string][]byte{}
			}
			p.Skills[skill][rel] = data
		default:
			p.Ignored = append(p.Ignored, name)
		}
	}
	for name, files := range p.Skills {
		if _, ok := files["SKILL.md"]; !ok {
			for rel := range files {
				p.Ignored = append(p.Ignored, "skills/"+name+"/"+rel)
			}
			delete(p.Skills, name)
		}
	}
	sort.Strings(p.Ignored)
	if len(p.Files) == 0 && len(p.Skills) == 0 {
		return nil, errors.New("no agent files (SOUL.md, AGENTS.md, …) or skills (skills/<name>/SKILL.md) found in archive")
	}
	return p, nil
}

// stripCommonTopDir peels one wrapping folder ("my-agent/SOUL.md") when
// every entry shares it.
func stripCommonTopDir(entries map[string][]byte) map[string][]byte {
	top := ""
	for name := range entries {
		i := strings.Index(name, "/")
		if i <= 0 {
			return entries
		}
		if top == "" {
			top = name[:i]
		} else if top != name[:i] {
			return entries
		}
	}
	if top == "" || top == "skills" {
		return entries
	}
	out := make(map[string][]byte, len(entries))
	for name, data := range entries {
		out[strings.TrimPrefix(name, top+"/")] = data
	}
	return out
}

// archiveFileStem turns the agent name into a download filename,
// falling back to the id when nothing printable is left.
func archiveFileStem(name, id string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r < 0x20 || strings.ContainsRune(`\/:*?"<>|`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	stem := strings.Trim(b.String(), " .-")
	if stem == "" {
		stem = id
	}
	return stem + "-config"
}
