// internal/handlers/game_server.go
package handlers

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/game"
	"github.com/jason-s-yu/cambia/internal/models"
)

// GameServer holds an in-memory LobbyStore and GameStore, plus logic to create new games.
type GameServer struct {
	Mutex      sync.Mutex
	LobbyStore *game.LobbyStore
	GameStore  *game.GameStore
}

// NewGameServer sets up ephemeral in-memory stores for lobbies and games.
func NewGameServer() *GameServer {
	return &GameServer{
		LobbyStore: game.NewLobbyStore(),
		GameStore:  game.NewGameStore(),
	}
}

// NewCambiaGameFromLobby in ephemeral mode: builds a new CambiaGame from the in-memory lobby participants.
func (gs *GameServer) NewCambiaGameFromLobby(ctx context.Context, lobby *game.Lobby) *game.CambiaGame {
	g := game.NewCambiaGame()
	g.LobbyID = lobby.ID
	g.HouseRules = lobby.HouseRules

	// Gather ephemeral participants from the lobby's connections.
	var players []*models.Player
	for userID := range lobby.Connections {
		players = append(players, &models.Player{
			ID:        userID,
			Connected: true,
			Hand:      []*models.Card{},
		})
	}
	g.Players = players

	// Attach OnGameEnd
	g.OnGameEnd = func(lobbyID uuid.UUID, winner uuid.UUID, scores map[uuid.UUID]int) {
		if ls, exists := gs.LobbyStore.GetLobby(lobbyID); exists {
			for uid := range ls.Connections {
				ls.ReadyStates[uid] = false
			}
			ls.InGame = false
		}
		resultMsg := map[string]interface{}{
			"type":   "game_results",
			"winner": winner.String(),
			"scores": map[string]int{},
		}
		for pid, sc := range scores {
			resultMsg["scores"].(map[string]int)[pid.String()] = sc
		}
		lobby.BroadcastChat(winner, fmt.Sprintf("Game ended, winner is %v", winner))
		lobby.BroadcastAll(resultMsg)
	}

	// Store game, start it
	gs.GameStore.AddGame(g)
	g.Start()
	return g
}
