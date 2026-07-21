package channels

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
	_ "golang.org/x/image/webp"
)

const (
	weComMaxInboundMediaBytes = 25 * 1024 * 1024
	weComMaxMediaRedirects    = 3
	weComMaxMarkdownBytes     = 20_480
	weComMaxInlineImageBytes  = 10 * 1024 * 1024
	weComMaxInlineImages      = 10
	weComUploadChunkBytes     = 512 * 1024
	weComMaxUploadChunks      = 100
)

func decryptWeComMedia(ciphertext []byte, encodedKey string) ([]byte, error) {
	key, err := base64.StdEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("wecom media decryption key is invalid")
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		return nil, errors.New("wecom encrypted media has invalid length")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, errors.New("wecom media decryption initialization failed")
	}
	plaintext := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(block, key[:aes.BlockSize]).CryptBlocks(plaintext, ciphertext)
	padLen := int(plaintext[len(plaintext)-1])
	if padLen < 1 || padLen > 32 || padLen > len(plaintext) {
		return nil, errors.New("wecom media has invalid PKCS#7 padding")
	}
	for _, value := range plaintext[len(plaintext)-padLen:] {
		if int(value) != padLen {
			return nil, errors.New("wecom media has invalid PKCS#7 padding")
		}
	}
	return plaintext[:len(plaintext)-padLen], nil
}

func downloadWeComMedia(
	ctx context.Context,
	client *http.Client,
	rawURL, aesKey, fallbackKind string,
	maxBytes int64,
) (bus.MediaItem, error) {
	if err := validateWeComMediaURL(rawURL); err != nil {
		return bus.MediaItem{}, err
	}
	if maxBytes <= 0 {
		return bus.MediaItem{}, errors.New("wecom media size limit is invalid")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	boundedClient := *client
	originalRedirect := client.CheckRedirect
	boundedClient.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= weComMaxMediaRedirects {
			return errors.New("wecom media redirect limit exceeded")
		}
		if err := validateWeComMediaURL(req.URL.String()); err != nil {
			return err
		}
		if originalRedirect != nil {
			return originalRedirect(req, via)
		}
		return nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return bus.MediaItem{}, errors.New("create wecom media download request failed")
	}
	resp, err := boundedClient.Do(req)
	if err != nil {
		return bus.MediaItem{}, errors.New("wecom media download failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return bus.MediaItem{}, fmt.Errorf("wecom media download returned HTTP status %d", resp.StatusCode)
	}

	readLimit := maxBytes
	if aesKey != "" {
		readLimit += 32
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, readLimit+1))
	if err != nil {
		return bus.MediaItem{}, errors.New("read wecom media response failed")
	}
	if int64(len(payload)) > readLimit {
		return bus.MediaItem{}, errors.New("wecom media exceeds the size limit")
	}
	if aesKey != "" {
		payload, err = decryptWeComMedia(payload, aesKey)
		if err != nil {
			return bus.MediaItem{}, err
		}
	}
	if int64(len(payload)) > maxBytes {
		return bus.MediaItem{}, errors.New("wecom media exceeds the size limit")
	}

	contentType := strings.TrimSpace(resp.Header.Get("Content-Type"))
	if parsed, _, parseErr := mime.ParseMediaType(contentType); parseErr == nil {
		contentType = parsed
	} else {
		contentType = ""
	}
	if contentType == "" || contentType == "application/octet-stream" {
		contentType = http.DetectContentType(payload)
	}
	filename := weComMediaFilename(resp.Header.Get("Content-Disposition"), fallbackKind, contentType)
	return bus.MediaItem{Filename: filename, ContentType: contentType, Bytes: payload}, nil
}

func validateWeComMediaURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return errors.New("wecom media URL must be HTTPS without user information")
	}
	return nil
}

