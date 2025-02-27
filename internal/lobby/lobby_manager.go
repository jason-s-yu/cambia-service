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
			AutoStart:   true, // default to auto-start
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
// It also broadcasts a "countdown_started" message to clients.
func (ls *LobbyState) StartCountdown(seconds int) {
	if ls.CountdownTimer != nil {
		// disregard this call if we're already counting down
		return
	}
	ls.BroadcastAll(map[string]interface{}{
		"type":       "countdown_started",
		"seconds":    seconds,
		"auto_start": true,
	})
	ls.CountdownTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		ls.BroadcastAll(map[string]interface{}{
			"type": "countdown_finished",
			"msg":  "All players ready, starting now...",
		})
	})
}

// CancelCountdown stops an active countdown if present.
func (ls *LobbyState) CancelCountdown() {
	if ls.CountdownTimer != nil {
		ls.CountdownTimer.Stop()
		ls.CountdownTimer = nil
		ls.BroadcastAll(map[string]interface{}{
			"type": "countdown_interrupted",
		})
	}
}

// UpdateRules updates local memory rules. For now we only handle "auto_start".
func (ls *LobbyState) UpdateRules(newRules map[string]interface{}) {
	if as, ok := newRules["auto_start"].(bool); ok {
		ls.AutoStart = as
	}
	ls.BroadcastAll(map[string]interface{}{
		"type":      "rule_update_ack",
		"autoStart": ls.AutoStart,
	})
}

// MarkUserReady checks if the user is connected and sets their ready state.
func (ls *LobbyState) MarkUserReady(userID uuid.UUID) {
	if _, ok := ls.Connections[userID]; !ok {
		// user not truly connected
		return
	}
	ls.ReadyStates[userID] = true
	ls.BroadcastReadyState(userID, true)
}

// MarkUserUnready unsets a user's ready state, then cancels the countdown if any.
func (ls *LobbyState) MarkUserUnready(userID uuid.UUID) {
	if _, ok := ls.Connections[userID]; !ok {
		// user not truly connected
		return
	}
	ls.ReadyStates[userID] = false
	ls.BroadcastReadyState(userID, false)
	ls.CancelCountdown()
}

func (ls *LobbyState) BroadcastCustom(msg map[string]interface{}) {
	ls.BroadcastAll(msg)
}

// BroadcastAll sends a JSON object to all connected users' OutChan.
func (ls *LobbyState) BroadcastAll(msg map[string]interface{}) {
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
	ls.BroadcastAll(map[string]interface{}{
		"type":      "lobby_update",
		"user_join": userID.String(),
		"ready_map": ls.ReadyStates, // pass the full readiness map if desired
	})
}

// BroadcastReadyState sends an update that a particular user changed their ready state.
func (ls *LobbyState) BroadcastReadyState(userID uuid.UUID, ready bool) {
	ls.BroadcastAll(map[string]interface{}{
		"type":     "ready_update",
		"user_id":  userID.String(),
		"is_ready": ready,
	})
}

// BroadcastLeave sends a "lobby_update" message indicating a user left.
func (ls *LobbyState) BroadcastLeave(userID uuid.UUID) {
	ls.BroadcastAll(map[string]interface{}{
		"type":      "lobby_update",
		"user_left": userID.String(),
		"ready_map": ls.ReadyStates,
	})
}

// BroadcastChat sends a chat message from a given user.
func (ls *LobbyState) BroadcastChat(userID uuid.UUID, msg string) {
	ls.BroadcastAll(map[string]interface{}{
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
	ls.BroadcastAll(map[string]interface{}{
		"type":      "lobby_update",
		"user_left": userID.String(),
		"ready_map": ls.ReadyStates,
	})
	// Cancel countdown if any
	ls.CancelCountdown()
}
