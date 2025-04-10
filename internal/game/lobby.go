// internal/game/lobby.go
package game

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Lobby is an ephemeral grouping of users with chat, rules, ready states, etc.
type Lobby struct {
	ID         uuid.UUID `json:"id"`
	HostUserID uuid.UUID `json:"hostUserID"`
	Type       string    `json:"type"`
	GameMode   string    `json:"gameMode"`

	// Users maps userID -> whether they've joined (true) or only invited (false).
	Users map[uuid.UUID]bool `json:"-"`

	// Connections holds the actual live WebSocket connections for joined users.
	Connections map[uuid.UUID]*LobbyConnection `json:"-"`
	// ReadyStates holds userID -> bool for "is ready".
	ReadyStates map[uuid.UUID]bool `json:"-"`

	GameInstanceCreated bool      `json:"-"`
	GameID              uuid.UUID `json:"-"`
	InGame              bool      `json:"inGame"`

	CountdownTimer *time.Timer `json:"-"`

	HouseRules    HouseRules    `json:"houseRules"`
	Circuit       Circuit       `json:"circuit"`
	LobbySettings LobbySettings `json:"lobbySettings"`

	// OnEmpty is called if we detect the lobby is empty (0 users) after removing a user.
	// Typically assigned by the code that creates & stores this lobby, e.g. via
	//   lobby.OnEmpty = func(lobbyID uuid.UUID) { store.DeleteLobby(lobbyID) }
	OnEmpty func(lobbyID uuid.UUID) `json:"-"`
}

// LobbyConnection is a single user's presence in the lobby.
type LobbyConnection struct {
	UserID  uuid.UUID
	Cancel  func()
	OutChan chan map[string]interface{}
	IsHost  bool
}

// Write pushes a message onto the user's OutChan.
func (conn *LobbyConnection) Write(msg map[string]interface{}) {
	conn.OutChan <- msg
}

// WriteError is a convenience to send an error object.
func (conn *LobbyConnection) WriteError(msg string) {
	conn.OutChan <- map[string]interface{}{
		"type":    "error",
		"message": msg,
	}
}

// Circuit and HouseRules are still present but not fully used yet.
type Circuit struct {
	Enabled bool         `json:"enabled"`
	Mode    string       `json:"mode"`
	Rules   CircuitRules `json:"rules"`
}

type CircuitRules struct {
	TargetScore            int  `json:"target_score"`
	WinBonus               int  `json:"winBonus"`
	FalseCambiaPenalty     int  `json:"falseCambiaPenalty"`
	FreezeUserOnDisconnect bool `json:"freezeUserOnDisconnect"`
}

type LobbySettings struct {
	AutoStart bool `json:"autoStart"`
}

// NewLobbyWithDefaults creates an ephemeral lobby with default house rules, etc.
func NewLobbyWithDefaults(hostID uuid.UUID) *Lobby {
	lobbyID, _ := uuid.NewRandom()
	defaultHouseRules := HouseRules{
		AllowDrawFromDiscardPile: false,
		AllowReplaceAbilities:    false,
		SnapRace:                 false,
		ForfeitOnDisconnect:      true,
		PenaltyDrawCount:         1,
		AutoKickTurnCount:        3,
		TurnTimerSec:             15,
	}
	return &Lobby{
		ID:          lobbyID,
		HostUserID:  hostID,
		Type:        "private",
		GameMode:    "head_to_head",
		Users:       make(map[uuid.UUID]bool),
		Connections: make(map[uuid.UUID]*LobbyConnection),
		ReadyStates: make(map[uuid.UUID]bool),

		HouseRules: defaultHouseRules,
		Circuit: Circuit{
			Enabled: false,
		},
		LobbySettings: LobbySettings{
			AutoStart: true,
		},
	}
}

// InviteUser ephemeral sets userID => false in the Users map (private-lobby invitation).
func (lobby *Lobby) InviteUser(userID uuid.UUID) {
	lobby.Users[userID] = false
}

