// internal/game/lobby_manager.go
package game

import (
	"context"
	"time"

	"github.com/google/uuid"
)

type Lobby struct {
	ID         uuid.UUID `json:"id"`
	HostUserID uuid.UUID `json:"hostUserID"`
	Type       string    `json:"type"`     // one of: "private", "public", "matchmaking"; defaults to "private"; private matches are invite or link only
	GameMode   string    `json:"gameMode"` // one of: "head_to_head", "group_of_4", "circuit_4p", "circuit_7p8p", "custom"

	Connections map[uuid.UUID]*LobbyConnection
	ReadyStates map[uuid.UUID]bool

	// GmaeInstaceCreated tracks whether a game instance has been initiated
	GameInstanceCreated bool
	GameID              uuid.UUID

	// InGame indicates whether a game is currently active. If so, we might block further starts.
	InGame bool

	CountdownTimer *time.Timer

	HouseRules    HouseRules    `json:"houseRules"`
	Circuit       Circuit       `json:"circuit"`
	LobbySettings LobbySettings `json:"lobbySettings"`
}

// LobbyConnection wraps a single user's active WebSocket connection for the lobby.
type LobbyConnection struct {
	UserID  uuid.UUID
	Cancel  context.CancelFunc
	OutChan chan map[string]interface{}
}

type HouseRules struct {
	AllowDrawFromDiscardPile bool `json:"allowDrawFromDiscardPile"` // allow players to draw from the discard pile
	AllowReplaceAbilities    bool `json:"allowReplaceAbilities"`    // allow cards discarded from a draw and replace to use their special abilities
	SnapRace                 bool `json:"snapRace"`                 // only allow the first card snapped to succeed; all others get penalized
	ForfeitOnDisconnect      bool `json:"forfeitOnDisconnect"`      // if a player disconnects, forfeit their game; if false, players can rejoin
	PenaltyDrawCount         int  `json:"penaltyDrawCount"`         // num cards to draw on false snap
	AutoKickTurnCount        int  `json:"autoKickTurnCount"`        // number of Cambia rounds to wait before auto-forfeiting a player that is nonresponsive
	TurnTimerSec             int  `json:"turnTimerSec"`             // number of seconds to wait for a player to make a move; default is 15 sec
}

type Circuit struct {
	Enabled bool         `json:"enabled"` // whether to enable Circuit mode
	Mode    string       `json:"mode"`    // one of: "elimination", "max_rounds"
	Rules   CircuitRules `json:"rules"`
}

type CircuitRules struct {
	TargetScore            int  `json:"target_score"`           // the target score for either elimination or first_to_score; players who reach this are either eliminated or win, respectively
	WinBonus               int  `json:"winBonus"`               // constant added to the winner's running score if they win
	FalseCambiaPenalty     int  `json:"falseCambiaPenalty"`     // penalty for a player who calls Cambia but doesn't win
	FreezeUserOnDisconnect bool `json:"freezeUserOnDisconnect"` // if true, freeze the user's score on disconnect and keep them out of the rounds; they can rejoin
}

type LobbySettings struct {
	AutoStart bool `json:"autoStart"` // default true
}

// NewLobby creates a new non-circuit Lobby under the specified host user.
//
// Default game settings (HouseRules) are applied:
//
// - `AllowDrawFromDiscardPile“: `false“
// - `AllowReplaceAbilities`: `false`
// - `SnapRace`: `false`
// - `ForfeitOnDisconnect`: `true`
// - `PenaltyDrawCount`: `1`
// - `AutoKickTurnCount`: `3`
// - `TurnTimerSec`: `15`
//
// Returns a pointer to the lobby
func NewLobbyWithDefaults(hostID uuid.UUID) *Lobby {
	var (
		defaultHouseRules = HouseRules{
			AllowDrawFromDiscardPile: false,
			AllowReplaceAbilities:    false,
			SnapRace:                 false,
			ForfeitOnDisconnect:      true,
			PenaltyDrawCount:         1,
			AutoKickTurnCount:        3,
			TurnTimerSec:             15,
		}
		defaultCircuitSettings = Circuit{Enabled: false}
		defaultLobbySettings   = LobbySettings{AutoStart: true}
	)

	lobbyID, _ := uuid.NewV7()

	return &Lobby{
		ID:            lobbyID,
		HostUserID:    hostID,
		Connections:   make(map[uuid.UUID]*LobbyConnection),
		ReadyStates:   make(map[uuid.UUID]bool),
		HouseRules:    defaultHouseRules,
		Circuit:       defaultCircuitSettings,
		LobbySettings: defaultLobbySettings,
	}
}