func weComMediaFilename(contentDisposition, fallbackKind, contentType string) string {
	filename := ""
	if _, params, err := mime.ParseMediaType(contentDisposition); err == nil {
		filename = params["filename"]
	}
	filename = filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	filename = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, filename)
	if filename == "" || filename == "." {
		base := "attachment"
		if fallbackKind == "image" {
			base = "image"
		} else if fallbackKind == "file" {
			base = "file"
		}
		extension := ""
		if extensions, err := mime.ExtensionsByType(contentType); err == nil && len(extensions) > 0 {
			extension = extensions[0]
		}
		filename = base + extension
	}
	return filename
}

func (w *WeCom) mapInboundContent(ctx context.Context, callback weComInboundMessage) (string, []bus.MediaItem, error) {
	switch callback.MsgType {
	case "text":
		if strings.TrimSpace(callback.Text.Content) == "" {
			return "", nil, errors.New("wecom text callback has empty content")
		}
		return callback.Text.Content, nil, nil
	case "image":
		item, err := downloadWeComMedia(ctx, w.http, callback.Image.URL, callback.Image.AESKey, "image", weComMaxInboundMediaBytes)
		if err != nil {
			return "", nil, fmt.Errorf("wecom image processing failed: %w", err)
		}
		return "", []bus.MediaItem{item}, nil
	case "file":
		item, err := downloadWeComMedia(ctx, w.http, callback.File.URL, callback.File.AESKey, "file", weComMaxInboundMediaBytes)
		if err != nil {
			return "", nil, fmt.Errorf("wecom file processing failed: %w", err)
		}
		return "", []bus.MediaItem{item}, nil
	case "mixed":
		var textParts []string
		var mediaItems []bus.MediaItem
		for _, entry := range callback.Mixed.Items {
			switch entry.MsgType {
			case "text":
				if entry.Text.Content != "" {
					textParts = append(textParts, entry.Text.Content)
				}
			case "image":
				item, err := downloadWeComMedia(ctx, w.http, entry.Image.URL, entry.Image.AESKey, "image", weComMaxInboundMediaBytes)
				if err != nil {
					return "", nil, fmt.Errorf("wecom mixed image processing failed: %w", err)
				}
				mediaItems = append(mediaItems, item)
			default:
				return "", nil, fmt.Errorf("unsupported wecom mixed message item %q", entry.MsgType)
			}
		}
		return strings.Join(textParts, "\n"), mediaItems, nil
	case "voice", "video":
		return "", nil, fmt.Errorf("unsupported wecom %s message", callback.MsgType)
	default:
		return "", nil, fmt.Errorf("unsupported wecom message type %q", callback.MsgType)
	}
}

func (w *WeCom) respondCallbackError(ctx context.Context, reqID, messageID string, cause error) error {
	content := "I couldn't process that message. Please send text, a supported image, or a file."
	if strings.Contains(cause.Error(), "unsupported wecom voice") || strings.Contains(cause.Error(), "unsupported wecom video") {
		content = "Voice and video messages aren't supported yet. Please send text, an image, or a file."
	}
	body := map[string]any{
		"msgtype": "stream",
		"stream": map[string]any{
			"id":      "error-" + messageID,
			"finish":  true,
			"content": content,
		},
	}
	requestCtx, cancel := context.WithTimeout(ctx, w.requestTimeout)
	defer cancel()
	_, err := w.request(requestCtx, reqID, weComCmdRespond, body)
	return err
}

type weComDeferredMedia struct {
	kind string
	item bus.MediaItem
}

