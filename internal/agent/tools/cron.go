package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/cron"
	"github.com/fastclaw-ai/fastclaw/internal/scope"
	"github.com/fastclaw-ai/fastclaw/internal/store"
)

type createCronJobArgs struct {
	Name     string `json:"name"`
	Schedule string `json:"schedule"`
	Message  string `json:"message"`
	Type     string `json:"type"`
}

type deleteCronJobArgs struct {
	ID string `json:"id"`
}

// RegisterCronTools registers cron job management tools.
//
// Channel + chatID for the originating turn are read from the registry
// at execute time via r.MessageChannel() / r.MessageChatID() so a single
// registration at agent construction handles every chat context the
// agent runs in. The agent loop's bindSession stamps the per-turn
// values onto the registry before any tool fires.
func RegisterCronTools(r *Registry, st store.Store, userID, agentID string) {
	r.Register("create_cron_job",
		"Create a scheduled task. Use this for any user request that names a specific time, an interval, or a recurring schedule (e.g. \"5 分钟后提醒\", \"every Monday 9am\", \"each day at 8\"). When the schedule fires, the agent receives `message` as a fresh inbound prompt on the same channel the request originated from. Do NOT write timed reminders into HEARTBEAT.md — that file is only for conditional self-checks reviewed at every heartbeat tick.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Short task name (for listing / debugging).",
				},
				"schedule": map[string]interface{}{
					"type":        "string",
					"description": "When to fire, in the CHATTER'S local timezone (the timezone shown on the 'Current date/time' line of your system prompt) — write '每天早上 9 点' as '0 9 * * *' directly, do NOT convert to UTC. For type='cron': a 5-field cron expression like '0 9 * * *'. For type='interval': a duration like '5m' / '30m' / '2h'. For type='once': an ISO-8601 datetime like '2026-05-02T15:56:52' (no offset = chatter's local time; an explicit offset like '+08:00' or 'Z' is honored as written).",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "The prompt the agent should receive when the schedule fires. Phrase it as instructions to yourself (e.g. \"提醒小m喝水\"), not as a user-facing message — the agent will compose the user reply when it processes the inbound.",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "Schedule type. Use 'once' for one-shot reminders ('5 分钟后…'), 'cron' for calendar-style recurring schedules ('每天 9 点'), or 'interval' for fixed-period polling ('每 30 分钟检查一次'). Defaults to 'cron'.",
					"enum":        []string{"cron", "interval", "once"},
				},
			},
			"required": []string{"name", "schedule", "message"},
		},
		makeCreateCronJob(st, r, userID, agentID),
	)

	r.Register("list_cron_jobs",
		"List all scheduled tasks for this agent.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		makeListCronJobs(st, r, userID, agentID),
	)

	r.Register("delete_cron_job",
		"Delete a scheduled task by ID.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"id": map[string]interface{}{
					"type":        "string",
					"description": "The cron job ID to delete",
				},
			},
			"required": []string{"id"},
		},
		makeDeleteCronJob(st, r, userID, agentID),
	)
}

func makeCreateCronJob(st store.Store, r *Registry, userID, agentID string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		r := r.forContext(ctx)

		var args createCronJobArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if args.Name == "" || args.Schedule == "" || args.Message == "" {
			return "", fmt.Errorf("name, schedule, and message are required")
		}
		jobType := args.Type
		if jobType == "" {
			jobType = "cron"
		}

		// Read the originating bus address at execute time — bindSession
		// stamps it on every turn, so this captures the channel/chatID
		// the user was on when they asked for the reminder.
		channel := r.MessageChannel()
		accountID := r.MessageAccountID()
		chatID := r.MessageChatID()
		// Stamp the chatter that asked for the reminder onto the row so
		// list_cron_jobs can isolate rows per chatter (otherwise two
		// chatters of one public agent would see each other's reminders),
		// and so the fired turn replays under that chatter's identity.
		chatterID := r.ChatterUserID()

		// The chatter's effective timezone governs how the schedule is
		// read: zone-less 'once' datetimes and cron wall-clock fields
		// both mean "their local time" (the same zone the system
		// prompt's date line is rendered in), not the server's. The
		// resolved name is frozen onto the row so the scheduler keeps
		// evaluating recurrences in it even if the chatter later moves.
		tzName := scope.Timezone(ctx, st, r.ChatterUserID(), agentID)
		loc := scope.LoadLocationOrLocal(tzName)

		id := generateUUID()
		now := time.Now()

		// Calculate NextRun based on type
		var nextRun time.Time
		switch jobType {
		case "once":
			t, err := time.Parse(time.RFC3339, args.Schedule)
			if err != nil {
				// No explicit offset — interpret in the chatter's zone.
				t, err = time.ParseInLocation("2006-01-02T15:04:05", args.Schedule, loc)
				if err != nil {
					return "", fmt.Errorf("once schedule must be ISO datetime (e.g. 2026-05-06T15:30:00), got: %q", args.Schedule)
				}
			}
			if t.Before(now) {
				return "", fmt.Errorf("schedule is in the past: %s", args.Schedule)
			}
			nextRun = t
		case "interval":
			sched := strings.TrimPrefix(args.Schedule, "every ")
			dur, err := time.ParseDuration(sched)
			if err != nil {
				return "", fmt.Errorf("invalid interval (e.g. '30m', '1h', 'every 2h'): %q", args.Schedule)
			}
			nextRun = now.Add(dur)
		default:
			// cron expression — first occurrence in the chatter's zone.
			// (Previously nextRun=now, which fired the job once
			// immediately on creation — a spurious reminder.)
			nextRun = cron.NextOccurrenceIn(args.Schedule, now, loc)
		}

		// An API turn whose account registered a scheduler: the job lives
		// in trig and fires to the account's product, which owns the chat
		// (FastClaw can't deliver into it).
		if sc, err := apiScheduler(ctx, st, r, userID); err != nil {
			return "", err
		} else if sc != nil {
			return createTrigJob(ctx, sc, r, agentID, args, jobType, nextRun, tzName, loc)
		} else if channel == "api" {
			// FastClaw can't deliver into an API product's chat; without
			// the product's scheduler the job would fire into nothing.
			return "", fmt.Errorf("scheduling isn't available in this chat: the app hasn't set up scheduled tasks. Tell the person you can't set reminders here yet")
		}

		job := &store.CronJobRecord{
			ID:        id,
			AgentID:   agentID,
			ChatterID: chatterID,
			Name:      args.Name,
			Type:      jobType,
			Schedule:  args.Schedule,
			Message:   args.Message,
			Channel:   channel,
			AccountID: accountID,
			ChatID:    chatID,
			// "" = server-local; the scheduler's LocationOf maps it
			// the same way LoadLocationOrLocal did above, so creation
			// and recurrence agree.
			Timezone:  tzName,
			Enabled:   true,
			NextRun:   &nextRun,
			CreatedAt: now,
		}

		if err := st.SaveCronJob(ctx, job); err != nil {
			return "", fmt.Errorf("save cron job: %w", err)
		}

		// Wake the scheduler to pick up this new job
		cron.NotifyJobCreated()

		// Echo the effective timezone + first fire so the model can
		// confirm the local-time interpretation to the user ("好的，
		// 北京时间每天 9 点") instead of guessing.
		tzShown := tzName
		if tzShown == "" {
			tzShown = loc.String() + " (server default)"
		}
		return fmt.Sprintf("Cron job created successfully.\nID: %s\nName: %s\nSchedule: %s\nType: %s\nTimezone: %s\nFirst fire: %s",
			id, args.Name, args.Schedule, jobType, tzShown, nextRun.In(loc).Format("2006-01-02 15:04:05 -0700")), nil
	}
}

