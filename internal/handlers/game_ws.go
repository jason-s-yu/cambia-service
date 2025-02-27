// internal/handlers/game_ws.go
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/game"
	"github.com/jason-s-yu/cambia/internal/models"
	"github.com/sirupsen/logrus"
)

// GameWSHandler sets up the WebSocket at /game/ws/{game_id}, subprotocol "game".
func GameWSHandler(logger *logrus.Logger, gs *GameServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// parse game_id from path
		pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/game/ws/"), "/")
		if len(pathParts) < 1 {
			http.Error(w, "missing game_id", http.StatusBadRequest)
			return
		}
		gameIDStr := pathParts[0]
		gameID, err := uuid.Parse(gameIDStr)
		if err != nil {
			http.Error(w, "invalid game_id", http.StatusBadRequest)
			return
		}

		// attempt to get the in-memory game
		g, ok := gs.GameStore.GetGame(gameID)
		if !ok {
			http.Error(w, "game not found", http.StatusNotFound)
			return
		}

		// upgrade to websocket with subprotocol=game
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"game"},
		})
		if err != nil {
			logger.Warnf("websocket accept error: %v", err)
			return
		}
		if c.Subprotocol() != "game" {
			c.Close(websocket.StatusPolicyViolation, "client must speak the game subprotocol")
			return
		}

		// authenticate user by cookie
		cookieToken := extractCookieToken(r.Header.Get("Cookie"), "auth_token")
		userIDStr, err := auth.AuthenticateJWT(cookieToken)
		if err != nil {
			logger.Warnf("invalid token: %v", err)
			c.Close(websocket.StatusPolicyViolation, "invalid auth_token")
			return
		}
		userUUID, err := uuid.Parse(userIDStr)
		if err != nil {
			logger.Warnf("invalid user UUID parse: %v", err)
			c.Close(websocket.StatusPolicyViolation, "invalid user ID")
			return
		}

		// attach player
		p := &models.Player{
			ID:        userUUID,
			Hand:      []*models.Card{},
			Connected: true,
			Conn:      c,
		}
		g.AddPlayer(p)
		logger.Infof("User %v joined game %v via WS", userUUID, gameID)

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// read loop
		go readGameMessages(ctx, g, p, logger)

		// TODO: writePump to push events to the client, but for now we might do direct writes in readGameMessages if needed.
	}
}

func readGameMessages(ctx context.Context, g *game.CambiaGame, p *models.Player, logger *logrus.Logger) {
	defer func() {
		p.Conn.Close(websocket.StatusNormalClosure, "closing")
		g.HandleDisconnect(p.ID)
	}()

	for {
		typ, data, err := p.Conn.Read(ctx)
		if err != nil {
			logger.Infof("user %v read err: %v", p.ID, err)
			return
		}
		if typ != websocket.MessageText {
			continue
		}

		var msg struct {
			Action  string                 `json:"action"`
			Payload map[string]interface{} `json:"payload"`
		}
		if e := json.Unmarshal(data, &msg); e != nil {
			logger.Warnf("invalid json from user %v: %v", p.ID, e)
			continue
		}

		switch msg.Action {
		case "draw", "discard", "snap", "cambia":
			// handle the game logic
			action := models.GameAction{
				ActionType: msg.Action,
				Payload:    msg.Payload,
			}
			g.HandlePlayerAction(p.ID, action)
		case "ping":
			_ = p.Conn.Write(ctx, websocket.MessageText, []byte(`{"action":"pong"}`))
		default:
			fmt.Printf("Unknown game action '%s' from user %v\n", msg.Action, p.ID)
		}
	}
}
