package gateway

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/fastclaw-ai/fastclaw/internal/agent"
	"github.com/fastclaw-ai/fastclaw/internal/bus"
	"github.com/fastclaw-ai/fastclaw/internal/provider"
	"github.com/fastclaw-ai/fastclaw/internal/taskqueue"
)

type weComStreamEmitter func(bus.OutboundMessage) error

type weComStreamRead struct {
	chunk provider.StreamChunk
	ok    bool
}

func pumpWeComStream(
	ctx context.Context,
	sr *provider.StreamReader,
	base bus.OutboundMessage,
	interval time.Duration,
	emit weComStreamEmitter,
) (string, error) {
	if sr == nil {
		return "", errors.New("wecom stream reader is nil")
	}
	if emit == nil {
		return "", errors.New("wecom stream emitter is nil")
	}
	if interval <= 0 {
		interval = 250 * time.Millisecond
	}
	if base.StreamID == "" {
		base.StreamID = uuid.NewString()
	}
	emitState := func(state bus.StreamState, text string) error {
		message := base
		message.StreamState = state
		message.Text = text
		return emit(message)
	}
	if err := emitState(bus.StreamStart, "…"); err != nil {
		return "", err
	}

	reads := make(chan weComStreamRead, 1)
	go func() {
		for {
			chunk, ok := sr.Next()
			select {
			case reads <- weComStreamRead{chunk: chunk, ok: ok}:
			case <-ctx.Done():
				return
			}
			if !ok || chunk.Done {
				return
			}
		}
	}()

	var cumulative string
	dirty := false
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	finish := func(cause error) (string, error) {
		finalText := cumulative
		if cause != nil {
			switch {
			case errors.Is(cause, context.Canceled), errors.Is(cause, context.DeadlineExceeded):
				if finalText != "" {
					finalText += "\n\n_Response interrupted before completion._"
				} else {
					finalText = "The response was interrupted before it completed."
				}
			default:
				if finalText != "" {
					finalText += "\n\n_I couldn't complete the response. Please try again._"
				} else {
					finalText = "I couldn't complete that response. Please try again."
				}
			}
		}
		if err := emitState(bus.StreamFinish, finalText); err != nil {
			if cause != nil {
				return finalText, errors.Join(cause, err)
			}
			return finalText, err
		}
		return finalText, cause
	}

	for {
		select {
		case <-ctx.Done():
			return finish(ctx.Err())
		case <-ticker.C:
			if dirty {
				if err := emitState(bus.StreamUpdate, cumulative); err != nil {
					return finish(err)
				}
				dirty = false
			}
		case next := <-reads:
			if !next.ok {
				return finish(sr.Err())
			}
			if next.chunk.Content != "" {
				cumulative += next.chunk.Content
				dirty = true
			}
			if next.chunk.Done {
				return finish(sr.Err())
			}
		}
	}
}

func (g *Gateway) handleWeComTaskStream(
	ctx context.Context,
	ag *agent.Agent,
	task *taskqueue.Task,
	workspaceBefore map[string]struct{},
	workspaceSnapshotOK bool,
) (string, error) {
	stream := ag.HandleMessageStream(ctx, task.Message)
	base := bus.OutboundMessage{
		Channel: "wecom", AccountID: task.AccountID, AgentID: task.AgentID,
		ChatID: task.Message.ChatID, ReplyToMsgID: task.Message.MessageID,
		ParseMode: "Markdown", StreamID: uuid.NewString(), AllowSplit: ag.SplitReplies(),
	}
	emit := func(out bus.OutboundMessage) error {
		if out.StreamState == bus.StreamFinish {
			text, items := splitMediaFromReply(ctx, g.workspace, task.AgentID, task.Message.ProjectID, task.Message.ChatID, out.Text)
			text, files := splitFilesFromReply(ctx, g.workspace, task.AgentID, task.Message.ProjectID, task.Message.ChatID, text)
			items = append(items, files...)
			if len(items) == 0 {
				items = appendNewWorkspaceMedia(ctx, g.workspace, task.AgentID, task.Message.ProjectID, task.Message.ChatID, workspaceBefore, workspaceSnapshotOK, items)
			}
			out.Text = text
			out.MediaItems = items
		}
		select {
		case g.bus.Outbound <- out:
			return nil
		case <-ctx.Done():
			return fmt.Errorf("enqueue wecom stream event: %w", ctx.Err())
		}
	}
	return pumpWeComStream(ctx, stream, base, 250*time.Millisecond, emit)
}
