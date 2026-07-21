package channels

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

const (
	weComMaxInboundMediaBytes = 25 * 1024 * 1024
	weComMaxMediaRedirects    = 3
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
