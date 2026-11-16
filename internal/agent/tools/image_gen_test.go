package tools

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

var testPNG = append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 32)...)

type fakeImageProvider struct {
	resp toolproviders.Response
	args map[string]any
}

func (f *fakeImageProvider) Category() string     { return "image_gen" }
func (f *fakeImageProvider) Name() string         { return "fal" }
func (f *fakeImageProvider) CredentialFree() bool { return true }
func (f *fakeImageProvider) Execute(_ context.Context, req toolproviders.Request) (toolproviders.Response, error) {
	f.args = req.Args
	return f.resp, nil
}

type savedImage struct {
	session, project, name, contentType string
	data                                []byte
}

func imageGenRegistry(t *testing.T, p *fakeImageProvider, files *ImageFiles) *Registry {
	t.Helper()
	reg := toolproviders.NewRegistry()
	reg.Register(p)
	r := NewRegistry(t.TempDir(), t.TempDir())
	r.SetSessionID("imc:acc_1:chat_9")
	r.SetProjectID("")
	RegisterImageGenChain(r, &toolproviders.Chain{
		Category: "image_gen", Order: []string{"fal/seedream"}, Registry: reg,
		GetConfig: func(string) toolproviders.ProviderConfig { return toolproviders.ProviderConfig{} },
	}, files)
	return r
}

func TestImageGenStoresEachPictureInTheTurnsWorkspace(t *testing.T) {
	t.Setenv(billing.EnvAllowPrivate, "true")
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write([]byte("\xff\xd8\xff jpeg"))
	}))
	defer cdn.Close()
	p := &fakeImageProvider{resp: toolproviders.Response{Images: []toolproviders.Image{
		{Data: testPNG, ContentType: "image/png"}, {URL: cdn.URL + "/a.jpg"},
	}}}
	var saved []savedImage
	files := &ImageFiles{
		Save: func(_ context.Context, session, project, name string, data []byte, ct string) (string, error) {
			saved = append(saved, savedImage{session, project, name, ct, data})
			return name, nil
		},
		Load: func(_ context.Context, session, project, path string) (agl.ToolFile, error) {
			if path == "/workspace/me.png" && session == "imc:acc_1:chat_9" {
				return agl.ToolFile{Name: "me.png", Bytes: testPNG}, nil
			}
			if path == "/workspace/notes.txt" {
				return agl.ToolFile{Name: "notes.txt", Bytes: []byte("hello")}, nil
			}
			return agl.ToolFile{}, errors.New("no such file")
		},
	}
	r := imageGenRegistry(t, p, files)

	out, err := r.Execute(context.Background(), "image_gen", `{"prompt":"me as a sticker","images":["/workspace/me.png"]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(saved) != 2 {
		t.Fatalf("saved %d pictures, want 2", len(saved))
	}
	for _, s := range saved {
		if s.session != "imc:acc_1:chat_9" || !strings.HasPrefix(s.name, "picture-") {
			t.Fatalf("saved %+v", s)
		}
	}
	if !strings.HasSuffix(saved[0].name, ".png") || string(saved[0].data) != string(testPNG) {
		t.Fatalf("first picture %+v", saved[0])
	}
	if !strings.HasSuffix(saved[1].name, ".jpg") || saved[1].contentType != "image/jpeg" {
		t.Fatalf("downloaded picture %+v", saved[1])
	}
	var res struct {
		Images     []string `json:"images"`
		PutInReply string   `json:"putInReply"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil || len(res.Images) != 2 {
		t.Fatalf("result %q", out)
	}
	if strings.Contains(out, "base64") || strings.Contains(out, cdn.URL) {
		t.Fatalf("image data reached the model: %q", out)
	}
	// The reference reached the provider as a data URL; "images" didn't.
	refs, _ := p.args["image_urls"].([]string)
	if len(refs) != 1 || !strings.HasPrefix(refs[0], "data:image/png;base64,") {
		t.Fatalf("provider refs = %v", p.args["image_urls"])
	}
	if _, ok := p.args["images"]; ok {
		t.Fatal("workspace paths were passed to the provider")
	}

	// A reference that isn't a picture, or isn't there, is refused.
	for _, bad := range []string{"/workspace/notes.txt", "/workspace/missing.png"} {
		if _, err := r.Execute(context.Background(), "image_gen", `{"prompt":"x","images":["`+bad+`"]}`); err == nil {
			t.Fatalf("%s accepted as a reference", bad)
		}
	}
}

func TestImageGenWithoutAWorkspaceNeverReturnsImageData(t *testing.T) {
	p := &fakeImageProvider{resp: toolproviders.Response{
		Text:   "1. ![image 1](data:image/png;base64,AAAA)",
		Images: []toolproviders.Image{{Data: testPNG}, {URL: "https://cdn.example/x.png"}},
	}}
	r := imageGenRegistry(t, p, nil)
	out, err := r.Execute(context.Background(), "image_gen", `{"prompt":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "base64") || !strings.Contains(out, "https://cdn.example/x.png") {
		t.Fatalf("out = %q", out)
	}
}
