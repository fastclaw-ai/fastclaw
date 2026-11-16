package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/toolproviders"
)

// ImageFiles is how image_gen reaches the chat's workspace: the agent
// stores each picture there (the reply shows it as /workspace/<name>, and
// the API returns it with the reply), and reads the reference images a
// call names. Session and project are the calling turn's.
type ImageFiles struct {
	Save func(ctx context.Context, sessionID, projectID, name string, data []byte, contentType string) (string, error)
	Load func(ctx context.Context, sessionID, projectID, path string) (agl.ToolFile, error)
}

// Pictures a provider returns as URLs are fetched with these limits.
const (
	maxImageBytes   = 25 << 20
	maxRefImages    = 4
	imageFetchLimit = 60 * time.Second
)

// imageHTTP fetches provider image URLs; it refuses private addresses (a
// configured endpoint could answer with any URL).
var imageHTTP = &http.Client{
	Timeout:   imageFetchLimit,
	Transport: &http.Transport{DialContext: billing.GuardedDial},
}

// RegisterImageGenChain registers the image_gen tool against a provider
// chain. Only registered when at least one provider in the chain has
// credentials configured — so an agent without image-gen keys doesn't see
// a tool it can't use. files stores the pictures in the chat's workspace
// so the model gets a short path, never the image data; nil (no
// workspace) returns the provider's URLs.
func RegisterImageGenChain(r *Registry, chain *toolproviders.Chain, files *ImageFiles) {
	if chain == nil {
		return
	}
	// "none" is a sentinel meaning the admin explicitly opted out of
	// fastclaw's image_gen. Detected anywhere in the chain → don't
	// register the tool at all so the model falls back to its own
	// native image-generation capability (or does without).
	for _, ref := range chain.Order {
		name := ref
		if i := strings.IndexByte(ref, '/'); i >= 0 {
			name = ref[:i]
		}
		if name == "none" {
			return
		}
	}
	if !chain.Available() {
		return
	}
	desc := []string{
		"Generate pictures (photo, illustration, portrait, poster, sticker, logo, …) from a text description with an image model, ready to show in this chat. Use it for every request to draw, paint or make an image.",
		"Write \"prompt\" as a rich visual description: subject, style, composition, colors, mood, and any text that must appear in the picture.",
		"It returns putInReply: put that exactly in your reply so the picture is shown, plus a short line of your own. Don't describe the image data or repeat the paths otherwise.",
	}
	props := map[string]any{
		"prompt": map[string]any{"type": "string", "description": "What the picture shows, in detail."},
		"aspect": map[string]any{"type": "string", "enum": []string{"square", "portrait", "landscape"}, "description": "Shape of the picture; default square."},
		"n":      map[string]any{"type": "integer", "description": "How many variations (default 1, max 4). Each one is charged."},
	}
	if files != nil && files.Load != nil {
		desc = append(desc, "To edit a photo the person sent or base a picture on it (a new style, an avatar, a sticker of them), list it in \"images\" by its /workspace path.")
		props["images"] = filesSchema(fmt.Sprintf(`Reference images from this chat, by workspace path, e.g. ["/workspace/image_3jk7l_0.jpg"] (at most %d).`, maxRefImages))
	}
	r.Register("image_gen", strings.Join(desc, "\n"), map[string]any{
		"type":       "object",
		"properties": props,
		"required":   []string{"prompt"},
	}, func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args map[string]any
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		turn := r.forContext(ctx)
		if err := attachRefs(ctx, files, turn, args); err != nil {
			return "", err
		}
		resp, err := chain.Execute(ctx, args)
		if err != nil {
			return "", err
		}
		if files == nil || files.Save == nil || len(resp.Images) == 0 {
			if len(resp.Images) > 0 {
				return urlsOnly(resp.Images), nil
			}
			return resp.Text, nil
		}
		return storeImages(ctx, files, turn, resp.Images)
	})
}

