package agl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/fastclaw-ai/fastclaw/internal/store"
)

// Runtime is the agents.config "runtime" value for agents whose turns run
// on the owner's computer through agl instead of an LLM.
const Runtime = "agl"

// Access levels a session may run with (agl protocol "Access levels").
const (
	AccessAsk      = "ask"
	AccessEdit     = "edit"
	AccessFull     = "full"
	AccessReadOnly = "read_only"
)

const maxCwdLen = 1024

// RuntimeConfig is agents.config["runtimeConfig"] for runtime "agl".
type RuntimeConfig struct {
	MachineID string `json:"machineId"`
	AglAgent  string `json:"aglAgent"`
	Cwd       string `json:"cwd"`
	Access    string `json:"access"`
}

// ValidCwd reports whether cwd may be sent to agl as a working
// directory ("" = agl's default; "~/" is expanded by agl).
func ValidCwd(cwd string) bool {
	return len(cwd) <= maxCwdLen && !strings.ContainsRune(cwd, 0) && utf8.ValidString(cwd)
}

// RuntimeOf returns an agent config's runtime ("" = the LLM loop).
func RuntimeOf(cfg map[string]interface{}) string {
	s, _ := cfg["runtime"].(string)
	return s
}

// IsAglAgent reports whether rec runs on a linked machine.
func IsAglAgent(rec *store.AgentRecord) bool {
	return rec != nil && RuntimeOf(rec.Config) == Runtime
}

// RuntimeConfigOf reads agents.config["runtimeConfig"].
func RuntimeConfigOf(cfg map[string]interface{}) RuntimeConfig {
	var rc RuntimeConfig
	m, _ := cfg["runtimeConfig"].(map[string]interface{})
	str := func(k string) string { s, _ := m[k].(string); return s }
	rc.MachineID, rc.AglAgent, rc.Cwd, rc.Access = str("machineId"), str("aglAgent"), str("cwd"), str("access")
	if rc.Access == "" {
		rc.Access = AccessAsk
	}
	return rc
}

// Map renders rc for agents.config and API responses.
func (rc RuntimeConfig) Map() map[string]interface{} {
	return map[string]interface{}{
		"machineId": rc.MachineID,
		"aglAgent":  rc.AglAgent,
		"cwd":       rc.Cwd,
		"access":    rc.Access,
	}
}

// ValidationError is a client error in a runtime config.
type ValidationError struct{ msg string }

func (e *ValidationError) Error() string { return e.msg }

func invalid(format string, a ...any) error { return &ValidationError{msg: fmt.Sprintf(format, a...)} }

// ValidateRuntimeConfig normalizes rc and checks it for an agent owned by
// ownerID: the machine must be ownerID's and not revoked, aglAgent
// non-empty (and one of the machine's agents when it reported any),
// access one of ask|edit|full|read_only (default ask). A
// *ValidationError is the caller's fault; anything else is a server error.
func ValidateRuntimeConfig(ctx context.Context, st store.MachineStore, ownerID string, rc *RuntimeConfig) error {
	rc.MachineID = strings.TrimSpace(rc.MachineID)
	rc.AglAgent = strings.TrimSpace(rc.AglAgent)
	rc.Cwd = strings.TrimSpace(rc.Cwd)
	rc.Access = strings.TrimSpace(rc.Access)
	if rc.MachineID == "" {
		return invalid("runtimeConfig.machineId is required")
	}
	if rc.AglAgent == "" {
		return invalid("runtimeConfig.aglAgent is required")
	}
	if len(rc.AglAgent) > 64 {
		return invalid("runtimeConfig.aglAgent is too long")
	}
	if !ValidCwd(rc.Cwd) {
		return invalid("runtimeConfig.cwd is invalid")
	}
	switch rc.Access {
	case "":
		rc.Access = AccessAsk
	case AccessAsk, AccessEdit, AccessFull, AccessReadOnly:
	default:
		return invalid(`runtimeConfig.access must be "ask", "edit", "full" or "read_only"`)
	}
	if st == nil {
		return errors.New("machine store not configured")
	}
	m, err := st.GetMachine(ctx, rc.MachineID)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return invalid("machine not found")
		}
		return err
	}
	if m.UserID != ownerID || m.Revoked() {
		return invalid("machine not found")
	}
	if len(m.Agents) > 0 {
		found := false
		for _, a := range m.Agents {
			if a.ID == rc.AglAgent {
				found = true
				break
			}
		}
		if !found {
			return invalid("agent %q is not available on this machine", rc.AglAgent)
		}
	}
	return nil
}
