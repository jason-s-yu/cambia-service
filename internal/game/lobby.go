// internal/game/lobby_manager.go
package game

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type Lobby struct {
	ID         uuid.UUID `json:"id"`
	HostUserID uuid.UUID `json:"hostUserID"`
	Type       string    `json:"type"`     // one of: "private", "public", "matchmaking"; defaults to "private"; private matches are invite or link only
	GameMode   string    `json:"gameMode"` // one of: "head_to_head", "group_of_4", "circuit_4p", "circuit_7p8p", "custom"

	Users map[uuid.UUID]bool // false if user is not in the lobby

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
	IsHost  bool
}

// Write will push a message to the user's message channel.
func (conn *LobbyConnection) Write(msg map[string]interface{}) {
	conn.OutChan <- msg
}

// WriteError will push an error message to the user's message channel.
// The structure is as follows:
//
//	{
//	 "type": "error",
//	 "message": msg
//	}
func (conn *LobbyConnection) WriteError(msg string) {
	conn.OutChan <- map[string]interface{}{
		"type":    "error",
		"message": msg,
	}
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
// Additionally, `autoStart` is enabled by default.
//
// Note that the Lobby struct contains a map of Connection pools in order to communicate
// with connected users via websockets. If you are using the Game/Lobby API internally or
// under your own implementation, you can leave the connections map empty and manage
// your communications on your own.
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

// InviteUser grants "permission" to a user to join this lobby. This only has an effect if the Type is "private".
func (lobby *Lobby) InviteUser(userID uuid.UUID) {
	lobby.Users[userID] = false
}

// AddConnection registers a user's connection to the lobby and sets their ready status.
// This is effectively a "join lobby" operation.
func (lobby *Lobby) AddConnection(userID uuid.UUID, conn *LobbyConnection) error {
	if lobby.Type == "private" {
		if _, ok := lobby.Users[userID]; !ok {
			// user not invited
			return fmt.Errorf("user %s not invited to the private lobby", userID)
		}
	}

	lobby.Users[userID] = true
	lobby.Connections[userID] = conn
	lobby.ReadyStates[userID] = false

	return nil
}

// JoinUser is an alias for AddConnection
func (lobby *Lobby) JoinUser(userID uuid.UUID, conn *LobbyConnection) error {
	return lobby.AddConnection(userID, conn)
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

	lobby.BroadcastAll(map[string]interface{}{
		"type":    "lobby_countdown_start",
		"seconds": seconds,
	})

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

func (lobby *Lobby) WhoIsReady() []uuid.UUID {
	var readyUsers []uuid.UUID
	for userID, ready := range lobby.ReadyStates {
		if ready {
			readyUsers = append(readyUsers, userID)
		}
	}
	return readyUsers
}

func (lobby *Lobby) WhoIsNotReady() []uuid.UUID {
	var notReadyUsers []uuid.UUID
	for userID, ready := range lobby.ReadyStates {
		if !ready {
			notReadyUsers = append(notReadyUsers, userID)
		}
	}
	return notReadyUsers
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
	delete(lobby.Users, userID)
	delete(lobby.Connections, userID)
	delete(lobby.ReadyStates, userID)

	lobby.CancelCountdown()
}
