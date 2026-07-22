package channels

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	miniredis "github.com/alicebob/miniredis/v2"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"
)

type capturedWeComRequest struct {
	reqID string
	cmd   string
	body  map[string]any
}

func TestWeComStreamUsesReplyReqIDStableIDAndCumulativeContent(t *testing.T) {
	w := newOutboundTestWeCom(t)
	w.storeReplyRef("msg-1", weComReplyRef{ReqID: "callback-req-id", ChatID: "chat-1", ExpiresAt: time.Now().Add(time.Minute)})
	requests := captureWeComRequests(w, nil)

	for _, msg := range []bus.OutboundMessage{
		{ChatID: "chat-1", ReplyToMsgID: "msg-1", StreamID: "stable-stream-id", StreamState: bus.StreamStart, Text: "…"},
		{ChatID: "chat-1", ReplyToMsgID: "msg-1", StreamID: "stable-stream-id", StreamState: bus.StreamUpdate, Text: "cumulative Markdown"},
	} {
		if err := w.SendMessage(msg); err != nil {
			t.Fatal(err)
		}
	}

	for i, wantContent := range []string{"…", "cumulative Markdown"} {
		got := <-requests
		if got.reqID != "callback-req-id" || got.cmd != weComCmdRespond {
			t.Fatalf("request %d metadata = %#v", i, got)
		}
		stream := nestedMap(t, got.body, "stream")
		if stream["id"] != "stable-stream-id" || stream["content"] != wantContent || stream["finish"] != false {
			t.Fatalf("request %d stream = %#v", i, stream)
		}
	}
}

func TestWeComStreamFinishSetsFinishTrueAndReleasesReplyRef(t *testing.T) {
	w := newOutboundTestWeCom(t)
	w.storeReplyRef("msg-1", weComReplyRef{ReqID: "callback-req-id", ChatID: "chat-1", ExpiresAt: time.Now().Add(time.Minute)})
	requests := captureWeComRequests(w, nil)
	if err := w.SendMessage(bus.OutboundMessage{
		ChatID: "chat-1", ReplyToMsgID: "msg-1", StreamID: "stream-1",
		StreamState: bus.StreamFinish, Text: "done",
	}); err != nil {
		t.Fatal(err)
	}
	stream := nestedMap(t, (<-requests).body, "stream")
	if stream["finish"] != true || stream["content"] != "done" {
		t.Fatalf("finish stream = %#v", stream)
	}
	if _, ok := w.lookupReplyRef("msg-1"); ok {
		t.Fatal("finished stream retained reply reference")
	}
}

func TestWeComStreamFinalAttachesJPEGAndPNGImages(t *testing.T) {
	w := newOutboundTestWeCom(t)
	w.storeReplyRef("msg-1", weComReplyRef{ReqID: "callback-req-id", ChatID: "chat-1", ExpiresAt: time.Now().Add(time.Minute)})
	requests := captureWeComRequests(w, nil)
	pngBytes := encodeTestImage(t, "png")
	jpegBytes := encodeTestImage(t, "jpeg")
	if err := w.SendMessage(bus.OutboundMessage{
		ChatID: "chat-1", ReplyToMsgID: "msg-1", StreamID: "stream-1", StreamState: bus.StreamFinish,
		Text: "with images", MediaItems: []bus.MediaItem{
			{Filename: "one.png", ContentType: "image/png", Bytes: pngBytes},
			{Filename: "two.jpg", ContentType: "image/jpeg", Bytes: jpegBytes},
		},
	}); err != nil {
		t.Fatal(err)
	}
	stream := nestedMap(t, (<-requests).body, "stream")
	items, ok := stream["msg_item"].([]any)
	if !ok || len(items) != 2 {
		t.Fatalf("msg_item = %#v", stream["msg_item"])
	}
	for i, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || item["msgtype"] != "image" {
			t.Fatalf("msg_item[%d] = %#v", i, raw)
		}
		imageBody := nestedMap(t, item, "image")
		if imageBody["base64"] == "" || imageBody["md5"] == "" {
			t.Fatalf("msg_item[%d] image = %#v", i, imageBody)
		}
	}
}

