// internal/handlers/game_server.go
package handlers

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/game"
	"github.com/jason-s-yu/cambia/internal/models"
)

// GlobalLobbyManager is needed so we can broadcast the end-of-game results to the right lobby
var GlobalLobbyManager *game.LobbyManager

// GameServer is a high-level struct that holds a reference to a GameStore
// and can create new games from lobbies
type GameServer struct {
	Mutex     sync.Mutex
	GameStore *game.GameStore
	Logf      func(f string, v ...interface{})
}

func NewGameServer() *GameServer {
	return &GameServer{
		GameStore: game.NewGameStore(),
		Logf:      log.Printf,
	}
}

// NewCambiaGameFromLobby fetches participants, creates an in-memory CambiaGame
func (gs *GameServer) NewCambiaGameFromLobby(ctx context.Context, lobby *models.Lobby) *game.CambiaGame {
	g := game.NewCambiaGame()
	g.LobbyID = lobby.ID

	houseRules := models.HouseRules{
		FreezeOnDisconnect:      lobby.HouseRules.FreezeOnDisconnect,
		ForfeitOnDisconnect:     lobby.HouseRules.ForfeitOnDisconnect,
		MissedRoundThreshold:    lobby.HouseRules.MissedRoundThreshold,
		PenaltyCardCount:        lobby.HouseRules.PenaltyCardCount,
		AllowDiscardAbilities:   lobby.HouseRules.AllowDiscardAbilities,
		DisconnectionRoundLimit: lobby.HouseRules.DisconnectionRoundLimit,
		TurnTimeoutSec:          lobby.HouseRules.TurnTimeoutSec,
		AutoStart:               lobby.HouseRules.AutoStart,
	}

	g.HouseRules = houseRules

	participants, err := fetchLobbyParticipants(ctx, lobby.ID)
	if err != nil {
		log.Printf("error fetching participants for lobby %v: %v\n", lobby.ID, err)
	}
	g.Players = participants

	// Set OnGameEnd callback
	g.OnGameEnd = func(lobbyID uuid.UUID, winner uuid.UUID, scores map[uuid.UUID]int) {
		// broadcast to the lobby that the game ended, reset readiness
		ls := GlobalLobbyManager.GetOrCreateLobbyState(lobbyID) // or if we have an existing state
		for uid := range ls.ReadyStates {
			ls.ReadyStates[uid] = false
		}
		resultMsg := map[string]interface{}{
			"type":   "game_results",
			"winner": winner.String(),
			"scores": map[string]int{},
		}
		for pid, sc := range scores {
			resultMsg["scores"].(map[string]int)[pid.String()] = sc
		}
		ls.BroadcastChat(winner, fmt.Sprintf("Game ended, winner is %v", winner))
		ls.BroadcastCustom(resultMsg)
	}

	gs.GameStore.AddGame(g)
	return g
}

// fetchLobbyParticipants from DB
func fetchLobbyParticipants(ctx context.Context, lobbyID uuid.UUID) ([]*models.Player, error) {
	q := `
		SELECT p.user_id, p.seat_position, u.username, u.is_ephemeral
		FROM lobby_participants p
		JOIN users u ON p.user_id = u.id
		WHERE p.lobby_id = $1
		ORDER BY p.seat_position
	`
	rows, err := database.DB.Query(ctx, q, lobbyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var players []*models.Player
	for rows.Next() {
		var userID uuid.UUID
		var seatPos int
		var username string
		var ephemeral bool
		if err := rows.Scan(&userID, &seatPos, &username, &ephemeral); err != nil {
			return nil, err
		}
		p := &models.Player{
			ID:        userID,
			Connected: true,
			Hand:      []*models.Card{},
			User: &models.User{
				ID:          userID,
				Username:    username,
				IsEphemeral: ephemeral,
			},
		}
		players = append(players, p)
	}
	return players, nil
}
