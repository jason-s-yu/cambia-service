package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/game"
	"github.com/jason-s-yu/cambia/internal/models"
)

func (s *GameServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/game/create" && r.Method == http.MethodPost {
		s.handleCreateGame(w, r)
		return
	}

	if strings.HasPrefix(r.URL.Path, "/game/reconnect/") {
		s.handleReconnect(w, r)
		return
	}

	// otherwise treat it as a WS join
	gameIDStr := strings.TrimPrefix(r.URL.Path, "/game/")
	gameID, err := uuid.Parse(gameIDStr)
	if err != nil {
		http.Error(w, "invalid game id", http.StatusBadRequest)
		return
	}

	g, ok := s.GameStore.GetGame(gameID)
	if !ok {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"game"},
	})
	if err != nil {
		log.Printf("websocket accept failed: %v", err)
		return
	}
	if c.Subprotocol() != "game" {
		c.Close(websocket.StatusPolicyViolation, "client must speak the game subprotocol")
		return
	}

	// identify user from cookie (ephemeral or permanent)
	userID, _ := EnsureEphemeralUser(w, r) // if no valid token, create ephemeral
	player := &models.Player{
		ID:        userID,
		Hand:      []*models.Card{},
		Connected: true,
		Conn:      c,
		User:      nil, // TODO: check if user needed
	}

	g.AddPlayer(player)

	go s.handleWSMessages(g, player)
}

func (s *GameServer) handleWSMessages(g *game.CambiaGame, p *models.Player) {
	defer func() {
		p.Conn.Close(websocket.StatusNormalClosure, "closing")
		g.HandleDisconnect(p.ID)
	}()

	ctx := context.Background()
	for {
		typ, msg, err := p.Conn.Read(ctx)
		if err != nil {
			log.Printf("read err from user %v: %v", p.ID, err)
			return
		}
		if typ == websocket.MessageText {
			var req map[string]interface{}
			if err := json.Unmarshal(msg, &req); err != nil {
				log.Printf("invalid json from user %v: %v", p.ID, err)
				continue
			}
			// parse "action"
			action, _ := req["action"].(string)
			switch action {
			case "draw":
				// handle draw
			case "disconnect":
				// simulate user closing
				return
			default:
				fmt.Printf("Unknown action %v from user %v\n", action, p.ID)
			}
		}
	}
}

func (s *GameServer) handleReconnect(w http.ResponseWriter, r *http.Request) {
	gameIDStr := strings.TrimPrefix(r.URL.Path, "/game/reconnect/")
	gameID, err := uuid.Parse(gameIDStr)
	if err != nil {
		http.Error(w, "invalid game id", http.StatusBadRequest)
		return
	}
	g, ok := s.GameStore.GetGame(gameID)
	if !ok {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}
	token := extractTokenFromCookie(r.Header.Get("Cookie"))
	userIDStr, err := auth.AuthenticateJWT(token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	userUUID, _ := uuid.Parse(userIDStr)

	g.HandleReconnect(userUUID)
	w.Write([]byte("Reconnected successfully. Now open WebSocket again to continue."))
}

func (s *GameServer) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	// create a new CambiaGame, add to store
	cg := game.NewCambiaGame()
	s.GameStore.AddGame(cg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"game_id": cg.ID,
	})
}
