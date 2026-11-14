package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type fakeImageMaker struct {
	machines []agl.LocalMachine
	runOut   string
	runs     []agl.ToolRun
	fetched  []agl.FetchRun
	fetchErr error
}

func (f *fakeImageMaker) Machines(context.Context, string) ([]agl.LocalMachine, error) {
	if f.machines == nil {
		return []agl.LocalMachine{miniWithCodex(true, "0.3.6", agl.CapImageGeneration)}, nil
	}
	return f.machines, nil
}

func miniWithCodex(online bool, aglVersion string, caps ...string) agl.LocalMachine {
	return agl.LocalMachine{ID: "mch_mini", Name: "mini", Online: online, AglVersion: aglVersion,
		Agents: []store.MachineAgent{{ID: "claude", Capabilities: []string{agl.CapWebSearch}}, {ID: "codex", Capabilities: caps}}}
}

func (f *fakeImageMaker) Run(_ context.Context, r agl.ToolRun) (string, error) {
	f.runs = append(f.runs, r)
	return f.runOut, nil
}

func (f *fakeImageMaker) Fetch(_ context.Context, r agl.FetchRun) (agl.FetchedFile, error) {
	f.fetched = append(f.fetched, r)
	if f.fetchErr != nil {
		return agl.FetchedFile{}, f.fetchErr
	}
	return agl.FetchedFile{Name: "img.png", ContentType: "image/png", Bytes: []byte("png")}, nil
}

func newImageTool(f *fakeImageMaker) *generateImageTool {
	return &generateImageTool{
		svc: f, userID: "u_alice", chat: agl.ToolChat{Channel: "wechat", ChatID: "c1"},
		save: func(_ context.Context, ff agl.FetchedFile) (string, error) { return "saved-" + ff.Name, nil },
		now:  func() time.Time { return time.Date(2026, 11, 6, 15, 4, 5, 0, time.UTC) },
	}
}

func TestGenerateImageBriefsCodexAndReturnsThePicture(t *testing.T) {
	f := &fakeImageMaker{runOut: "/Users/a/Downloads/20261106-150405-image.png"}
	out, err := newImageTool(f).run(context.Background(), json.RawMessage(`{"prompt":"a woman with long hair, warm light","aspect":"portrait","machine":"mini"}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.runs) != 1 {
		t.Fatalf("runs: %+v", f.runs)
	}
	r := f.runs[0]
	if r.Agent != "codex" || r.Machine != "mch_mini" || r.UserID != "u_alice" {
		t.Fatalf("run: %+v", r)
	}
	for _, want := range []string{"image generation tool", "Do NOT draw it with code", "a woman with long hair, warm light", "portrait", "~/Downloads/20261106-150405-image.png", imageUnavailable} {
		if !strings.Contains(r.Task, want) {
			t.Errorf("task lacks %q:\n%s", want, r.Task)
		}
	}
	if len(f.fetched) != 1 || f.fetched[0].Path != "~/Downloads/20261106-150405-image.png" || f.fetched[0].Machine != "mch_mini" {
		t.Fatalf("fetched: %+v", f.fetched)
	}
	if !strings.Contains(out, `"putInReply":"![](/workspace/saved-img.png)"`) {
		t.Fatalf("out: %s", out)
	}
}

func TestGenerateImageUnavailableIsAToolErrorWithoutFetching(t *testing.T) {
	f := &fakeImageMaker{runOut: "IMAGEGEN_UNAVAILABLE: image_generation feature is disabled"}
	_, err := newImageTool(f).run(context.Background(), json.RawMessage(`{"prompt":"a cat"}`))
	var te *agl.ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "feature is disabled") {
		t.Fatalf("err: %v", err)
	}
	if len(f.fetched) != 0 {
		t.Fatalf("fetched after unavailable: %+v", f.fetched)
	}
}

func TestGenerateImageMissingFileExplains(t *testing.T) {
	f := &fakeImageMaker{runOut: "I drew it instead", fetchErr: &agl.ToolError{Msg: "no such file"}}
	_, err := newImageTool(f).run(context.Background(), json.RawMessage(`{"prompt":"a cat"}`))
	var te *agl.ToolError
	if !errors.As(err, &te) || !strings.Contains(te.Msg, "isn't at") || !strings.Contains(te.Msg, "I drew it instead") {
		t.Fatalf("err: %v", err)
	}
}

func TestGenerateImageNeedsPrompt(t *testing.T) {
	f := &fakeImageMaker{}
	if _, err := newImageTool(f).run(context.Background(), json.RawMessage(`{"prompt":"  "}`)); err == nil || len(f.runs) != 0 {
		t.Fatalf("empty prompt: %v %+v", err, f.runs)
	}
}

func TestGenerateImageFollowsCapabilities(t *testing.T) {
	cases := []struct {
		name     string
		machines []agl.LocalMachine
		ok       bool
		errHas   string
	}{
		{"reported", []agl.LocalMachine{miniWithCodex(true, "0.3.6", agl.CapImageGeneration)}, true, ""},
		{"turned off", []agl.LocalMachine{miniWithCodex(true, "0.3.6")}, false, "can generate images"},
		{"older agl assumes codex can", []agl.LocalMachine{miniWithCodex(true, "0.3.5")}, true, ""},
		{"offline", []agl.LocalMachine{miniWithCodex(false, "0.3.6", agl.CapImageGeneration)}, false, "offline"},
		{"online one first", []agl.LocalMachine{
			{ID: "mch_off", Name: "off", AglVersion: "0.3.6", Agents: []store.MachineAgent{{ID: "codex", Capabilities: []string{agl.CapImageGeneration}}}},
			miniWithCodex(true, "0.3.6", agl.CapImageGeneration),
		}, true, ""},
	}
	for _, c := range cases {
		f := &fakeImageMaker{machines: c.machines, runOut: "done"}
		_, err := newImageTool(f).run(context.Background(), json.RawMessage(`{"prompt":"a cat"}`))
		if c.ok {
			if err != nil || len(f.runs) != 1 || f.runs[0].Machine != "mch_mini" {
				t.Errorf("%s: err %v runs %+v", c.name, err, f.runs)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.errHas) || len(f.runs) != 0 {
			t.Errorf("%s: err %v runs %+v", c.name, err, f.runs)
		}
	}
	if CanGenerateImage([]agl.LocalMachine{miniWithCodex(true, "0.3.6")}) {
		t.Error("mounted without an image-capable agent")
	}
	if !CanGenerateImage([]agl.LocalMachine{miniWithCodex(false, "0.3.6", agl.CapImageGeneration)}) {
		t.Error("not mounted for an offline machine that can (the tool explains it is offline)")
	}
}

func TestGenerateImageDefersToImageGen(t *testing.T) {
	desc := func(withImageGen bool) string {
		r := NewRegistry(t.TempDir(), t.TempDir())
		if withImageGen {
			r.Register("image_gen", "cloud images", map[string]any{"type": "object"}, func(context.Context, json.RawMessage) (string, error) { return "", nil })
		}
		RegisterGenerateImage(r, &fakeImageMaker{}, nil, nil, "u", agl.ToolChat{}, nil)
		for _, d := range r.Definitions() {
			if d.Function.Name == GenerateImageToolName {
				return d.Function.Description
			}
		}
		t.Fatal("generate_image not registered")
		return ""
	}
	if alone := desc(false); !strings.Contains(alone, "EVERY request") {
		t.Errorf("without image_gen: %s", alone)
	}
	if both := desc(true); !strings.Contains(both, "image_gen is faster") || strings.Contains(both, "EVERY request") {
		t.Errorf("with image_gen: %s", both)
	}
}
