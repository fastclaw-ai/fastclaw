package agent

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/provider"
)

// maybeRecoverToolCalls runs recoverToolCallsFromContent on the
// response when the model returned no native tool_calls but did emit
// text. On a successful recovery it mutates the response in place:
// splices the parsed calls into resp.ToolCalls, replaces resp.Content
// with the residual (any human-readable preamble before the XML), and
// clears RawAssistant so the next round's history replay rebuilds the
// assistant message from the now-recovered fields instead of replaying
// the bad XML payload back into the model's context.
//
// Even when no calls are recoverable, if recoverToolCallsFromContent
// scrubbed leaked special-token noise (DeepSeek/Qwen `<｜…｜>` style
// delimiters that detokenize as visible `<| … |>` / `< | DSML | … >`
// garbage), we still replace resp.Content with the scrubbed version so
// the UI doesn't render the leaked tokens.
//
// Logs once at info level with the agent + model + recovered tool names
// so an operator can see how often the path triggers and for which
// (model, prompt) combinations — without this signal the recovery
// silently papers over genuine prompt/tool-definition bugs that should
// surface.
func (a *Agent) maybeRecoverToolCalls(resp *provider.Response) {
	if resp == nil || resp.HasToolCalls() || resp.Content == "" {
		return
	}
	recovered, residual := recoverToolCallsFromContent(resp.Content)
	if len(recovered) == 0 {
		if residual != resp.Content {
			slog.Info("scrubbed_leaked_tool_tokens",
				"agent", a.name, "model", a.model)
			resp.Content = residual
			resp.RawAssistant = nil
		}
		return
	}
	names := make([]string, 0, len(recovered))
	for _, tc := range recovered {
		names = append(names, tc.Function.Name)
	}
	slog.Info("recovered_tool_calls from assistant content",
		"agent", a.name, "model", a.model, "count", len(recovered),
		"tools", names)
	resp.ToolCalls = recovered
	resp.Content = residual
	resp.RawAssistant = nil
}

// scrubLeakedToolCallContent removes visible DSML / tool-call markup from
// assistant text that is going to be shown directly to the user. It is used
// on terminal synthesis paths where we do NOT want to execute any newly
// recovered calls (for example after the iteration cap has already been hit),
// but we still must not render raw `< | | DSML | | invoke ...>` garbage.
func scrubLeakedToolCallContent(content string) string {
	recovered, residual := recoverToolCallsFromContent(content)
	if len(recovered) > 0 || residual != content {
		return strings.TrimSpace(residual)
	}
	return content
}

// recoverToolCallsFromContent parses tool-call attempts that some open-
// source models (DeepSeek, Qwen variants) emit as XML in the assistant
// `content` field instead of using the OpenAI Chat Completions
// `tool_calls` schema. The shape we recognize is the Anthropic
// function_calls XML many of those models were trained on:
//
//	<invoke name="exec">
//	  <parameter name="command" string="true">echo hi</parameter>
//	  <parameter name="timeout" string="false">15</parameter>
//	</invoke>
//
// Returned tool calls have synthetic IDs (`recovered_…`) so the
// downstream tool_result message can be paired with the original
// assistant message — IDs the model itself proposed would collide with
// real OpenAI-style IDs from later turns.
//
// The second return is the original content with every matched invoke
// (and any optional <tool_calls>/<function_calls>/<DSML> wrapper) block
// stripped, so the saved assistant message doesn't keep the raw XML
// alongside the recovered structured calls — that would double-bill the
// tool call in the chat UI and confuse the next round.
//
// Returns (nil, content) when no invoke blocks match; the caller can
// then fall through to the normal "model didn't ask for a tool" branch
// without the recovery path adding any cost.
func recoverToolCallsFromContent(content string) ([]provider.ToolCall, string) {
	// Fast path: skip when there's nothing tool-call-shaped at all.
	// We trigger on either the normal <invoke marker or the leaked
	// special-token shape (`<|`, `<｜`, `< | DSML`, etc.) so DeepSeek/
	// Qwen detokenization garbage gets at least scrubbed even when no
	// real invoke is present to recover.
	if !strings.Contains(content, "<invoke") && !tagLeakHintRE.MatchString(content) {
		return nil, content
	}
	// Collapse leaked-token noise (`<｜tool_calls｜>`, `< | | DSML | |
	// invoke …>`, closing variants) into plain `<tag …>` shapes the
	// parser already understands. No-op when the content is clean.
	normalized := leakedTagRE.ReplaceAllString(content, `<${1}${2}${3}>`)

	matches := invokeRE.FindAllStringSubmatchIndex(normalized, -1)
	if len(matches) == 0 {
		// Nothing recoverable. If normalization changed the content we
		// still scrub wrapper tags from the residual so the UI doesn't
		// render the leaked-token garbage — the model "called" something
		// we couldn't reconstruct, but at least the chat reads clean.
		if normalized == content {
			return nil, content
		}
		scrubbed := strings.TrimSpace(stripRE.ReplaceAllString(normalized, ""))
		return nil, scrubbed
	}
	calls := make([]provider.ToolCall, 0, len(matches))
	for i, m := range matches {
		// m: [whole-lo, whole-hi, name-lo, name-hi, body-lo, body-hi]
		name := normalized[m[2]:m[3]]
		body := normalized[m[4]:m[5]]
		args := parseInvokeParameters(body)
		argJSON, err := json.Marshal(args)
		if err != nil {
			// Marshal of map[string]any can only fail on cycles, which
			// our scalar map can't produce — but keep the fail-open
			// behavior anyway: skip this one rather than panicking and
			// killing the whole turn.
			continue
		}
		calls = append(calls, provider.ToolCall{
			ID:   fmt.Sprintf("recovered_%d", i),
			Type: "function",
			Function: provider.FunctionCall{
				Name:      name,
				Arguments: string(argJSON),
			},
		})
	}
	if len(calls) == 0 {
		if normalized == content {
			return nil, content
		}
		scrubbed := strings.TrimSpace(stripRE.ReplaceAllString(normalized, ""))
		return nil, scrubbed
	}
	// Strip the recovered XML out of the content. We pull every
	// <invoke> block, plus the common outer wrappers (tool_calls,
	// function_calls, DSML) so the residual text is just the model's
	// human-readable preamble — if any — without dangling tags.
	stripped := stripRE.ReplaceAllString(normalized, "")
	stripped = strings.TrimSpace(stripped)
	return calls, stripped
}

