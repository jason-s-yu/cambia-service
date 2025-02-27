// internal/game/lobby_manager.go
package game

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/models"
)

// OnLobbyCountdownFinishFunc defines the signature of a callback function
// that is invoked when the countdown finishes in a LobbyState.
//
// It passes the lobby's UUID so the callback can perform game creation or
// other logic without a circular import of the handlers or game packages.
type OnLobbyCountdownFinishFunc func(lobbyID uuid.UUID)

// LobbyManager manages all active lobbies in memory. Each lobby is tracked
// by a LobbyState, keyed by the lobby's UUID.
type LobbyManager struct {
	mu      sync.Mutex
	lobbies map[uuid.UUID]*LobbyState
}

// LobbyState represents the in-memory state for a single lobby's real-time connections
// plus a local copy of HouseRules for auto_start, turn_timeout, etc.
type LobbyState struct {
	LobbyID uuid.UUID

	Connections map[uuid.UUID]*LobbyConnection
	ReadyStates map[uuid.UUID]bool

	// We store an in-memory copy of HouseRules. The server can override them from "rule_update".
	Rules models.HouseRules

	// InGame indicates whether a game is currently active. If so, we might block further starts.
	InGame bool

	// OnCountdownFinish is the callback that is invoked when the countdown ends.
	OnCountdownFinish OnLobbyCountdownFinishFunc

	CountdownTimer *time.Timer
}

// LobbyConnection wraps a single user's active WebSocket connection for the lobby.
type LobbyConnection struct {
	UserID  uuid.UUID
	Cancel  context.CancelFunc
	OutChan chan map[string]interface{}
}

// NewLobbyManager creates and returns a new LobbyManager.
func NewLobbyManager() *LobbyManager {
	return &LobbyManager{
		lobbies: make(map[uuid.UUID]*LobbyState),
	}
}

// GetOrCreateLobbyState returns the LobbyState for the given lobbyID, creating if not present.
//
// This method sets default HouseRules if a new state is created, but OnCountdownFinish
// remains nil until you set it, if you want automatic game creation.
func (lm *LobbyManager) GetOrCreateLobbyState(lobbyID uuid.UUID) *LobbyState {
	lm.mu.Lock()
	defer lm.mu.Unlock()
	ls, ok := lm.lobbies[lobbyID]
	if !ok {
		ls = &LobbyState{
			LobbyID:     lobbyID,
			Connections: make(map[uuid.UUID]*LobbyConnection),
			ReadyStates: make(map[uuid.UUID]bool),
			Rules: models.HouseRules{
				AutoStart:      true,
				TurnTimeoutSec: 15,
			},
			InGame: false,
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

// StartCountdown initiates a countdown if not already counting down, referencing Rules.AutoStart.
//
// seconds is how long the countdown lasts. After it finishes, we call OnCountdownFinish, if set.
func (ls *LobbyState) StartCountdown(seconds int) {
	// If already in a game or countdown is running, do nothing
	if ls.InGame {
		return
	}
	if ls.CountdownTimer != nil {
		return
	}

	ls.BroadcastAll(map[string]interface{}{
		"type":         "countdown_started",
		"seconds":      seconds,
		"auto_start":   ls.Rules.AutoStart,
		"turn_timeout": ls.Rules.TurnTimeoutSec,
	})

	ls.CountdownTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		ls.CountdownTimer = nil

		ls.BroadcastAll(map[string]interface{}{
			"type": "countdown_finished",
			"msg":  "All players ready, starting now...",
		})

		// If OnCountdownFinish is set, call it.
		if ls.OnCountdownFinish != nil && !ls.InGame {
			ls.InGame = true // mark InGame to avoid double starts
			ls.OnCountdownFinish(ls.LobbyID)
		}
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

// UpdateRules updates local memory rules. For now we handle auto_start, turn_timeout_sec, etc.
func (ls *LobbyState) UpdateRules(newRules map[string]interface{}) {
	if as, ok := newRules["auto_start"].(bool); ok {
		ls.Rules.AutoStart = as
	}
	if tts, ok := newRules["turn_timeout_sec"].(float64); ok {
		ls.Rules.TurnTimeoutSec = int(tts)
	}
	ls.BroadcastAll(map[string]interface{}{
		"type":             "rule_update_ack",
		"auto_start":       ls.Rules.AutoStart,
		"turn_timeout_sec": ls.Rules.TurnTimeoutSec,
	})
}

// MarkUserReady sets a user's ready state if they're connected.
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

// BroadcastCustom sends a custom message to all in the lobby
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
		"ready_map": ls.ReadyStates,
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
	ls.CancelCountdown()
}
