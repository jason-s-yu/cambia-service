// internal/game/lobby_store.go
package game

import (
	"sync"

	"github.com/google/uuid"
)

// LobbyStore manages ephemeral lobbies in memory only.
type LobbyStore struct {
	mu      sync.Mutex
	lobbies map[uuid.UUID]*Lobby
}

// NewLobbyStore returns an in-memory store for Lobbies.
func NewLobbyStore() *LobbyStore {
	return &LobbyStore{
		lobbies: make(map[uuid.UUID]*Lobby),
	}
}

// AddLobby stores the lobby in memory. Typically you also define OnEmpty so that the lobby can remove itself.
func (s *LobbyStore) AddLobby(lobby *Lobby) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lobbies[lobby.ID] = lobby
}

// DeleteLobby removes the ephemeral lobby from memory.
func (s *LobbyStore) DeleteLobby(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lobbies, id)
}

// GetLobby retrieves a lobby if it exists.
func (s *LobbyStore) GetLobby(id uuid.UUID) (*Lobby, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	l, ok := s.lobbies[id]
	return l, ok
}

// GetLobbies returns the entire map, typically for debugging or listing.
func (s *LobbyStore) GetLobbies() map[uuid.UUID]*Lobby {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lobbies
}
