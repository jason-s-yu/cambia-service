package game

import (
	"sync"

	"github.com/google/uuid"
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

// GetGameByLobbyID returns a game that references a given lobby ID, or nil if none is found
// This requires that each CambiaGame store a LobbyID.
func (store *GameStore) GetGameByLobbyID(lobbyID uuid.UUID) *CambiaGame {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, g := range store.games {
		if g.LobbyID == lobbyID {
			return g
		}
	}
	return nil
}
