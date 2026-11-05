package agl

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestFetchPathRules(t *testing.T) {
	ok := map[[2]string]string{
		{"~/Downloads/cat.png", ""}:      "~/Downloads/cat.png",
		{"/Users/a/out/Report.PDF", ""}:  "/Users/a/out/Report.PDF",
		{"out/cat.png", "~/code"}:        "~/code/out/cat.png",
		{"~/../../etc/x.png", ""}:        "~/etc/x.png", // stays under home
		{"/tmp/a/../b/chart.jpeg", "/x"}: "/tmp/b/chart.jpeg",
	}
	for in, want := range ok {
		got, err := fetchPath(in[0], in[1])
		if err != nil || got != want {
			t.Errorf("fetchPath(%q, %q) = %q, %v; want %q", in[0], in[1], got, err, want)
		}
	}
	bad := []struct{ path, cwd, why string }{
		{"", "", "required"},
		{"cat.png", "", "full path"},
		{"~/.ssh/id_rsa.png", "", "hidden"},
		{"/repo/.git/x.png", "", "hidden"},
		{"~/cat.svg", "", "PNG"},
		{"~/.zshrc", "", "hidden"},
		{"~/notes.env", "", "Only"},
		{"/etc/passwd", "", "Only"},
	}
	for _, c := range bad {
		_, err := fetchPath(c.path, c.cwd)
		var te *ToolError
		if !errors.As(err, &te) || !strings.Contains(te.Msg, c.why) {
			t.Errorf("fetchPath(%q) err = %v; want ToolError containing %q", c.path, err, c.why)
		}
	}
}

func TestFetchReadsOnlyOwnAllowedMachine(t *testing.T) {
	f := newToolsFixture(t)
	ctx := context.Background()
	got, err := f.tools.Fetch(ctx, FetchRun{UserID: "u_alice", Path: "img/cat.png"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Name != "cat.png" || got.ContentType != "image/png" || string(got.Bytes) != "hi" || !got.IsImage() {
		t.Fatalf("got %+v", got)
	}
	if c := f.link.calls("fs.read"); len(c) != 1 || c[0] != `{"path":"~/code/img/cat.png"}` {
		t.Fatalf("fs.read calls = %v", c)
	}
	// Another user's machine, a machine that disallows agents, a revoked one.
	for _, m := range []string{"BobMac", "Desktop", "Old"} {
		if _, err := f.tools.Fetch(ctx, FetchRun{UserID: "u_alice", Machine: m, Path: "~/a.png"}); err == nil {
			t.Errorf("fetch from %s should fail", m)
		}
	}
	if len(f.link.calls("fs.read")) != 1 {
		t.Fatal("refused fetches must not reach the machine")
	}
	f.link.mu.Lock()
	f.link.online = false
	f.link.mu.Unlock()
	if _, err := f.tools.Fetch(ctx, FetchRun{UserID: "u_alice", Path: "~/a.png"}); err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("offline err = %v", err)
	}
}
