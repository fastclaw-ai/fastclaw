package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/sandbox"
)

func TestLoadSkillRegisteredByDefaultAndLoadsFullContent(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "chart-maker")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: chart-maker
description: Build charts from tabular data.
---

Run {baseDir}/scripts/render.py with JSON input.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterLoadSkill(r, []string{filepath.Join(home, "skills")})

	fn := r.GetFunc("load_skill")
	if fn == nil {
		t.Fatal("load_skill was not registered")
	}
	rawArgs, err := json.Marshal(map[string]string{"name": "chart-maker"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := fn(context.Background(), rawArgs)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "Run "+skillDir+"/scripts/render.py") {
		t.Fatalf("load_skill did not return full content with baseDir replaced:\n%s", got)
	}
	if !strings.Contains(got, "INTERNAL CONTEXT") {
		t.Fatalf("load_skill output missing internal wrapper:\n%s", got)
	}
}

func TestLoadSkillUsesDirectoryPrecedence(t *testing.T) {
	agentSkills := filepath.Join(t.TempDir(), "skills")
	userSkills := filepath.Join(t.TempDir(), "skills")
	for _, dir := range []string{agentSkills, userSkills} {
		if err := os.MkdirAll(filepath.Join(dir, "shared"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(userSkills, "shared", "SKILL.md"), []byte("user version"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentSkills, "shared", "SKILL.md"), []byte("agent version"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterLoadSkill(r, []string{agentSkills, userSkills})
	rawArgs, err := json.Marshal(map[string]string{"name": "shared"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.GetFunc("load_skill")(context.Background(), rawArgs)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "agent version") {
		t.Fatalf("load_skill did not use first matching directory:\n%s", got)
	}
	if strings.Contains(got, "user version") {
		t.Fatalf("load_skill should not include lower-priority skill:\n%s", got)
	}
}

func TestLoadSkillMarksMissingEnvRequirement(t *testing.T) {
	home := t.TempDir()
	skillDir := filepath.Join(home, "skills", "deepcoin-trade")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `---
name: deepcoin-trade
description: Place orders.
metadata:
  openclaw:
    requires:
      env: ["DC_API_KEY", "DC_SECRET_KEY"]
---

Authenticated instructions.`
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	r := NewRegistry(t.TempDir(), t.TempDir())
	RegisterLoadSkill(r, []string{filepath.Join(home, "skills")})
	rawArgs, err := json.Marshal(map[string]string{"name": "deepcoin-trade"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := r.GetFunc("load_skill")(context.Background(), rawArgs)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(got, "SKILL CURRENTLY UNAVAILABLE") {
		t.Fatalf("load_skill output missing unavailable warning:\n%s", got)
	}
	if !strings.Contains(got, "DC_API_KEY, DC_SECRET_KEY") {
		t.Fatalf("load_skill output missing env names:\n%s", got)
	}
}

// TestUnavailableReasonFlagsMissingBinary guards the preflight that
// keeps a skill from describing tools the machine doesn't have.
//
// The failure it prevents: SKILL.md asserts "camoufox-cli is the default
// browser tool in this sandbox", the model believes it, runs the command,
// gets `command not found`, and goes looking for the binary — a
// filesystem-wide `find /` that blew past its timeout. The skill was
// installed; only its executable was missing, and nothing said so.
func TestUnavailableReasonFlagsMissingBinary(t *testing.T) {
	body := []byte(`---
name: browser
description: Drive a browser.
metadata:
  fastclaw:
    requires:
      bins: [definitely-not-a-real-binary-xyz]
---

Use it.`)
	reason := unavailableReason(body, true, nil)
	if reason == "" {
		t.Fatal("missing binary was not reported")
	}
	if !strings.Contains(reason, "definitely-not-a-real-binary-xyz") {
		t.Errorf("reason should name the missing binary; got %q", reason)
	}
	if !strings.Contains(reason, "do NOT go looking for it on the filesystem") {
		t.Errorf("reason should steer away from a filesystem hunt; got %q", reason)
	}
}

// `bins` is the spelling bundled skills already use (find-skills declares
// `bins: [npx]`). It parsed into nothing before, so the declaration was
// inert — this pins that it is actually honoured now.
func TestUnavailableReasonAcceptsBothBinSpellings(t *testing.T) {
	for _, key := range []string{"bins", "bin"} {
		body := []byte(`---
name: x
description: y
metadata:
  fastclaw:
    requires:
      ` + key + `: [definitely-not-a-real-binary-xyz]
---

Use it.`)
		if reason := unavailableReason(body, true, nil); reason == "" {
			t.Errorf("requires.%s was ignored", key)
		}
	}
}

func TestUnavailableReasonSilentWhenBinaryPresent(t *testing.T) {
	body := []byte(`---
name: x
description: y
metadata:
  fastclaw:
    requires:
      bins: [sh]
---

Use it.`)
	if reason := unavailableReason(body, true, nil); reason != "" {
		t.Errorf("present binary should not be reported unavailable; got %q", reason)
	}
}

// A skill with no requires block must stay silent — the preflight is
// opt-in and must not start gating skills that never declared anything.
func TestUnavailableReasonSilentWithoutRequires(t *testing.T) {
	body := []byte(`---
name: x
description: y
---

Use it.`)
	if reason := unavailableReason(body, true, nil); reason != "" {
		t.Errorf("skill without requires should not be gated; got %q", reason)
	}
}

// TestUnavailableReasonSkipsBinProbeWhenSandboxed is the compatibility
// guard for sandboxed deployments.
//
// The sandbox image installs camoufox-cli, uv and friends on purpose
// (deploy/docker/sandbox/Dockerfile) — that is the whole reason skills
// may depend on them. Probing the HOST PATH in that mode inverts the
// bug this preflight exists to fix: instead of a missing binary going
// unannounced, a present one gets declared missing and the model is
// steered off a skill that works.
func TestUnavailableReasonSkipsBinProbeWhenSandboxed(t *testing.T) {
	body := []byte(`---
name: browser
description: Drive a browser.
metadata:
  fastclaw:
    requires:
      bins: [definitely-not-a-real-binary-xyz]
---

Use it.`)
	if reason := unavailableReason(body, false, nil); reason != "" {
		t.Errorf("host PATH must not be probed when exec is sandboxed; got %q", reason)
	}
	// Env requirements are process-level and still apply in both modes.
	envBody := []byte(`---
name: browser
description: Drive a browser.
metadata:
  fastclaw:
    requires:
      env: [DEFINITELY_UNSET_VAR_XYZ]
---

Use it.`)
	if reason := unavailableReason(envBody, false, nil); reason == "" {
		t.Error("env requirements should still be reported in sandbox mode")
	}
}

// TestExecRunsOnHostIsConservative pins the predicate the preflight
// depends on. Anything that could route a command into a container must
// answer false — a wrong "host" makes us assert things about an
// environment we never inspected.
func TestExecRunsOnHostIsConservative(t *testing.T) {
	t.Run("admin with no sandbox wiring runs on host", func(t *testing.T) {
		r := NewRegistry(t.TempDir(), t.TempDir())
		r.SetCallerIsAdmin(true)
		if !r.ExecRunsOnHost() {
			t.Error("plain self-hosted admin turn should be host execution")
		}
	})
	t.Run("sandbox required is not host", func(t *testing.T) {
		r := NewRegistry(t.TempDir(), t.TempDir())
		r.SetCallerIsAdmin(true)
		r.SetSandboxRequired(true)
		if r.ExecRunsOnHost() {
			t.Error("enforced sandbox must not report host execution")
		}
	})
	t.Run("optional sandbox provider is not host", func(t *testing.T) {
		r := NewRegistry(t.TempDir(), t.TempDir())
		r.SetCallerIsAdmin(true)
		r.SetSandboxProvider(func(context.Context) (sandbox.Executor, error) { return nil, nil })
		if r.ExecRunsOnHost() {
			t.Error("optional-sandbox mode may route to a container; must not claim host")
		}
	})
	t.Run("guest is not host", func(t *testing.T) {
		r := NewRegistry(t.TempDir(), t.TempDir())
		r.SetCallerIsAdmin(false)
		if r.ExecRunsOnHost() {
			t.Error("non-admin chatters are forced into the sandbox; must not claim host")
		}
	})
	t.Run("nil registry is not host", func(t *testing.T) {
		var r *Registry
		if r.ExecRunsOnHost() {
			t.Error("nil registry must fail closed")
		}
	})
}

// TestUnavailableReasonFlagsMissingTool covers the requirement that
// actually broke an agent handoff: a skill whose whole purpose is image
// generation, installed onto an agent with no image_gen credentials.
//
// Nothing in the old pipeline noticed. The skill installed, `skill list`
// showed it, the files were on disk — and every check reported healthy
// while the agent could not produce a single image. Unlike env and bins,
// this one is knowable for certain: the registry either has the tool or
// it does not.
func TestUnavailableReasonFlagsMissingTool(t *testing.T) {
	body := []byte(`---
name: anthropic-art
description: Generate editorial illustrations.
metadata:
  fastclaw:
    requires:
      tools: [image_gen]
---

Draw things.`)

	noTools := func(string) bool { return false }
	reason := unavailableReason(body, true, noTools)
	if reason == "" {
		t.Fatal("missing image_gen was not reported")
	}
	if !strings.Contains(reason, "image_gen") {
		t.Errorf("reason should name the missing tool; got %q", reason)
	}
	if !strings.Contains(reason, "reporting success") {
		t.Errorf("reason should forbid claiming success anyway; got %q", reason)
	}

	hasTools := func(string) bool { return true }
	if reason := unavailableReason(body, true, hasTools); reason != "" {
		t.Errorf("tool present should not be reported unavailable; got %q", reason)
	}
}

// The tool probe is registry-backed, so unlike the binary probe it is
// valid in sandboxed deployments too — tool availability is a property
// of the agent, not of which filesystem exec lands on.
func TestUnavailableReasonChecksToolsEvenWhenSandboxed(t *testing.T) {
	body := []byte(`---
name: anthropic-art
description: Generate editorial illustrations.
metadata:
  fastclaw:
    requires:
      tools: [image_gen]
---

Draw things.`)
	if reason := unavailableReason(body, false, func(string) bool { return false }); reason == "" {
		t.Error("tool requirements must be checked in sandbox mode too")
	}
}

func TestParseSkillRequirementsExposesAllThree(t *testing.T) {
	body := []byte(`---
name: x
description: y
metadata:
  fastclaw:
    requires:
      env: [A_KEY]
      bins: [ffmpeg]
      tools: [image_gen]
---

Body.`)
	got := ParseSkillRequirements(body)
	if len(got.Env) != 1 || got.Env[0] != "A_KEY" {
		t.Errorf("Env = %v", got.Env)
	}
	if len(got.Bins) != 1 || got.Bins[0] != "ffmpeg" {
		t.Errorf("Bins = %v", got.Bins)
	}
	if len(got.Tools) != 1 || got.Tools[0] != "image_gen" {
		t.Errorf("Tools = %v", got.Tools)
	}
	if empty := ParseSkillRequirements([]byte("no frontmatter")); len(empty.Env)+len(empty.Bins)+len(empty.Tools) != 0 {
		t.Errorf("expected zero value for a skill with no requires, got %+v", empty)
	}
}

// A skill the chatter added themselves points at skills/<name>/… (their
// own folder, which read_file reaches) rather than a host path, lists its
// reference files, and isn't treated as a secret from them.
func TestLoadSkillPersonalSkillUsesRelativePaths(t *testing.T) {
	userRoot := t.TempDir()
	skillDir := filepath.Join(userRoot, "skills", "writer")
	if err := os.MkdirAll(filepath.Join(skillDir, "refs"), 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("---\nname: writer\ndescription: d\n---\nSee {baseDir}/refs/style.md"), 0o644)
	os.WriteFile(filepath.Join(skillDir, "refs", "style.md"), []byte("short sentences"), 0o644)

	r := NewRegistry(t.TempDir(), t.TempDir())
	r.userSkillsRoot = userRoot
	RegisterLoadSkill(r, []string{filepath.Join(t.TempDir(), "agent-skills"), filepath.Join(userRoot, "skills")})
	got, err := r.GetFunc("load_skill")(context.Background(), json.RawMessage(`{"name":"writer"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "See skills/writer/refs/style.md") || strings.Contains(got, userRoot) {
		t.Fatalf("paths: %s", got)
	}
	if !strings.Contains(got, "read_file at these paths when the instructions refer to them: skills/writer/refs/style.md") {
		t.Fatalf("files not listed: %s", got)
	}
	if strings.Contains(got, "INTERNAL CONTEXT") {
		t.Fatalf("their own skill was wrapped as internal: %s", got)
	}
}