func TestWeComStreamSendFailureRetainsReplyRefForRetry(t *testing.T) {
	w := newOutboundTestWeCom(t)
	w.storeReplyRef("msg-1", weComReplyRef{ReqID: "callback-req-id", ChatID: "chat-1", ExpiresAt: time.Now().Add(time.Minute)})
	attempts := 0
	w.sendRequestHook = func(context.Context, string, string, any) (weComFrame, error) {
		attempts++
		if attempts == 1 {
			return weComFrame{}, errors.New("fixture send failure")
		}
		return weComFrame{}, nil
	}
	msg := bus.OutboundMessage{
		ChatID: "chat-1", ReplyToMsgID: "msg-1", StreamID: "stream-1", StreamState: bus.StreamUpdate, Text: "partial",
	}
	err := w.SendMessage(msg)
	if err == nil {
		t.Fatal("stream send succeeded")
	}
	if _, ok := w.lookupReplyRef("msg-1"); !ok {
		t.Fatal("failed stream discarded reply reference needed for retry")
	}
	if err := w.SendMessage(msg); err != nil {
		t.Fatalf("retry stream update: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("send attempts = %d, want 2", attempts)
	}
}

func TestWeComStreamFinishFailureRetainsReplyRefUntilSuccessfulRetry(t *testing.T) {
	w := newOutboundTestWeCom(t)
	w.storeReplyRef("msg-1", weComReplyRef{ReqID: "callback-req-id", ChatID: "chat-1", ExpiresAt: time.Now().Add(time.Minute)})
	attempts := 0
	w.sendRequestHook = func(context.Context, string, string, any) (weComFrame, error) {
		attempts++
		if attempts == 1 {
			return weComFrame{}, errors.New("fixture finish failure")
		}
		return weComFrame{}, nil
	}
	msg := bus.OutboundMessage{
		ChatID: "chat-1", ReplyToMsgID: "msg-1", StreamID: "stream-1", StreamState: bus.StreamFinish, Text: "done",
	}
	if err := w.SendMessage(msg); err == nil {
		t.Fatal("stream finish succeeded")
	}
	if _, ok := w.lookupReplyRef("msg-1"); !ok {
		t.Fatal("failed stream finish discarded reply reference needed for retry")
	}
	if err := w.SendMessage(msg); err != nil {
		t.Fatalf("retry stream finish: %v", err)
	}
	if _, ok := w.lookupReplyRef("msg-1"); ok {
		t.Fatal("successful stream finish retained reply reference")
	}
}

func TestWeComMissingReplyRefDropsIntermediateStatesAndFallsBackFinalProactively(t *testing.T) {
	w := newOutboundTestWeCom(t)
	requests := captureWeComRequests(w, nil)

	base := bus.OutboundMessage{
		ChatID: "chat-1", ReplyToMsgID: "msg-from-previous-leaseholder", StreamID: "stream-1",
	}
	for _, state := range []bus.StreamState{bus.StreamStart, bus.StreamUpdate} {
		msg := base
		msg.StreamState = state
		msg.Text = "cumulative intermediate content"
		if err := w.SendMessage(msg); err != nil {
			t.Fatalf("missing-ref %s: %v", state, err)
		}
	}
	select {
	case request := <-requests:
		t.Fatalf("missing-ref intermediate state sent request: %#v", request)
	default:
	}

	finish := base
	finish.StreamState = bus.StreamFinish
	finish.Text = "complete final response"
	if err := w.SendMessage(finish); err != nil {
		t.Fatalf("missing-ref finish fallback: %v", err)
	}
	request := <-requests
	if request.cmd != weComCmdSend || request.body["chatid"] != "chat-1" || request.body["msgtype"] != "markdown" {
		t.Fatalf("finish fallback request = %#v", request)
	}
	if markdown := nestedMap(t, request.body, "markdown"); markdown["content"] != finish.Text {
		t.Fatalf("finish fallback markdown = %#v", markdown)
	}
}

func TestWeComProactiveMarkdownTargetsOriginalChatID(t *testing.T) {
	w := newOutboundTestWeCom(t)
	requests := captureWeComRequests(w, nil)
	if err := w.SendMessage(bus.OutboundMessage{ChatID: "original-group-1", Text: "scheduled **reply**"}); err != nil {
		t.Fatal(err)
	}
	got := <-requests
	if got.cmd != weComCmdSend || got.body["chatid"] != "original-group-1" || got.body["msgtype"] != "markdown" {
		t.Fatalf("proactive request = %#v", got)
	}
	markdown := nestedMap(t, got.body, "markdown")
	if markdown["content"] != "scheduled **reply**" {
		t.Fatalf("markdown = %#v", markdown)
	}
}

func TestWeComLongMarkdownSplitsOnUTF8Boundary(t *testing.T) {
	text := "第一行\n第二行包含 emoji 🦀 和汉字\n第三行"
	chunks := splitWeComMarkdown(text, 18)
	if len(chunks) < 2 {
		t.Fatalf("chunks = %#v", chunks)
	}
	if strings.Join(chunks, "") != text {
		t.Fatalf("chunks changed content: %#v", chunks)
	}
	for i, chunk := range chunks {
		if !utf8.ValidString(chunk) || len([]byte(chunk)) > 18 {
			t.Fatalf("chunk %d invalid or oversized: %q", i, chunk)
		}
	}
}

func TestWeComUploadMediaInitChunkFinish(t *testing.T) {
	w := newOutboundTestWeCom(t)
	requests := captureWeComRequests(w, func(req capturedWeComRequest) weComFrame {
		switch req.cmd {
		case weComCmdUploadInit:
			return frameWithBody(map[string]any{"upload_id": "upload-1"})
		case weComCmdUploadFinish:
			return frameWithBody(map[string]any{"media_id": "media-1", "type": "file"})
		default:
			return weComFrame{}
		}
	})
	data := bytes.Repeat([]byte{0x5a}, weComUploadChunkBytes+1)
	mediaID, err := w.uploadMedia(context.Background(), "file", bus.MediaItem{Filename: "report.bin", Bytes: data})
	if err != nil {
		t.Fatal(err)
	}
	if mediaID != "media-1" {
		t.Fatalf("media ID = %q", mediaID)
	}

	initReq := <-requests
	if initReq.cmd != weComCmdUploadInit || int(initReq.body["total_chunks"].(float64)) != 2 {
		t.Fatalf("init request = %#v", initReq)
	}
	for wantIndex := 0; wantIndex < 2; wantIndex++ {
		chunkReq := <-requests
		if chunkReq.cmd != weComCmdUploadChunk || int(chunkReq.body["chunk_index"].(float64)) != wantIndex {
			t.Fatalf("chunk %d request = %#v", wantIndex, chunkReq)
		}
	}
	if finishReq := <-requests; finishReq.cmd != weComCmdUploadFinish || finishReq.body["upload_id"] != "upload-1" {
		t.Fatalf("finish request = %#v", finishReq)
	}
}

func TestWeComOutboundFileUploadsThenSendsMediaID(t *testing.T) {
	w := newOutboundTestWeCom(t)
	requests := captureWeComRequests(w, func(req capturedWeComRequest) weComFrame {
		switch req.cmd {
		case weComCmdUploadInit:
			return frameWithBody(map[string]any{"upload_id": "upload-1"})
		case weComCmdUploadFinish:
			return frameWithBody(map[string]any{"media_id": "media-1"})
		default:
			return weComFrame{}
		}
	})
	if err := w.SendMessage(bus.OutboundMessage{
		ChatID: "chat-1", MediaItems: []bus.MediaItem{{Filename: "report.pdf", ContentType: "application/pdf", Bytes: []byte("pdf")}},
	}); err != nil {
		t.Fatal(err)
	}
	var sentMedia *capturedWeComRequest
	for i := 0; i < 4; i++ {
		req := <-requests
		if req.cmd == weComCmdSend {
			sentMedia = &req
		}
	}
	if sentMedia == nil || sentMedia.body["chatid"] != "chat-1" || sentMedia.body["msgtype"] != "file" {
		t.Fatalf("media send = %#v", sentMedia)
	}
	if nestedMap(t, sentMedia.body, "file")["media_id"] != "media-1" {
		t.Fatalf("media body = %#v", sentMedia.body)
	}
}

func TestWeComConvertsWebPAndGIFToPNGForImageDelivery(t *testing.T) {
	webpBytes, err := base64.StdEncoding.DecodeString("UklGRjwAAABXRUJQVlA4IDAAAADQAQCdASoBAAEAAgA0JaACdLoB+AADsAD+8MQL/yC5YXXI1/8gP+QH/ID/+PIAAAA=")
	if err != nil {
		t.Fatal(err)
	}
	gifBytes := encodeTestImage(t, "gif")
	for _, item := range []bus.MediaItem{
		{Filename: "one.webp", ContentType: "image/webp", Bytes: webpBytes},
		{Filename: "two.gif", ContentType: "image/gif", Bytes: gifBytes},
	} {
		got, err := normalizeWeComImage(item)
		if err != nil {
			t.Fatalf("normalize %s: %v", item.Filename, err)
		}
		if got.ContentType != "image/png" || !strings.HasSuffix(got.Filename, ".png") {
			t.Fatalf("normalized item = %#v", got)
		}
		if _, err := png.Decode(bytes.NewReader(got.Bytes)); err != nil {
			t.Fatalf("normalized PNG: %v", err)
		}
	}
}

func TestWeComSendFailureSurfacesErrCodeAndKeepsRedisEntryPending(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	mb := bus.NewRedis(bus.RedisConfig{Client: client, Prefix: "test", Group: "workers", Consumer: "leaseholder-1"})
	if err := mb.Start(ctx); err != nil {
		t.Fatal(err)
	}
	w, err := NewWeCom(WeComOptions{BotID: "bot-1", Secret: "fixture-secret"}, mb)
	if err != nil {
		t.Fatal(err)
	}
	w.sendRequestHook = func(context.Context, string, string, any) (weComFrame, error) {
		return weComFrame{}, newWeComProtocolError("aibot_send_msg", 45001, "send rejected", "fixture-secret")
	}
	mb.Outbound <- bus.OutboundMessage{Channel: "wecom", AccountID: "bot-1", ChatID: "chat-1", Text: "hello"}

	handlerErr := make(chan error, 1)
	consumeCtx, stop := context.WithCancel(ctx)
	go func() {
		_ = mb.ConsumeTargetedOutbound(consumeCtx, "wecom", "bot-1", func(msg bus.OutboundMessage) error {
			err := w.SendMessage(msg)
			handlerErr <- err
			return err
		})
	}()
	var protocolErr *WeComProtocolError
	select {
	case err := <-handlerErr:
		if !errors.As(err, &protocolErr) || protocolErr.ErrCode != 45001 {
			t.Fatalf("send error = %T %v", err, err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for targeted send failure")
	}
	stop()

	stream := targetedWeComTestStreamKey("test", "bot-1")
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		pending, pendingErr := client.XPending(ctx, stream, "workers").Result()
		if pendingErr == nil && pending.Count == 1 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("failed WeCom send was acknowledged")
}

func TestWeComRedisTargetedOutboundStartsAfterAuthentication(t *testing.T) {
	mr := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	defer client.Close()
	busCtx, stopBus := context.WithCancel(context.Background())
	defer stopBus()
	mb := bus.NewRedis(bus.RedisConfig{Client: client, Prefix: "test", Group: "workers", Consumer: "leaseholder-1"})
	if err := mb.Start(busCtx); err != nil {
		t.Fatal(err)
	}

	authenticated := make(chan struct{}, 1)
	sent := make(chan map[string]any, 1)
	wsURL := newWeComWSServer(t, func(conn *websocket.Conn) {
		defer conn.Close()
		var auth weComFrame
		if err := conn.ReadJSON(&auth); err != nil {
			return
		}
		if err := conn.WriteJSON(weComFrame{Headers: auth.Headers, ErrCode: 0, ErrMsg: "ok"}); err != nil {
			return
		}
		authenticated <- struct{}{}
		for {
			var frame weComFrame
			if err := conn.ReadJSON(&frame); err != nil {
				return
			}
			if frame.Cmd == weComCmdSend {
				var body map[string]any
				_ = json.Unmarshal(frame.Body, &body)
				sent <- body
			}
			if err := conn.WriteJSON(weComFrame{Headers: frame.Headers, ErrCode: 0, ErrMsg: "ok"}); err != nil {
				return
			}
		}
	})
	w, err := NewWeCom(WeComOptions{
		BotID: "bot-1", Secret: "fixture-secret", WSURL: wsURL,
		HeartbeatInterval: time.Hour, RequestTimeout: time.Second,
		ReconnectBaseDelay: 10 * time.Millisecond, ReconnectMaxDelay: 20 * time.Millisecond,
		ReconnectJitter: func(d time.Duration) time.Duration { return d },
	}, mb)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.Start(ctx) }()
	waitWeComSignal(t, authenticated, "targeted consumer authentication")
	mb.Outbound <- bus.OutboundMessage{Channel: "wecom", AccountID: "bot-1", ChatID: "chat-1", Text: "durable reply"}
	select {
	case body := <-sent:
		if body["chatid"] != "chat-1" || body["msgtype"] != "markdown" {
			t.Fatalf("targeted websocket send = %#v", body)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for targeted websocket send")
	}
	cancel()
	waitWeComDone(t, done)
}

func newOutboundTestWeCom(t *testing.T) *WeCom {
	t.Helper()
	w, err := NewWeCom(WeComOptions{BotID: "bot-1", Secret: "fixture-secret"}, bus.New())
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func captureWeComRequests(w *WeCom, respond func(capturedWeComRequest) weComFrame) <-chan capturedWeComRequest {
	requests := make(chan capturedWeComRequest, 32)
	w.sendRequestHook = func(_ context.Context, reqID, cmd string, body any) (weComFrame, error) {
		data, _ := json.Marshal(body)
		var decoded map[string]any
		_ = json.Unmarshal(data, &decoded)
		request := capturedWeComRequest{reqID: reqID, cmd: cmd, body: decoded}
		requests <- request
		if respond != nil {
			return respond(request), nil
		}
		return weComFrame{}, nil
	}
	return requests
}

func frameWithBody(body any) weComFrame {
	data, _ := json.Marshal(body)
	return weComFrame{Body: data}
}

func nestedMap(t *testing.T, parent map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := parent[key].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v", key, parent[key])
	}
	return value
}

func encodeTestImage(t *testing.T, format string) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var out bytes.Buffer
	var err error
	switch format {
	case "png":
		err = png.Encode(&out, img)
	case "jpeg":
		err = jpeg.Encode(&out, img, nil)
	case "gif":
		err = gif.Encode(&out, img, nil)
	default:
		err = fmt.Errorf("unsupported test image format %q", format)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func targetedWeComTestStreamKey(prefix, accountID string) string {
	sum := sha256.Sum256([]byte(accountID))
	return fmt.Sprintf("%s:bus:outbound:target:wecom:%x", prefix, sum[:12])
}
