package agent

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/config"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// fakeMemoryStore is a deterministic in-memory MemoryStore for the
// chatbot-prompt assembly tests. Lookups are scoped by (agentID, userID,
// filename); empty result returns "" with no error. Mirrors the actual
// DBStore "exact" semantics so the "render placeholder when empty" rule
// can be exercised both ways.
type fakeMemoryStore struct {
	// key = agentID + "|" + userID + "|" + filename
	files map[string][]byte
}

func newFakeMemoryStore() *fakeMemoryStore {
	return &fakeMemoryStore{files: map[string][]byte{}}
}

func (f *fakeMemoryStore) put(agentID, userID, filename, content string) {
	f.files[agentID+"|"+userID+"|"+filename] = []byte(content)
}

func (f *fakeMemoryStore) GetMemory(ctx context.Context, agentID, userID string) (string, error) {
	if v, ok := f.files[agentID+"|"+userID+"|MEMORY.md"]; ok {
		return string(v), nil
	}
	return "", nil
}

func (f *fakeMemoryStore) SaveMemory(ctx context.Context, agentID, userID, content string) error {
	f.put(agentID, userID, "MEMORY.md", content)
	return nil
}

func (f *fakeMemoryStore) GetWorkspaceFile(ctx context.Context, agentID, userID, filename string) ([]byte, error) {
	// Overlay: caller's row first, fall back to owner-keyed (userID="" sentinel).
	if v, ok := f.files[agentID+"|"+userID+"|"+filename]; ok {
		return v, nil
	}
	if v, ok := f.files[agentID+"||"+filename]; ok {
		return v, nil
	}
	return nil, nil
}

func (f *fakeMemoryStore) GetWorkspaceFileExact(ctx context.Context, agentID, userID, filename string) ([]byte, error) {
	if v, ok := f.files[agentID+"|"+userID+"|"+filename]; ok {
		return v, nil
	}
	return nil, nil
}

func (f *fakeMemoryStore) SaveWorkspaceFile(ctx context.Context, agentID, userID, filename string, data []byte) error {
	f.put(agentID, userID, filename, string(data))
	return nil
}

