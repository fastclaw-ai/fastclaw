package gateway

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/workspace"
)

func TestAppendNewWorkspaceMediaSkipsHistoricalFiles(t *testing.T) {
	ctx := context.Background()
	ws := workspace.NewLocalFS(t.TempDir())
	put := func(name, body string) {
		t.Helper()
		if err := ws.Put(ctx, "agent", "", "chat", name, bytes.NewBufferString(body), int64(len(body)), "application/octet-stream"); err != nil {
			t.Fatal(err)
		}
	}

	put("old.pdf", "old")
	before, ok := snapshotWorkspacePaths(ctx, ws, "agent", "", "chat")
	if !ok {
		t.Fatal("snapshot failed")
	}

	// Rewriting an old artifact simulates sandbox sync refreshing its mtime.
	put("old.pdf", "old")
	put("current.pdf", "current")

	got := appendNewWorkspaceMedia(ctx, ws, "agent", "", "chat", before, ok, nil)
	if len(got) != 1 || got[0].Filename != "current.pdf" || string(got[0].Bytes) != "current" {
		t.Fatalf("got %#v, want only current.pdf", got)
	}
}

func TestAppendNewWorkspaceMediaRequiresSuccessfulSnapshot(t *testing.T) {
	ctx := context.Background()
	ws := workspace.NewLocalFS(t.TempDir())
	if err := ws.Put(ctx, "agent", "", "chat", "old.pdf", bytes.NewBufferString("old"), 3, "application/pdf"); err != nil {
		t.Fatal(err)
	}

	got := appendNewWorkspaceMedia(ctx, ws, "agent", "", "chat", nil, false, nil)
	if len(got) != 0 {
		t.Fatalf("got %d attachments after failed snapshot, want 0", len(got))
	}
}

func TestSplitFilesFromReplyUsesExplicitFinalDocument(t *testing.T) {
	ctx := context.Background()
	ws := workspace.NewLocalFS(t.TempDir())
	for name, body := range map[string]string{"draft.pdf": "draft", "final.pdf": "final"} {
		if err := ws.Put(ctx, "agent", "", "chat", name, bytes.NewBufferString(body), int64(len(body)), "application/pdf"); err != nil {
			t.Fatal(err)
		}
	}

	text, items := splitFilesFromReply(ctx, ws, "agent", "", "chat", "已完成：[最终报告](/workspace/final.pdf)")
	if text != "已完成：" {
		t.Fatalf("text = %q", text)
	}
	if len(items) != 1 || items[0].Filename != "final.pdf" || string(items[0].Bytes) != "final" {
		t.Fatalf("items = %#v, want only final.pdf", items)
	}
}

// A bare relative link to a workspace file is a file to send; web links
// and links to files that don't exist stay in the text.
func TestSplitFilesFromReplyRelativePath(t *testing.T) {
	ctx := context.Background()
	ws := workspace.NewLocalFS(t.TempDir())
	if err := ws.Put(ctx, "agent", "", "chat", "oil-widget-v1.2.9.scripting", bytes.NewBufferString("pkg"), 3, "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	reply := "包在这里：[oil-widget-v1.2.9.scripting](oil-widget-v1.2.9.scripting)\n参考 [文档](https://example.com/a.html) 和 [旧版](missing.zip)"
	text, items := splitFilesFromReply(ctx, ws, "agent", "", "chat", reply)
	if len(items) != 1 || items[0].Filename != "oil-widget-v1.2.9.scripting" {
		t.Fatalf("items = %#v", items)
	}
	for _, keep := range []string{"[文档](https://example.com/a.html)", "[旧版](missing.zip)"} {
		if !strings.Contains(text, keep) {
			t.Errorf("text lost %q: %q", keep, text)
		}
	}
	if strings.Contains(text, "(oil-widget") {
		t.Errorf("sent file's link left in text: %q", text)
	}
}

func TestIsWorkspaceFileRef(t *testing.T) {
	for ref, want := range map[string]bool{
		"/workspace/a.pdf": true, "report.pdf": true, "out/data.zip": true,
		"https://x.com/a.pdf": false, "mailto:a@b.c": false, "#top": false,
		"/etc/passwd": false, "README": false, "www.example.com": false,
	} {
		if got := isWorkspaceFileRef(ref); got != want {
			t.Errorf("%q: got %v want %v", ref, got, want)
		}
	}
}
