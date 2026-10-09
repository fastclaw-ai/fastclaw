package session

import "testing"

func TestTurnActive(t *testing.T) {
	m := NewManager(t.TempDir())
	if m.TurnActive("nope") {
		t.Fatal("unknown key reported active")
	}
	if len(m.sessions) != 0 {
		t.Fatal("TurnActive created a session")
	}

	s := m.Get("web", "", "chat-1", "")
	var key string
	for k, v := range m.sessions {
		if v == s {
			key = k
		}
	}
	if key == "" {
		t.Fatal("session not cached")
	}
	if m.TurnActive(key) {
		t.Fatal("idle session reported active")
	}
	s.BeginTurn()
	s.BeginTurn()
	if !m.TurnActive(key) {
		t.Fatal("running session reported idle")
	}
	s.EndTurn()
	if !m.TurnActive(key) {
		t.Fatal("still one turn in flight")
	}
	s.EndTurn()
	if m.TurnActive(key) {
		t.Fatal("finished session reported active")
	}
}
