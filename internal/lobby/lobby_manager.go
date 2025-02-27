// internal/lobby/lobby_manager.go

package lobby

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// LobbyManager manages all active lobbies in memory. Each lobby is tracked
// by a LobbyState, keyed by the lobby's UUID.
type LobbyManager struct {
	mu      sync.Mutex
	lobbies map[uuid.UUID]*LobbyState
}

// LobbyState represents the in-memory state for a single lobby's real-time connections.
type LobbyState struct {
	LobbyID        uuid.UUID
	Connections    map[uuid.UUID]*LobbyConnection // userID -> connection
	ReadyStates    map[uuid.UUID]bool             // is each user ready?
	AutoStart      bool                           // if true, starts a countdown when all are ready
	CountdownTimer *time.Timer                    // reference to active countdown timer, if any
}

// LobbyConnection wraps a single user's active WebSocket connection for the lobby.
type LobbyConnection struct {
	UserID  uuid.UUID
	Cancel  context.CancelFunc // used to kill the read loop if needed
	OutChan chan map[string]interface{}
}

// NewLobbyManager creates and returns a new LobbyManager.
func NewLobbyManager() *LobbyManager {
	return &LobbyManager{
		lobbies: make(map[uuid.UUID]*LobbyState),
	}
}

// GetOrCreateLobbyState returns the LobbyState for the given lobbyID, creating if not present.
func (lm *LobbyManager) GetOrCreateLobbyState(lobbyID uuid.UUID) *LobbyState {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	ls, ok := lm.lobbies[lobbyID]
	if !ok {
		ls = &LobbyState{
			LobbyID:     lobbyID,
			Connections: make(map[uuid.UUID]*LobbyConnection),
			ReadyStates: make(map[uuid.UUID]bool),
			AutoStart:   false,
		}
		lm.lobbies[lobbyID] = ls
	}
	return ls
}

// RemoveLobbyState removes a lobby from memory if it exists, e.g. if the lobby is done.
func (lm *LobbyManager) RemoveLobbyState(lobbyID uuid.UUID) {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	delete(lm.lobbies, lobbyID)
}

// StartCountdown initiates a countdown if AutoStart is true and all players are ready.
func (ls *LobbyState) StartCountdown(seconds int) {
	if ls.CountdownTimer != nil {
		// already counting down
		return
	}
	ls.CountdownTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		ls.broadcast(map[string]interface{}{
			"type": "countdown_finished",
			"msg":  "All players ready, starting now...",
		})
		// TODO: game transition
	})
	ls.broadcast(map[string]interface{}{
		"type":       "countdown_started",
		"seconds":    seconds,
		"auto_start": true,
	})
}

// CancelCountdown stops an active countdown if present.
func (ls *LobbyState) CancelCountdown() {
	if ls.CountdownTimer != nil {
		ls.CountdownTimer.Stop()
		ls.CountdownTimer = nil
		ls.broadcast(map[string]interface{}{
			"type": "countdown_canceled",
		})
	}
}

// broadcast sends a JSON object to all connected users' OutChan.
func (ls *LobbyState) broadcast(msg map[string]interface{}) {
	for _, conn := range ls.Connections {
		conn.OutChan <- msg
	}
}

// AreAllReady returns true if all known participants are ready.
func (ls *LobbyState) AreAllReady() bool {
	if len(ls.ReadyStates) == 0 {
		return false
	}
	for _, ready := range ls.ReadyStates {
		if !ready {
			return false
		}
	}
	return true
}

// BroadcastJoin sends a "lobby_update" message indicating a user joined.
func (ls *LobbyState) BroadcastJoin(userID uuid.UUID) {
	ls.broadcast(map[string]interface{}{
		"type":      "lobby_update",
		"user_join": userID.String(),
		"ready_map": ls.ReadyStates, // pass the full readiness map if desired
	})
}

// BroadcastReadyState sends an update that a particular user changed their ready state.
func (ls *LobbyState) BroadcastReadyState(userID uuid.UUID, ready bool) {
	ls.broadcast(map[string]interface{}{
		"type":     "ready_update",
		"user_id":  userID.String(),
		"is_ready": ready,
	})
}

// BroadcastLeave sends a "lobby_update" message indicating a user left.
func (ls *LobbyState) BroadcastLeave(userID uuid.UUID) {
	ls.broadcast(map[string]interface{}{
		"type":      "lobby_update",
		"user_left": userID.String(),
		"ready_map": ls.ReadyStates,
	})
}

// BroadcastChat sends a chat message from a given user.
func (ls *LobbyState) BroadcastChat(userID uuid.UUID, msg string) {
	ls.broadcast(map[string]interface{}{
		"type":    "chat",
		"user_id": userID.String(),
		"msg":     msg,
		"ts":      time.Now().Unix(),
	})
}

// RemoveUser removes a user from Connections & ReadyStates (if the user
// unexpectedly disconnects). It's used in readPump's defer if we see an error or close.
func (ls *LobbyState) RemoveUser(userID uuid.UUID) {
	delete(ls.Connections, userID)
	delete(ls.ReadyStates, userID)
	ls.broadcast(map[string]interface{}{
		"type":      "lobby_update",
		"user_left": userID.String(),
		"ready_map": ls.ReadyStates,
	})
	// Cancel countdown if any
	ls.CancelCountdown()
}
