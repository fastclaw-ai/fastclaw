package agl

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestAskWaitsForTheDecision(t *testing.T) {
	tl := NewTools(nil, nil, nil)
	chat := WebToolChat("u1", "agent", "s1")
	var mu sync.Mutex
	var notices []ToolNotice
	notify := func(n ToolNotice) { mu.Lock(); notices = append(notices, n); mu.Unlock() }
	seen := func() []ToolNotice { mu.Lock(); defer mu.Unlock(); return append([]ToolNotice(nil), notices...) }
	ask := ApprovalAsk{Chat: chat, UserID: "u1", Agent: "Notion", Title: "update page", Notify: notify, Wait: time.Minute}

	done := make(chan string, 1)
	go func() { d, _ := tl.Ask(context.Background(), ask); done <- d }()
	deadline := time.Now().Add(2 * time.Second)
	for !tl.HasPendingApproval(chat) && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Only the person it asks can see or answer it.
	if _, ok := tl.PendingApproval(chat, "someone-else"); ok {
		t.Fatal("another user sees the request")
	}
	if p, ok := tl.PendingApproval(chat, "u1"); !ok || p.Agent != "Notion" || p.Title != "update page" {
		t.Fatalf("pending: %+v %v", p, ok)
	}
	// A second request in the same chat waits for the first.
	if _, err := tl.Ask(context.Background(), ask); err == nil {
		t.Fatal("second request accepted")
	}
	if found, err := tl.Decide(context.Background(), chat, "u1", "allow_session"); !found || err != nil {
		t.Fatalf("decide: %v %v", found, err)
	}
	if d := <-done; d != "allow_session" {
		t.Fatalf("decision = %q", d)
	}
	if n := seen(); len(n) != 2 || n[0].Kind != NoticeApproval || n[1].Decision != "allow_session" {
		t.Fatalf("notices: %+v", n)
	}

	ask.Wait = 10 * time.Millisecond
	if d, err := tl.Ask(context.Background(), ask); d != DecisionTimeout || err != nil {
		t.Fatalf("timeout: %q %v", d, err)
	}
	if n := seen(); tl.HasPendingApproval(chat) || n[len(n)-1].Decision != DecisionTimeout {
		t.Fatalf("after timeout: %+v", n)
	}
}
