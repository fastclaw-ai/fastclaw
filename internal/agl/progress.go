package agl

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Tool-mode progress: a long local run reports what the agent is doing
// to the chat now and then, so the person knows it's alive. Sources, in
// order of preference: the agent's own interim notes (Codex commentary:
// "先看一下目录结构…") and else its latest tool call ("运行 ls ~/code").
// The final answer is never sent here — the LLM agent replies with it.

const (
	// MsgWorkingAt: first notice; "%s" agent, then what it's doing.
	MsgWorkingAt = "%s 正在你的电脑上处理：%s"
	// MsgProgress: later updates; agent, minutes, what it's doing.
	MsgProgress = "%s 进展（%d 分钟）：%s"
	// MsgStillWorking: no new activity since the last update.
	MsgStillWorking = "%s 还在处理（已 %d 分钟）…"

	progressEvery = 60 * time.Second
	// stillWorkingEvery: quiet runs still get a sign of life this often.
	stillWorkingEvery = 3 * time.Minute
	progressMaxRunes  = 160
	commentaryMaxLen  = 2 << 10
)

type progress struct {
	started time.Time

	commentary   strings.Builder
	inCommentary bool
	note         string // latest interim note
	tool         string // latest tool call
	noteNew      bool   // note changed since the last update
	toolNew      bool

	lastSent time.Time
}

func newProgress(now time.Time) *progress { return &progress{started: now} }

func (p *progress) event(e AgentEvent) {
	switch e.Kind {
	case "text_delta":
		if e.Phase != "commentary" {
			p.inCommentary = false
			return
		}
		if !p.inCommentary {
			p.commentary.Reset()
			p.inCommentary = true
		}
		if p.commentary.Len() < commentaryMaxLen {
			p.commentary.WriteString(e.Text)
		}
		if n := oneLine(p.commentary.String()); n != "" {
			p.note, p.noteNew = n, true
		}
	case "tool_start":
		p.inCommentary = false
		if s := toolSummary(e.Name, e.Input); s != "" {
			p.tool, p.toolNew = s, true
		}
	case "turn_started":
		*p = progress{started: p.started, lastSent: p.lastSent}
	}
}

// activity is what to report now ("" = nothing new) and marks it sent.
func (p *progress) activity() string {
	switch {
	case p.noteNew:
		p.noteNew, p.toolNew = false, false
		return p.note
	case p.toolNew:
		p.toolNew = false
		return p.tool
	}
	return ""
}

// first is the first notice's text.
func (p *progress) first(agentName string, now time.Time) string {
	p.lastSent = now
	if a := p.activity(); a != "" {
		return fmt.Sprintf(MsgWorkingAt, agentName, a)
	}
	return fmt.Sprintf(MsgWorking, agentName)
}

// update is a later update's text, or "" to stay quiet this time.
func (p *progress) update(agentName string, now time.Time) string {
	mins := int(now.Sub(p.started) / time.Minute)
	if mins < 1 {
		mins = 1
	}
	if a := p.activity(); a != "" {
		p.lastSent = now
		return fmt.Sprintf(MsgProgress, agentName, mins, a)
	}
	if now.Sub(p.lastSent) >= stillWorkingEvery {
		p.lastSent = now
		return fmt.Sprintf(MsgStillWorking, agentName, mins)
	}
	return ""
}

// toolSummary turns a tool call into a short line for the chat.
func toolSummary(name string, input json.RawMessage) string {
	var in map[string]any
	_ = json.Unmarshal(input, &in)
	str := func(keys ...string) string {
		for _, k := range keys {
			switch v := in[k].(type) {
			case string:
				if strings.TrimSpace(v) != "" {
					return v
				}
			case []any:
				parts := make([]string, 0, len(v))
				for _, x := range v {
					if s, ok := x.(string); ok {
						parts = append(parts, s)
					}
				}
				// ["bash","-lc","<script>"] → the script.
				if len(parts) == 3 && (parts[1] == "-lc" || parts[1] == "-c") {
					return parts[2]
				}
				if len(parts) > 0 {
					return strings.Join(parts, " ")
				}
			}
		}
		return ""
	}
	lower := strings.ToLower(name)
	var line string
	switch {
	case strings.Contains(lower, "image"):
		line = "生成图片"
		if s := str("prompt", "description"); s != "" {
			line += " " + s
		}
	case lower == "bash" || strings.Contains(lower, "exec") || strings.Contains(lower, "shell") || strings.Contains(lower, "command"):
		if s := str("description"); s != "" && lower == "bash" {
			line = s
		} else if s := str("command", "cmd", "argv"); s != "" {
			line = "运行 " + s
		}
	case strings.Contains(lower, "edit") || strings.Contains(lower, "write") || strings.Contains(lower, "patch"):
		line = "修改文件"
		if s := str("file_path", "path"); s != "" {
			line = "修改 " + s
		}
	case strings.Contains(lower, "read") || lower == "view":
		if s := str("file_path", "path"); s != "" {
			line = "查看 " + s
		}
	case strings.Contains(lower, "grep") || strings.Contains(lower, "glob") || strings.Contains(lower, "search"):
		line = "搜索"
		if s := str("pattern", "query"); s != "" {
			line += " " + s
		}
	case strings.Contains(lower, "web") || strings.Contains(lower, "fetch"):
		line = "查网页"
		if s := str("url", "query"); s != "" {
			line += " " + s
		}
	}
	if line == "" && name != "" {
		line = "使用 " + name
	}
	return oneLine(line)
}

// oneLine collapses whitespace and clips to progressMaxRunes.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) <= progressMaxRunes {
		return s
	}
	r := []rune(s)
	return string(r[:progressMaxRunes]) + "…"
}
