package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/gorilla/websocket"

	"github.com/fastclaw-ai/fastclaw/internal/config"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// wsFrame is the envelope for all WebSocket messages.
type wsFrame struct {
	Type    string          `json:"type"`              // "req", "res", "event"
	ID      string          `json:"id,omitempty"`      // request/response correlation
	Event   string          `json:"event,omitempty"`   // for type=event
	Method  string          `json:"method,omitempty"`  // for type=req
	Params  json.RawMessage `json:"params,omitempty"`  // for type=req
	OK      *bool           `json:"ok,omitempty"`      // for type=res
	Payload json.RawMessage `json:"payload,omitempty"` // for type=res
	Error   *wsError        `json:"error,omitempty"`   // for type=res
}

type wsError struct {
	Message string `json:"message"`
}

type connectParams struct {
	Auth struct {
		Token string `json:"token"`
	} `json:"auth"`
}

// HandleWebSocket handles WebSocket connections for the OpenClaw protocol.
func (s *Server) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("websocket upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	slog.Info("websocket client connected", "remote", r.RemoteAddr)

	// Send connect challenge
	challenge := wsFrame{
		Type:  "event",
		Event: "connect.challenge",
	}
	if err := conn.WriteJSON(challenge); err != nil {
		slog.Error("websocket write challenge failed", "error", err)
		return
	}

	authenticated := false
	var wsUserID string

	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				slog.Info("websocket client disconnected", "remote", r.RemoteAddr)
			} else {
				slog.Error("websocket read error", "error", err)
			}
			return
		}

		var frame wsFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			slog.Warn("websocket invalid frame", "error", err)
			continue
		}

		if frame.Type != "req" {
			continue
		}

		switch frame.Method {
		case "connect":
			var params connectParams
			if err := json.Unmarshal(frame.Params, &params); err != nil {
				s.wsRespondError(conn, frame.ID, "invalid connect params")
				continue
			}

			userID, err := s.resolveUserID(params.Auth.Token)
			if err != nil {
				// Allow anonymous connect only when no token is configured
				// anywhere (purely local, no auth).
				if s.token == "" && s.registry == nil {
					userID = config.DefaultUserID
				} else {
					s.wsRespondError(conn, frame.ID, "authentication failed")
					continue
				}
			}
			wsUserID = userID
			authenticated = true
			s.wsRespondOK(conn, frame.ID, json.RawMessage(`{}`))

		case "agents.list":
			if !authenticated {
				s.wsRespondError(conn, frame.ID, "not authenticated")
				continue
			}

			space, err := s.resolver.UserSpaceFor(wsUserID)
			if err != nil {
				s.wsRespondError(conn, frame.ID, "user space unavailable: "+err.Error())
				continue
			}
			payload, _ := json.Marshal(map[string]any{"agents": buildAgentList(space)})
			s.wsRespondOK(conn, frame.ID, payload)

		default:
			s.wsRespondError(conn, frame.ID, "unknown method: "+frame.Method)
		}
	}
}

func (s *Server) wsRespondOK(conn *websocket.Conn, id string, payload json.RawMessage) {
	ok := true
	resp := wsFrame{
		Type:    "res",
		ID:      id,
		OK:      &ok,
		Payload: payload,
	}
	if err := conn.WriteJSON(resp); err != nil {
		slog.Error("websocket write error", "error", err)
	}
}

func (s *Server) wsRespondError(conn *websocket.Conn, id string, msg string) {
	ok := false
	resp := wsFrame{
		Type:  "res",
		ID:    id,
		OK:    &ok,
		Error: &wsError{Message: msg},
	}
	if err := conn.WriteJSON(resp); err != nil {
		slog.Error("websocket write error", "error", err)
	}
}
