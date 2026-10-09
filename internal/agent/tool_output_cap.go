package agent

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxToolResultBytes caps how much of one tool result enters the model
// context. A single exec that dumps a generated HTML page (half a MB of
// inlined assets) made the next request ~150k tokens: the provider took
// longer than the response-header timeout just to start answering, and
// every later round paid for it again. The head and tail are kept (where
// errors and summaries usually are); the full text is saved to the
// session workspace so the model can read the rest in slices.
const (
	maxToolResultBytes  = 64 << 10
	toolResultHeadBytes = 48 << 10
	toolResultTailBytes = 12 << 10
)

// capToolResult returns content unchanged when it fits, otherwise a
// head + tail excerpt with a note pointing at the saved full output.
// dir is the turn's working directory (Registry.HostWorkDir); "" skips
// saving and the note says the middle was dropped.
func capToolResult(content, dir, callID string) string {
	if len(content) <= maxToolResultBytes {
		return content
	}
	head := truncateUTF8(content, toolResultHeadBytes)
	tail := tailUTF8(content, toolResultTailBytes)
	omitted := len(content) - len(head) - len(tail)

	where := "the omitted middle was not saved"
	if dir != "" {
		name := "tool-" + sanitizeFileToken(callID) + ".txt"
		rel := filepath.Join(".tool-outputs", name)
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err == nil {
			if err := os.WriteFile(full, []byte(content), 0o644); err == nil {
				where = fmt.Sprintf("the full output is saved at %s (relative to the working directory) — read the part you need with a ranged read (sed -n / head / tail / grep) instead of printing it all", rel)
			} else {
				slog.Warn("save full tool output", "path", full, "error", err)
			}
		}
	}
	return fmt.Sprintf("%s\n\n[… output truncated: %d bytes total, %d bytes omitted here; %s …]\n\n%s",
		head, len(content), omitted, where, tail)
}

func truncateUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func tailUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	i := len(s) - n
	for i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

func sanitizeFileToken(s string) string {
	s = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, s)
	if s == "" {
		return "output"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}
