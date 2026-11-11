package agent

import (
	"strings"
	"testing"
)

// Feeds s to a gate in chunks of n bytes; returns what was let through.
func gateText(s string, n int) (string, bool) {
	var g deltaGate
	var out strings.Builder
	for i := 0; i < len(s); i += n {
		out.WriteString(g.Push(s[i:min(i+n, len(s))]))
	}
	out.WriteString(g.Flush())
	return out.String(), g.Blocked()
}

func TestDeltaGateWithholdsToolMarkup(t *testing.T) {
	leaked := `我查一下。<｜｜DSML｜｜ calls> <｜｜DSML｜｜ invoke name="web_search"> <｜｜DSML｜｜ parameter name="query" string="true">"Maxgent"</｜｜DSML｜｜ parameter> </｜｜DSML｜｜ invoke> </｜｜DSML｜｜ calls>`
	for _, in := range []string{
		leaked,
		strings.ReplaceAll(leaked, "｜", " | "),
		`我查一下。<function_calls><invoke name="web_search"></invoke></function_calls>`,
		`我查一下。<tool_calls>…`,
	} {
		for _, n := range []int{1, 2, 3, 5, 7, 64, 1000} {
			got, blocked := gateText(in, n)
			if got != "我查一下。" || !blocked {
				t.Errorf("chunk %d of %q: got %q blocked=%v", n, in[:30], got, blocked)
			}
		}
	}
}

func TestDeltaGatePassesOrdinaryText(t *testing.T) {
	for _, in := range []string{
		"a < b and c > d",
		"use <div> and <span>",
		"x <",
		"<b>bold</b> 和 < 3",
		"Invoke it: <invoke-less text",
	} {
		for _, n := range []int{1, 2, 3, 64} {
			if got, blocked := gateText(in, n); got != in || blocked {
				t.Errorf("chunk %d of %q: got %q blocked=%v", n, in, got, blocked)
			}
		}
	}
}
