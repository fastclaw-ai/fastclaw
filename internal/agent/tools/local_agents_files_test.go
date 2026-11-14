package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
)

func fakeLoad(got *[]string) LoadFileFunc {
	return func(_ context.Context, p string) (agl.ToolFile, error) {
		*got = append(*got, p)
		if strings.Contains(p, "missing") {
			return agl.ToolFile{}, errors.New("no such file")
		}
		return agl.ToolFile{Name: p[strings.LastIndex(p, "/")+1:], Bytes: []byte("img")}, nil
	}
}

// The photo the person sent goes along with the task.
func TestRunLocalAgentHandsOverFiles(t *testing.T) {
	f := &fakeImageMaker{runOut: "done"}
	var loaded []string
	tool := &localAgentsTool{svc: f, userID: "u_alice", load: fakeLoad(&loaded)}
	if _, err := tool.run(context.Background(), json.RawMessage(`{"agent":"codex","task":"make a sticker","files":["/workspace/image_956hz_0.jpg"]}`)); err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || len(f.runs) != 1 || len(f.runs[0].Files) != 1 || f.runs[0].Files[0].Name != "image_956hz_0.jpg" {
		t.Fatalf("loaded %v, runs %+v", loaded, f.runs)
	}

	_, err := tool.run(context.Background(), json.RawMessage(`{"agent":"codex","task":"x","files":["/workspace/missing.png"]}`))
	var te *agl.ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "/workspace/missing.png") || len(f.runs) != 1 {
		t.Fatalf("missing file: err %v, runs %d", err, len(f.runs))
	}
}

func TestRunLocalAgentFilesParamOnlyWithLoader(t *testing.T) {
	for _, load := range []LoadFileFunc{nil, fakeLoad(new([]string))} {
		r := NewRegistry(t.TempDir(), t.TempDir())
		RegisterLocalAgents(r, &fakeImageMaker{}, "u", agl.ToolChat{}, nil, load)
		blob, _ := json.Marshal(r.Definitions())
		if has := strings.Contains(string(blob), `"files"`); has != (load != nil) {
			t.Fatalf("files param with load=%v: %v", load != nil, has)
		}
	}
}

// A sticker of the person's photo: Codex gets the photo and is told to
// work from it.
func TestGenerateImageWithReferenceImages(t *testing.T) {
	f := &fakeImageMaker{runOut: "/Users/a/Downloads/x.png"}
	tool := newImageTool(f)
	var loaded []string
	tool.load = fakeLoad(&loaded)
	if _, err := tool.run(context.Background(), json.RawMessage(`{"prompt":"a photo-booth sticker of her","images":["/workspace/image_956hz_0.jpg"]}`)); err != nil {
		t.Fatal(err)
	}
	r := f.runs[0]
	if len(r.Files) != 1 || !strings.Contains(r.Task, "Reference: the image(s) listed at the end") {
		t.Fatalf("run: files %d, task:\n%s", len(r.Files), r.Task)
	}

	f2 := &fakeImageMaker{runOut: "/Users/a/Downloads/x.png"}
	if _, err := newImageTool(f2).run(context.Background(), json.RawMessage(`{"prompt":"a cat"}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f2.runs[0].Task, "Reference:") || len(f2.runs[0].Files) != 0 {
		t.Fatalf("no refs expected: %s", f2.runs[0].Task)
	}
}
