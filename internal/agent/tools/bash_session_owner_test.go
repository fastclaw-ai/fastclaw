package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestShellManager_OwnerIsolation guards the cross-tenant leak fixed by
// stamping an ownerKey on every background shell: a Get with a different
// ownerKey must return nil, indistinguishable from "no such id", so a
// chatter who guesses another's bash_id cannot read its output or kill
// its process.
func TestShellManager_OwnerIsolation(t *testing.T) {
	m := newShellManager()
	defer m.Close()

	ownerA := OwnerKeyFor("ag_1", "u_alice", "s-aaa")
	ownerB := OwnerKeyFor("ag_1", "u_bob", "s-bbb")

	// Alice starts a shell printing a secret.
	sA, err := m.Start("printf 'alice-secret\\n'", nil, ownerA)
	if err != nil {
		t.Fatalf("alice start: %v", err)
	}
	waitDone(t, sA, 2*time.Second)

	// Bob starts an unrelated shell.
	sB, err := m.Start("printf 'bob-secret\\n'", nil, ownerB)
	if err != nil {
		t.Fatalf("bob start: %v", err)
	}
	waitDone(t, sB, 2*time.Second)

	// Same owner can read its own shell.
	if got := m.Get(sA.id, ownerA); got == nil {
		t.Fatalf("alice cannot read her own shell %s", sA.id)
	} else {
		out, _ := got.readNew()
		if !strings.Contains(string(out), "alice-secret") {
			t.Errorf("alice got wrong output: %q", out)
		}
	}

	// Cross-owner read must be refused — nil, like "no such id".
	if got := m.Get(sA.id, ownerB); got != nil {
		t.Errorf("SECURITY: bob read alice's shell %s — owner isolation broken", sA.id)
	}
	if got := m.Get(sB.id, ownerA); got != nil {
		t.Errorf("SECURITY: alice read bob's shell %s — owner isolation broken", sB.id)
	}

	// A guessed id that doesn't exist also returns nil (parity — caller
	// can't tell "exists but not yours" from "doesn't exist").
	if got := m.Get("bash_999", ownerA); got != nil {
		t.Errorf("nonexistent id returned non-nil: %v", got)
	}
}

// TestShellManager_OwnerIsolation_Kill confirms a cross-owner kill_shell
// is also refused (not just reads). Mirrors the Get gate the kill tool
// relies on.
func TestShellManager_OwnerIsolation_Kill(t *testing.T) {
	m := newShellManager()
	defer m.Close()

	ownerA := OwnerKeyFor("ag_1", "u_alice", "s-aaa")
	ownerB := OwnerKeyFor("ag_1", "u_bob", "s-bbb")

	// Alice starts a long-running shell.
	sA, err := m.Start("sleep 30", nil, ownerA)
	if err != nil {
		t.Fatalf("alice start: %v", err)
	}
	defer sA.kill()

	// Bob must not be able to resolve (and thus kill) Alice's shell.
	if got := m.Get(sA.id, ownerB); got != nil {
		t.Errorf("SECURITY: bob resolved alice's shell for kill — isolation broken")
	}
	// Alice can.
	if got := m.Get(sA.id, ownerA); got == nil {
		t.Errorf("alice cannot resolve her own shell for kill")
	}
}

// TestShellManager_EmptyOwnerKeySkipsGate documents the opt-out: an empty
// ownerKey means "no chat tenancy" (boot/admin paths, legacy tests) and
// must NOT block access. Without this, boot-driven exec would be locked
// out of shells it legitimately owns.
func TestShellManager_EmptyOwnerKeySkipsGate(t *testing.T) {
	m := newShellManager()
	defer m.Close()

	s, err := m.Start("printf 'boot\\n'", nil, "some-owner")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waitDone(t, s, 2*time.Second)

	// Empty ownerKey bypasses the check even though the shell was
	// started with a real owner — this is the boot/admin escape hatch.
	if got := m.Get(s.id, ""); got == nil {
		t.Errorf("empty ownerKey should bypass isolation but got nil")
	}
}

// TestBashOutput_CrossTenantRefused exercises the full tool path
// (registerBashOutput → shellManager.Get) to confirm the ownerKey is
// plumbed from TurnContext through to the gate, closing the real
// attack: a chatter reading another's shell via the bash_output tool.
func TestBashOutput_CrossTenantRefused(t *testing.T) {
	r := NewRegistry(t.TempDir(), t.TempDir())
	defer r.Close()
	r.agentID = "ag_1"

	// Alice starts a shell in her turn context.
	ctxAlice := WithTurnContext(context.Background(), &TurnContext{
		ChatterUserID: "u_alice",
		SessionID:     "s-aaa",
	})
	// Drive Start through the exec tool so the ownerKey wiring is the
	// real production path (not a direct shellMgr.Start call).
	execArgs := `{"command":"printf 'alice-secret\\n'","run_in_background":true}`
	if _, err := r.Execute(ctxAlice, "exec", execArgs); err != nil {
		t.Fatalf("alice exec: %v", err)
	}
	// The only shell belongs to alice → bash_1.
	sA := r.shellMgr.Get("bash_1", OwnerKeyFor("ag_1", "u_alice", "s-aaa"))
	if sA == nil {
		t.Fatalf("alice's shell not found as bash_1")
	}
	waitDone(t, sA, 2*time.Second)

	// Bob tries to read it from his own turn context — must be refused.
	ctxBob := WithTurnContext(context.Background(), &TurnContext{
		ChatterUserID: "u_bob",
		SessionID:     "s-bbb",
	})
	args := `{"bash_id":"bash_1"}`
	got, err := r.Execute(ctxBob, "bash_output", args)
	if err == nil {
		t.Errorf("SECURITY: bob read alice's shell without error: %q", got)
	}
	if err != nil && !strings.Contains(err.Error(), "no such bash_id") {
		t.Errorf("expected 'no such bash_id' refusal, got err: %q", err.Error())
	}
	if strings.Contains(got, "alice-secret") || (err != nil && strings.Contains(err.Error(), "alice-secret")) {
		t.Errorf("SECURITY: alice's output leaked to bob: result=%q err=%q", got, err)
	}

	// Alice can still read her own shell through the tool.
	if got, err := r.Execute(ctxAlice, "bash_output", args); err != nil {
		t.Errorf("alice failed to read her own shell: %v", err)
	} else if !strings.Contains(got, "alice-secret") {
		t.Errorf("alice didn't get her own output: %q", got)
	}
}
