package channels

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

func TestDecryptWeComMediaAES256CBC(t *testing.T) {
	key := bytes.Repeat([]byte{0x31}, 32)
	want := []byte("wecom encrypted fixture")
	ciphertext := encryptWeComMediaFixture(t, want, key)

	got, err := decryptWeComMedia(ciphertext, base64.StdEncoding.EncodeToString(key))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decrypted bytes = %q", got)
	}
}

func TestDecryptWeComMediaRejectsBadKeyAndPadding(t *testing.T) {
	if _, err := decryptWeComMedia([]byte("ciphertext"), "not-base64"); err == nil {
		t.Fatal("invalid key succeeded")
	}
	shortKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 31))
	if _, err := decryptWeComMedia(bytes.Repeat([]byte{0}, 16), shortKey); err == nil {
		t.Fatal("short key succeeded")
	}

	key := bytes.Repeat([]byte{0x32}, 32)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	badPlaintext := bytes.Repeat([]byte{0}, 32)
	badCiphertext := make([]byte, len(badPlaintext))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(badCiphertext, badPlaintext)
	if _, err := decryptWeComMedia(badCiphertext, base64.StdEncoding.EncodeToString(key)); err == nil {
		t.Fatal("invalid PKCS#7 padding succeeded")
	}
}

func TestDownloadWeComMediaEnforcesHTTPSAndSizeLimit(t *testing.T) {
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("should not be requested"))
	}))
	defer httpServer.Close()
	if _, err := downloadWeComMedia(context.Background(), http.DefaultClient, httpServer.URL, "", "file", 32); err == nil {
		t.Fatal("HTTP media URL succeeded")
	}

	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte{1}, 33))
	}))
	defer tlsServer.Close()
	if _, err := downloadWeComMedia(context.Background(), tlsServer.Client(), tlsServer.URL, "", "file", 32); err == nil {
		t.Fatal("oversized media succeeded")
	}
}

func TestWeComMapsImageAndFileToMediaItems(t *testing.T) {
	imageBytes := append([]byte(nil), pngFixture...)
	fileBytes := []byte("%PDF-1.7 fixture")
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/image":
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Disposition", `attachment; filename="picture.png"`)
			_, _ = w.Write(imageBytes)
		case "/file":
			w.Header().Set("Content-Type", "application/pdf")
			w.Header().Set("Content-Disposition", `attachment; filename="report.pdf"`)
			_, _ = w.Write(fileBytes)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	mb := bus.New()
	w, err := NewWeCom(WeComOptions{
		BotID: "bot-1", Secret: "fixture-secret", HTTPClient: server.Client(),
	}, mb)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		msgType string
		body    map[string]any
		name    string
		want    []byte
	}{
		{msgType: "image", body: map[string]any{"url": server.URL + "/image"}, name: "picture.png", want: imageBytes},
		{msgType: "file", body: map[string]any{"url": server.URL + "/file"}, name: "report.pdf", want: fileBytes},
	}
	for i, tc := range tests {
		frame := weComCallbackTestFrame("callback-media", map[string]any{
			"msgid": "msg-" + tc.msgType, "aibotid": "bot-1", "chattype": "single",
			"from":    map[string]string{"userid": "member-1"},
			"msgtype": tc.msgType, tc.msgType: tc.body,
		})
		if err := w.handleCallbackFrame(context.Background(), frame); err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		got := waitInboundMessage(t, mb)
		if len(got.MediaItems) != 1 || got.MediaItems[0].Filename != tc.name || !bytes.Equal(got.MediaItems[0].Bytes, tc.want) {
			t.Fatalf("case %d media = %#v", i, got.MediaItems)
		}
	}
}

func TestWeComMapsMixedTextAndImagesInOrder(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Disposition", `attachment; filename="`+strings.TrimPrefix(r.URL.Path, "/")+`.png"`)
		_, _ = w.Write(append(pngFixture, r.URL.Path...))
	}))
	defer server.Close()

	mb := bus.New()
	w, err := NewWeCom(WeComOptions{BotID: "bot-1", Secret: "fixture-secret", HTTPClient: server.Client()}, mb)
	if err != nil {
		t.Fatal(err)
	}
	frame := weComCallbackTestFrame("callback-mixed", map[string]any{
		"msgid": "msg-mixed", "aibotid": "bot-1", "chattype": "single",
		"from": map[string]string{"userid": "member-1"}, "msgtype": "mixed",
		"mixed": map[string]any{"msg_item": []any{
			map[string]any{"msgtype": "text", "text": map[string]string{"content": "first"}},
			map[string]any{"msgtype": "image", "image": map[string]string{"url": server.URL + "/one"}},
			map[string]any{"msgtype": "text", "text": map[string]string{"content": "second"}},
			map[string]any{"msgtype": "image", "image": map[string]string{"url": server.URL + "/two"}},
		}},
	})
	if err := w.handleCallbackFrame(context.Background(), frame); err != nil {
		t.Fatal(err)
	}
	got := waitInboundMessage(t, mb)
	if got.Text != "first\nsecond" {
		t.Fatalf("mixed text = %q", got.Text)
	}
	if len(got.MediaItems) != 2 || got.MediaItems[0].Filename != "one.png" || got.MediaItems[1].Filename != "two.png" {
		t.Fatalf("mixed media order = %#v", got.MediaItems)
	}
}