// invokeRE pulls one <invoke name="..."> ... </invoke> block at a time.
//   - non-greedy `(?s).*?` so two adjacent invokes don't merge into one.
//   - tolerates `<invoke>` with no name attribute by demanding a quote-
//     delimited name= attribute up front; the parser is recovery-only
//     and a name-less invoke can't be turned into a tool call anyway.
var invokeRE = regexp.MustCompile(`(?s)<invoke\s+name="([^"]+)"\s*>(.*?)</invoke>`)

// parameterRE matches `<parameter name="key" string="true|false">VALUE</parameter>`.
// The `string` attribute is the type hint:
//   - string="true"  → VALUE is the JSON string contents (we re-quote it).
//   - string="false" → VALUE is raw JSON (number, bool, array, object).
//
// Absent attribute defaults to treating VALUE as a string — that's the
// safest interpretation when the model omits the hint, and it's what
// human-readable XML would imply.
var parameterRE = regexp.MustCompile(`(?s)<parameter\s+name="([^"]+)"(?:\s+string="(true|false)")?\s*>(.*?)</parameter>`)

// stripRE finds:
//   - every invoke block (so we can drop it after a successful parse)
//   - the optional outer <tool_calls> / <function_calls> / <DSML>
//     wrappers some models add (open AND close tags), so the residual
//     content doesn't keep a dangling `</tool_calls>`.
//
// The DSML wrapper keeps whatever followed the marker: DeepSeek-style
// gateways also emit `<｜｜DSML｜｜ calls>`, which normalizes to
// `<DSML｜｜ calls>`, so any tag starting with DSML is a wrapper.
var stripRE = regexp.MustCompile(`(?s)<invoke\s+name="[^"]+"\s*>.*?</invoke>|</?(?:tool_calls|function_calls)\s*/?>|</?DSML[^>]*>`)

// tagLeakHintRE flags content worth running the leaked-token normalizer
// on. We match either a normal `<invoke` (the original recovery
// trigger) or the leaked special-token prefix shape (`<|`, `<｜`,
// `</|`, `< | …`, etc.) so DeepSeek/Qwen detokenization garbage that
// doesn't contain a recoverable <invoke> still gets scrubbed.
var tagLeakHintRE = regexp.MustCompile(`<invoke|<\s*/?\s*[|｜]`)