func (w *WeCom) sendPassiveStream(ctx context.Context, msg bus.OutboundMessage) error {
	if msg.StreamID == "" {
		return errors.New("wecom stream message requires stream ID")
	}
	ref, ok := w.lookupReplyRef(msg.ReplyToMsgID)
	if !ok {
		return errors.New("wecom passive reply reference is missing or expired")
	}
	if msg.ChatID != ref.ChatID {
		w.deleteReplyRef(msg.ReplyToMsgID)
		return errors.New("wecom passive reply chat does not match the original conversation")
	}
	finish := msg.StreamState == bus.StreamFinish
	if msg.StreamState != bus.StreamStart && msg.StreamState != bus.StreamUpdate && !finish {
		w.deleteReplyRef(msg.ReplyToMsgID)
		return fmt.Errorf("unsupported wecom stream state %q", msg.StreamState)
	}
	if !finish && len(msg.MediaItems) > 0 {
		w.deleteReplyRef(msg.ReplyToMsgID)
		return errors.New("wecom media can only be attached to a finished stream")
	}
	if finish {
		defer w.deleteReplyRef(msg.ReplyToMsgID)
	}

	chunks := splitWeComMarkdown(msg.Text, weComMaxMarkdownBytes)
	if len(chunks) == 0 {
		chunks = []string{""}
	}
	var inlineImages []any
	var deferred []weComDeferredMedia
	if finish {
		var err error
		inlineImages, deferred, err = prepareWeComStreamMedia(msg.MediaItems)
		if err != nil {
			return err
		}
	}
	stream := map[string]any{
		"id":      msg.StreamID,
		"finish":  finish,
		"content": chunks[0],
	}
	if finish && len(inlineImages) > 0 {
		stream["msg_item"] = inlineImages
	}
	body := map[string]any{"msgtype": "stream", "stream": stream}
	if _, err := w.request(ctx, ref.ReqID, weComCmdRespond, body); err != nil {
		w.deleteReplyRef(msg.ReplyToMsgID)
		return err
	}
	if !finish {
		return nil
	}
	for _, content := range chunks[1:] {
		if err := w.sendProactiveMarkdown(ctx, ref.ChatID, content); err != nil {
			return err
		}
	}
	for _, media := range deferred {
		if err := w.sendUploadedMedia(ctx, ref.ChatID, media.kind, media.item); err != nil {
			return err
		}
	}
	return nil
}

func prepareWeComStreamMedia(items []bus.MediaItem) ([]any, []weComDeferredMedia, error) {
	inline := make([]any, 0, min(len(items), weComMaxInlineImages))
	deferred := make([]weComDeferredMedia, 0)
	for _, item := range items {
		if isWeComImage(item) {
			normalized, err := normalizeWeComImage(item)
			if err != nil {
				return nil, nil, err
			}
			if len(inline) < weComMaxInlineImages && len(normalized.Bytes) <= weComMaxInlineImageBytes {
				sum := md5.Sum(normalized.Bytes)
				inline = append(inline, map[string]any{
					"msgtype": "image",
					"image": map[string]string{
						"base64": base64.StdEncoding.EncodeToString(normalized.Bytes),
						"md5":    fmt.Sprintf("%x", sum),
					},
				})
				continue
			}
			deferred = append(deferred, weComDeferredMedia{kind: "image", item: normalized})
			continue
		}
		deferred = append(deferred, weComDeferredMedia{kind: "file", item: item})
	}
	return inline, deferred, nil
}

func (w *WeCom) sendProactiveMessage(ctx context.Context, msg bus.OutboundMessage) error {
	if msg.Text != "" {
		for _, content := range splitWeComMarkdown(msg.Text, weComMaxMarkdownBytes) {
			if err := w.sendProactiveMarkdown(ctx, msg.ChatID, content); err != nil {
				return err
			}
		}
	}
	for _, item := range msg.MediaItems {
		kind := "file"
		if isWeComImage(item) {
			var err error
			item, err = normalizeWeComImage(item)
			if err != nil {
				return err
			}
			kind = "image"
		}
		if err := w.sendUploadedMedia(ctx, msg.ChatID, kind, item); err != nil {
			return err
		}
	}
	return nil
}

func (w *WeCom) sendProactiveMarkdown(ctx context.Context, chatID, content string) error {
	body := map[string]any{
		"chatid":   chatID,
		"msgtype":  "markdown",
		"markdown": map[string]string{"content": content},
	}
	_, err := w.request(ctx, w.requestID(weComCmdSend), weComCmdSend, body)
	return err
}

func (w *WeCom) sendUploadedMedia(ctx context.Context, chatID, kind string, item bus.MediaItem) error {
	mediaID, err := w.uploadMedia(ctx, kind, item)
	if err != nil {
		return err
	}
	body := map[string]any{
		"chatid":  chatID,
		"msgtype": kind,
		kind:      map[string]string{"media_id": mediaID},
	}
	_, err = w.request(ctx, w.requestID(weComCmdSend), weComCmdSend, body)
	return err
}

