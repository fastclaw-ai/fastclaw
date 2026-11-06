package agent

import (
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
)

// makeBundle returns an fstest.MapFS that looks like the bundled_skills/
// embed tree: bundled_skills/<name>/SKILL.md (+ extra files).
func makeBundle(skillName, skillBody string, extras map[string]string) fstest.MapFS {
	m := fstest.MapFS{
		"bundled_skills/" + skillName + "/SKILL.md": &fstest.MapFile{Data: []byte(skillBody)},
	}
	for rel, body := range extras {
		m["bundled_skills/"+skillName+"/"+rel] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func mustRead(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func TestInstallBundledSkills_Fresh(t *testing.T) {
	target := t.TempDir()
	src := makeBundle("foo", "v1 body\n", nil)

	installBundledSkillsTo(src, "bundled_skills", target)

	if got := mustRead(t, filepath.Join(target, "foo", "SKILL.md")); got != "v1 body\n" {
		t.Errorf("SKILL.md = %q, want %q", got, "v1 body\n")
	}
	if _, err := os.Stat(filepath.Join(target, "foo", bundledHashFile)); err != nil {
		t.Errorf("sidecar missing after fresh install: %v", err)
	}
}

func TestInstallBundledSkills_UpToDateNoOp(t *testing.T) {
	target := t.TempDir()
	src := makeBundle("foo", "v1 body\n", nil)

	installBundledSkillsTo(src, "bundled_skills", target)
	skillPath := filepath.Join(target, "foo", "SKILL.md")
	stat1, err := os.Stat(skillPath)
	if err != nil {
		t.Fatal(err)
	}

	// Re-run with identical bundle. The decision should be UpToDate, so
	// SKILL.md should not be rewritten (mod time unchanged).
	installBundledSkillsTo(src, "bundled_skills", target)
	stat2, err := os.Stat(skillPath)
	if err != nil {
		t.Fatal(err)
	}
	if !stat1.ModTime().Equal(stat2.ModTime()) {
		t.Errorf("SKILL.md was rewritten on no-op re-install (mtime changed)")
	}
}

func TestInstallBundledSkills_UpgradeWhenUntouched(t *testing.T) {
	target := t.TempDir()
	srcV1 := makeBundle("foo", "v1 body\n", map[string]string{"helper.py": "print('v1')\n"})

	installBundledSkillsTo(srcV1, "bundled_skills", target)

	// Simulate a binary upgrade shipping a new bundle. helper.py is gone,
	// SKILL.md changed.
	srcV2 := makeBundle("foo", "v2 body\n", nil)
	installBundledSkillsTo(srcV2, "bundled_skills", target)

	if got := mustRead(t, filepath.Join(target, "foo", "SKILL.md")); got != "v2 body\n" {
		t.Errorf("SKILL.md not upgraded: got %q", got)
	}
	if _, err := os.Stat(filepath.Join(target, "foo", "helper.py")); !os.IsNotExist(err) {
		t.Errorf("helper.py should have been removed in upgrade, err=%v", err)
	}
}

func TestInstallBundledSkills_SkipWhenUserModified(t *testing.T) {
	target := t.TempDir()
	srcV1 := makeBundle("foo", "v1 body\n", nil)

	installBundledSkillsTo(srcV1, "bundled_skills", target)

	// User edits SKILL.md.
	skillPath := filepath.Join(target, "foo", "SKILL.md")
	if err := os.WriteFile(skillPath, []byte("user-edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Newer bundle ships v2; we should NOT clobber the user's edit.
	srcV2 := makeBundle("foo", "v2 body\n", nil)
	installBundledSkillsTo(srcV2, "bundled_skills", target)

	if got := mustRead(t, skillPath); got != "user-edited\n" {
		t.Errorf("user edit was clobbered: %q", got)
	}
}

func TestInstallBundledSkills_AdoptsSidecarWhenLegacyMatchesBundle(t *testing.T) {
	target := t.TempDir()
	src := makeBundle("foo", "v1 body\n", nil)

	// Pre-sidecar install: copy the bundle by hand and skip writing the
	// sidecar (mimics a binary that predated this mechanism).
	if err := copyEmbedTree(src, "bundled_skills/foo", filepath.Join(target, "foo")); err != nil {
		t.Fatal(err)
	}
	sidecar := filepath.Join(target, "foo", bundledHashFile)
	if _, err := os.Stat(sidecar); !os.IsNotExist(err) {
		t.Fatalf("sidecar shouldn't exist yet, err=%v", err)
	}

	// Re-run with same bundle. Should silently adopt the sidecar.
	installBundledSkillsTo(src, "bundled_skills", target)
	if _, err := os.Stat(sidecar); err != nil {
		t.Errorf("sidecar not adopted: %v", err)
	}
}

func TestInstallBundledSkills_SkipsLegacyWhenDiskDiffersFromBundle(t *testing.T) {
	target := t.TempDir()

	// Pre-sidecar install of an OLDER bundle (no sidecar written).
	srcOld := makeBundle("foo", "v0 body\n", nil)
	if err := copyEmbedTree(srcOld, "bundled_skills/foo", filepath.Join(target, "foo")); err != nil {
		t.Fatal(err)
	}

	// New binary ships v1. Disk doesn't match new bundle and there's no
	// sidecar to prove untouched-ness, so we conservatively skip.
	srcNew := makeBundle("foo", "v1 body\n", nil)
	installBundledSkillsTo(srcNew, "bundled_skills", target)

	if got := mustRead(t, filepath.Join(target, "foo", "SKILL.md")); got != "v0 body\n" {
		t.Errorf("legacy install with no sidecar should be skipped, but got %q", got)
	}
}

func TestDiskTreeHash_IgnoresDotfiles(t *testing.T) {
	target := t.TempDir()
	skillDir := filepath.Join(target, "foo")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("body\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h1, err := diskTreeHash(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	// Inject OS noise + sidecar; hash must not change.
	if err := os.WriteFile(filepath.Join(skillDir, ".DS_Store"), []byte("junk"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, bundledHashFile), []byte("deadbeef"), 0o644); err != nil {
		t.Fatal(err)
	}
	h2, err := diskTreeHash(skillDir)
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Errorf("disk hash changed when only dotfiles were added: %s vs %s", h1, h2)
	}
}

func TestRetireBundledSkills(t *testing.T) {
	target := t.TempDir()
	installBundledSkillsTo(makeBundle("old", "as shipped\n", nil), "bundled_skills", target)
	installBundledSkillsTo(makeBundle("edited", "as shipped\n", nil), "bundled_skills", target)
	if err := os.WriteFile(filepath.Join(target, "edited", "SKILL.md"), []byte("mine now\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A same-named skill the user wrote by hand (no sidecar).
	if err := os.MkdirAll(filepath.Join(target, "handmade"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "handmade", "SKILL.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	retireBundledSkills(target, []string{"old", "edited", "handmade", "missing"})

	if _, err := os.Stat(filepath.Join(target, "old")); !os.IsNotExist(err) {
		t.Errorf("unmodified retired skill not removed: %v", err)
	}
	for _, keep := range []string{"edited", "handmade"} {
		if _, err := os.Stat(filepath.Join(target, keep, "SKILL.md")); err != nil {
			t.Errorf("%s removed: %v", keep, err)
		}
	}
}
