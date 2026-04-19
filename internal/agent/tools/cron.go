package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

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
func RegisterCronTools(r *Registry, st store.Store, userID, agentID, channel, chatID string) {
	r.Register("create_cron_job",
		"Create a scheduled task. The task will run at the specified schedule and send the message to the current channel.",
		map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type":        "string",
					"description": "Task name",
				},
				"schedule": map[string]interface{}{
					"type":        "string",
					"description": "Cron expression like '0 9 * * *', interval like 'every 30m', or one-time ISO datetime",
				},
				"message": map[string]interface{}{
					"type":        "string",
					"description": "The prompt/message to send to the agent when triggered",
				},
				"type": map[string]interface{}{
					"type":        "string",
					"description": "Schedule type: 'cron' (default), 'interval', 'once'",
				},
			},
			"required": []string{"name", "schedule", "message"},
		},
		makeCreateCronJob(st, userID, agentID, channel, chatID),
	)

	r.Register("list_cron_jobs",
		"List all scheduled tasks for this agent.",
		map[string]interface{}{
			"type":       "object",
			"properties": map[string]interface{}{},
		},
		makeListCronJobs(st, userID, agentID),
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
		makeDeleteCronJob(st, userID),
	)
}

func makeCreateCronJob(st store.Store, userID, agentID, channel, chatID string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
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

		id := generateUUID()
		now := time.Now()
		job := &store.CronJobRecord{
			ID:        id,
			
			AgentID:   agentID,
			Name:      args.Name,
			Type:      jobType,
			Schedule:  args.Schedule,
			Message:   args.Message,
			Channel:   channel,
			ChatID:    chatID,
			Timezone:  "UTC",
			Enabled:   true,
			NextRun:   &now,
			CreatedAt: now,
		}

		if err := st.SaveCronJob(ctx, job); err != nil {
			return "", fmt.Errorf("save cron job: %w", err)
		}

		return fmt.Sprintf("Cron job created successfully.\nID: %s\nName: %s\nSchedule: %s\nType: %s", id, args.Name, args.Schedule, jobType), nil
	}
}

func makeListCronJobs(st store.Store, userID, agentID string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		jobs, err := st.ListCronJobs(ctx)
		if err != nil {
			return "", fmt.Errorf("list cron jobs: %w", err)
		}

		var filtered []store.CronJobRecord
		for _, j := range jobs {
			if j.AgentID == agentID {
				filtered = append(filtered, j)
			}
		}

		if len(filtered) == 0 {
			return "No cron jobs found for this agent.", nil
		}

		data, _ := json.MarshalIndent(filtered, "", "  ")
		return string(data), nil
	}
}

func makeDeleteCronJob(st store.Store, userID string) ToolFunc {
	return func(ctx context.Context, rawArgs json.RawMessage) (string, error) {
		var args deleteCronJobArgs
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("parse args: %w", err)
		}
		if args.ID == "" {
			return "", fmt.Errorf("id is required")
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
