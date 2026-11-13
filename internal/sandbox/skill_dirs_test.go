package sandbox

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
)

// The sandbox's /skills: agent, then the owner's (product) skills, then
// global — never a chatter's.
func TestSkillDirsForAgentAddsOwnerNotChatter(t *testing.T) {
	home := "/h"
	got := skillDirsForAgent(home, "a1", "u_owner")
	want := []string{
		filepath.Join(home, "agents", "a1", "agent", "skills"),
		filepath.Join(home, "users", "u_owner", "skills"),
		filepath.Join(home, "skills"),
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"", "../x", "a/b"} {
		if got := skillDirsForAgent(home, "a1", bad); len(got) != 2 {
			t.Fatalf("%q: %v", bad, got)
		}
	}
	ctx := WithUserID(WithSkillOwner(context.Background(), "u_owner"), "u_chat")
	if SkillOwnerFromContext(ctx) != "u_owner" {
		t.Fatal("owner lost")
	}
}
