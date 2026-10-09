package store

import (
	"context"
	"strconv"
	"testing"
)

// TestListSessionMessagesPage walks a session's archive page by page
// and checks that every page starts on a real user turn (never on a
// tool result or a runtime-injected prompt) and that the pages together
// cover every row exactly once.
func TestListSessionMessagesPage(t *testing.T) {
	db := openTestDB(t)
	defer db.Close()
	ctx := context.Background()
	uid, agent, key := "user-1", "agent-A", "s-page-test"

	// Turns of varying length: user, then n assistant/tool rows. Turn 2
	// also carries a runtime-injected user row, which must not be
	// treated as a page boundary.
	var want []string
	appendRow := func(m SessionMessage) {
		t.Helper()
		if err := db.AppendSessionMessage(ctx, uid, agent, key, m); err != nil {
			t.Fatalf("append: %v", err)
		}
		want = append(want, m.Content)
	}
	for turn, n := range []int{1, 7, 2, 12, 1, 3} {
		appendRow(SessionMessage{Role: "user", Content: "u" + strconv.Itoa(turn)})
		for i := 0; i < n; i++ {
			if turn == 1 && i == 3 {
				appendRow(SessionMessage{Role: "user", Content: "goal" + strconv.Itoa(i), Origin: "goal_context"})
				continue
			}
			role := "tool"
			if i%2 == 0 {
				role = "assistant"
			}
			appendRow(SessionMessage{Role: role, Content: "t" + strconv.Itoa(turn) + "." + strconv.Itoa(i)})
		}
	}

	var pages [][]SessionMessage
	before := int64(-1)
	for range len(want) {
		msgs, start, hasMore, err := db.ListSessionMessagesPage(ctx, uid, agent, key, before, 5)
		if err != nil {
			t.Fatalf("page: %v", err)
		}
		if len(msgs) == 0 {
			t.Fatalf("empty page at before=%d", before)
		}
		if msgs[0].Role != "user" || msgs[0].Origin != "" {
			t.Errorf("page at before=%d starts with %s/%q (%s)", before, msgs[0].Role, msgs[0].Origin, msgs[0].Content)
		}
		pages = append([][]SessionMessage{msgs}, pages...)
		if !hasMore {
			if start != 0 {
				t.Errorf("last page start = %d, want 0", start)
			}
			break
		}
		before = start
	}

	var got []string
	for _, page := range pages {
		for _, m := range page {
			got = append(got, m.Content)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("pages cover %d rows, want %d: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("row %d = %q, want %q", i, got[i], want[i])
		}
	}

	if msgs, start, _, err := db.ListSessionMessagesPage(ctx, uid, agent, "s-missing", -1, 5); err != nil || len(msgs) != 0 || start != -1 {
		t.Errorf("missing session: msgs=%d start=%d err=%v, want empty with start -1", len(msgs), start, err)
	}
}
