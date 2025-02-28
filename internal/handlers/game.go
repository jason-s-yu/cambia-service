// internal/handlers/game.go
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/game"
)

// ServeHTTP is a HTTP handler that parses routes to /game/ws and redirects to the appropriate controller.
//
// Deprecated: this function no longer handles the read loop. For WS, see game_ws.go's GameWSHandler.
func (s *GameServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// e.g. POST /game/create
	if r.URL.Path == "/game/create" && r.Method == http.MethodPost {
		s.handleCreateGame(w, r)
		return
	}

	// e.g. GET /game/reconnect/{uuid}
	if strings.HasPrefix(r.URL.Path, "/game/reconnect/") {
		s.handleReconnect(w, r)
		return
	}

	// otherwise, if you want WebSocket, see game_ws.go
	http.Error(w, "unsupported route, use /game/ws/{id} for websockets", http.StatusNotFound)
}

// handleCreateGame simply creates a new in-memory CambiaGame for debugging.
func (s *GameServer) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	cg := game.NewCambiaGame()
	s.GameStore.AddGame(cg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"game_id": cg.ID,
	})
}

// handleReconnect is an example route if you want to reconnect a user by HTTP, but the WS approach is recommended.
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
