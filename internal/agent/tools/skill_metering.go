package tools

import (
	"context"
	"path"
	"regexp"
	"strings"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
)

// Skill metering (docs/usage-metering.md, kind "skill"). A skill whose
// configured secrets (API keys) are injected into a command spends the
// operator's money at some third-party API that FastClaw can't see. FastClaw
// doesn't try to know what each API charges: it counts runs of the skill's
// scripts — provider = the skill's name — and the integrating app prices a
// run per skill. Skills without configured secrets aren't metered.
//
// Only commands that EXECUTE a script under a skill's directory get its env
// and are metered. Reading the skill (cat SKILL.md, ls, grep references/…)
// is free and gets no keys. The two always go together, so a command the
// parser misreads can only miss its keys — never use them unmetered.

// skillRun is the keyed skill a command runs, or nil.
type skillRun struct {
	name   string
	script string // first script run, path under the skill's dir (reports)
	runs   int    // simple commands in this command that run its scripts
	env    map[string]string
}

// resolveSkill finds the skill whose scripts a command executes, and that
// skill's configured env. nil when it runs no script of a configured skill.
//
// A skill path is either under one of skillDirs (host shell) or under
// /skills/<name>/ (every skill's mount inside the sandbox).
func resolveSkill(command string, envProvider SkillEnvProvider, skillDirs []string) *skillRun {
	if envProvider == nil {
		return nil
	}
	var run *skillRun
	for _, target := range executedPaths(command, 0) {
		name, script := skillOfPath(target, skillDirs)
		if name == "" {
			continue
		}
		if run == nil {
			env := envProvider(name)
			if env == nil {
				continue
			}
			run = &skillRun{name: name, script: script, env: env}
		}
		if name == run.name {
			run.runs++
		}
	}
	return run
}

// skillOfPath splits a path to a file inside a skill's directory into the
// skill's name and the file's path under it; "" when it isn't one.
func skillOfPath(p string, skillDirs []string) (name, rest string) {
	p = path.Clean(p)
	for _, dir := range append(append([]string{}, skillDirs...), "/skills") {
		dir = strings.TrimRight(dir, "/")
		if dir == "" || !strings.HasPrefix(p, dir+"/") {
			continue
		}
		parts := strings.SplitN(p[len(dir)+1:], "/", 2)
		if len(parts) == 2 && parts[0] != "" && parts[0] != ".." && parts[1] != "" {
			return parts[0], parts[1]
		}
	}
	return "", ""
}

// resolveSkillEnv returns the configured env of the skill whose scripts a
// command runs, or nil.
func resolveSkillEnv(command string, envProvider SkillEnvProvider, skillDirs []string) map[string]string {
	if run := resolveSkill(command, envProvider, skillDirs); run != nil {
		return run.env
	}
	return nil
}

// ---- which files does a shell command execute ----

// Words skipped at the start of a simple command (keywords and wrappers
// that run the rest of the line as a command).
var commandPrefixes = map[string]bool{
	"if": true, "then": true, "else": true, "elif": true, "do": true,
	"while": true, "until": true, "!": true, "{": true, "time": true,
	"nohup": true, "exec": true, "command": true, "builtin": true,
	"sudo": true, "nice": true, "stdbuf": true, "xargs": true, "env": true,
	"timeout": true,
}

// Interpreters whose first non-flag argument is the script they run.
var interpreters = regexp.MustCompile(`^(python[0-9.]*|pypy[0-9.]*|node|nodejs|bun|deno|tsx|ts-node|bash|sh|zsh|dash|ksh|ruby|perl|php|uv|npx|pnpx|pnpm)$`)

// Launcher subcommands passed over before the script (uv run x.py,
// npx tsx x.ts, deno run x.ts, bun run x.ts).
var launcherWords = map[string]bool{
	"run": true, "exec": true, "x": true, "dlx": true, "tsx": true,
	"ts-node": true, "node": true, "python": true, "python3": true, "--": true,
}

