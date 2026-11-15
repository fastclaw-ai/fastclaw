package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
)

func TestV1SchedulerRegistration(t *testing.T) {
	h := newV1Harness(t)
	t.Setenv(billing.EnvAllowPrivate, "")
	for name, body := range map[string]map[string]any{
		"loopback": {"url": "https://127.0.0.1", "api_key": "trig_x"},
		"private":  {"url": "https://10.1.2.3", "api_key": "trig_x"},
		"http":     {"url": "http://93.184.216.34", "api_key": "trig_x"},
		"no key":   {"url": "https://93.184.216.34"},
		"no url":   {"api_key": "trig_x"},
	} {
		if code, out := h.do("PUT", "/v1/scheduler", h.keyA, body); code != 400 || errCode(out) != "invalid_request" {
			t.Fatalf("%s: %d %v", name, code, out)
		}
	}

	// A trig that knows the key (private addresses allowed for the fake).
	t.Setenv(billing.EnvAllowPrivate, "true")
	trigSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/app" || r.Header.Get("Authorization") != "Bearer trig_good" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"code":"unauthorized","message":"bad key"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"app":{}}`))
	}))
	defer trigSrv.Close()

	if code, out := h.do("PUT", "/v1/scheduler", h.keyA, map[string]any{"url": trigSrv.URL, "api_key": "trig_bad"}); code != 400 {
		t.Fatalf("bad key accepted: %d %v", code, out)
	}
	code, out := h.do("PUT", "/v1/scheduler", h.keyA, map[string]any{"url": trigSrv.URL + "/", "api_key": "trig_good"})
	if code != 200 || out["url"] != trigSrv.URL || out["key_set"] != true {
		t.Fatalf("register: %d %v", code, out)
	}
	if code, out := h.do("GET", "/v1/scheduler", h.keyA, nil); code != 200 || out["url"] != trigSrv.URL || out["key_set"] != true || out["api_key"] != nil {
		t.Fatalf("GET: %d %v", code, out)
	}
	if _, out := h.do("GET", "/v1/scheduler", h.keyB, nil); out["url"] != "" {
		t.Fatalf("B sees A's scheduler: %v", out)
	}
	if u, key, _ := h.st.GetScheduler(context.Background(), h.appA); u != trigSrv.URL || key != "trig_good" {
		t.Fatalf("stored = %q %q", u, key)
	}
	if code, out := h.do("PUT", "/v1/scheduler", h.keyA, map[string]any{"url": ""}); code != 200 || out["key_set"] != false {
		t.Fatalf("remove: %d %v", code, out)
	}
}