// leakedTagRE collapses leaked tool-call delimiters back into the plain
// `<tag …>` / `</tag>` shape the recovery parser understands. Some
// open-source models (DeepSeek-V3/R1, Qwen variants) use special tokens
// like `<｜tool_calls｜>` / `<｜DSML｜>` for tool-call framing; when the
// tokenizer round-trip fails or a downstream layer renders those tokens
// as text, the user sees shapes like:
//
//	<｜tool_calls｜>
//	<|tool_calls|>
//	< | | DSML | | invoke name="exec">
//	</ | | DSML | | invoke>
//	<｜/invoke｜>
//
// The `/` for a closing tag may sit either right after `<` or inside
// the leaked noise (depending on which side of the pipe the model put
// it), so we let either noise group consume it.
//
// Captures:
//
//	$1 = optional `/` for closing tags
//	$2 = real tag name (invoke / parameter / tool_calls / function_calls / DSML)
//	$3 = attributes that follow the tag name (e.g. ` name="exec"`)
//
// Replacement `<${1}${2}${3}>` reconstructs `<invoke name="exec">` etc.
// Clean inputs like `<DSML>` / `<invoke name="x">` round-trip unchanged
// because all the noise groups are zero-width.
var leakedTagRE = regexp.MustCompile(`<(?:[|｜\s]|DSML)*(/?)(?:[|｜\s]|DSML)*(invoke|parameter|tool_calls|function_calls|DSML)([^>]*?)[|｜\s]*>`)

// parseInvokeParameters walks the parameters inside one invoke body and
// returns the assembled arguments map. Unknown/malformed parameters are
// silently skipped — we prefer to call the tool with whatever args we
// could parse rather than reject the recovery wholesale.
func parseInvokeParameters(body string) map[string]any {
	out := map[string]any{}
	for _, p := range parameterRE.FindAllStringSubmatch(body, -1) {
		// p[1]=name, p[2]="true"/"false"/"", p[3]=raw VALUE
		name := p[1]
		typeHint := p[2]
		raw := p[3]
		if typeHint == "false" {
			// Raw JSON value. If it doesn't parse, fall back to string —
			// better to ship a wrong-typed arg than drop the parameter
			// entirely (the tool itself can usually coerce numeric
			// strings, and the loop's BeforeToolCall hook logs the args
			// so an operator can see what came through).
			var v any
			if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &v); err == nil {
				out[name] = v
				continue
			}
		}
		out[name] = raw
	}
	return out
}

// markupStartRE finds where tool-call markup begins in streamed text:
// leaked special tokens (`<｜`, `< |`) or a bare XML call wrapper.
var markupStartRE = regexp.MustCompile(`<\s*(?:[|｜]|DSML|invoke\s+name=|function_calls|tool_calls)`)

// markupWords are what a held-back `<…` tail may still turn into.
var markupWords = []string{"DSML", "invoke name=", "function_calls", "tool_calls"}

// deltaGate keeps tool-call markup out of the live text stream. Some
// models (DeepSeek-style) write their calls as text; maybeRecoverToolCalls
// turns them back into calls, but only once the response is complete, and
// streamed deltas can't be taken back: a product relaying them into a chat
// would show the markup. From the first sign of markup on, the rest of the
// round is withheld; the cleaned text, if any, arrives in the round's
// final content.
type deltaGate struct {
	pending string
	blocked bool
}

// Push takes a delta and returns the text safe to send now.
func (g *deltaGate) Push(s string) string {
	if g.blocked {
		return ""
	}
	buf := g.pending + s
	g.pending = ""
	if loc := markupStartRE.FindStringIndex(buf); loc != nil {
		g.blocked = true
		return buf[:loc[0]]
	}
	if i := strings.LastIndex(buf, "<"); i >= 0 && mayBecomeMarkup(buf[i:]) {
		g.pending = buf[i:]
		return buf[:i]
	}
	return buf
}

// Flush returns what is still held when the stream ends cleanly.
func (g *deltaGate) Flush() string {
	if g.blocked {
		return ""
	}
	out := g.pending
	g.pending = ""
	return out
}

// Blocked reports whether markup was seen (and the rest withheld).
func (g *deltaGate) Blocked() bool { return g.blocked }

// mayBecomeMarkup: tail starts with "<" and later deltas could still
// complete it into markup.
func mayBecomeMarkup(tail string) bool {
	if len(tail) > 32 || strings.ContainsAny(tail[1:], "<>") {
		return false
	}
	rest := strings.TrimLeft(tail[1:], " \t\r\n")
	if rest == "" || !utf8.ValidString(rest) {
		return true // a split multi-byte rune (｜) may be on its way
	}
	for _, w := range markupWords {
		if strings.HasPrefix(w, rest) {
			return true
		}
	}
	return false
}