// ListKnowledgeDocs makes the fake satisfy knowledgeDocLister so the
// prompt's knowledge section renders from uploaded knowledge/* rows.
// Sorted for deterministic [K#] ordering, matching the DBStore query.
func (f *fakeMemoryStore) ListKnowledgeDocs(ctx context.Context, agentID, userID string) ([]store.KnowledgeDoc, error) {
	prefix := agentID + "|" + userID + "|knowledge/"
	var out []store.KnowledgeDoc
	for key, data := range f.files {
		if !strings.HasPrefix(key, prefix) {
			continue
		}
		out = append(out, store.KnowledgeDoc{
			Path:    strings.TrimPrefix(key, agentID+"|"+userID+"|"),
			Content: string(data),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

const (
	testAgentID = "agt_test"
	ownerUID    = "u_owner"
	chatterUID  = "u_chatter"
)

func newChatbotBuilder(store *fakeMemoryStore) *ContextBuilder {
	mem := NewMemoryWithStoreForUser("", store, ownerUID, testAgentID)
	cb := NewContextBuilder("", mem, "")
	cb.store = store
	cb.agentID = testAgentID
	cb.userID = ownerUID
	cb.SetPromptMode(config.PromptModeChatbot)
	return cb
}

func TestChatbotPrompt_EmptyChatter(t *testing.T) {
	store := newFakeMemoryStore()
	// SOUL.md owner-keyed; chatter inherits via overlay.
	store.put(testAgentID, ownerUID, "SOUL.md", "# DTJ Soul\nbe terse.")
	cb := newChatbotBuilder(store)
	chatterMem := cb.memory.WithUserID(chatterUID)

	prompt := cb.BuildSystemPromptAs(chatterUID, chatterMem, false)

	// Headers we depend on for the fingerprint log.
	mustContain(t, prompt, "# SOUL.md")
	mustContain(t, prompt, "<current_chatter_profile")
	mustContain(t, prompt, "</current_chatter_profile>")
	mustContain(t, prompt, "<chatter_long_term_memory")
	mustContain(t, prompt, "</chatter_long_term_memory>")

	// Placeholder bodies (chatter is brand-new, no USER.md or MEMORY.md row).
	mustContain(t, prompt, "(empty — no profile recorded yet for this chatter")
	mustContain(t, prompt, "(empty — nothing recorded yet for this chatter")

	// Persistence instruction block — the load-bearing bit that tells
	// the model it CAN write across sessions.
	mustContain(t, prompt, "Remembering things across conversations")
	mustContain(t, prompt, "You CAN remember chatters across sessions")
	mustContain(t, prompt, "you MUST call write_file")

	// SOUL.md owner-fallback overlay should bring the owner's row to the
	// chatter view.
	mustContain(t, prompt, "DTJ Soul")
	mustContain(t, prompt, "be terse")
}

func TestChatbotPrompt_PopulatedChatter(t *testing.T) {
	store := newFakeMemoryStore()
	store.put(testAgentID, ownerUID, "SOUL.md", "# DTJ Soul")
	store.put(testAgentID, ownerUID, "knowledge/faq.md", "# FAQ\n- Plan: Pro includes web search")
	store.put(testAgentID, chatterUID, "USER.md", "# Current Chatter\n- Name: 品冠")
	store.put(testAgentID, chatterUID, "MEMORY.md", "# Memory Log\n- 用户在做产品")
	cb := newChatbotBuilder(store)
	chatterMem := cb.memory.WithUserID(chatterUID)

	prompt := cb.BuildSystemPromptAs(chatterUID, chatterMem, false)

	// Populated USER.md must show real content, not the placeholder.
	mustContain(t, prompt, "Name: 品冠")
	mustContain(t, prompt, "Treat the content below as factual")
	mustNotContain(t, prompt, "(empty — no profile recorded yet for this chatter")

	// Populated MEMORY.md must show real content + the trust framing.
	mustContain(t, prompt, "用户在做产品")
	mustContain(t, prompt, "Treat as factual and current")
	mustNotContain(t, prompt, "(empty — nothing recorded yet for this chatter")

	// Agent knowledge is owner-uploaded reference material, distinct
	// from chatter memory and persona files. A small corpus is injected
	// in full — no query dependence, stable across turns.
	mustContain(t, prompt, `<agent_knowledge_base mode="full">`)
	mustContain(t, prompt, "source_id: K1")
	mustContain(t, prompt, "file: faq.md")
	mustContain(t, prompt, "path: knowledge/faq.md")
	mustContain(t, prompt, "cite it inline with the source_id")
	mustContain(t, prompt, "Plan: Pro includes web search")
}

func TestChatbotPrompt_KnowledgeSourceIDsFollowUploadOrder(t *testing.T) {
	store := newFakeMemoryStore()
	store.put(testAgentID, ownerUID, "KNOWLEDGE.md", "pinned owner notes")
	store.put(testAgentID, ownerUID, "knowledge/aaa-faq.md", "faq body")
	store.put(testAgentID, ownerUID, "knowledge/bbb-pricing.md", "pricing body")
	cb := newChatbotBuilder(store)

	prompt := cb.BuildSystemPromptAs(chatterUID, cb.memory.WithUserID(chatterUID), false)

	// Pinned KNOWLEDGE.md is K1, uploaded files follow in path order.
	mustContain(t, prompt, "## [K1] KNOWLEDGE.md")
	mustContain(t, prompt, "pinned owner notes")
	mustContain(t, prompt, "source_id: K2\nfile: aaa-faq.md\npath: knowledge/aaa-faq.md")
	mustContain(t, prompt, "source_id: K3\nfile: bbb-pricing.md\npath: knowledge/bbb-pricing.md")

	// And the extraction side agrees with what was injected — this pair
	// is the contract the web citation badges depend on.
	sources := extractKnowledgeCitationSources(prompt)
	if len(sources) != 3 {
		t.Fatalf("extracted %d sources, want 3: %+v", len(sources), sources)
	}
	if sources[1].ID != "K2" || sources[1].File != "aaa-faq.md" || sources[1].Path != "knowledge/aaa-faq.md" {
		t.Fatalf("unexpected source[1]: %+v", sources[1])
	}
}

func TestChatbotPrompt_LargeKnowledgeSwitchesToIndexMode(t *testing.T) {
	store := newFakeMemoryStore()
	big := strings.Repeat("知识库内容 knowledge body. ", 2000) // ~48k chars, over the full-inject budget
	store.put(testAgentID, ownerUID, "knowledge/aaa-handbook.md", big)
	store.put(testAgentID, ownerUID, "knowledge/bbb-faq.md", "small faq body")
	cb := newChatbotBuilder(store)

	prompt := cb.BuildSystemPromptAs(chatterUID, cb.memory.WithUserID(chatterUID), false)

	mustContain(t, prompt, `<agent_knowledge_base mode="index">`)
	mustContain(t, prompt, "knowledge_search")
	mustContain(t, prompt, "- aaa-handbook.md")
	mustContain(t, prompt, "- bbb-faq.md")
	// The corpus body must NOT be inlined in index mode.
	mustNotContain(t, prompt, "small faq body")
	if len(prompt) > 20000+len(big)/10 {
		t.Fatalf("index-mode prompt looks like it inlined the corpus (len=%d)", len(prompt))
	}
	// No [K#] source blocks in index mode → no citation metadata.
	if sources := extractKnowledgeCitationSources(prompt); len(sources) != 0 {
		t.Fatalf("index mode should not emit citation sources, got %+v", sources)
	}
}

func TestChatbotPrompt_NoMemorySearchEscapeHatch(t *testing.T) {
	store := newFakeMemoryStore()
	cb := newChatbotBuilder(store)
	chatterMem := cb.memory.WithUserID(chatterUID)
	prompt := cb.BuildSystemPromptAs(chatterUID, chatterMem, false)

	// memory_search must not appear in the chatbot prompt — it's a) not
	// in the chatbot tool allowlist and b) we explicitly tell the model
	// there is no search tool for chatter memory. If this assertion
	// trips, someone re-added the tool to chatbotBuiltinAllowlist or
	// re-added a memory_search mention to the chatbotInfo template.
	mustNotContain(t, prompt, "memory_search")
}

// Agent mode is the default and must NOT get the chatbot persistence
// scaffolding (those instructions only make sense paired with the
// chatbot tool allowlist + system prompt shape).
func TestAgentMode_NoChatbotPersistenceInstructions(t *testing.T) {
	store := newFakeMemoryStore()
	mem := NewMemoryWithStoreForUser("", store, ownerUID, testAgentID)
	cb := NewContextBuilder("", mem, "")
	cb.store = store
	cb.agentID = testAgentID
	cb.userID = ownerUID
	// promptMode left empty → defaults to agent mode.

	prompt := cb.BuildSystemPromptAs(chatterUID, mem.WithUserID(chatterUID), false)

	mustNotContain(t, prompt, "Remembering things across conversations")
	mustNotContain(t, prompt, "You CAN remember chatters across sessions")
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Errorf("expected prompt to contain %q (prompt length=%d)\n--- prompt head ---\n%s\n--- end ---",
			needle, len(haystack), firstN(haystack, 800))
	}
}

func mustNotContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if strings.Contains(haystack, needle) {
		t.Errorf("expected prompt to NOT contain %q", needle)
	}
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// A chatter with no USER.md / MEMORY.md row of their own must never see
// the agent's on-disk copy: that file is whichever chatter wrote last.
// Each app user runs in their own user space, so chatterUID == cb.userID
// there, which used to open the disk fallback.
func TestChatbotPrompt_NoDiskFallbackForPerChatterFiles(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "USER.md"), []byte("language: Vietnamese (someone else)"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "MEMORY.md"), []byte("someone else's memory"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := newFakeMemoryStore()
	cb := newChatbotBuilder(store)
	cb.home = home
	for _, name := range []string{"USER.md", "MEMORY.md"} {
		if got := cb.loadFileForUser(name, ownerUID); got != "" {
			t.Fatalf("%s: read the shared disk copy: %q", name, got)
		}
	}
	prompt := cb.BuildSystemPromptAs(ownerUID, cb.memory, false)
	if strings.Contains(prompt, "someone else") {
		t.Fatalf("prompt carries another chatter's file:\n%s", prompt)
	}
}
