package sandbox

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// BoxLite Cloud rejects unknown create properties, and answers 408 with the
// box id when a cold image pull outlasts its start wait. createBox must send
// only known fields and wait the pending box out instead of failing.
func TestBoxliteCreateWaitsOutPendingBox(t *testing.T) {
	old := boxlitePollInterval
	boxlitePollInterval = 10 * time.Millisecond
	t.Cleanup(func() { boxlitePollInterval = old })

	var polls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && r.URL.Path == "/boxes":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			for k := range body {
				if k != "image" {
					w.WriteHeader(http.StatusBadRequest)
					_, _ = w.Write([]byte(`{"message":"property ` + k + ` should not exist"}`))
					return
				}
			}
			w.WriteHeader(http.StatusRequestTimeout)
			_, _ = w.Write([]byte(`{"statusCode":408,"boxId":"bx1","progress":{"phase":"preparing_image"}}`))
		case r.Method == "GET" && r.URL.Path == "/boxes/bx1":
			status := "configured"
			if polls.Add(1) >= 3 {
				status = "running"
			}
			_, _ = w.Write([]byte(`{"box_id":"bx1","status":"` + status + `"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()

	e := &BoxliteExecutor{baseURL: srv.URL, image: "img", client: srv.Client()}
	if err := e.createBox(context.Background()); err != nil {
		t.Fatalf("createBox: %v", err)
	}
	if e.boxID != "bx1" {
		t.Fatalf("boxID = %q, want bx1", e.boxID)
	}
	if polls.Load() < 3 {
		t.Fatalf("returned before the box was running (polls=%d)", polls.Load())
	}
}