var assignment = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// executedPaths lists, for each simple command in a shell command, the
// file it executes: the command word itself, or an interpreter's script
// argument. Inline code (python -c, node -e) executes no file; sh -c
// "…" is parsed in turn.
func executedPaths(command string, depth int) []string {
	if depth > 3 {
		return nil
	}
	var out []string
	for _, words := range simpleCommands(command) {
		i := commandWord(words)
		if i < 0 {
			continue
		}
		cmd := words[i]
		if !interpreters.MatchString(path.Base(cmd)) {
			out = append(out, cmd)
			continue
		}
		base := path.Base(cmd)
		shell := base == "bash" || base == "sh" || base == "zsh" || base == "dash" || base == "ksh"
		python := strings.HasPrefix(base, "python") || strings.HasPrefix(base, "pypy")
		for j := i + 1; j < len(words); j++ {
			a := words[j]
			if strings.HasPrefix(a, "-") && a != "--" {
				if shell && !strings.HasPrefix(a, "--") && strings.Contains(a, "c") {
					if j+1 < len(words) {
						out = append(out, executedPaths(words[j+1], depth+1)...)
					}
					break
				}
				if shell {
					continue
				}
				if (python && (a == "-c" || a == "-m")) || a == "-e" || a == "--eval" || a == "-p" || a == "--print" {
					break // inline code or a module: no file
				}
				if python && (a == "-W" || a == "-X") {
					j++ // takes a value
				}
				continue
			}
			if !shell && !python && launcherWords[a] {
				continue
			}
			out = append(out, a)
			break
		}
	}
	return out
}

// commandWord is the index of a simple command's command word, past
// assignments, keywords and wrappers (with their options and timeout's
// duration); -1 when there is none.
func commandWord(words []string) int {
	i := 0
	for i < len(words) {
		w := words[i]
		switch {
		case assignment.MatchString(w):
			i++
		case commandPrefixes[w]:
			i++
			for i < len(words) && (strings.HasPrefix(words[i], "-") || (w == "timeout" && duration.MatchString(words[i]))) {
				i++
			}
		default:
			return i
		}
	}
	return -1
}

var duration = regexp.MustCompile(`^[0-9.]+[smhd]?$`)

// simpleCommands splits a shell command into its simple commands (split at
// ; & | && || newlines, ( ) and command substitutions), each as words with
// quotes removed. It is a reader, not a shell: anything it can't follow
// (substitution inside quotes, heredocs) just isn't seen as executed.
func simpleCommands(s string) [][]string {
	var (
		cmds    [][]string
		words   []string
		cur     strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	endWord := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
		}
		words = nil
	}
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		switch {
		case escaped:
			cur.WriteRune(r)
			inWord, escaped = true, false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			endWord()
		case r == '>' || r == '<':
			// A redirection's target is no argument: drop it (and a
			// file descriptor before it, as in 2>&1).
			if inWord && strings.Trim(cur.String(), "0123456789") == "" {
				cur.Reset()
				inWord = false
			}
			endWord()
			for i+1 < len(rs) && (rs[i+1] == '>' || rs[i+1] == '&' || rs[i+1] == ' ') {
				i++
			}
			for i+1 < len(rs) && !strings.ContainsRune(" \t\n;&|()`", rs[i+1]) {
				i++
			}
		case strings.ContainsRune(";&|()`\n", r):
			endCmd()
		case r == '$' && i+1 < len(rs) && rs[i+1] == '(':
			endCmd()
			i++
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	endCmd()
	return cmds
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
		billing.Estimate{Units: float64(s.runs), Unit: "run"})
}

// record meters the runs of a command that started, whatever its exit
// status: the command is the model's, so a trailing "; false" must not make
// the API calls it made free. A loop inside one command is still one run.
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
		Units: float64(s.runs), Unit: "run", Detail: detail,
	})
}
