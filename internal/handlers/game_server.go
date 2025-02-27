// internal/handlers/game_server.go
package handlers

import (
	"context"
	"log"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/game"
	"github.com/jason-s-yu/cambia/internal/models"
)

// GameServer is a high-level struct that holds a reference to a GameStore
// and can create new games from lobbies, etc.
type GameServer struct {
	GameStore *game.GameStore
	Logf      func(f string, v ...interface{})
}

func NewGameServer() *GameServer {
	return &GameServer{
		GameStore: game.NewGameStore(),
		Logf:      log.Printf,
	}
}

// NewCambiaGameFromLobby fetches participants, creates an in-memory CambiaGame, starts it.
func (gs *GameServer) NewCambiaGameFromLobby(ctx context.Context, lobby *models.Lobby) *game.CambiaGame {
	// create a new game
	g := game.NewCambiaGame()

	// copy relevant house rules from the lobby
	g.HouseRules.FreezeOnDisconnect = lobby.HouseRuleFreezeDisconnect
	g.HouseRules.ForfeitOnDisconnect = lobby.HouseRuleForfeitDisconnect
	g.HouseRules.MissedRoundThreshold = lobby.HouseRuleMissedRoundThreshold
	g.HouseRules.PenaltyCardCount = lobby.PenaltyCardCount
	g.HouseRules.AllowDiscardAbilities = lobby.AllowReplacedDiscardAbilities
	g.HouseRules.DisconnectionRoundLimit = lobby.DisconnectionThreshold

	// defaulting to 15
	g.HouseRules.TurnTimeoutSec = 15

	// fetch participants from DB
	participants, err := fetchLobbyParticipants(ctx, lobby.ID)
	if err != nil {
		log.Printf("error fetching participants for lobby %v: %v\n", lobby.ID, err)
	}
	g.Players = participants

	// add game to store
	gs.GameStore.AddGame(g)

	// start the game
	g.Start()
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
