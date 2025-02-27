package game

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/models"
)

type GameStore struct {
	mu    sync.Mutex
	games map[uuid.UUID]*CambiaGame
}

func NewGameStore() *GameStore {
	return &GameStore{
		games: make(map[uuid.UUID]*CambiaGame),
	}
}

func (s *GameStore) AddGame(game *CambiaGame) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.games[game.ID] = game
}

func (s *GameStore) GetGame(id uuid.UUID) (*CambiaGame, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	g, exists := s.games[id]
	return g, exists
}

func (s *GameStore) DeleteGame(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.games, id)
}

// NewCambiaGameFromLobby creates the in-memory game from the lobby participants, copying house rules, etc.
func (s *GameStore) NewCambiaGameFromLobby(lobby *models.Lobby, ctx context.Context) *CambiaGame {
	s.mu.Lock()
	defer s.mu.Unlock()

	g := NewCambiaGame()
	g.HouseRules = models.HouseRules{
		FreezeOnDisconnect:      lobby.HouseRuleFreezeDisconnect,
		ForfeitOnDisconnect:     lobby.HouseRuleForfeitDisconnect,
		MissedRoundThreshold:    lobby.HouseRuleMissedRoundThreshold,
		PenaltyCardCount:        lobby.PenaltyCardCount,
		AllowDiscardAbilities:   lobby.AllowReplacedDiscardAbilities,
		DisconnectionRoundLimit: lobby.DisconnectionThreshold,
	}

	// TODO: mode based turn timer, for now we rely on HouseRules
	players, err := fetchParticipants(ctx, lobby.ID)
	if err != nil {
		log.Printf("NewCambiaGameFromLobby error: %v", err)
	} else {
		g.Players = players
		for _, p := range players {
			g.lastSeen[p.ID] = time.Now()
		}
	}

	s.games[g.ID] = g
	g.Start()
	return g
}
