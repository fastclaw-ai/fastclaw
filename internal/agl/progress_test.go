package agl

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestToolSummary(t *testing.T) {
	cases := map[string]string{
		`Bash|{"command":"ls -la ~/code","description":"List code dir"}`: "List code dir",
		`exec_command|{"command":["bash","-lc","git log -1"]}`:           "运行 git log -1",
		`Edit|{"file_path":"/x/main.go"}`:                                "修改 /x/main.go",
		`Read|{"file_path":"/x/README.md"}`:                              "查看 /x/README.md",
		`image_generation|{"prompt":"a cat"}`:                            "生成图片 a cat",
		`Grep|{"pattern":"TODO"}`:                                        "搜索 TODO",
		`mcp__foo|{}`:                                                    "使用 mcp__foo",
		`Bash|{"command":"echo ` + strings.Repeat("x", 400) + `"}`:       "运行 echo " + strings.Repeat("x", progressMaxRunes-utf8.RuneCountInString("运行 echo ")) + "…",
	}
	for in, want := range cases {
		name, input, _ := strings.Cut(in, "|")
		if got := toolSummary(name, json.RawMessage(input)); got != want {
			t.Errorf("toolSummary(%s) = %q, want %q", in, got, want)
		}
	}
}

func TestProgressPrefersNotesAndStaysQuiet(t *testing.T) {
	t0 := time.Now()
	p := newProgress(t0)
	if got := p.first("Codex", t0.Add(20*time.Second)); got != "Codex 正在你的电脑上处理…" {
		t.Fatalf("first without activity = %q", got)
	}
	p.event(AgentEvent{Kind: "text_delta", Phase: "commentary", Text: "先看一下"})
	p.event(AgentEvent{Kind: "text_delta", Phase: "commentary", Text: "目录结构。"})
	p.event(AgentEvent{Kind: "tool_start", Name: "exec_command", Input: json.RawMessage(`{"command":"ls"}`)})
	if got := p.update("Codex", t0.Add(80*time.Second)); got != "Codex 进展（1 分钟）：先看一下目录结构。" {
		t.Fatalf("update = %q", got)
	}
	// Nothing new: quiet, until stillWorkingEvery passes.
	if got := p.update("Codex", t0.Add(140*time.Second)); got != "" {
		t.Fatalf("quiet update = %q", got)
	}
	p.event(AgentEvent{Kind: "tool_start", Name: "exec_command", Input: json.RawMessage(`{"command":"git status"}`)})
	if got := p.update("Codex", t0.Add(200*time.Second)); got != "Codex 进展（3 分钟）：运行 git status" {
		t.Fatalf("tool update = %q", got)
	}
	if got := p.update("Codex", t0.Add(200*time.Second+stillWorkingEvery)); got != "Codex 还在处理（已 6 分钟）…" {
		t.Fatalf("still working = %q", got)
	}
	// The answer text is never progress.
	p.event(AgentEvent{Kind: "text_delta", Text: "Final answer"})
	if got := p.update("Codex", t0.Add(7*time.Minute)); strings.Contains(got, "Final") {
		t.Fatalf("answer leaked: %q", got)
	}
}

func TestToolsRunSendsProgress(t *testing.T) {
	f := newToolsFixture(t)
	f.tools.NoticeAfter = 30 * time.Millisecond
	f.tools.ProgressEvery = 30 * time.Millisecond
	res, errc := f.run(ToolRun{Agent: "claude", Task: "draw"})
	turn := f.promptTurn(t)
	f.ev(AgentEvent{Kind: "turn_started", TurnID: turn})
	f.ev(AgentEvent{Kind: "text_delta", Phase: "commentary", Text: "Generating the picture now."})
	f.out.waitFor(t, "Claude Code 正在你的电脑上处理：Generating the picture now.")
	f.ev(AgentEvent{Kind: "tool_start", Name: "Bash", Input: json.RawMessage(`{"command":"cp a.png ~/Downloads/"}`)})
	f.out.waitFor(t, "Claude Code 进展（1 分钟）：运行 cp a.png ~/Downloads/")
	f.ev(AgentEvent{Kind: "text_delta", Text: "Saved."})
	f.ev(AgentEvent{Kind: "turn_ended", TurnID: turn, Outcome: "completed"})
	if out, err := <-res, <-errc; err != nil || !strings.Contains(out, "Saved.") {
		t.Fatalf("result = %q, %v", out, err)
	}
}
