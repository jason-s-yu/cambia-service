// internal/handlers/game_ws.go
package handlers

import (
	"context"
	"encoding/json"
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

		g, ok := gs.GameStore.GetGame(gameID)
		if !ok {
			http.Error(w, "game not found", http.StatusNotFound)
			return
		}

		// if not set, set the broadcast callback
		if g.BroadcastFn == nil {
			g.BroadcastFn = func(ev game.GameEvent) {
				// broadcast to all players
				for _, pl := range g.Players {
					if pl.Conn != nil {
						data, _ := json.Marshal(ev)
						pl.Conn.Write(context.Background(), websocket.MessageText, data)
					}
				}
			}
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

		go readGameMessages(ctx, g, p, logger)
	}
}

// readGameMessages listens for JSON messages from a single player's WS.
// We parse the "type" field and any "payload" to handle "action_*" commands.
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
			Type    string                 `json:"type"`
			Card    map[string]interface{} `json:"card,omitempty"`
			Payload map[string]interface{} `json:"payload,omitempty"`
		}
		if e := json.Unmarshal(data, &msg); e != nil {
			logger.Warnf("invalid json from user %v: %v", p.ID, e)
			continue
		}

		switch msg.Type {
		// "action_snap", "action_draw_stockpile", "action_draw_discard", "action_discard", "action_replace", "action_cambia"
		case "action_snap":
			var act models.GameAction
			act.ActionType = "action_snap"
			if msg.Card != nil {
				idStr, _ := msg.Card["id"].(string)
				act.Payload = map[string]interface{}{
					"id": idStr,
				}
			}
			g.HandlePlayerAction(p.ID, act)

		case "action_draw_stockpile":
			g.HandlePlayerAction(p.ID, models.GameAction{
				ActionType: "action_draw_stockpile",
				Payload:    map[string]interface{}{},
			})

		case "action_draw_discard":
			g.HandlePlayerAction(p.ID, models.GameAction{
				ActionType: "action_draw_discard",
				Payload:    map[string]interface{}{},
			})

		case "action_discard":
			var act models.GameAction
			act.ActionType = "action_discard"
			if msg.Card != nil {
				idStr, _ := msg.Card["id"].(string)
				act.Payload = map[string]interface{}{
					"id": idStr,
				}
			}
			g.HandlePlayerAction(p.ID, act)

		case "action_replace":
			var act models.GameAction
			act.ActionType = "action_replace"
			if msg.Card != nil {
				idStr, _ := msg.Card["id"].(string)
				idxFloat, _ := msg.Card["idx"].(float64)
				act.Payload = map[string]interface{}{
					"id":  idStr,
					"idx": idxFloat,
				}
			}
			g.HandlePlayerAction(p.ID, act)

		case "action_cambia":
			g.HandlePlayerAction(p.ID, models.GameAction{
				ActionType: "action_cambia",
			})

		case "ping":
			_ = p.Conn.Write(ctx, websocket.MessageText, []byte(`{"action":"pong"}`))

		default:
			logger.Warnf("Unknown game action '%s' from user %v", msg.Type, p.ID)
		}
	}
}
