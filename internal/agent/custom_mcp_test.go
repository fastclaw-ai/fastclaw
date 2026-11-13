package agent

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/usermcp"
)

func TestMergeServers(t *testing.T) {
	own := []usermcp.Server{{ID: "a", Name: "Notes"}, {ID: "b", Name: "Search"}}
	offered := []usermcp.Server{{ID: "b", Name: "Search"}, {ID: "c", Name: "notes"}, {ID: "d", Name: "Wiki"}}
	got := mergeServers(own, offered)
	want := []string{"Notes", "Search", "notes (2)", "Wiki"}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i, w := range want {
		if got[i].Name != w {
			t.Fatalf("got[%d] = %q, want %q", i, got[i].Name, w)
		}
	}
}
