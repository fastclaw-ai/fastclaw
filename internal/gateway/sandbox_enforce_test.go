package gateway

import (
	"testing"

	"github.com/fastclaw-ai/fastclaw/internal/buildinfo"
	"github.com/fastclaw-ai/fastclaw/internal/config"
)

// The admin "force sandbox" toggle must flip enforcement when the pool is
// (re)built, and only while the sandbox itself is enabled.
func TestBuildSystemSandboxPoolPublishesEnforce(t *testing.T) {
	t.Setenv("FASTCLAW_DEPLOY", "")
	t.Setenv("FASTCLAW_SANDBOX_ENFORCE", "")
	t.Cleanup(func() { buildinfo.SetSandboxEnforceSetting(false) })

	buildSystemSandboxPool(config.SandboxCfg{Enabled: false, Enforce: true}, nil)
	if buildinfo.IsSandboxEnforced() {
		t.Fatal("enforce with sandbox disabled must not enforce")
	}

	p := buildSystemSandboxPool(config.SandboxCfg{Enabled: true, Enforce: true, Backend: "docker"}, nil)
	if p != nil {
		defer p.CloseAll()
	}
	if !buildinfo.IsSandboxEnforced() {
		t.Fatal("enabled+enforce should enforce")
	}

	p2 := buildSystemSandboxPool(config.SandboxCfg{Enabled: true, Backend: "docker"}, nil)
	if p2 != nil {
		defer p2.CloseAll()
	}
	if buildinfo.IsSandboxEnforced() {
		t.Fatal("turning enforce off should take effect on rebuild")
	}
}
