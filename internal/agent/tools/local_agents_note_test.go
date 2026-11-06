package tools

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/agl"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestLocalAgentsNoteNamesStrengths(t *testing.T) {
	note := LocalAgentsNote([]agl.LocalMachine{{Name: "mini", Online: true,
		Agents: []store.MachineAgent{{ID: "claude"}, {ID: "codex"}}}})
	t.Log(note)
	for _, want := range []string{"mini (online: claude, codex)", "codex: ", "GENERATES REAL IMAGES", "generate_image for Codex", "generate_image for a picture", "claude: ", "can't generate images", "Never tell the person you can't"} {
		if !strings.Contains(note, want) {
			t.Errorf("note lacks %q", want)
		}
	}
}
