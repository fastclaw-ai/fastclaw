package imagegen

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// OpenAI generates via POST /v1/images/generations. Model (the suffix in
// "openai/<model>") defaults to "gpt-image-1" which returns base64; older
// dall-e-3 returns a URL. Both paths are handled transparently.
type OpenAI struct{}

func (OpenAI) Category() string { return Category }
func (OpenAI) Name() string     { return "openai" }

func (o *OpenAI) Execute(ctx context.Context, req toolproviders.Request) (toolproviders.Response, error) {
	a, err := parseArgs(req.Args)
	if err != nil {
		return toolproviders.Response{}, err
	}
	if req.Config.APIKey == "" {
		return toolproviders.Response{}, fmt.Errorf("openai: missing api key")
	}
	model := req.Config.Model
	if model == "" {
		model = "gpt-image-1"
	}
	size := a.Size
	if size == "" {
		size = openAISize(model, a.Aspect)
	}

	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()

	endpoint := "https://api.openai.com/v1/images/generations"
	if req.Config.Endpoint != "" {
		endpoint = req.Config.Endpoint
	}
	var httpReq *http.Request
	if len(a.Refs) > 0 {
		// Reference images go to the edits endpoint as a multipart form.
		if !strings.HasPrefix(model, "gpt-image") {
			return toolproviders.Response{}, errNoRefs("openai")
		}
		body, contentType, err := openAIEditForm(model, a, size)
		if err != nil {
			return toolproviders.Response{}, err
		}
		editURL := strings.Replace(endpoint, "/images/generations", "/images/edits", 1)
		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, editURL, body)
		if err != nil {
			return toolproviders.Response{}, err
		}
		httpReq.Header.Set("Content-Type", contentType)
	} else {
		buf, _ := json.Marshal(map[string]any{
			"model":  model,
			"prompt": a.Prompt,
			"n":      a.N,
			"size":   size,
		})
		var err error
		httpReq, err = http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(buf))
		if err != nil {
			return toolproviders.Response{}, err
		}
		httpReq.Header.Set("Content-Type", "application/json")
	}
	httpReq.Header.Set("Authorization", "Bearer "+req.Config.APIKey)

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return toolproviders.Response{}, toolproviders.Retry(fmt.Errorf("openai image request: %w", err))
	}
	defer resp.Body.Close()
	if err := retriableHTTP("openai", resp); err != nil {
		return toolproviders.Response{}, err
	}
	var out struct {
		Data []struct {
			URL     string `json:"url"`
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 200<<20)).Decode(&out); err != nil {
		return toolproviders.Response{}, fmt.Errorf("openai decode: %w", err)
	}
	images := make([]toolproviders.Image, 0, len(out.Data))
	urls := make([]string, 0, len(out.Data))
	b64s := make([]string, 0, len(out.Data))
	for _, d := range out.Data {
		switch {
		case d.URL != "":
			urls = append(urls, d.URL)
			images = append(images, toolproviders.Image{URL: d.URL})
		case d.B64JSON != "":
			data, err := base64.StdEncoding.DecodeString(d.B64JSON)
			if err != nil {
				continue
			}
			b64s = append(b64s, d.B64JSON)
			images = append(images, toolproviders.Image{Data: data, ContentType: "image/png"})
		}
	}
	if len(images) == 0 {
		return toolproviders.Response{}, toolproviders.ErrNoResults
	}
	text := renderURLs(a.Prompt, urls)
	if text == "" {
		text = renderB64(a.Prompt, b64s)
	}
	return toolproviders.Response{Text: text, Images: images}, nil
}

// openAISize maps the tool's shape to a size the model accepts.
func openAISize(model, aspect string) string {
	if strings.HasPrefix(model, "dall-e-3") {
		switch aspect {
		case "portrait":
			return "1024x1792"
		case "landscape":
			return "1792x1024"
		}
		return "1024x1024"
	}
	switch aspect {
	case "portrait":
		return "1024x1536"
	case "landscape":
		return "1536x1024"
	}
	return "1024x1024"
}

// openAIEditForm builds the /images/edits multipart body: the prompt and
// each reference image (data: URLs) as an image[] file.
func openAIEditForm(model string, a args, size string) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("model", model)
	_ = w.WriteField("prompt", a.Prompt)
	_ = w.WriteField("n", strconv.Itoa(a.N))
	_ = w.WriteField("size", size)
	for i, ref := range a.Refs {
		data, ct, err := decodeDataURL(ref)
		if err != nil {
			return nil, "", fmt.Errorf("openai: reference image %d: %w", i+1, err)
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="image[]"; filename="ref%d%s"`, i+1, extFor(ct)))
		h.Set("Content-Type", ct)
		part, err := w.CreatePart(h)
		if err != nil {
			return nil, "", err
		}
		if _, err := part.Write(data); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// decodeDataURL reads a base64 data: URL.
func decodeDataURL(u string) ([]byte, string, error) {
	rest, ok := strings.CutPrefix(u, "data:")
	if !ok {
		return nil, "", fmt.Errorf("not a data URL")
	}
	meta, payload, ok := strings.Cut(rest, ",")
	if !ok || !strings.HasSuffix(meta, ";base64") {
		return nil, "", fmt.Errorf("not a base64 data URL")
	}
	data, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, "", err
	}
	ct := strings.TrimSuffix(meta, ";base64")
	if ct == "" {
		ct = "application/octet-stream"
	}
	return data, ct, nil
}

func extFor(ct string) string {
	switch ct {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/gif":
		return ".gif"
	}
	return ".png"
}

func retriableHTTP(name string, resp *http.Response) error {
	if resp.StatusCode == http.StatusOK {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	err := fmt.Errorf("%s HTTP %d: %s", name, resp.StatusCode, string(body))
	switch {
	case resp.StatusCode == http.StatusTooManyRequests,
		resp.StatusCode == http.StatusRequestTimeout,
		resp.StatusCode >= 500:
		return toolproviders.Retry(err)
	default:
		return err
	}
}
