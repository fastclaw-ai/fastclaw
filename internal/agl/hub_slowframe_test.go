package agl

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// A big message that arrives slowly but steadily must not trip the read
// deadline: the deadline moves with every chunk.
func TestReadMessageExtendingSlowFrame(t *testing.T) {
	const deadline = 300 * time.Millisecond
	want := bytes.Repeat([]byte("x"), 256<<10)
	got := make(chan []byte, 1)
	errs := make(chan error, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			errs <- err
			return
		}
		defer ws.Close()
		extend := func() { _ = ws.SetReadDeadline(time.Now().Add(deadline)) }
		extend()
		data, err := readMessageExtending(ws, extend)
		if err != nil {
			errs <- err
			return
		}
		got <- data
	}))
	defer srv.Close()

	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	w, err := ws.NextWriter(websocket.TextMessage)
	if err != nil {
		t.Fatal(err)
	}
	// 8 chunks, 100ms apart: ~800ms in total, well past the 300ms deadline.
	step := len(want) / 8
	for i := 0; i < len(want); i += step {
		if _, err := w.Write(want[i : i+step]); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-got:
		if !bytes.Equal(data, want) {
			t.Fatalf("got %d bytes, want %d", len(data), len(want))
		}
	case err := <-errs:
		t.Fatalf("read failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
}
