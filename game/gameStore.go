package game

import (
	"sync"

	"github.com/google/uuid"
)

type GameStore struct {
	mu    sync.Mutex
	games map[uuid.UUID]*Game
}

func NewGameStore() *GameStore {
	return &GameStore{
		games: make(map[uuid.UUID]*Game),
	}
}

func (store *GameStore) AddGame(game *Game) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.games[game.ID] = game
}

func (store *GameStore) GetGame(id uuid.UUID) (*Game, bool) {
	store.mu.Lock()
	defer store.mu.Unlock()
	game, exists := store.games[id]
	return game, exists
}

func (store *GameStore) DeleteGame(id uuid.UUID) {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.games, id)
}