func makeListCronJobs(st store.Store, r *Registry, userID, agentID string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		r := r.forContext(ctx)

		var scheduled []trigJobView
		if sc, err := apiScheduler(ctx, st, r, userID); err != nil {
			return "", err
		} else if sc != nil {
			if scheduled, err = listTrigJobs(ctx, sc, agentID, r.ChatterUserID()); err != nil {
				return "", err
			}
		}

		jobs, err := st.ListCronJobsByAgent(ctx, agentID)
		if err != nil {
			return "", fmt.Errorf("list cron jobs: %w", err)
		}
		// Isolate rows per chatter: a public agent serving multiple senders
		// must not leak one chatter's reminders to another. Rows are only
		// visible to the chatter that created them; legacy rows with an
		// empty chatter_id (written before per-chatter attribution) are
		// hidden from every chatter here — they remain visible to the agent
		// owner via the web dashboard / CLI. This mirrors the OwnerKeyFor
		// tenancy boundary background shells already enforce.
		chatterID := r.ChatterUserID()
		filtered := jobs[:0:0]
		for _, j := range jobs {
			if j.ChatterID != "" && j.ChatterID == chatterID {
				filtered = append(filtered, j)
			}
		}

		if len(filtered) == 0 && len(scheduled) == 0 {
			return "No cron jobs found for this agent.", nil
		}

		var data []byte
		if len(filtered) == 0 {
			data, _ = json.MarshalIndent(scheduled, "", "  ")
		} else if len(scheduled) == 0 {
			data, _ = json.MarshalIndent(filtered, "", "  ")
		} else {
			data, _ = json.MarshalIndent(map[string]any{"jobs": scheduled, "older_jobs": filtered}, "", "  ")
		}
		return string(data), nil
	}
}

func makeDeleteCronJob(st store.Store, r *Registry, userID, agentID string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		r := r.forContext(ctx)

		var args deleteCronJobArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if args.ID == "" {
			return "", fmt.Errorf("id is required")
		}
		if sc, err := apiScheduler(ctx, st, r, userID); err != nil {
			return "", err
		} else if sc != nil && validTrigID(args.ID) {
			deleted, err := sc.Delete(ctx, trigKey(agentID, r.ChatterUserID(), args.ID))
			if err != nil {
				return "", fmt.Errorf("delete cron job: %w", err)
			}
			if deleted {
				return fmt.Sprintf("Cron job %s deleted.", args.ID), nil
			}
		}
		// Enforce the same per-chatter tenancy as list_cron_jobs: a job
		// created by one chatter must not be deletable by another. We
		// load the row first and verify it belongs to the calling chatter
		// (and this agent). A mismatch is reported as "not found" so the
		// error doesn't leak the row's existence to a non-owner.
		job, err := st.GetCronJob(ctx, args.ID)
		if err != nil || job == nil || job.AgentID != agentID ||
			job.ChatterID == "" || job.ChatterID != r.ChatterUserID() {
			return "", fmt.Errorf("cron job %q not found", args.ID)
		}
		if err := st.DeleteCronJob(ctx, args.ID); err != nil {
			return "", fmt.Errorf("delete cron job: %w", err)
		}
		return fmt.Sprintf("Cron job %s deleted.", args.ID), nil
	}
}

func generateUUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
