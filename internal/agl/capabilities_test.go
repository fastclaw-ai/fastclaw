package agl

import (
	"strings"
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

func TestCapabilitiesReportedOrAssumed(t *testing.T) {
	codex := store.MachineAgent{ID: "codex"}
	if !(LocalMachine{AglVersion: "0.3.5"}).HasCapability(codex, CapImageGeneration) {
		t.Error("agl 0.3.5: codex image generation should be assumed")
	}
	if (LocalMachine{AglVersion: "0.3.6"}).HasCapability(codex, CapImageGeneration) {
		t.Error("agl 0.3.6 reported none: must not be assumed")
	}
	codex.Capabilities = []string{CapImageGeneration}
	if !(LocalMachine{AglVersion: "0.3.6"}).HasCapability(codex, CapImageGeneration) {
		t.Error("reported capability ignored")
	}
}

func TestAgentProfile(t *testing.T) {
	m := LocalMachine{AglVersion: "0.3.6"}
	on := AgentProfile(m, store.MachineAgent{ID: "codex", Capabilities: []string{CapImageGeneration, CapBrowser}})
	for _, want := range []string{"code, files and shell", "GENERATES REAL IMAGES", "generate_image", "web browser"} {
		if !strings.Contains(on, want) {
			t.Errorf("profile lacks %q: %s", want, on)
		}
	}
	off := AgentProfile(m, store.MachineAgent{ID: "codex"})
	if strings.Contains(off, "GENERATES REAL IMAGES") || !strings.Contains(off, "turned off") {
		t.Errorf("codex without image generation: %s", off)
	}
}

func TestMachineAgentsKeepCapabilities(t *testing.T) {
	got := machineAgents([]agentInfo{{ID: "codex", Name: "Codex", Available: true, Capabilities: []string{"image_generation", "browser"}}})
	if len(got) != 1 || strings.Join(got[0].Capabilities, ",") != "image_generation,browser" {
		t.Fatalf("%+v", got)
	}
}
