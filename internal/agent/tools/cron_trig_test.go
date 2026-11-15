package tools

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/billing"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// fakeTrig is an in-memory trig /v1/jobs.
type fakeTrig struct {
	mu   sync.Mutex
	jobs map[string]map[string]any
	auth string
}

func (f *fakeTrig) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auth = r.Header.Get("Authorization")
	w.Header().Set("Content-Type", "application/json")
	if r.URL.Path == "/v1/jobs" {
		prefix := r.URL.Query().Get("prefix")
		list := []map[string]any{}
		for k, j := range f.jobs {
			if strings.HasPrefix(k, prefix) {
				list = append(list, j)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jobs": list, "nextCursor": ""})
		return
	}
	key, _ := url.PathUnescape(strings.TrimPrefix(r.URL.EscapedPath(), "/v1/jobs/"))
	switch r.Method {
	case http.MethodPut:
		var body map[string]any
		blob, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(blob, &body)
		sched := body["schedule"].(map[string]any)
		if sched["kind"] == "cron" && len(strings.Fields(sched["expr"].(string))) != 5 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"invalid_schedule","message":"cron needs 5 fields"}}`))
			return
		}
		next := time.Now().Add(time.Hour).UTC()
		job := map[string]any{"key": key, "schedule": sched, "payload": body["payload"], "status": "active", "nextRunAt": next}
		f.jobs[key] = job
		w.WriteHeader(201)
		_ = json.NewEncoder(w).Encode(map[string]any{"job": job, "created": true})
	case http.MethodDelete:
		_, ok := f.jobs[key]
		delete(f.jobs, key)
		_ = json.NewEncoder(w).Encode(map[string]any{"deleted": ok})
	default:
		w.WriteHeader(404)
	}
}

func setupTrigCron(t *testing.T) (*fakeTrig, func(chatter, channel string) *Registry, store.Store) {
	t.Helper()
	t.Setenv(billing.EnvAllowPrivate, "true")
	ft := &fakeTrig{jobs: map[string]map[string]any{}}
	srv := httptest.NewServer(ft)
	t.Cleanup(srv.Close)

	st := newTestStore(t)
	ctx := context.Background()
	if err := st.CreateUser(ctx, &store.UserRecord{ID: "acct", Username: "acct", Email: "a@x.io", Role: "user", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateUser(ctx, &store.UserRecord{ID: "u_end", Username: "u_end", Role: "app_user", Status: "active", OwnerUserID: "acct"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetScheduler(ctx, "acct", srv.URL, "trig_test"); err != nil {
		t.Fatal(err)
	}
	newReg := func(chatter, channel string) *Registry {
		r := NewRegistry(t.TempDir(), t.TempDir())
		r.SetOwnerUserID("acct")
		r.SetChatterUserID(chatter)
		r.SetMessageContext(channel, "", "imc:acc_1:chat_9")
		RegisterCronTools(r, st, "acct", "agt_1")
		return r
	}
	return ft, newReg, st
}

func TestCronToolsUseTheAccountSchedulerOnAPITurns(t *testing.T) {
	ft, newReg, st := setupTrigCron(t)
	r := newReg("u_end", "api")
	ctx := context.Background()

	args, _ := json.Marshal(createCronJobArgs{Name: "喝水", Type: "cron", Schedule: "0 9 * * *", Message: "提醒用户喝水"})
	out, err := r.Execute(ctx, "create_cron_job", string(args))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if ft.auth != "Bearer trig_test" {
		t.Fatalf("auth = %q", ft.auth)
	}
	if len(ft.jobs) != 1 {
		t.Fatalf("trig jobs = %d, want 1", len(ft.jobs))
	}
	var key string
	for k := range ft.jobs {
		key = k
	}
	if !strings.HasPrefix(key, "fc/agt_1/u_end/t") {
		t.Fatalf("key = %q", key)
	}
	blob, _ := json.Marshal(ft.jobs[key]["payload"])
	var p TrigPayload
	_ = json.Unmarshal(blob, &p)
	if p.SessionKey != "imc:acc_1:chat_9" || p.AgentID != "agt_1" || p.Message != "提醒用户喝水" || p.Source != "fastclaw.cron" {
		t.Fatalf("payload = %+v", p)
	}
	if !strings.Contains(out, "ID: "+p.ID) {
		t.Fatalf("output %q lacks the id %s", out, p.ID)
	}
	// Nothing in FastClaw's own cron table: it would fire into "api".
	if rows, _ := st.ListCronJobsByAgent(ctx, "agt_1"); len(rows) != 0 {
		t.Fatalf("built-in cron rows = %d, want 0", len(rows))
	}

	out, err = r.Execute(ctx, "list_cron_jobs", "{}")
	if err != nil || !strings.Contains(out, p.ID) || !strings.Contains(out, "喝水") {
		t.Fatalf("list = %q, %v", out, err)
	}
	// Another chatter of the same agent sees none of it.
	if out, _ := newReg("u_other", "api").Execute(ctx, "list_cron_jobs", "{}"); strings.Contains(out, p.ID) {
		t.Fatalf("other chatter sees the job: %q", out)
	}

	del, _ := json.Marshal(deleteCronJobArgs{ID: p.ID})
	if out, err := r.Execute(ctx, "delete_cron_job", string(del)); err != nil || !strings.Contains(out, "deleted") {
		t.Fatalf("delete = %q, %v", out, err)
	}
	if len(ft.jobs) != 0 {
		t.Fatalf("trig jobs after delete = %d", len(ft.jobs))
	}
}

func TestCronToolsScheduleKinds(t *testing.T) {
	ft, newReg, _ := setupTrigCron(t)
	r := newReg("u_end", "api")
	ctx := context.Background()

	at := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	for _, c := range []struct {
		typ, sched, kind, expr string
	}{
		{"once", at.Format(time.RFC3339), "at", at.Format(time.RFC3339)},
		{"interval", "every 30m", "every", "30m0s"},
	} {
		args, _ := json.Marshal(createCronJobArgs{Name: c.typ, Type: c.typ, Schedule: c.sched, Message: "m"})
		if _, err := r.Execute(ctx, "create_cron_job", string(args)); err != nil {
			t.Fatalf("%s: %v", c.typ, err)
		}
		found := false
		for _, j := range ft.jobs {
			s := j["schedule"].(map[string]any)
			if s["kind"] == c.kind {
				found = true
				if got, _ := time.Parse(time.RFC3339, s["expr"].(string)); c.kind == "at" && !got.Equal(at) {
					t.Fatalf("at expr = %v, want %v", s["expr"], c.expr)
				} else if c.kind != "at" && s["expr"] != c.expr {
					t.Fatalf("%s expr = %v, want %v", c.kind, s["expr"], c.expr)
				}
			}
		}
		if !found {
			t.Fatalf("no %s job", c.kind)
		}
	}

	args, _ := json.Marshal(createCronJobArgs{Name: "x", Type: "interval", Schedule: "30s", Message: "m"})
	if _, err := r.Execute(ctx, "create_cron_job", string(args)); err == nil {
		t.Fatal("30s interval accepted")
	}
	args, _ = json.Marshal(createCronJobArgs{Name: "x", Type: "cron", Schedule: "0 9 * *", Message: "m"})
	if _, err := r.Execute(ctx, "create_cron_job", string(args)); err == nil || !strings.Contains(err.Error(), "invalid schedule") {
		t.Fatalf("bad cron: %v", err)
	}
}

func TestCronToolsKeepBuiltInSchedulerOffAPI(t *testing.T) {
	ft, newReg, st := setupTrigCron(t)
	r := newReg("u_end", "wechat")
	args, _ := json.Marshal(createCronJobArgs{Name: "n", Type: "cron", Schedule: "0 9 * * *", Message: "m"})
	if _, err := r.Execute(context.Background(), "create_cron_job", string(args)); err != nil {
		t.Fatal(err)
	}
	if len(ft.jobs) != 0 {
		t.Fatalf("a channel turn's job went to trig")
	}
	if rows, _ := st.ListCronJobsByAgent(context.Background(), "agt_1"); len(rows) != 1 {
		t.Fatalf("built-in rows = %d, want 1", len(rows))
	}
}

func TestCronToolsCapJobsPerChatter(t *testing.T) {
	_, newReg, _ := setupTrigCron(t)
	r := newReg("u_end", "api")
	args, _ := json.Marshal(createCronJobArgs{Name: "n", Type: "cron", Schedule: "0 9 * * *", Message: "m"})
	for i := 0; i < MaxScheduledJobsPerChatter; i++ {
		if _, err := r.Execute(context.Background(), "create_cron_job", string(args)); err != nil {
			t.Fatalf("job %d: %v", i, err)
		}
	}
	if _, err := r.Execute(context.Background(), "create_cron_job", string(args)); err == nil {
		t.Fatal("job over the cap accepted")
	}
}

func TestCronToolsRefuseAPITurnsWithoutAScheduler(t *testing.T) {
	_, newReg, st := setupTrigCron(t)
	if err := st.SetScheduler(context.Background(), "acct", "", ""); err != nil {
		t.Fatal(err)
	}
	r := newReg("u_end", "api")
	args, _ := json.Marshal(createCronJobArgs{Name: "n", Type: "cron", Schedule: "0 9 * * *", Message: "m"})
	_, err := r.Execute(context.Background(), "create_cron_job", string(args))
	if err == nil || !strings.Contains(err.Error(), "isn't available in this chat") {
		t.Fatalf("err = %v", err)
	}
	if rows, _ := st.ListCronJobsByAgent(context.Background(), "agt_1"); len(rows) != 0 {
		t.Fatalf("a job that can't be delivered was saved")
	}
}