// NewLobby creates a new Lobby under the specified host user.
// Returns a pointer to the lobby
func NewCircuitWithDefaults(hostID uuid.UUID) *Lobby {
	var (
		defaultHouseRules = HouseRules{
			AllowDrawFromDiscardPile: true,
			PenaltyDrawCount:         1,
			AllowReplaceAbilities:    true,
			SnapRace:                 true,
			ForfeitOnDisconnect:      true,
			AutoKickTurnCount:        3,
			TurnTimerSec:             15,
		}
		defaultCircuitSettings = Circuit{
			Enabled: true,
			Mode:    "elimination",
			Rules: CircuitRules{
				TargetScore:            100,
				WinBonus:               -1,
				FalseCambiaPenalty:     5,
				FreezeUserOnDisconnect: false, // i.e. user is automatically eliminated
			},
		}
		defaultLobbySettings = LobbySettings{AutoStart: true}
	)

	lobbyID, _ := uuid.NewV7()

	return &Lobby{
		ID:            lobbyID,
		HostUserID:    hostID,
		Connections:   make(map[uuid.UUID]*LobbyConnection),
		ReadyStates:   make(map[uuid.UUID]bool),
		HouseRules:    defaultHouseRules,
		Circuit:       defaultCircuitSettings,
		LobbySettings: defaultLobbySettings,
	}
}

func NewLobbyWithSettings(hostID uuid.UUID, houseRules HouseRules, circuit Circuit, lobbySettings LobbySettings) *Lobby {
	lobbyID, _ := uuid.NewV7()

	return &Lobby{
		ID:            lobbyID,
		HostUserID:    hostID,
		Connections:   make(map[uuid.UUID]*LobbyConnection),
		ReadyStates:   make(map[uuid.UUID]bool),
		HouseRules:    houseRules,
		Circuit:       circuit,
		LobbySettings: lobbySettings,
	}
}

// StartCountdown initiates a countdown if not already counting down, referencing Rules.AutoStart.
//
// seconds is how long the countdown lasts. After it finishes, we call OnCountdownFinish, if set.
func (lobby *Lobby) StartCountdown(seconds int, callback func(uuid.UUID)) bool {
	// If already in a game or countdown is running, do nothing
	if lobby.InGame {
		return false
	}
	if lobby.CountdownTimer != nil {
		return false
	}

	lobby.CountdownTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		callback(lobby.ID)
	})

	return true
}

// CancelCountdown stops an active countdown if present.
func (lobby *Lobby) CancelCountdown() {
	if lobby.CountdownTimer != nil {
		lobby.CountdownTimer.Stop()
		lobby.CountdownTimer = nil
	}
}

// MarkUserReady sets a user's ready state if they're connected.
func (lobby *Lobby) MarkUserReady(userID uuid.UUID) {
	if _, ok := lobby.Connections[userID]; !ok {
		// user not truly connected
		return
	}
	lobby.ReadyStates[userID] = true
	lobby.BroadcastReadyState(userID, true)
}

// MarkUserUnready unsets a user's ready state, then cancels the countdown if any.
func (lobby *Lobby) MarkUserUnready(userID uuid.UUID) {
	if _, ok := lobby.Connections[userID]; !ok {
		// user not truly connected
		return
	}
	lobby.ReadyStates[userID] = false
	lobby.BroadcastReadyState(userID, false)
	lobby.CancelCountdown()
}

// AreAllReady returns true if all known participants are ready.
func (lobby *Lobby) AreAllReady() bool {
	if len(lobby.ReadyStates) == 0 {
		return false
	}
	for _, ready := range lobby.ReadyStates {
		if !ready {
			return false
		}
	}
	return true
}

// BroadcastCustom sends a custom message to all in the lobby
func (lobby *Lobby) BroadcastCustom(msg map[string]interface{}) {
	lobby.BroadcastAll(msg)
}

// BroadcastAll sends a JSON object to all connected users' OutChan.
func (lobby *Lobby) BroadcastAll(msg map[string]interface{}) {
	for _, conn := range lobby.Connections {
		conn.OutChan <- msg
	}
}

// BroadcastJoin sends a "lobby_update" message indicating a user joined.
func (lobby *Lobby) BroadcastJoin(userID uuid.UUID) {
	lobby.BroadcastAll(map[string]interface{}{
		"type":      "lobby_update",
		"user_join": userID.String(),
		"ready_map": lobby.ReadyStates,
	})
}

// BroadcastReadyState sends an update that a particular user changed their ready state.
func (lobby *Lobby) BroadcastReadyState(userID uuid.UUID, ready bool) {
	lobby.BroadcastAll(map[string]interface{}{
		"type":     "ready_update",
		"user_id":  userID.String(),
		"is_ready": ready,
	})
}

// BroadcastLeave sends a "lobby_update" message indicating a user left.
func (lobby *Lobby) BroadcastLeave(userID uuid.UUID) {
	lobby.BroadcastAll(map[string]interface{}{
		"type":      "lobby_update",
		"user_left": userID.String(),
		"ready_map": lobby.ReadyStates,
	})
}

// BroadcastChat sends a chat message from a given user.
func (lobby *Lobby) BroadcastChat(userID uuid.UUID, msg string) {
	lobby.BroadcastAll(map[string]interface{}{
		"type":    "chat",
		"user_id": userID.String(),
		"msg":     msg,
		"ts":      time.Now().Unix(),
	})
}

// RemoveUser removes a user from Connections & ReadyStates (if the user
// unexpectedly disconnects). It's used in readPump's defer if we see an error or close.
func (lobby *Lobby) RemoveUser(userID uuid.UUID) {
	delete(lobby.Connections, userID)
	delete(lobby.ReadyStates, userID)

	lobby.CancelCountdown()
}
