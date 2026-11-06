package agent

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"strings"
)

//go:embed all:bundled_skills
var bundledSkillsFS embed.FS

// bundledHashFile is the per-skill sidecar that records the bundled-tree
// hash from the most recent install. Its presence + match with the on-disk
// tree is how InstallBundledSkills decides whether the user has customized
// the skill (skip) or it's still the as-shipped copy (safe to overwrite
// when a newer binary ships an updated bundle).
const bundledHashFile = ".bundled-hash"

// BundledSkillNames returns the folder names of every skill embedded in the
// binary. Exposed so startup/reload code can protect these entries from
// the object-store hydrator's "prune local-only dirs" step (bundled skills
// aren't stored in the object store; they're always regenerated on startup
// by InstallBundledSkills).
func BundledSkillNames() []string {
	entries, err := fs.ReadDir(bundledSkillsFS, "bundled_skills")
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names
}

// InstallBundledSkills syncs every skill embedded under bundled_skills/ to
// the managed skills directory. Honors FASTCLAW_HOME so per-product
// instances each get their own copy.
//
// Upgrade behavior is governed by a per-skill .bundled-hash sidecar:
//   - Target absent → install fresh, write sidecar.
//   - Sidecar present + on-disk hash matches sidecar + sidecar matches the
//     newly-shipped bundle hash → already up to date, no-op.
//   - Sidecar present + on-disk hash matches sidecar + bundle hash differs
//     → user hasn't touched it, safely replace tree with new bundle.
//   - Sidecar present but on-disk hash diverges → user customized, skip.
//   - No sidecar (legacy install) + on-disk hash happens to match the
//     current bundle → silently adopt the sidecar so future upgrades flow.
//   - No sidecar + on-disk hash doesn't match → can't tell user-modified
//     from older-bundle, conservative skip.
func InstallBundledSkills() {
	targetDir := managedSkillsDir()
	if targetDir == "" {
		return
	}
	installBundledSkillsTo(bundledSkillsFS, "bundled_skills", targetDir)
	retireBundledSkills(targetDir, retiredBundledSkills)
}

// retiredBundledSkills were bundled by older binaries and no longer are.
// Their as-shipped copies are removed from the managed skills directory
// on startup; a copy the user edited stays (it is theirs now).
//
//   - local-coding-agents: drove the gateway host's codex / claude CLIs
//     with exec. Coding agents now run on the person's own computer
//     through agl (run_local_agent; console → Devices).
var retiredBundledSkills = []string{"local-coding-agents"}

// retireBundledSkills deletes each named skill under targetDir when it is
// still exactly what a bundle installed (on-disk hash == its sidecar).
func retireBundledSkills(targetDir string, names []string) {
	for _, name := range names {
		dir := filepath.Join(targetDir, name)
		sidecar, err := readBundledHash(dir)
		if err != nil {
			continue // absent, or never installed by a bundle
		}
		disk, err := diskTreeHash(dir)
		if err != nil || disk != sidecar {
			slog.Info("keeping retired bundled skill, user-modified", "name", name, "path", dir)
			continue
		}
		if err := os.RemoveAll(dir); err != nil {
			slog.Warn("failed to remove retired bundled skill", "name", name, "error", err)
			continue
		}
		slog.Info("removed retired bundled skill", "name", name, "path", dir)
	}
}

// installBundledSkillsTo is the testable core: pulls bundled skills from
// any fs.FS rooted at srcRoot into targetDir on disk. Production callers
// pass the embed.FS; tests pass an fstest.MapFS.
func installBundledSkillsTo(srcFS fs.FS, srcRoot, targetDir string) {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		slog.Warn("failed to create bundled skills target", "dir", targetDir, "error", err)
		return
	}

	entries, err := fs.ReadDir(srcFS, srcRoot)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		skillName := entry.Name()
		skillTarget := filepath.Join(targetDir, skillName)
		srcSkillRoot := path.Join(srcRoot, skillName)

		bundleHash, err := embedTreeHash(srcFS, srcSkillRoot)
		if err != nil {
			slog.Warn("failed to hash bundled skill", "name", skillName, "error", err)
			continue
		}

		decision, reason := decideBundledInstall(skillTarget, bundleHash)
		switch decision {
		case decisionFresh:
			if err := copyEmbedTree(srcFS, srcSkillRoot, skillTarget); err != nil {
				slog.Warn("failed to install bundled skill", "name", skillName, "error", err)
				continue
			}
			if err := writeBundledHash(skillTarget, bundleHash); err != nil {
				slog.Warn("failed to write bundled hash sidecar", "name", skillName, "error", err)
			}
			slog.Info("installed bundled skill", "name", skillName, "path", skillTarget)
		case decisionOverwrite:
			// Wipe first so files removed in the new bundle don't linger.
			if err := os.RemoveAll(skillTarget); err != nil {
				slog.Warn("failed to clear stale bundled skill", "name", skillName, "error", err)
				continue
			}
			if err := copyEmbedTree(srcFS, srcSkillRoot, skillTarget); err != nil {
				slog.Warn("failed to upgrade bundled skill", "name", skillName, "error", err)
				continue
			}
			if err := writeBundledHash(skillTarget, bundleHash); err != nil {
				slog.Warn("failed to write bundled hash sidecar", "name", skillName, "error", err)
			}
			slog.Info("upgraded bundled skill", "name", skillName, "path", skillTarget)
		case decisionAdoptSidecar:
			if err := writeBundledHash(skillTarget, bundleHash); err != nil {
				slog.Warn("failed to write bundled hash sidecar", "name", skillName, "error", err)
				continue
			}
			slog.Info("adopted bundled skill sidecar", "name", skillName, "path", skillTarget)
		case decisionUpToDate:
			// nothing to do
		case decisionUserModified:
			slog.Debug("skipping bundled skill, user-modified", "name", skillName, "path", skillTarget, "reason", reason)
		}
	}
}