// AddConnection ephemeral sets user as "connected," also sets ReadyStates[userID] = false.
func (lobby *Lobby) AddConnection(userID uuid.UUID, conn *LobbyConnection) error {
	if lobby.Type == "private" {
		if _, ok := lobby.Users[userID]; !ok {
			return fmt.Errorf("user %s not invited to the private lobby", userID)
		}
	}
	lobby.Users[userID] = true
	lobby.Connections[userID] = conn
	lobby.ReadyStates[userID] = false
	return nil
}

// RemoveUser ephemeral: remove from Users, Connections, ReadyStates. If empty => call OnEmpty callback.
func (lobby *Lobby) RemoveUser(userID uuid.UUID) {
	delete(lobby.Users, userID)
	delete(lobby.Connections, userID)
	delete(lobby.ReadyStates, userID)

	lobby.CancelCountdown()

	if len(lobby.Users) == 0 && lobby.OnEmpty != nil {
		lobby.OnEmpty(lobby.ID)
	}
}

// StartCountdown begins a countdown if not in a game, not already counting down.
func (lobby *Lobby) StartCountdown(seconds int, callback func(*Lobby)) bool {
	if lobby.InGame || lobby.CountdownTimer != nil {
		return false
	}

	lobby.BroadcastAll(map[string]interface{}{
		"type":    "lobby_countdown_start",
		"seconds": seconds,
	})
	lobby.CountdownTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		callback(lobby)
	})
	return true
}

// CancelCountdown stops any existing countdown.
func (lobby *Lobby) CancelCountdown() {
	if lobby.CountdownTimer != nil {
		lobby.CountdownTimer.Stop()
		lobby.CountdownTimer = nil
	}
}

// MarkUserReady ephemeral sets a user's ready state to true, then broadcasts.
func (lobby *Lobby) MarkUserReady(userID uuid.UUID) {
	if _, ok := lobby.Connections[userID]; !ok {
		return
	}
	lobby.ReadyStates[userID] = true
	lobby.BroadcastReadyState(userID, true)
}

// MarkUserUnready ephemeral sets a user's ready state to false, then cancels countdown.
func (lobby *Lobby) MarkUserUnready(userID uuid.UUID) {
	if _, ok := lobby.Connections[userID]; !ok {
		return
	}
	lobby.ReadyStates[userID] = false
	lobby.BroadcastReadyState(userID, false)
	lobby.CancelCountdown()
}

// AreAllReady returns true if all *connected* users are ready. If no users, returns false.
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

// BroadcastAll sends msg to every connected user in this lobby.
func (lobby *Lobby) BroadcastAll(msg map[string]interface{}) {
	for _, conn := range lobby.Connections {
		conn.OutChan <- msg
	}
}

// BroadcastJoin notifies that a user joined.
func (lobby *Lobby) BroadcastJoin(userID uuid.UUID) {
	lobby.BroadcastAll(map[string]interface{}{
		"type":      "lobby_update",
		"user_join": userID.String(),
		"ready_map": lobby.ReadyStates,
	})
}

// BroadcastReadyState notifies that user changed readiness.
func (lobby *Lobby) BroadcastReadyState(userID uuid.UUID, ready bool) {
	lobby.BroadcastAll(map[string]interface{}{
		"type":     "ready_update",
		"user_id":  userID.String(),
		"is_ready": ready,
	})
}

// BroadcastLeave notifies that a user left.
func (lobby *Lobby) BroadcastLeave(userID uuid.UUID) {
	lobby.BroadcastAll(map[string]interface{}{
		"type":      "lobby_update",
		"user_left": userID.String(),
		"ready_map": lobby.ReadyStates,
	})
}

// BroadcastChat broadcasts a chat message from userID.
func (lobby *Lobby) BroadcastChat(userID uuid.UUID, msg string) {
	lobby.BroadcastAll(map[string]interface{}{
		"type":    "chat",
		"user_id": userID.String(),
		"msg":     msg,
		"ts":      time.Now().Unix(),
	})
}