func (w *WeCom) uploadMedia(ctx context.Context, kind string, item bus.MediaItem) (string, error) {
	if kind != "file" && kind != "image" && kind != "voice" && kind != "video" {
		return "", fmt.Errorf("unsupported wecom upload type %q", kind)
	}
	if len(item.Bytes) == 0 {
		return "", errors.New("wecom upload media is empty")
	}
	totalChunks := (len(item.Bytes) + weComUploadChunkBytes - 1) / weComUploadChunkBytes
	if totalChunks > weComMaxUploadChunks {
		return "", fmt.Errorf("wecom upload exceeds %d chunks", weComMaxUploadChunks)
	}
	filename := filepath.Base(strings.ReplaceAll(item.Filename, "\\", "/"))
	if filename == "" || filename == "." {
		filename = "attachment.bin"
	}
	sum := md5.Sum(item.Bytes)
	initBody := map[string]any{
		"type": kind, "filename": filename, "total_size": len(item.Bytes),
		"total_chunks": totalChunks, "md5": fmt.Sprintf("%x", sum),
	}
	initFrame, err := w.request(ctx, w.requestID(weComCmdUploadInit), weComCmdUploadInit, initBody)
	if err != nil {
		return "", err
	}
	var initResult struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(initFrame.Body, &initResult); err != nil || initResult.UploadID == "" {
		return "", errors.New("wecom upload initialization returned no upload ID")
	}
	for chunkIndex := 0; chunkIndex < totalChunks; chunkIndex++ {
		start := chunkIndex * weComUploadChunkBytes
		end := min(start+weComUploadChunkBytes, len(item.Bytes))
		chunkBody := map[string]any{
			"upload_id": initResult.UploadID, "chunk_index": chunkIndex,
			"base64_data": base64.StdEncoding.EncodeToString(item.Bytes[start:end]),
		}
		if _, err := w.request(ctx, w.requestID(weComCmdUploadChunk), weComCmdUploadChunk, chunkBody); err != nil {
			return "", err
		}
	}
	finishFrame, err := w.request(ctx, w.requestID(weComCmdUploadFinish), weComCmdUploadFinish, map[string]string{"upload_id": initResult.UploadID})
	if err != nil {
		return "", err
	}
	var finishResult struct {
		MediaID string `json:"media_id"`
	}
	if err := json.Unmarshal(finishFrame.Body, &finishResult); err != nil || finishResult.MediaID == "" {
		return "", errors.New("wecom upload completion returned no media ID")
	}
	return finishResult.MediaID, nil
}

func isWeComImage(item bus.MediaItem) bool {
	contentType := item.ContentType
	if parsed, _, err := mime.ParseMediaType(contentType); err == nil {
		contentType = parsed
	}
	if strings.HasPrefix(strings.ToLower(contentType), "image/") {
		return true
	}
	switch strings.ToLower(filepath.Ext(item.Filename)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp", ".tif", ".tiff":
		return true
	}
	return strings.HasPrefix(http.DetectContentType(item.Bytes), "image/")
}

func normalizeWeComImage(item bus.MediaItem) (bus.MediaItem, error) {
	decoded, format, err := image.Decode(bytes.NewReader(item.Bytes))
	if err != nil {
		return bus.MediaItem{}, fmt.Errorf("decode wecom outbound image: %w", err)
	}
	switch format {
	case "jpeg":
		item.ContentType = "image/jpeg"
		return item, nil
	case "png":
		item.ContentType = "image/png"
		return item, nil
	default:
		var output bytes.Buffer
		if err := png.Encode(&output, decoded); err != nil {
			return bus.MediaItem{}, fmt.Errorf("convert wecom outbound image to PNG: %w", err)
		}
		base := strings.TrimSuffix(filepath.Base(item.Filename), filepath.Ext(item.Filename))
		if base == "" || base == "." {
			base = "image"
		}
		item.Filename = base + ".png"
		item.ContentType = "image/png"
		item.Bytes = output.Bytes()
		return item, nil
	}
}
