package channels

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
)

const (
	weComDefaultWSURL     = "wss://openws.work.weixin.qq.com"
	weComCmdSubscribe     = "aibot_subscribe"
	weComCmdPing          = "ping"
	weComCmdRespond       = "aibot_respond_msg"
	weComCmdSend          = "aibot_send_msg"
	weComCmdUploadInit    = "aibot_upload_media_init"
	weComCmdUploadChunk   = "aibot_upload_media_chunk"
	weComCmdUploadFinish  = "aibot_upload_media_finish"
	weComCmdMsgCallback   = "aibot_msg_callback"
	weComCmdEventCallback = "aibot_event_callback"
)

type weComFrame struct {
	Cmd     string          `json:"cmd,omitempty"`
	Headers weComHeaders    `json:"headers"`
	Body    json.RawMessage `json:"body,omitempty"`
	ErrCode int             `json:"errcode,omitempty"`
	ErrMsg  string          `json:"errmsg,omitempty"`
}

type weComHeaders struct {
	ReqID string `json:"req_id"`
}

type weComSubscribeBody struct {
	BotID  string `json:"bot_id"`
	Secret string `json:"secret"`
}

// WeComProtocolError preserves the concrete upstream code while ensuring the
// associated message has already had the credential removed.
type WeComProtocolError struct {
	Operation string
	ErrCode   int
	ErrMsg    string
}

func (e *WeComProtocolError) Error() string {
	if e == nil {
		return "wecom protocol error"
	}
	return fmt.Sprintf("wecom %s failed: errcode=%d errmsg=%s", e.Operation, e.ErrCode, e.ErrMsg)
}

func newWeComProtocolError(operation string, code int, message, secret string) *WeComProtocolError {
	return &WeComProtocolError{
		Operation: operation,
		ErrCode:   code,
		ErrMsg:    redactWeComValue(message, secret),
	}
}

func redactWeComValue(value, secret string) string {
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[REDACTED]")
	}
	return value
}

func defaultWeComRequestID(prefix string) string {
	return prefix + "-" + uuid.NewString()
}

func marshalWeComBody(body any) (json.RawMessage, error) {
	if body == nil {
		return nil, nil
	}
	data, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return data, nil
}

func authenticateWeComConn(
	ctx context.Context,
	conn *websocket.Conn,
	botID, secret string,
	requestTimeout time.Duration,
	requestID func(string) string,
) error {
	reqID := requestID(weComCmdSubscribe)
	body, err := marshalWeComBody(weComSubscribeBody{BotID: botID, Secret: secret})
	if err != nil {
		return err
	}
	deadline := time.Now().Add(requestTimeout)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set wecom authentication write deadline: %w", err)
	}
	if err := conn.WriteJSON(weComFrame{
		Cmd: weComCmdSubscribe, Headers: weComHeaders{ReqID: reqID}, Body: body,
	}); err != nil {
		return fmt.Errorf("send wecom authentication: %w", err)
	}
	_ = conn.SetWriteDeadline(time.Time{})
	if err := conn.SetReadDeadline(deadline); err != nil {
		return fmt.Errorf("set wecom authentication read deadline: %w", err)
	}
	defer conn.SetReadDeadline(time.Time{})

	for {
		var response weComFrame
		if err := conn.ReadJSON(&response); err != nil {
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				return fmt.Errorf("wecom authentication timed out: %w", context.DeadlineExceeded)
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("read wecom authentication response: %w", err)
		}
		if response.Headers.ReqID != reqID {
			continue
		}
		if response.ErrCode != 0 {
			return newWeComProtocolError("authentication", response.ErrCode, response.ErrMsg, secret)
		}
		return nil
	}
}
