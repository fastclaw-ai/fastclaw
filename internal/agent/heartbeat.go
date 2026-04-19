package agent

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fastclaw-ai/fastclaw/internal/bus"
)

const (
	// DefaultHeartbeatInterval is the default interval between heartbeat checks.
	DefaultHeartbeatInterval = 30 * time.Minute
)

// HeartbeatConfig holds heartbeat configuration.
type HeartbeatConfig struct {
	Interval time.Duration
}

// Heartbeat runs periodic checks and triggers agent actions.
type Heartbeat struct {
	agent    *Agent
	bus      *bus.MessageBus
	interval time.Duration
}

// NewHeartbeat creates a new heartbeat for the given agent.
func NewHeartbeat(ag *Agent, mb *bus.MessageBus, interval time.Duration) *Heartbeat {
	if interval <= 0 {
		interval = DefaultHeartbeatInterval
	}
	return &Heartbeat{
		agent:    ag,
		bus:      mb,
		interval: interval,
	}
}

// Start begins the heartbeat goroutine. It blocks until ctx is cancelled.
func (hb *Heartbeat) Start(ctx context.Context) {
	slog.Info("heartbeat started",
		"agent", hb.agent.Name(),
		"interval", hb.interval,
	)

	ticker := time.NewTicker(hb.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("heartbeat stopped", "agent", hb.agent.Name())
			return
		case <-ticker.C:
			hb.tick(ctx)
		}
	}
}

func (hb *Heartbeat) tick(ctx context.Context) {
	slog.Info("heartbeat tick", "agent", hb.agent.Name())

	// 1. Check HEARTBEAT.md for tasks
	tasks := hb.loadHeartbeatTasks()
	if tasks != "" {
		now := time.Now()
		heartbeatMsg := fmt.Sprintf(
			"[Heartbeat — %s]\nCurrent tasks from HEARTBEAT.md:\n%s\n\nReview these tasks and take action on any that need attention based on the current date/time.",
			now.Format("2006-01-02 15:04:05"),
			tasks,
		)

		// Feed as an inbound message through the bus
		hb.bus.Inbound <- bus.InboundMessage{
			Channel:  "heartbeat",
			ChatID:   "heartbeat_" + hb.agent.Name(),
			UserID:   "system",
			Text:     heartbeatMsg,
			PeerKind: "dm",
		}
	}

	// 2. Trigger memory update
	hb.updateMemory()
}

func (hb *Heartbeat) loadHeartbeatTasks() string {
	path := filepath.Join(hb.agent.home(), "HEARTBEAT.md")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return ""
	}
	return content
}

func (hb *Heartbeat) updateMemory() {
	slog.Info("heartbeat: triggering memory update", "agent", hb.agent.Name())
	hb.agent.memory.ReviewAndUpdateMemory(hb.agent.home())
}
