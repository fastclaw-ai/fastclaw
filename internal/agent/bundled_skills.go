package agent

import (
	"embed"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

//go:embed bundled_skills/*
var bundledSkillsFS embed.FS

// InstallBundledSkills copies bundled skills to the managed skills directory
// (~/.fastclaw/skills/) if they don't already exist.
func InstallBundledSkills() {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	targetDir := filepath.Join(home, ".fastclaw", "skills")
	os.MkdirAll(targetDir, 0o755)

	entries, err := fs.ReadDir(bundledSkillsFS, "bundled_skills")
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillName := entry.Name()
		skillTarget := filepath.Join(targetDir, skillName)

		// Skip if already exists (user may have customized it)
		if _, err := os.Stat(filepath.Join(skillTarget, "SKILL.md")); err == nil {
			continue
		}

		// Copy skill directory
		os.MkdirAll(skillTarget, 0o755)
		skillFiles, _ := fs.ReadDir(bundledSkillsFS, filepath.Join("bundled_skills", skillName))
		for _, f := range skillFiles {
			if f.IsDir() {
				continue
			}
			data, err := fs.ReadFile(bundledSkillsFS, filepath.Join("bundled_skills", skillName, f.Name()))
			if err != nil {
				continue
			}
			os.WriteFile(filepath.Join(skillTarget, f.Name()), data, 0o644)
		}
		slog.Info("installed bundled skill", "name", skillName, "path", skillTarget)
	}
}
