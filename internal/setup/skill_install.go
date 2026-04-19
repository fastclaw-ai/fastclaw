package setup

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const clawhubAPI = "https://clawhub.ai"

// handleInstallSkill installs a skill from GitHub or ClawHub.
// POST /api/skills/install
// Body: { "source": "github" | "clawhub", "repo": "owner/repo", "skill": "skill-name" }
func (s *Server) handleInstallSkill(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Source string `json:"source"` // "github" or "clawhub" (default: auto-detect)
		Repo   string `json:"repo"`   // GitHub: "owner/repo"
		Skill  string `json:"skill"`  // skill name/slug
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid request"})
		return
	}

	// Auto-detect source
	if req.Source == "" {
		if req.Repo != "" {
			req.Source = "github"
		} else if req.Skill != "" {
			req.Source = "clawhub"
		} else {
			jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "repo or skill required"})
			return
		}
	}

	switch req.Source {
	case "clawhub":
		installFromClawHub(w, req.Skill)
	case "github":
		installFromGitHub(w, req.Repo, req.Skill)
	default:
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "source must be 'github' or 'clawhub'"})
	}
}

// handleSearchSkills searches skills from ClawHub registry.
// GET /api/skills/search?q=xxx&source=clawhub
func (s *Server) handleSearchSkills(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query().Get("q")
	source := r.URL.Query().Get("source")
	if source == "" {
		source = "clawhub"
	}

	client := &http.Client{Timeout: 10_000_000_000}

	if source == "clawhub" {
		url := fmt.Sprintf("%s/api/v1/search?q=%s&limit=20", clawhubAPI, query)
		resp, err := client.Get(url)
		if err != nil {
			jsonResponse(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		w.Header().Set("Content-Type", "application/json")
		w.Write(body)
		return
	}

	jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "unsupported source"})
}

// ── ClawHub Installation ────────────────────────────────────────

func installFromClawHub(w http.ResponseWriter, slug string) {
	if slug == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "skill name required"})
		return
	}

	client := &http.Client{Timeout: 30_000_000_000}

	// 1. Get skill metadata
	metaURL := fmt.Sprintf("%s/api/v1/skills/%s", clawhubAPI, slug)
	metaResp, err := client.Get(metaURL)
	if err != nil {
		jsonResponse(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer metaResp.Body.Close()

	if metaResp.StatusCode != http.StatusOK {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": fmt.Sprintf("skill '%s' not found on ClawHub", slug)})
		return
	}

	var meta struct {
		Name          string `json:"name"`
		LatestVersion struct {
			Version string `json:"version"`
		} `json:"latestVersion"`
	}
	json.NewDecoder(metaResp.Body).Decode(&meta)

	version := meta.LatestVersion.Version
	if version == "" {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": "no version available"})
		return
	}

	// 2. Download ZIP
	downloadURL := fmt.Sprintf("%s/api/v1/download?slug=%s&version=%s", clawhubAPI, slug, version)
	dlResp, err := client.Get(downloadURL)
	if err != nil {
		jsonResponse(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(dlResp.Body)
		jsonResponse(w, http.StatusBadGateway, map[string]any{"ok": false, "error": fmt.Sprintf("download failed: %s", string(body))})
		return
	}

	zipData, _ := io.ReadAll(dlResp.Body)

	// 3. Extract ZIP to skills directory
	home, _ := os.UserHomeDir()
	skillDir := filepath.Join(home, ".fastclaw", "skills", slug)
	os.MkdirAll(skillDir, 0o755)

	if err := extractZip(zipData, skillDir); err != nil {
		jsonResponse(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	slog.Info("skill installed from ClawHub", "name", slug, "version", version)
	jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "name": slug, "version": version, "source": "clawhub"})
}

// ── GitHub Installation ─────────────────────────────────────────

func installFromGitHub(w http.ResponseWriter, repo, skill string) {
	if repo == "" {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "repo is required"})
		return
	}

	repo = strings.TrimPrefix(repo, "https://github.com/")
	repo = strings.TrimPrefix(repo, "github.com/")
	repo = strings.TrimSuffix(repo, ".git")
	repo = strings.TrimSuffix(repo, "/")

	parts := strings.SplitN(repo, "/", 2)
	if len(parts) != 2 {
		jsonResponse(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "repo must be owner/repo"})
		return
	}

	client := &http.Client{Timeout: 15_000_000_000}

	// Try multiple paths for SKILL.md
	paths := []string{}
	if skill != "" {
		paths = append(paths,
			fmt.Sprintf("skills/%s/SKILL.md", skill),
			fmt.Sprintf("skills/.curated/%s/SKILL.md", skill),
			fmt.Sprintf("%s/SKILL.md", skill),
		)
	} else {
		paths = append(paths, "SKILL.md", "skills/SKILL.md")
	}

	for _, branch := range []string{"main", "master"} {
		for _, path := range paths {
			rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/%s", repo, branch, path)
			resp, err := client.Get(rawURL)
			if err != nil {
				continue
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				continue
			}

			content, _ := io.ReadAll(resp.Body)
			skillName := extractSkillName(string(content))
			if skillName == "" {
				skillName = skill
				if skillName == "" {
					skillName = parts[1]
				}
			}

			home, _ := os.UserHomeDir()
			skillDir := filepath.Join(home, ".fastclaw", "skills", skillName)
			os.MkdirAll(skillDir, 0o755)
			os.WriteFile(filepath.Join(skillDir, "SKILL.md"), content, 0o644)

			slog.Info("skill installed from GitHub", "name", skillName, "repo", repo)
			jsonResponse(w, http.StatusOK, map[string]any{"ok": true, "name": skillName, "source": "github"})
			return
		}
	}

	// Not found — try listing available skills
	skills, err := listRepoSkills(client, repo)
	if err != nil || len(skills) == 0 {
		jsonResponse(w, http.StatusNotFound, map[string]any{"ok": false, "error": fmt.Sprintf("no SKILL.md found in %s", repo)})
		return
	}
	jsonResponse(w, http.StatusOK, map[string]any{"ok": false, "pick": true, "skills": skills})
}

// ── Helpers ─────────────────────────────────────────────────────

func extractSkillName(content string) string {
	if !strings.HasPrefix(content, "---") {
		return ""
	}
	end := strings.Index(content[3:], "---")
	if end < 0 {
		return ""
	}
	for _, line := range strings.Split(content[3:3+end], "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name:") {
			name := strings.TrimSpace(strings.TrimPrefix(line, "name:"))
			return strings.Trim(name, "\"'")
		}
	}
	return ""
}

func listRepoSkills(client *http.Client, repo string) ([]string, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/contents/skills", repo)
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	var entries []struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	json.NewDecoder(resp.Body).Decode(&entries)
	var skills []string
	for _, e := range entries {
		if e.Type == "dir" && !strings.HasPrefix(e.Name, ".") {
			skills = append(skills, e.Name)
		}
	}
	return skills, nil
}

func extractZip(data []byte, targetDir string) error {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return fmt.Errorf("invalid zip: %w", err)
	}
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Security: prevent path traversal
		name := filepath.Clean(f.Name)
		if strings.Contains(name, "..") {
			continue
		}
		target := filepath.Join(targetDir, name)
		os.MkdirAll(filepath.Dir(target), 0o755)
		rc, err := f.Open()
		if err != nil {
			continue
		}
		out, err := os.Create(target)
		if err != nil {
			rc.Close()
			continue
		}
		io.Copy(out, rc)
		out.Close()
		rc.Close()
	}
	return nil
}
