// internal/handlers/game.go
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	// Removed unused auth and game imports for this specific file context
	// "github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/game"
)

// ServeHTTP is a HTTP handler that parses routes to /game/ws and redirects to the appropriate controller.
//
// Deprecated: this function no longer handles the read loop. For WS, see game_ws.go's GameWSHandler.
// The game interaction primarily happens over WebSockets now.
func (s *GameServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// e.g. POST /game/create
	if r.URL.Path == "/game/create" && r.Method == http.MethodPost {
		s.handleCreateGame(w, r)
		return
	}

	// e.g. GET /game/reconnect/{uuid}
	// This HTTP reconnect handler is largely deprecated in favor of the WebSocket handler's reconnect logic.
	// It's kept here perhaps for testing or legacy reasons but doesn't handle full state synchronization.
	if strings.HasPrefix(r.URL.Path, "/game/reconnect/") {
		s.handleReconnect(w, r) // Note: This HTTP handler cannot fully reconnect the WS state.
		return
	}

	// otherwise, if you want WebSocket, see game_ws.go
	http.Error(w, "Unsupported route. Use /game/ws/{id} for game WebSockets.", http.StatusNotFound)
}

// handleCreateGame simply creates a new in-memory CambiaGame for debugging.
// This does not handle player joining or authentication via HTTP.
func (s *GameServer) handleCreateGame(w http.ResponseWriter, r *http.Request) {
	// Authentication should ideally happen here if required for creating games.
	// For simplicity, skipping auth for this debug endpoint.
	cg := game.NewCambiaGame()
	s.GameStore.AddGame(cg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"game_id": cg.ID,
	})
}

// handleReconnect is an example route if you want to reconnect a user by HTTP.
// Deprecated: The WebSocket handler GameWSHandler provides a more robust reconnection flow.
// This HTTP handler only marks the player as logically reconnected in the game state
// but doesn't re-establish the WebSocket or send sync state.
func (s *GameServer) handleReconnect(w http.ResponseWriter, r *http.Request) {
	gameIDStr := strings.TrimPrefix(r.URL.Path, "/game/reconnect/")
	gameID, err := uuid.Parse(gameIDStr)
	if err != nil {
		http.Error(w, "invalid game id", http.StatusBadRequest)
		return
	}
	_, ok := s.GameStore.GetGame(gameID)
	if !ok {
		http.Error(w, "game not found", http.StatusNotFound)
		return
	}

	// Authenticate user via token from cookie
	// Note: Using auth directly here might be needed if middleware isn't applied
	// token := extractTokenFromCookie(r.Header.Get("Cookie")) // Assumes extractTokenFromCookie exists
	// userIDStr, err := auth.AuthenticateJWT(token)
	// if err != nil {
	// 	http.Error(w, "invalid token", http.StatusForbidden)
	// 	return
	// }
	// userUUID, _ := uuid.Parse(userIDStr)

	// --- Authentication logic removed for brevity, assuming userUUID is somehow obtained ---
	// In a real scenario, you'd get userUUID from the authenticated token.
	// var userUUID uuid.UUID // Placeholder - This needs proper auth implementation

	// Mark player as reconnected in game logic, but cannot pass the *websocket.Conn here.
	// The WebSocket handler should be used for actual reconnection.
	// g.HandleReconnect(userUUID) // Removed the call as it requires the conn object which is unavailable here.

	// Acknowledge the HTTP request, instructing the user to use WebSocket.
	w.Write([]byte("Reconnect acknowledged via HTTP. Please establish a WebSocket connection to /game/ws/" + gameIDStr + " to fully rejoin."))
}
