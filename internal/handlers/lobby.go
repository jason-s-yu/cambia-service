// internal/handlers/lobby.go
package handlers

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/models"
)

// validLobbyTypes enumerates the only allowed lobby type strings
var ValidLobbyTypes = map[string]bool{
	"private":     true,
	"public":      true,
	"matchmaking": true,
}

// GlobalGameServer is the global instance that can be set by InitLobbyHandlers, if desired.
var GlobalGameServer *GameServer

// InitLobbyHandlers sets a global game server for lobby handling.
func InitLobbyHandlers(gs *GameServer) {
	GlobalGameServer = gs
}

// CreateLobbyHandler handles the creation of a new lobby. It also creates a new CambiaGame
// and returns the "game_id" in the response so clients can connect to the game WS.
//
// JSON request structure:
//
//	{
//	  "type": "private",
//	  "circuit_mode": false,
//	  "ranked": false,
//	  "ranking_mode": "1v1",
//	  "rules": {
//	    "disconnection_threshold": 2,
//	    "house_rule_freeze_disconnect": false,
//	    "house_rule_forfeit_disconnect": false,
//	    "house_rule_missed_round_threshold": 2,
//	    "penalty_card_count": 2,
//	    "allow_replaced_discard_abilities": false
//	  }
//	}
//
// Sample response:
//
//	{
//	  "id": "...",
//	  "host_user_id": "...",
//	  "type": "private",
//	  "circuit_mode": false,
//	  "ranked": false,
//	  "ranking_mode": "1v1",
//	  "disconnection_threshold": 2,
//	  ...
//	  "rules": {
//	    "disconnection_threshold": 2,
//	    "house_rule_freeze_disconnect": false,
//	    ...
//	  },
//	  "game_id": "<some-game-id>"
//	}
func CreateLobbyHandler(w http.ResponseWriter, r *http.Request) {
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

	var req struct {
		Type        string `json:"type"`
		CircuitMode bool   `json:"circuit_mode"`
		Ranked      bool   `json:"ranked"`
		RankingMode string `json:"ranking_mode"`

		HouseRules struct {
			DisconnectionThreshold        int  `json:"disconnection_threshold"`
			HouseRuleFreezeDisconnect     bool `json:"house_rule_freeze_disconnect"`
			HouseRuleForfeitDisconnect    bool `json:"house_rule_forfeit_disconnect"`
			HouseRuleMissedRoundThreshold int  `json:"house_rule_missed_round_threshold"`
			PenaltyCardCount              int  `json:"penalty_card_count"`
			AllowReplacedDiscardAbilities bool `json:"allow_replaced_discard_abilities"`

			// newly recognized
			AutoStart      bool `json:"auto_start"`
			TurnTimeoutSec int  `json:"turn_timeout_sec"`
		} `json:"house_rules"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad lobby request payload", http.StatusBadRequest)
		return
	}
	if !ValidLobbyTypes[req.Type] {
		http.Error(w, fmt.Sprintf("invalid lobby type '%s'", req.Type), http.StatusBadRequest)
		return
	}

	lobbyID := uuid.Must(uuid.NewRandom())
	lobby := models.Lobby{
		ID:          lobbyID,
		HostUserID:  userID,
		Type:        req.Type,
		CircuitMode: req.CircuitMode,
		Ranked:      req.Ranked,
		RankingMode: req.RankingMode,
		HouseRules: models.HouseRules{
			DisconnectionRoundLimit: req.HouseRules.DisconnectionThreshold,
			FreezeOnDisconnect:      req.HouseRules.HouseRuleFreezeDisconnect,
			ForfeitOnDisconnect:     req.HouseRules.HouseRuleForfeitDisconnect,
			MissedRoundThreshold:    req.HouseRules.HouseRuleMissedRoundThreshold,
			PenaltyCardCount:        req.HouseRules.PenaltyCardCount,
			AllowDiscardAbilities:   req.HouseRules.AllowReplacedDiscardAbilities,
			AutoStart:               req.HouseRules.AutoStart,
			TurnTimeoutSec:          req.HouseRules.TurnTimeoutSec,
		},
	}

	ctx := r.Context()
	if err := database.InsertLobby(ctx, &lobby); err != nil {
		http.Error(w, fmt.Sprintf("failed to create lobby: %v", err), http.StatusInternalServerError)
		return
	}

	// auto-join the host
	if err := database.InsertParticipant(ctx, lobbyID, userID, 1); err != nil {
		http.Error(w, fmt.Sprintf("failed to insert host participant: %v", err), http.StatusInternalServerError)
		return
	}

	resp := map[string]interface{}{
		"id":           lobby.ID.String(),
		"host_user_id": lobby.HostUserID.String(),
		"type":         lobby.Type,
		"circuit_mode": lobby.CircuitMode,
		"ranked":       lobby.Ranked,
		"ranking_mode": lobby.RankingMode,
		"house_rules": map[string]interface{}{
			"disconnection_threshold":           lobby.HouseRules.DisconnectionRoundLimit,
			"house_rule_freeze_disconnect":      lobby.HouseRules.FreezeOnDisconnect,
			"house_rule_forfeit_disconnect":     lobby.HouseRules.ForfeitOnDisconnect,
			"house_rule_missed_round_threshold": lobby.HouseRules.MissedRoundThreshold,
			"penalty_card_count":                lobby.HouseRules.PenaltyCardCount,
			"allow_replaced_discard_abilities":  lobby.HouseRules.AllowDiscardAbilities,
			"auto_start":                        lobby.HouseRules.AutoStart,
			"turn_timeout_sec":                  lobby.HouseRules.TurnTimeoutSec,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// JoinLobbyHandler handles a request by a user to join an existing lobby.
func JoinLobbyHandler(w http.ResponseWriter, r *http.Request) {
	cookie := r.Header.Get("Cookie")
	if !strings.Contains(cookie, "auth_token=") {
		http.Error(w, "missing auth_token", http.StatusUnauthorized)
		return
	}
	token := extractTokenFromCookie(cookie)

	userIDStr, err := auth.AuthenticateJWT(token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "invalid user ID in token", http.StatusBadRequest)
		return
	}

	var req struct {
		LobbyID    string `json:"lobby_id"`
		SeatNumber int    `json:"seat_number"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid join request payload", http.StatusBadRequest)
		return
	}

	lobbyUUID, err := uuid.Parse(req.LobbyID)
	if err != nil {
		http.Error(w, "invalid lobby_id", http.StatusBadRequest)
		return
	}

	inLobby, err := database.IsUserInLobby(r.Context(), lobbyUUID, userID)
	if err != nil {
		log.Error(err.Error())
		http.Error(w, "database error checking membership", http.StatusInternalServerError)
		return
	}
	if inLobby {
		http.Error(w, "already in that lobby", http.StatusConflict)
		return
	}

	if req.SeatNumber < 1 {
		req.SeatNumber = rand.Intn(1000) + 1
	}

	err = database.InsertParticipant(r.Context(), lobbyUUID, userID, req.SeatNumber)
	if err != nil {
		http.Error(w, "failed to insert participant", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Joined lobby successfully"))
}

// StartLobbyGameHandler is a factory that returns an http.HandlerFunc that starts a game via the given GameServer.
func StartLobbyGameHandler(gs *GameServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie := r.Header.Get("Cookie")
		if !strings.Contains(cookie, "auth_token=") {
			http.Error(w, "missing auth_token", http.StatusUnauthorized)
			return
		}
		token := extractTokenFromCookie(cookie)
		userIDStr, err := auth.AuthenticateJWT(token)
		if err != nil {
			http.Error(w, "invalid token", http.StatusForbidden)
			return
		}
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			http.Error(w, "invalid user ID in token", http.StatusBadRequest)
			return
		}

		var req struct {
			LobbyID string `json:"lobby_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid start request", http.StatusBadRequest)
			return
		}
		lobbyUUID, err := uuid.Parse(req.LobbyID)
		if err != nil {
			http.Error(w, "invalid lobby_id", http.StatusBadRequest)
			return
		}

		lobby, err := database.GetLobby(r.Context(), lobbyUUID)
		if err != nil {
			http.Error(w, "lobby not found", http.StatusNotFound)
			return
		}
		if lobby.HostUserID != userID {
			http.Error(w, "only lobby host can start the game", http.StatusForbidden)
			return
		}

		g := gs.NewCambiaGameFromLobby(r.Context(), lobby)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message":   "Game started",
			"game_id":   g.ID.String(),
			"lobby_id":  lobby.ID.String(),
			"num_users": len(g.Players),
		})
	}
}

// ListLobbiesHandler returns all lobbies in the DB, primarily for debugging or admin usage.
func ListLobbiesHandler(w http.ResponseWriter, r *http.Request) {
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

	lobbies, err := database.GetAllLobbies(r.Context())
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list lobbies: %v", err), http.StatusInternalServerError)
		return
	}

	type lobbyResp struct {
		ID          string                 `json:"id"`
		HostUserID  string                 `json:"host_user_id"`
		Type        string                 `json:"type"`
		CircuitMode bool                   `json:"circuit_mode"`
		Ranked      bool                   `json:"ranked"`
		RankingMode string                 `json:"ranking_mode"`
		HouseRules  map[string]interface{} `json:"house_rules"`
	}

	var resp []lobbyResp
	for _, l := range lobbies {
		rmap := map[string]interface{}{
			"disconnection_threshold":           l.HouseRules.DisconnectionRoundLimit,
			"house_rule_freeze_disconnect":      l.HouseRules.FreezeOnDisconnect,
			"house_rule_forfeit_disconnect":     l.HouseRules.ForfeitOnDisconnect,
			"house_rule_missed_round_threshold": l.HouseRules.MissedRoundThreshold,
			"penalty_card_count":                l.HouseRules.PenaltyCardCount,
			"allow_replaced_discard_abilities":  l.HouseRules.AllowDiscardAbilities,
			"auto_start":                        l.HouseRules.AutoStart,
			"turn_timeout_sec":                  l.HouseRules.TurnTimeoutSec,
		}
		resp = append(resp, lobbyResp{
			ID:          l.ID.String(),
			HostUserID:  l.HostUserID.String(),
			Type:        l.Type,
			CircuitMode: l.CircuitMode,
			Ranked:      l.Ranked,
			RankingMode: l.RankingMode,
			HouseRules:  rmap,
		})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// GetLobbyHandler returns a single lobby by ID, if it exists.
func GetLobbyHandler(w http.ResponseWriter, r *http.Request) {
	cookie := r.Header.Get("Cookie")
	if !strings.Contains(cookie, "auth_token=") {
		http.Error(w, "missing auth_token", http.StatusUnauthorized)
		return
	}
	token := extractCookieToken(cookie, "auth_token")
	if _, err := auth.AuthenticateJWT(token); err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}

	lobbyIDStr := r.URL.Query().Get("lobby_id")
	if lobbyIDStr == "" {
		http.Error(w, "missing lobby_id param", http.StatusBadRequest)
		return
	}
	lid, err := uuid.Parse(lobbyIDStr)
	if err != nil {
		http.Error(w, "invalid lobby_id", http.StatusBadRequest)
		return
	}

	lobby, err := database.GetLobby(r.Context(), lid)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to get lobby: %v", err), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(lobby)
}

// DeleteLobbyHandler removes a lobby from the DB, if the user is the host or an admin.
func DeleteLobbyHandler(w http.ResponseWriter, r *http.Request) {
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
	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "invalid userID in token", http.StatusBadRequest)
		return
	}

	var req struct {
		LobbyID string `json:"lobby_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	lid, err := uuid.Parse(req.LobbyID)
	if err != nil {
		http.Error(w, "invalid lobby_id", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	lobby, err := database.GetLobby(ctx, lid)
	if err != nil {
		http.Error(w, "lobby not found", http.StatusNotFound)
		return
	}

	if lobby.HostUserID != userUUID {
		http.Error(w, "only host user can delete this lobby", http.StatusForbidden)
		return
	}

	err = database.DeleteLobby(ctx, lid)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to delete lobby: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("lobby deleted"))
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
