package agent

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceRel(t *testing.T) {
	ok := map[string]string{
		"/workspace/image_956hz_0.jpg": "image_956hz_0.jpg",
		"workspace/out/a.png":          "out/a.png",
		"a.png":                        "a.png",
		"./b/c.png":                    "b/c.png",
	}
	for in, want := range ok {
		if got, good := workspaceRel(in); !good || got != want {
			t.Errorf("workspaceRel(%q) = %q, %v; want %q", in, got, good, want)
		}
	}
	for _, in := range []string{"", "/workspace", "/etc/passwd", "../x.png", "/workspace/../x", ".env", "/workspace/.git/config", "a/.ssh/k"} {
		if got, good := workspaceRel(in); good {
			t.Errorf("workspaceRel(%q) = %q; want refused", in, got)
		}
	}
}

func TestLoadSessionFileFromHostWorkspace(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "image_956hz_0.jpg"), []byte("jpg"), 0o644); err != nil {
		t.Fatal(err)
	}
	a := &Agent{workspacePath: dir}
	f, err := a.loadSessionFile(context.Background(), "s", "", "/workspace/image_956hz_0.jpg")
	if err != nil || f.Name != "image_956hz_0.jpg" || string(f.Bytes) != "jpg" {
		t.Fatalf("got %+v, %v", f, err)
	}
	if _, err := a.loadSessionFile(context.Background(), "s", "", "/workspace/nope.png"); err == nil {
		t.Fatal("missing file loaded")
	}
}
