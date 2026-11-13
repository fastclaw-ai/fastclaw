package tools

import (
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
)

// A skill's configured keys (REPLICATE_API_TOKEN, …) are the operator's
// secrets, and the sandbox is the chatter's: whatever runs next to the
// skill's script can read what the script is given. So a command that
// gets keys must be the script's run alone — one interpreter (or the
// script itself), the script, plain arguments — and the keys are given to
// that one process (`K=v cmd`), never exported to a command line the
// model or the person chatting composed around it.
//
// This stops "run the skill; printenv" and friends. It can't stop someone
// with root in the sandbox who rewrites the script first: keyed skills
// that must stay secret from chatters need to run outside their sandbox.

// keyedInterpreters may launch a keyed skill's script.
var keyedInterpreters = map[string]bool{
	"python": true, "python3": true, "node": true, "bash": true, "sh": true,
}

// literalFeed is `echo '<text>' |` (or printf) in front of a command: the
// way skills document passing JSON on stdin. Single-quoted text expands
// nothing, so it can stay; the keys still go to the command after the pipe
// only.
var literalFeed = regexp.MustCompile(`^(?:echo|printf)(?:\s+-[neE]+)?(?:\s+'[^']*')+\s*\|\s*`)

// keyedRun checks command (as the model wrote it) for a keyed run of the
// skill named skill, and returns it with env given to that process only.
// An error says what to change; the command must not run.
func keyedRun(command, skill string, env map[string]string, skillDirs []string) (string, error) {
	body := strings.TrimSpace(command)
	feed := literalFeed.FindString(body)
	body = strings.TrimPrefix(body, feed)
	tail := ""
	// "2>&1" only merges output the sandbox already captures.
	if strings.HasSuffix(body, " 2>&1") {
		body = strings.TrimSpace(strings.TrimSuffix(body, " 2>&1"))
		tail = " 2>&1"
	}
	if err := plainShellWords(body); err != nil {
		return "", keyedRunError(skill, err.Error())
	}
	cmds := simpleCommands(body)
	if len(cmds) != 1 {
		return "", keyedRunError(skill, "it must be a single command")
	}
	words := cmds[0]
	script := words[0]
	if keyedInterpreters[path.Base(words[0])] {
		if len(words) < 2 {
			return "", keyedRunError(skill, "name the script to run")
		}
		script = words[1]
	}
	if name, rest := skillOfPath(script, skillDirs); name != skill || rest == "" {
		return "", keyedRunError(skill, fmt.Sprintf("start it with the script's path (e.g. python /skills/%s/<script>), with no options or other commands before it", skill))
	}

	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(feed)
	for _, k := range keys {
		if !assignment.MatchString(k + "=") {
			continue
		}
		b.WriteString(k)
		b.WriteString("=")
		b.WriteString(shellQuote(env[k]))
		b.WriteString(" ")
	}
	b.WriteString(body)
	b.WriteString(tail)
	return b.String(), nil
}

func keyedRunError(skill, why string) error {
	return fmt.Errorf("not run: this command runs the %q skill, which is given its configured keys, so the skill's script must run on its own — %s. Use exactly: <python|python3|node|bash|sh> /skills/%s/<script> <arguments> — optionally fed by echo '<single-quoted text>' | in front, or pass input with exec's stdin — with no other commands, pipes, ;, &&, redirects, $ or backticks in the same call (single-quote arguments that contain $). Run anything else in a separate exec call", skill, why, skill)
}

// plainShellWords rejects anything the shell would expand or treat as an
// operator: outside single quotes no $ or backtick; outside any quotes no
// ; & | < > ( ) { } or newline. Quotes must be closed.
func plainShellWords(s string) error {
	var quote rune
	escaped := false
	for _, r := range s {
		switch {
		case escaped:
			if r == '\n' {
				return fmt.Errorf("no line continuations")
			}
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			}
		case r == '\\':
			escaped = true
		case quote == '"':
			switch r {
			case '"':
				quote = 0
			case '$', '`':
				return fmt.Errorf("no $ or backticks (use single quotes)")
			}
		case r == '\'' || r == '"':
			quote = r
		case r == '$' || r == '`':
			return fmt.Errorf("no $ or backticks (use single quotes)")
		case strings.ContainsRune(";&|<>(){}\n\r", r):
			return fmt.Errorf("no %q", string(r))
		}
	}
	if quote != 0 || escaped {
		return fmt.Errorf("a quote is left open")
	}
	return nil
}
