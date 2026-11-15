package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/store"
	"github.com/fastclaw-ai/fastclaw/internal/trig"
)

// Cron jobs of API turns, kept in the account's scheduler (trig): when one
// is due, trig posts the payload below to the account's product (it owns
// the chat), which runs a turn in the same session with `message` and
// sends the reply. See docs/scheduler.md.

// MaxScheduledJobsPerChatter caps one chatter's jobs on one agent.
const MaxScheduledJobsPerChatter = 30

// TrigPayload is what trig sends back to the product when the job fires.
type TrigPayload struct {
	V          int    `json:"v"`
	Source     string `json:"source"` // "fastclaw.cron"
	ID         string `json:"id"`
	AgentID    string `json:"agentId"`
	Chatter    string `json:"chatter"`
	SessionKey string `json:"sessionKey"`
	Name       string `json:"name"`
	Message    string `json:"message"`
	Type       string `json:"type"`
	Schedule   string `json:"schedule"`
	Timezone   string `json:"timezone,omitempty"`
}

// trigJobView is a job as list_cron_jobs shows it.
type trigJobView struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Schedule  string `json:"schedule"`
	Timezone  string `json:"timezone,omitempty"`
	Message   string `json:"message"`
	Status    string `json:"status"`
	NextRun   string `json:"nextRun,omitempty"`
	LastFired string `json:"lastFired,omitempty"`
}

// apiScheduler is the trig client of the account behind this API turn, nil
// when the turn isn't an API turn or the account has no scheduler. The
// account is the one paying for the chatter (the API caller that sent the
// end user), else the agent's owner.
func apiScheduler(ctx context.Context, st store.Store, r *Registry, ownerID string) (*trig.Client, error) {
	if r.MessageChannel() != "api" || st == nil {
		return nil, nil
	}
	accounts := []string{}
	if t, err := st.BillingTarget(ctx, r.ChatterUserID()); err == nil && t.AccountID != "" {
		accounts = append(accounts, t.AccountID)
	}
	if ownerID != "" && (len(accounts) == 0 || accounts[0] != ownerID) {
		accounts = append(accounts, ownerID)
	}
	for _, a := range accounts {
		u, key, err := st.GetScheduler(ctx, a)
		if err != nil {
			if errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, fmt.Errorf("read scheduler: %w", err)
		}
		if u != "" && key != "" {
			return trig.New(u, key), nil
		}
	}
	return nil, nil
}

// trigKeyPart keeps a key segment inside trig's key alphabet.
func trigKeyPart(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '.', r == '_', r == ':', r == '@', r == '-':
			return r
		}
		return '_'
	}, s)
}

func trigPrefix(agentID, chatter string) string {
	return "fc/" + trigKeyPart(agentID) + "/" + trigKeyPart(chatter) + "/"
}

func trigKey(agentID, chatter, id string) string { return trigPrefix(agentID, chatter) + id }

func newTrigID() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return "t" + hex.EncodeToString(b)
}

// validTrigID: ids this file hands out ("t" + 12 hex).
func validTrigID(id string) bool {
	if len(id) != 13 || id[0] != 't' {
		return false
	}
	_, err := hex.DecodeString(id[1:])
	return err == nil
}

func trigTimezone(tzName string, loc *time.Location) string {
	if tzName != "" {
		return tzName
	}
	if loc == nil || loc.String() == "Local" || loc.String() == "" {
		return "UTC"
	}
	return loc.String()
}

func createTrigJob(ctx context.Context, sc *trig.Client, r *Registry, agentID string, args createCronJobArgs, jobType string, nextRun time.Time, tzName string, loc *time.Location) (string, error) {
	chatter := r.ChatterUserID()
	sessionKey := r.MessageChatID()
	if sessionKey == "" {
		return "", fmt.Errorf("this conversation has no session to send the reminder to")
	}
	existing, err := sc.List(ctx, trigPrefix(agentID, chatter), MaxScheduledJobsPerChatter+20)
	if err != nil {
		return "", fmt.Errorf("check scheduled jobs: %w", err)
	}
	active := 0
	for _, j := range existing {
		if j.Status != "done" {
			active++
		}
	}
	if active >= MaxScheduledJobsPerChatter {
		return "", fmt.Errorf("this chat already has %d scheduled tasks (the most allowed); delete one with delete_cron_job first", active)
	}

	tz := trigTimezone(tzName, loc)
	var sched trig.Schedule
	switch jobType {
	case "once":
		sched = trig.Schedule{Kind: "at", Expr: nextRun.Format(time.RFC3339)}
	case "interval":
		d, _ := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(args.Schedule, "every ")))
		if d < time.Minute {
			return "", fmt.Errorf("interval must be at least 1m, got %q", args.Schedule)
		}
		sched = trig.Schedule{Kind: "every", Expr: d.String()}
	default:
		sched = trig.Schedule{Kind: "cron", Expr: strings.TrimSpace(args.Schedule), Timezone: tz}
	}

	id := newTrigID()
	job, err := sc.Put(ctx, trigKey(agentID, chatter, id), trig.PutJob{
		Schedule: sched,
		Payload: TrigPayload{
			V: 1, Source: "fastclaw.cron", ID: id,
			AgentID: agentID, Chatter: chatter, SessionKey: sessionKey,
			Name: args.Name, Message: args.Message,
			Type: jobType, Schedule: args.Schedule, Timezone: tz,
		},
		// A fire missed during an outage still comes, once; the product
		// decides whether it's too late to be useful.
		Missed: "once",
	})
	if err != nil {
		var te *trig.Error
		if errors.As(err, &te) && te.Code == "invalid_schedule" {
			return "", fmt.Errorf("invalid schedule %q: %s", args.Schedule, te.Message)
		}
		return "", fmt.Errorf("save cron job: %w", err)
	}
	first := nextRun
	if job.NextRunAt != nil {
		first = *job.NextRunAt
	}
	return fmt.Sprintf("Cron job created successfully.\nID: %s\nName: %s\nSchedule: %s\nType: %s\nTimezone: %s\nFirst fire: %s",
		id, args.Name, args.Schedule, jobType, tz, first.In(loc).Format("2006-01-02 15:04:05 -0700")), nil
}

func listTrigJobs(ctx context.Context, sc *trig.Client, agentID, chatter string) ([]trigJobView, error) {
	jobs, err := sc.List(ctx, trigPrefix(agentID, chatter), 200)
	if err != nil {
		return nil, fmt.Errorf("list cron jobs: %w", err)
	}
	out := make([]trigJobView, 0, len(jobs))
	for _, j := range jobs {
		if j.Status == "done" {
			continue
		}
		var p TrigPayload
		_ = json.Unmarshal(j.Payload, &p)
		v := trigJobView{
			ID: p.ID, Name: p.Name, Type: p.Type, Schedule: p.Schedule,
			Timezone: p.Timezone, Message: p.Message, Status: j.Status,
		}
		if v.ID == "" {
			v.ID = j.Key[strings.LastIndex(j.Key, "/")+1:]
		}
		loc := time.UTC
		if l, err := time.LoadLocation(p.Timezone); err == nil {
			loc = l
		}
		if j.NextRunAt != nil {
			v.NextRun = j.NextRunAt.In(loc).Format("2006-01-02 15:04:05 -0700")
		}
		if j.LastFiredAt != nil {
			v.LastFired = j.LastFiredAt.In(loc).Format("2006-01-02 15:04:05 -0700")
		}
		out = append(out, v)
	}
	return out, nil
}
