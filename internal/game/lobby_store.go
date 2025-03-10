package game

import (
	"sync"

	"github.com/google/uuid"
)

// LobbyStore manages all active lobbies in memory. Each lobby is tracked
// by a Lobby, keyed by the lobby's UUID.
type LobbyStore struct {
	mu      sync.Mutex
	lobbies map[uuid.UUID]*Lobby
}

// NewLobbyStore creates and returns a new LobbyStore.
func NewLobbyStore() *LobbyStore {
	return &LobbyStore{
		lobbies: make(map[uuid.UUID]*Lobby),
	}
}

// GetLobby retrieves a lobby from the store by its UUID.
func (s *LobbyStore) GetLobby(id uuid.UUID) (*Lobby, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	lobby, exists := s.lobbies[id]
	return lobby, exists
}

func (s *LobbyStore) GetLobbies() map[uuid.UUID]*Lobby {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lobbies
}

// AddLobby adds a new lobby to the store.
func (s *LobbyStore) AddLobby(lobby *Lobby) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lobbies[lobby.ID] = lobby
}

// DeleteLobby removes a lobby from memory if it exists, e.g. if the lobby is closed or deleted.
// This function should be automatically called once the last user leaves a lobby.
func (s *LobbyStore) DeleteLobby(id uuid.UUID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.lobbies, id)
}
