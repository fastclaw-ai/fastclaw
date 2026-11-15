package agent

import "unicode/utf8"

// guestToolCardMax caps the tool output a regular chatter's browser gets
// in a tool card. The model still has the whole (redacted) result.
const guestToolCardMax = 1500

// guestToolResultView is what a regular chatter's web client shows for a
// tool result. A loaded skill's instructions are the agent's private
// configuration and never leave the server; other output is shortened.
func guestToolResultView(tool, result string) string {
	switch tool {
	case "load_skill":
		return "(skill loaded)"
	}
	if utf8.RuneCountInString(result) <= guestToolCardMax {
		return result
	}
	r := []rune(result)
	return string(r[:guestToolCardMax]) + "\n…"
}
