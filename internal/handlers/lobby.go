// internal/handlers/lobby.go
package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/game"
)

var (
	validGameTypes = map[string]bool{
		"private":     true,
		"public":      true,
		"matchmaking": true,
	}
	validGameModes = map[string]bool{
		"head_to_head": true,
		"group_of_4":   true,
		"circuit_4p":   true,
		"circuit_7p8p": true,
		"custom":       true,
	}
)

// CreateLobbyHandler handles the creation of a new lobby and adds it to the lobby store
func CreateLobbyHandler(gs *GameServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie := r.Header.Get("Cookie")
		if !strings.Contains(cookie, "auth_token=") {
			http.Error(w, "missing auth_token", http.StatusUnauthorized)
			return
		}
		token := extractCookieToken(cookie, "auth_token")

		userIDStr, err := auth.AuthenticateJWT(token)
		if err != nil {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			http.Error(w, "invalid user id format in token", http.StatusBadRequest)
			return
		}

		lobby := game.NewLobbyWithDefaults(userID)

		if err := json.NewDecoder(r.Body).Decode(lobby); err != nil {
			http.Error(w, "bad lobby request payload", http.StatusBadRequest)
			return
		}

		if lobby.Type != "" && !validGameTypes[lobby.Type] {
			http.Error(w, "invalid lobby type", http.StatusBadRequest)
			return
		}

		if lobby.GameMode != "" && !validGameModes[lobby.GameMode] {
			http.Error(w, "invalid game mode", http.StatusBadRequest)
			return
		}

		// add new lobby to instance store
		gs.LobbyStore.AddLobby(lobby)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(lobby)
	}
}

// ListLobbiesHandler returns all lobbies in the DB, primarily for debugging or admin usage.
func ListLobbiesHandler(gs *GameServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie := r.Header.Get("Cookie")
		if !strings.Contains(cookie, "auth_token=") {
			http.Error(w, "missing auth_token", http.StatusUnauthorized)
			return
		}
		token := extractTokenFromCookie(cookie)
		if _, err := auth.AuthenticateJWT(token); err != nil {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}

		lobbies := gs.LobbyStore.GetLobbies()

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(lobbies)
	}
}

// extractTokenFromCookie returns the JWT token from the "auth_token" cookie segment.
func extractTokenFromCookie(cookie string) string {
	parts := strings.Split(cookie, "auth_token=")
	if len(parts) < 2 {
		return ""
	}
	token := parts[1]
	if idx := strings.Index(token, ";"); idx != -1 {
		token = token[:idx]
	}
	return token
}
