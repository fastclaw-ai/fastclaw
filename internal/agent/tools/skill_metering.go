package tools

import (
	"context"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
)

// Skill metering (docs/usage-metering.md, kind "skill"). A skill whose
// configured secrets (API keys) were injected into a command spends the
// operator's money at some third-party API that FastClaw can't see. FastClaw
// doesn't try to know what each API charges: it counts runs — one event per
// command that ran a keyed skill's script, provider = the skill's name — and
// the integrating app prices a run per skill. Skills without configured
// secrets cost nothing extra and aren't metered.

// skillRun is the keyed skill a command runs, or nil.
type skillRun struct {
	name   string
	script string // path under the skill's dir, for reporting only
	env    map[string]string
}

// resolveSkill finds the skill a command references and its configured env.
//
// Two matching paths:
//  1. host paths from skillDirs (e.g. "/Users/.../agents/<id>/skills") —
//     used when exec runs on the host shell.
//  2. sandbox-internal "/skills/<name>" prefix — every skill is mounted
//     into the container at that location regardless of where it lives on
//     the host, so commands the model writes inside the sandbox use this
//     form.
//
// nil when the command references no skill with configured env.
func resolveSkill(command string, envProvider SkillEnvProvider, skillDirs []string) *skillRun {
	if envProvider == nil {
		return nil
	}
	try := func(rest string) *skillRun {
		rest = strings.TrimPrefix(rest, "/")
		parts := strings.SplitN(rest, "/", 2)
		if len(parts) == 0 || parts[0] == "" {
			return nil
		}
		env := envProvider(parts[0])
		if env == nil {
			return nil
		}
		run := &skillRun{name: parts[0], env: env}
		if len(parts) == 2 {
			script := parts[1]
			if i := strings.IndexAny(script, " \t\n;|&)'\"<>"); i >= 0 {
				script = script[:i]
			}
			run.script = script
		}
		return run
	}
	for _, dir := range skillDirs {
		if dir == "" {
			continue
		}
		if i := strings.Index(command, dir); i >= 0 {
			if run := try(command[i+len(dir):]); run != nil {
				return run
			}
		}
	}
	if i := strings.Index(command, "/skills/"); i >= 0 {
		return try(command[i+len("/skills/"):])
	}
	return nil
}

// resolveSkillEnv returns the configured env of the skill a command
// references, or nil.
func resolveSkillEnv(command string, envProvider SkillEnvProvider, skillDirs []string) map[string]string {
	if run := resolveSkill(command, envProvider, skillDirs); run != nil {
		return run.env
	}
	return nil
}

// metered reports whether a run spends the operator's money: some
// configured value is non-empty.
func (s *skillRun) metered() bool {
	if s == nil {
		return false
	}
	for _, v := range s.env {
		if v != "" {
			return true
		}
	}
	return false
}

// authorize asks the account's billing hook before a keyed skill runs.
// nil means go ahead; a denial's text is the hook's message.
func (s *skillRun) authorize(ctx context.Context) error {
	if !s.metered() {
		return nil
	}
	return billing.AuthorizeTool(ctx, billing.KindSkill, s.name, "",
		billing.Estimate{Units: 1, Unit: "run"})
}

// record meters one run that started, whatever its exit status: the
// command is the model's, so a trailing "; false" must not make the API
// calls it made free.
func (s *skillRun) record(ctx context.Context) {
	if !s.metered() {
		return
	}
	detail := map[string]any{}
	if s.script != "" {
		detail["script"] = s.script
	}
	billing.Record(ctx, billing.Event{
		Kind: billing.KindSkill, Provider: s.name,
		Units: 1, Unit: "run", Detail: detail,
	})
}
