package imagegen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// Replicate posts to https://api.replicate.com/v1/models/<owner>/<name>/predictions
// with `Prefer: wait` so we get a synchronous response (up to 60s) instead of
// having to poll the prediction URL. Auth is `Authorization: Bearer <token>`
// (Replicate also accepts the legacy `Token <token>` form; we use Bearer to
// match the rest of the catalog).
type Replicate struct{}

func (Replicate) Category() string { return Category }
func (Replicate) Name() string     { return "replicate" }

// replicateModelRoutes maps the short model key (everything after "replicate/")
// to a "<owner>/<name>" path on Replicate. Callers can also pass a raw
// owner/name pair (e.g. "replicate/black-forest-labs/flux-schnell") and we'll
// route through unchanged.
var replicateModelRoutes = map[string]string{
	"flux-schnell": "black-forest-labs/flux-schnell",
	"flux-dev":     "black-forest-labs/flux-dev",
	"flux-pro":     "black-forest-labs/flux-1.1-pro",
	"sdxl":         "stability-ai/sdxl",
	"ideogram":     "ideogram-ai/ideogram-v2",
}

func (r *Replicate) Execute(ctx context.Context, req toolproviders.Request) (toolproviders.Response, error) {
	a, err := parseArgs(req.Args)
	if err != nil {
		return toolproviders.Response{}, err
	}
	if req.Config.APIKey == "" {
		return toolproviders.Response{}, fmt.Errorf("replicate: missing api key")
	}
	modelKey := req.Config.Model
	if modelKey == "" {
		modelKey = "flux-schnell"
	}
	path, ok := replicateModelRoutes[modelKey]
	if !ok {
		path = modelKey
	}

	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	input := map[string]any{
		"prompt":      a.Prompt,
		"num_outputs": a.N,
	}
	if len(a.Refs) > 0 {
		return toolproviders.Response{}, errNoRefs("replicate")
	}
	switch {
	case a.Size != "":
		// Replicate flux models use aspect_ratio (e.g. "1:1"); width/height
		// were removed from the schema.
		input["aspect_ratio"] = a.Size
	case a.Aspect == "portrait":
		input["aspect_ratio"] = "3:4"
	case a.Aspect == "landscape":
		input["aspect_ratio"] = "4:3"
	case a.Aspect != "":
		input["aspect_ratio"] = "1:1"
	}
	buf, _ := json.Marshal(map[string]any{"input": input})

	url := "https://api.replicate.com/v1/models/" + path + "/predictions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(buf))
	if err != nil {
		return toolproviders.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+req.Config.APIKey)
	// Prefer: wait makes Replicate hold the connection open until the
	// prediction finishes (or 60s elapses), so we don't have to implement
	// a polling loop here. Beyond 60s the response returns status="processing"
	// and we surface that as ErrNoResults so the chain can fall back.
	httpReq.Header.Set("Prefer", "wait")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return toolproviders.Response{}, toolproviders.Retry(fmt.Errorf("replicate request: %w", err))
	}
	defer resp.Body.Close()
	// Replicate returns 201 Created on accepted predictions even when
	// Prefer:wait waited for completion, so treat both 200 and 201 as OK.
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return toolproviders.Response{}, retriableHTTP("replicate", resp)
	}
	var out struct {
		Status string          `json:"status"` // "succeeded" / "failed" / "processing" / ...
		Error  string          `json:"error,omitempty"`
		Output json.RawMessage `json:"output"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return toolproviders.Response{}, fmt.Errorf("replicate decode: %w", err)
	}
	if out.Status == "failed" {
		return toolproviders.Response{}, fmt.Errorf("replicate failed: %s", out.Error)
	}
	if out.Status != "succeeded" {
		// "starting" / "processing" / "canceled" — treat as retriable so the
		// next provider in the chain gets a shot.
		return toolproviders.Response{}, toolproviders.Retry(fmt.Errorf("replicate status %q", out.Status))
	}
	urls := decodeReplicateOutput(out.Output)
	if len(urls) == 0 {
		return toolproviders.Response{}, toolproviders.ErrNoResults
	}
	return toolproviders.Response{Text: renderURLs(a.Prompt, urls), Images: urlImages(urls)}, nil
}

// decodeReplicateOutput accepts both the array shape (flux: ["url1", "url2"])
// and the single-string shape (some older models return one URL) and returns
// a flat slice.
func decodeReplicateOutput(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		return arr
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && single != "" {
		return []string{single}
	}
	return nil
}
