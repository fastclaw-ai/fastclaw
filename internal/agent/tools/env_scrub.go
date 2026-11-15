package tools

import (
	"os"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/redact"
)

// sensitiveEnvPrefixes are NAME prefixes (case-insensitive) that mark
// an env var as operator-only and never inherited by LLM-driven
// subprocesses. FASTCLAW_STORAGE_* and FASTCLAW_OBJECT_STORE_* carry
// the daemon's DSN + object-store credentials; FASTCLAW_SANDBOX_BOXLITE_*
// carries the sandbox provider's apikey. The model has no business
// reading any of these.
var sensitiveEnvPrefixes = []string{
	"FASTCLAW_STORAGE_",
	"FASTCLAW_OBJECT_STORE_",
	"FASTCLAW_SANDBOX_BOXLITE_",
	"AWS_",
	"GOOGLE_APPLICATION_CREDENTIALS",
}

// sensitiveEnvSubstrings are case-insensitive NAME substrings that mark
// a var as likely-secret. Deliberately wider than the prefix list — we
// can't enumerate every operator's bespoke env, so any name containing
// these is stripped. Skills inject their own env explicitly through
// SkillEnvProvider, so legitimate skill credentials still reach the
// child process; only INHERITED parent-env secrets get scrubbed.
var sensitiveEnvSubstrings = []string{
	"SECRET",
	"TOKEN",
	"PASSWORD",
	"PASSWD",
	"CREDENTIAL",
	"PRIVATE_KEY",
	"_API_KEY",
	"APIKEY",
	"ACCESS_KEY",
	"ACCESSKEY",
	"SECRET_KEY",
	"SECRETKEY",
	"_DSN",
	"DATABASE_URL",
}

// nonSensitiveEnvExceptions are names that match a prefix/substring rule
// above but carry no credential and must survive scrubbing.
//
// FASTCLAW_STORAGE_TYPE holds "sqlite" or "postgres" — a backend
// selector, never a secret. It matters because an operator chatting with
// their agent can now have it run `fastclaw agents init` / `skill install`
// through exec. Those commands open the store themselves via
// config.LoadEnv, and with the TYPE stripped a Postgres deployment would
// silently fall back to the sqlite default: the CLI would report success
// while writing the new agent to a local file the gateway never reads.
// Keeping TYPE (while FASTCLAW_STORAGE_DSN stays scrubbed) turns that
// silent divergence into a loud connect error instead.
var nonSensitiveEnvExceptions = map[string]bool{
	"FASTCLAW_STORAGE_TYPE": true,
}

// isSensitiveEnvKey reports whether the given env-var NAME (no =value
// suffix) should be stripped from the parent env before spawning a
// child shell the LLM agent can drive.
func isSensitiveEnvKey(name string) bool {
	upper := strings.ToUpper(name)
	if nonSensitiveEnvExceptions[upper] {
		return false
	}
	for _, p := range sensitiveEnvPrefixes {
		if strings.HasPrefix(upper, p) {
			return true
		}
	}
	for _, sub := range sensitiveEnvSubstrings {
		if strings.Contains(upper, sub) {
			return true
		}
	}
	return false
}

// scrubSensitiveEnv copies env with credential-bearing entries removed.
// Use for every child process the agent can spawn (exec / bash_session
// / host_exec) — the screenshot incident where a chatter coaxed the
// model into `env | grep ALIYUN` and the AccessKey + DSN ended up in
// the chat reply proved that "the model has shell" implies "the user
// has all env vars" in practice, regardless of how the system prompt
// instructs the model.
func scrubSensitiveEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		idx := strings.IndexByte(kv, '=')
		name := kv
		if idx >= 0 {
			name = kv[:idx]
		}
		if isSensitiveEnvKey(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// buildSubprocessEnv returns the full env for an LLM-driven child:
// the parent env with sensitive entries scrubbed, then per-skill
// overrides applied on top so skills can pass their own (legitimate)
// credentials. Callers should use this everywhere instead of letting
// Go default to bare inheritance of os.Environ().
func buildSubprocessEnv(skillEnv map[string]string) []string {
	return mergeEnv(scrubSensitiveEnv(os.Environ()), skillEnv)
}

func init() {
	// The daemon's own secret env values are hidden from regular
	// chatters' turns wherever they turn up (internal/redact).
	redact.LoadEnv(isSensitiveEnvKey)
}

// registerSkillSecrets hides a skill's credential values (by env name,
// the same rule as above), so a script that prints its env doesn't hand
// the key to the chatter.
func registerSkillSecrets(env map[string]string) {
	for k, v := range env {
		if isSensitiveEnvKey(k) {
			redact.Register(v)
		}
	}
}