type installDecision int

const (
	decisionFresh installDecision = iota
	decisionUpToDate
	decisionOverwrite
	decisionAdoptSidecar
	decisionUserModified
)

// decideBundledInstall classifies what to do with one bundled skill's
// target directory given the freshly-computed bundle hash. See
// InstallBundledSkills for the policy rationale.
func decideBundledInstall(targetDir, bundleHash string) (installDecision, string) {
	if _, err := os.Stat(filepath.Join(targetDir, "SKILL.md")); err != nil {
		return decisionFresh, ""
	}
	diskHash, diskErr := diskTreeHash(targetDir)
	if diskErr != nil {
		return decisionUserModified, fmt.Sprintf("disk hash failed: %v", diskErr)
	}
	sidecarHash, sidecarErr := readBundledHash(targetDir)
	if sidecarErr != nil {
		// Pre-sidecar install. Adopt the sidecar silently if disk content
		// already matches the current bundle (so next upgrade flows). Else
		// be conservative — we can't tell user-modified from older-bundle.
		if diskHash == bundleHash {
			return decisionAdoptSidecar, ""
		}
		return decisionUserModified, "no sidecar; disk hash != bundle hash"
	}
	if diskHash != sidecarHash {
		return decisionUserModified, "disk hash != sidecar hash"
	}
	if sidecarHash == bundleHash {
		return decisionUpToDate, ""
	}
	return decisionOverwrite, ""
}

// embedTreeHash hashes every file under root in lexical order, mixing in
// the relative path so a rename can't masquerade as a content-only change.
func embedTreeHash(srcFS fs.FS, root string) (string, error) {
	h := sha256.New()
	err := fs.WalkDir(srcFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		data, err := fs.ReadFile(srcFS, p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\n%d\n", rel, len(data))
		h.Write(data)
		h.Write([]byte{'\n'})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// diskTreeHash mirrors embedTreeHash but walks an on-disk directory.
// Skips dotfiles (the .bundled-hash sidecar, .DS_Store, etc.) so the
// disk hash stays apples-to-apples with the bundle hash.
func diskTreeHash(root string) (string, error) {
	h := sha256.New()
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		// Skip dotfiles at any depth — bundled skills don't ship hidden
		// files, so anything starting with "." on disk is either our
		// sidecar or OS noise (.DS_Store, editor swap files, …).
		for _, seg := range strings.Split(rel, "/") {
			if strings.HasPrefix(seg, ".") {
				return nil
			}
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\n%d\n", rel, len(data))
		h.Write(data)
		h.Write([]byte{'\n'})
		return nil
	})
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func readBundledHash(targetDir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(targetDir, bundledHashFile))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func writeBundledHash(targetDir, hash string) error {
	return os.WriteFile(filepath.Join(targetDir, bundledHashFile), []byte(hash+"\n"), 0o644)
}

// managedSkillsDir is the per-FastClaw-instance global skills location.
// Mirrors fastclawManagedDir in internal/agent/skills.go but kept local
// here so this file's only dependency is os/filepath.
func managedSkillsDir() string {
	if h := os.Getenv("FASTCLAW_HOME"); h != "" {
		return filepath.Join(h, "skills")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".fastclaw", "skills")
}

// copyEmbedTree walks src in the embed.FS and writes every regular file under
// it to the corresponding path under dst, creating intermediate directories.
func copyEmbedTree(src fs.FS, srcRoot, dst string) error {
	return fs.WalkDir(src, srcRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(srcRoot, p)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}
