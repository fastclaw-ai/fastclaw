package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type setTimezoneArgs struct {
	Timezone string `json:"timezone"`
}

// RegisterTimezoneTool registers set_timezone. The tool persists the
// timezone in TWO places:
//
//  1. USER.md — the chatter profile loaded into every system prompt.
//     This is the primary path: chatterLocation() reads USER.md first,
//     so the date line shows the correct timezone on every new session
//     without depending on a database query.
//  2. scope prefs (database) — used by cron scheduling so jobs fire at
//     the chatter's local time. This is the secondary path.
//
// Writing to both guarantees the timezone survives across sessions and
// is visible to the model in the system prompt.
func RegisterTimezoneTool(r *Registry, st store.Store) {
	r.Register("set_timezone",
		"Record the current chatter's timezone. Call this whenever the chatter tells you their timezone, city, or country (e.g. \"我在北京\" → Asia/Shanghai). This persists the timezone to the chatter's profile so future sessions use their local time automatically.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"timezone": map[string]interface{}{
					"type":        "string",
					"description": "IANA timezone name like 'Asia/Shanghai', 'Europe/Berlin', 'America/New_York'. Derive it from the city/country if the chatter didn't name a zone directly.",
				},
			},
			"required": []string{"timezone"},
		},
		makeSetTimezone(st, r),
	)
}

func makeSetTimezone(st store.Store, r *Registry) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args setTimezoneArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if args.Timezone == "" {
			return "", fmt.Errorf("timezone is required")
		}
		if args.Timezone == "Local" {
			return "", fmt.Errorf("timezone must be a concrete IANA name like 'Asia/Shanghai', not 'Local'")
		}
		loc, err := time.LoadLocation(args.Timezone)
		if err != nil {
			return "", fmt.Errorf("unknown timezone %q — use an IANA name like 'Asia/Shanghai': %w", args.Timezone, err)
		}
		chatterUID := r.chatterFromCtx(ctx)
		if chatterUID == "" {
			return "", fmt.Errorf("no chatter identity on this turn — cannot persist timezone")
		}

		// 1. Write to USER.md — primary persistence path.
		// chatterLocation() reads USER.md first, so this guarantees the
		// date line shows the correct timezone on every future session.
		if r.systemFileStore != nil {
			userMDUID := r.systemFileUserID(ctx, "USER.md")
			upsertUserMDTimezone(ctx, r, userMDUID, args.Timezone)
		}

		// 2. Write to scope prefs (database) — secondary path for cron.
		if st != nil {
			_ = scope.SaveUserTimezone(ctx, st, chatterUID, args.Timezone)
		}

		return fmt.Sprintf("Timezone saved: %s. The chatter's local time is now %s.",
			args.Timezone, time.Now().In(loc).Format("2006-01-02 15:04:05 -0700 (Monday)")), nil
	}
}

// upsertUserMDTimezone reads the current USER.md, adds or updates a
// "Timezone: <tz>" line, and writes it back. If USER.md doesn't exist
// yet, it creates a minimal one with just the timezone.
func upsertUserMDTimezone(ctx context.Context, r *Registry, userID, tz string) {
	const filename = "USER.md"
	const tzPrefix = "- Timezone: "

	content := ""
	if data, err := r.readSystemFileForUser(ctx, userID, filename); err == nil {
		content = strings.TrimSpace(string(data))
	}

	// Check if there's already a Timezone line and update it.
	if idx := strings.Index(content, tzPrefix); idx >= 0 {
		// Find end of the line.
		end := strings.Index(content[idx:], "\n")
		if end < 0 {
			end = len(content) - idx
		}
		content = content[:idx] + tzPrefix + tz + content[idx+end:]
	} else if content != "" {
		// Append to existing content.
		content = content + "\n" + tzPrefix + tz
	} else {
		// Create new USER.md with timezone.
		content = "# Current Chatter\n" + tzPrefix + tz
	}

	_ = r.systemFileStore.SaveWorkspaceFile(ctx, r.agentID, userID, filename, []byte(content))
}
