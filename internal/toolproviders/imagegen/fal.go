package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// Fal posts to https://fal.run/<model-path>. The "<model>" part of the
// reference becomes the tail of the URL (e.g. "fal/flux-dev" →
// https://fal.run/fal-ai/flux/dev). Auth is "Key <token>".
type Fal struct{}

func (Fal) Category() string { return Category }
func (Fal) Name() string     { return "fal" }

var falModelRoutes = map[string]string{
	"flux-dev":     "fal-ai/flux/dev",
	"flux-schnell": "fal-ai/flux/schnell",
	"flux-pro":     "fal-ai/flux-pro",
}

func (f *Fal) Execute(ctx context.Context, req toolproviders.Request) (toolproviders.Response, error) {
	a, err := parseArgs(req.Args)
	if err != nil {
		return toolproviders.Response{}, err
	}
	if req.Config.APIKey == "" {
		return toolproviders.Response{}, fmt.Errorf("fal: missing api key")
	}
	modelKey := req.Config.Model
	if modelKey == "" {
		modelKey = "flux-dev"
	}
	path, ok := falModelRoutes[modelKey]
	if !ok {
		// Let callers pass a raw model path too (e.g. "fal/fal-ai/flux/dev").
		path = modelKey
	}
	if len(a.Refs) > 0 {
		// Editing needs an image-to-image model: the "edit_model" option
		// (e.g. fal-ai/bytedance/seedream/v4/edit), or the model itself
		// when it already is one.
		switch {
		case req.Config.Options["edit_model"] != "":
			path = req.Config.Options["edit_model"]
		case !strings.Contains(path, "edit"):
			return toolproviders.Response{}, errNoRefs("fal")
		}
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	body := map[string]any{
		"prompt":     a.Prompt,
		"num_images": a.N,
	}
	switch {
	case a.Size != "":
		body["image_size"] = a.Size
	case a.Aspect != "":
		body["image_size"] = falImageSize(a.Aspect)
	}
	if len(a.Refs) > 0 {
		body["image_urls"] = a.Refs
	}
	buf, _ := json.Marshal(body)
	url := "https://fal.run/" + path
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return toolproviders.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Key "+req.Config.APIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return toolproviders.Response{}, toolproviders.Retry(fmt.Errorf("fal request: %w", err))
	}
	defer resp.Body.Close()
	if err := retriableHTTP("fal", resp); err != nil {
		return toolproviders.Response{}, err
	}
	var out struct {
		Images []struct {
			URL string `json:"url"`
		} `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return toolproviders.Response{}, fmt.Errorf("fal decode: %w", err)
	}
	urls := make([]string, 0, len(out.Images))
	for _, img := range out.Images {
		if img.URL != "" {
			urls = append(urls, img.URL)
		}
	}
	if len(urls) == 0 {
		return toolproviders.Response{}, toolproviders.ErrNoResults
	}
	return toolproviders.Response{Text: renderURLs(a.Prompt, urls), Images: urlImages(urls)}, nil
}

// falImageSize maps the tool's shape to fal's image_size presets.
func falImageSize(aspect string) string {
	switch aspect {
	case "portrait":
		return "portrait_4_3"
	case "landscape":
		return "landscape_4_3"
	default:
		return "square_hd"
	}
}
