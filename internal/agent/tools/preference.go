package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type setPreferenceArgs struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// RegisterPreferenceTool registers set_preference — lets the chatter
// configure personal settings (API keys, preferences, etc.) through
// conversation. Writes to user-agent scope so the preference is
// specific to (this chatter, this agent). The chatter's identity is
// resolved at execute time via r.ChatterUserID() + r.AgentID().
//
// The preference is stored under the "prefs" namespace in configs,
// the same namespace as timezone. The scope precedence (system →
// user → agent → user-agent) means a user-agent pref overrides
// agent-level and system defaults.
func RegisterPreferenceTool(r *Registry, st store.Store) {
	r.Register("set_preference",
		"Save a personal preference or API key for the current chatter on this agent. "+
			"Use this when the user wants to configure something that should persist across conversations — "+
			"for example their timezone, language, an API key for image generation, drawing style, etc. "+
			"The preference is scoped to this user + this agent only, not shared with other agents or users.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"key": map[string]interface{}{
					"type":        "string",
					"description": "The preference key, e.g. 'timezone', 'language', 'replicate_api_token', 'drawing_style'. Use snake_case.",
				},
				"value": map[string]interface{}{
					"type":        "string",
					"description": "The preference value to store.",
				},
			},
			"required": []string{"key", "value"},
		},
		makeSetPreference(st, r),
	)
}

func makeSetPreference(st store.Store, r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args setPreferenceArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if args.Key == "" {
			return "", fmt.Errorf("key is required")
		}
		if args.Value == "" {
			return "", fmt.Errorf("value is required")
		}

		chatterUID := r.chatterFromCtx(ctx)
		if chatterUID == "" {
			return "", fmt.Errorf("no chatter identity on this turn — cannot persist preference")
		}
		agentID := r.AgentID()
		if agentID == "" {
			return "", fmt.Errorf("no agent identity — cannot persist preference")
		}

		// Read existing prefs at user-agent scope, merge the new key.
		data := map[string]interface{}{}
		if rec, err := st.GetConfigByName(ctx, store.KindSetting, chatterUID, agentID, scope.PrefsNamespace); err == nil && rec != nil {
			for k, v := range rec.Data {
				data[k] = v
			}
		}
		data[args.Key] = args.Value

		if err := scope.SaveSetting(ctx, st, chatterUID, agentID, scope.PrefsNamespace, data); err != nil {
			return "", fmt.Errorf("save preference: %w", err)
		}
		return fmt.Sprintf("Preference saved: %s = %s (scoped to you on this agent).", args.Key, args.Value), nil
	}
}