// attachRefs turns the "images" workspace paths into data: URLs for the
// providers (args["image_urls"]).
func attachRefs(ctx context.Context, files *ImageFiles, turn *Registry, args map[string]any) error {
	raw, _ := args["images"].([]any)
	delete(args, "images")
	if len(raw) == 0 {
		return nil
	}
	if files == nil || files.Load == nil {
		return &agl.ToolError{Msg: "Reference images can't be used in this chat."}
	}
	if len(raw) > maxRefImages {
		return &agl.ToolError{Msg: fmt.Sprintf("At most %d reference images.", maxRefImages)}
	}
	urls := make([]string, 0, len(raw))
	for _, v := range raw {
		p, _ := v.(string)
		f, err := files.Load(ctx, turn.SessionID(), turn.ProjectID(), p)
		if err != nil {
			return &agl.ToolError{Msg: fmt.Sprintf("Could not read %s from the workspace: %v. Use the exact /workspace path of the picture.", p, err)}
		}
		ct := http.DetectContentType(f.Bytes)
		if !strings.HasPrefix(ct, "image/") {
			return &agl.ToolError{Msg: p + " is not a picture."}
		}
		urls = append(urls, "data:"+ct+";base64,"+base64.StdEncoding.EncodeToString(f.Bytes))
	}
	args["image_urls"] = urls
	return nil
}

// storeImages saves each picture in the chat's workspace and returns the
// paths for the reply.
func storeImages(ctx context.Context, files *ImageFiles, turn *Registry, images []toolproviders.Image) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	var paths []string
	var failed []string
	for i, img := range images {
		data, ct := img.Data, img.ContentType
		if len(data) == 0 && img.URL != "" {
			var err error
			data, ct, err = fetchImage(ctx, img.URL)
			if err != nil {
				failed = append(failed, err.Error())
				continue
			}
		}
		if len(data) == 0 {
			continue
		}
		if ct == "" || !strings.HasPrefix(ct, "image/") {
			ct = http.DetectContentType(data)
		}
		name := fmt.Sprintf("picture-%s-%d%s", stamp, i+1, imageExt(ct))
		saved, err := files.Save(ctx, turn.SessionID(), turn.ProjectID(), name, data, ct)
		if err != nil {
			failed = append(failed, err.Error())
			continue
		}
		paths = append(paths, "/workspace/"+saved)
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("image_gen: the pictures were generated but could not be stored: %s", strings.Join(failed, "; "))
	}
	var reply strings.Builder
	for _, p := range paths {
		fmt.Fprintf(&reply, "![](%s)\n", p)
	}
	blob, _ := json.Marshal(map[string]any{
		"images":     paths,
		"putInReply": strings.TrimSpace(reply.String()),
	})
	return string(blob), nil
}

func fetchImage(ctx context.Context, u string) ([]byte, string, error) {
	if err := billing.CheckScheme(u); err != nil {
		return nil, "", fmt.Errorf("image url: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, imageFetchLimit)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := imageHTTP.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("download image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download image: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("download image: %w", err)
	}
	if len(data) > maxImageBytes {
		return nil, "", fmt.Errorf("image larger than %d MB", maxImageBytes>>20)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

func imageExt(ct string) string {
	switch {
	case strings.HasPrefix(ct, "image/jpeg"):
		return ".jpg"
	case strings.HasPrefix(ct, "image/webp"):
		return ".webp"
	case strings.HasPrefix(ct, "image/gif"):
		return ".gif"
	}
	return ".png"
}

// urlsOnly is the reply without a workspace: the provider's URLs (never
// inline image data, which would flood the context).
func urlsOnly(images []toolproviders.Image) string {
	var b strings.Builder
	n := 0
	for _, img := range images {
		if img.URL == "" {
			continue
		}
		n++
		fmt.Fprintf(&b, "![image %d](%s)\n", n, img.URL)
	}
	if n == 0 {
		return "The pictures were generated but can't be shown in this chat (no workspace to store them)."
	}
	return "putInReply:\n" + b.String()
}
