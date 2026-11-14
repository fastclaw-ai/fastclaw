package agl

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPushNamesAndFolder(t *testing.T) {
	if got := pushDir(LocalMachine{AppName: "WeClaw"}); got != "~/Downloads/WeClaw" {
		t.Errorf("pushDir = %q", got)
	}
	if got := pushDir(LocalMachine{AppName: "../x/"}); got != "~/Downloads/x" {
		t.Errorf("pushDir unsafe = %q", got)
	}
	if got := pushDir(LocalMachine{}); got != "~/Downloads/FastClaw" {
		t.Errorf("pushDir default = %q", got)
	}
	for in, want := range map[string]string{
		"image_956hz_0.jpg":  "image_956hz_0.jpg",
		"../../.ssh/key.png": "key.png",
		".env":               "env",
		"a\\b\\c.pdf":        "c.pdf",
		"":                   "file",
		"line\nbreak.png":    "linebreak.png",
	} {
		if got := pushName(in); got != want {
			t.Errorf("pushName(%q) = %q, want %q", in, got, want)
		}
	}
}

// A file from the chat is written to the computer before the task, and
// the task names where it is.
func TestRunHandsOverFiles(t *testing.T) {
	f := newToolsFixture(t)
	res, errc := f.run(ToolRun{Agent: "claude", Task: "make a sticker from my photo", Files: []ToolFile{
		{Name: "image_956hz_0.jpg", Bytes: []byte("hi")},
		{Name: "image_956hz_0.jpg", Bytes: []byte("yo")},
	}})
	turn := f.promptTurn(t)

	writes := f.link.calls("fs.write")
	if len(writes) != 2 {
		t.Fatalf("fs.write = %v", writes)
	}
	var w struct {
		Path    string `json:"path"`
		Content string `json:"content_base64"`
	}
	_ = json.Unmarshal([]byte(writes[0]), &w)
	if w.Path != "~/Downloads/FastClaw/image_956hz_0.jpg" || w.Content != "aGk=" {
		t.Fatalf("first write = %+v", w)
	}
	_ = json.Unmarshal([]byte(writes[1]), &w)
	if w.Path != "~/Downloads/FastClaw/image_956hz_0-2.jpg" {
		t.Fatalf("same name not kept apart: %+v", w)
	}
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(f.link.calls("session.prompt")[0]), &p)
	if !strings.HasPrefix(p.Text, "make a sticker from my photo\n\n") ||
		!strings.Contains(p.Text, "- ~/Downloads/FastClaw/image_956hz_0.jpg\n- ~/Downloads/FastClaw/image_956hz_0-2.jpg") {
		t.Fatalf("task = %q", p.Text)
	}
	f.ev(AgentEvent{Kind: "turn_ended", TurnID: turn, Outcome: "completed"})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	<-res
}

// A read-only link can't take files: the task doesn't start and the model
// gets something to tell the person.
func TestRunFilesReadOnlyLink(t *testing.T) {
	f := newToolsFixture(t)
	f.link.fsWriteErr = errors.New("`fs.write` needs a link with more access than `ask` (agl connect … --access)")
	res, errc := f.run(ToolRun{Agent: "claude", Task: "look at this", Files: []ToolFile{{Name: "a.png", Bytes: []byte("x")}}})
	<-res
	err := <-errc
	var te *ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "only allows reading") {
		t.Fatalf("err = %v", err)
	}
	if len(f.link.calls("session.prompt")) != 0 {
		t.Fatal("task started without its files")
	}
}

// With agl's built-in tools on an ask link, the person approves the copy
// in the chat (a line that names the file), then the task starts.
func TestRunHandsOverFilesWithApproval(t *testing.T) {
	f := builtinFixture(t)
	f.link.toolsList = `[{"name":"write","description":"Write.","input_schema":{"type":"object","properties":{}}}]`
	f.link.toolsCall = `{"status":"approval_required","approval_id":"ap1","title":"Write x (0 bytes)"}`
	res, errc := f.run(ToolRun{Agent: "claude", Machine: "Mini", Task: "make a sticker", Files: []ToolFile{{Name: "image_956hz_0.jpg", Bytes: []byte("hi")}}})
	f.out.waitFor(t, "Mini · Save image_956hz_0.jpg (1 KB, from the chat) to ~/Downloads/WeClaw")
	if !f.tools.InterceptApproval(context.Background(), f.chat, "u_alice", "1") {
		t.Fatal("reply not taken")
	}
	turn := f.promptTurn(t)
	calls := f.link.calls("tools.call")
	if len(calls) != 1 || !strings.Contains(calls[0], `"name":"write"`) ||
		!strings.Contains(calls[0], `"content_base64":"aGk="`) || !strings.Contains(calls[0], `"path":"~/Downloads/WeClaw/image_956hz_0.jpg"`) {
		t.Fatalf("tools.call = %v", calls)
	}
	if len(f.link.calls("fs.write")) != 0 {
		t.Fatal("plain fs.write used")
	}
	var p struct {
		Text string `json:"text"`
	}
	_ = json.Unmarshal([]byte(f.link.calls("session.prompt")[0]), &p)
	if !strings.Contains(p.Text, "- ~/Downloads/WeClaw/image_956hz_0.jpg") {
		t.Fatalf("task = %q", p.Text)
	}
	f.ev(AgentEvent{Kind: "turn_ended", TurnID: turn, Outcome: "completed"})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	<-res
}

// Denying the copy stops the task before it starts.
func TestRunHandsOverFilesDenied(t *testing.T) {
	f := builtinFixture(t)
	f.link.toolsList = `[{"name":"write","description":"Write.","input_schema":{"type":"object","properties":{}}}]`
	f.link.toolsCall = `{"status":"approval_required","approval_id":"ap1","title":"Write x"}`
	res, errc := f.run(ToolRun{Agent: "claude", Machine: "Mini", Task: "x", Files: []ToolFile{{Name: "a.png", Bytes: []byte("hi")}}})
	f.out.waitFor(t, "Save a.png")
	f.tools.InterceptApproval(context.Background(), f.chat, "u_alice", "3")
	<-res
	var te *ToolError
	if err := <-errc; !errors.As(err, &te) || !strings.Contains(te.Msg, "denied") {
		t.Fatalf("err = %v", err)
	}
	if len(f.link.calls("session.prompt")) != 0 {
		t.Fatal("task started")
	}
}