func TestWeComMediaFailureRepliesWithoutPublishingInbound(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("not valid encrypted media"))
	}))
	defer server.Close()

	mb := bus.New()
	w, err := NewWeCom(WeComOptions{BotID: "bot-1", Secret: "fixture-secret", HTTPClient: server.Client()}, mb)
	if err != nil {
		t.Fatal(err)
	}
	replied := make(chan map[string]any, 1)
	w.sendRequestHook = func(_ context.Context, reqID, cmd string, body any) (weComFrame, error) {
		data, _ := json.Marshal(body)
		var decoded map[string]any
		_ = json.Unmarshal(data, &decoded)
		decoded["req_id"] = reqID
		decoded["cmd"] = cmd
		replied <- decoded
		return weComFrame{}, nil
	}
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32))
	frame := weComCallbackTestFrame("callback-media-error", map[string]any{
		"msgid": "msg-image", "aibotid": "bot-1", "chattype": "single",
		"from": map[string]string{"userid": "member-1"}, "msgtype": "image",
		"image": map[string]string{"url": server.URL + "/temporary-sensitive-path", "aeskey": key},
	})
	if err := w.handleCallbackFrame(context.Background(), frame); err == nil {
		t.Fatal("corrupt media callback succeeded")
	}
	assertNoInboundMessage(t, mb)
	select {
	case reply := <-replied:
		data, _ := json.Marshal(reply)
		if bytes.Contains(data, []byte(key)) || bytes.Contains(data, []byte("temporary-sensitive-path")) {
			t.Fatalf("passive media error exposed sensitive values")
		}
		if reply["req_id"] != "callback-media-error" || reply["cmd"] != weComCmdRespond {
			t.Fatalf("passive media error reply = %#v", reply)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for passive media error reply")
	}
}

func TestWeComVoiceAndVideoReturnUnsupportedError(t *testing.T) {
	for _, msgType := range []string{"voice", "video"} {
		mb := bus.New()
		w, err := NewWeCom(WeComOptions{BotID: "bot-1", Secret: "fixture-secret"}, mb)
		if err != nil {
			t.Fatal(err)
		}
		replied := make(chan struct{}, 1)
		w.sendRequestHook = func(context.Context, string, string, any) (weComFrame, error) {
			replied <- struct{}{}
			return weComFrame{}, nil
		}
		frame := weComCallbackTestFrame("callback-unsupported", map[string]any{
			"msgid": "msg-unsupported", "aibotid": "bot-1", "chattype": "single",
			"from": map[string]string{"userid": "member-1"}, "msgtype": msgType,
		})
		if err := w.handleCallbackFrame(context.Background(), frame); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("%s error = %v", msgType, err)
		}
		assertNoInboundMessage(t, mb)
		waitWeComSignal(t, replied, msgType+" unsupported reply")
	}
}

func TestWeComMediaErrorRedactsURLAndAESKey(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusBadGateway)
	}))
	defer server.Close()
	urlValue := server.URL + "/temporary-sensitive-url?token=do-not-print"
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x55}, 32))
	_, err := downloadWeComMedia(context.Background(), server.Client(), urlValue, key, "file", 1024)
	if err == nil {
		t.Fatal("failed media download succeeded")
	}
	if strings.Contains(err.Error(), urlValue) || strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "do-not-print") {
		t.Fatalf("media error exposed temporary URL or AES key")
	}
}

func encryptWeComMediaFixture(t *testing.T, plaintext, key []byte) []byte {
	t.Helper()
	padLen := 32 - len(plaintext)%32
	padded := append(append([]byte(nil), plaintext...), bytes.Repeat([]byte{byte(padLen)}, padLen)...)
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, len(padded))
	cipher.NewCBCEncrypter(block, key[:16]).CryptBlocks(out, padded)
	return out
}

var pngFixture = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
}
