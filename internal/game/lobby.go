// internal/game/lobby.go
package game

import (
	"encoding/json" // Added for marshaling
	"fmt"
	"log" // Added for logging
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

	// HouseRules is embedded directly from the game package
	HouseRules HouseRules `json:"houseRules"`
	// Circuit settings embedded directly from the game package
	Circuit Circuit `json:"circuit"`

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
	// Prevent blocking if channel is full or closed
	select {
	case conn.OutChan <- msg:
	default:
		log.Printf("LobbyConnection for user %s: OutChan full or closed, message dropped.", conn.UserID)
	}
}

// WriteError is a convenience to send an error object.
func (conn *LobbyConnection) WriteError(msg string) {
	conn.Write(map[string]interface{}{
		"type":    "error",
		"message": msg,
	})
}

// --- Removed Redeclared Structs ---
// Circuit and CircuitRules are now defined in game.go and used here via game.Circuit and game.CircuitRules

// LobbySettings holds settings specific to the lobby behavior.
type LobbySettings struct {
	AutoStart bool `json:"autoStart"`
}

// NewLobbyWithDefaults creates an ephemeral lobby with default house rules, etc.
func NewLobbyWithDefaults(hostID uuid.UUID) *Lobby {
	lobbyID, _ := uuid.NewRandom()
	// Use the default HouseRules struct definition from game.go
	defaultHouseRules := HouseRules{
		AllowDrawFromDiscardPile: false,
		AllowReplaceAbilities:    false,
		SnapRace:                 false,
		ForfeitOnDisconnect:      true,
		PenaltyDrawCount:         2, // Default is 2
		AutoKickTurnCount:        3, // Example default
		TurnTimerSec:             15,
	}
	// Use the default Circuit struct definition from game.go
	defaultCircuit := Circuit{
		Enabled: false,
		Rules: CircuitRules{ // Initialize nested rules
			TargetScore:            100,  // Example default
			WinBonus:               -1,   // Example default
			FalseCambiaPenalty:     1,    // Example default
			FreezeUserOnDisconnect: true, // Example default
		},
	}

	return &Lobby{
		ID:          lobbyID,
		HostUserID:  hostID,
		Type:        "private",      // Default lobby type
		GameMode:    "head_to_head", // Default game mode
		Users:       make(map[uuid.UUID]bool),
		Connections: make(map[uuid.UUID]*LobbyConnection),
		ReadyStates: make(map[uuid.UUID]bool),

		HouseRules: defaultHouseRules, // Assign default game rules
		Circuit:    defaultCircuit,    // Assign default circuit rules

		LobbySettings: LobbySettings{
			AutoStart: true, // Default lobby setting
		},
	}
}

// InviteUser ephemeral sets userID => false in the Users map (private-lobby invitation).
func (lobby *Lobby) InviteUser(userID uuid.UUID) {
	// Only add if not already present or joined
	if _, exists := lobby.Users[userID]; !exists {
		lobby.Users[userID] = false // Mark as invited
		log.Printf("Lobby %s: User %s invited.", lobby.ID, userID)
		// Broadcast invite event
		lobby.BroadcastAll(map[string]interface{}{
			"type":      "lobby_invite",
			"invitedID": userID.String(),
		})
	} else {
		log.Printf("Lobby %s: User %s already present or invited.", lobby.ID, userID)
	}
}

// AddConnection ephemeral sets user as "connected," also sets ReadyStates[userID] = false.
func (lobby *Lobby) AddConnection(userID uuid.UUID, conn *LobbyConnection) error {
	// Check if user exists (was invited or previously joined)
	joined, exists := lobby.Users[userID]
	if !exists {
		// If not invited (public lobby or direct join attempt)
		if lobby.Type != "private" {
			lobby.Users[userID] = true // Add directly for non-private
		} else {
			return fmt.Errorf("user %s not invited to the private lobby %s", userID, lobby.ID)
		}
	} else if joined {
		// User is rejoining/replacing connection
		log.Printf("Lobby %s: User %s is re-establishing connection.", lobby.ID, userID)
		// Close old connection's channel if it exists? Handled by context cancel usually.
	}

	// Add/update connection and reset ready state
	lobby.Connections[userID] = conn
	lobby.ReadyStates[userID] = false
	lobby.Users[userID] = true // Mark as definitely joined now

	log.Printf("Lobby %s: User %s connected.", lobby.ID, userID)
	// Broadcast join event
	lobby.BroadcastJoin(userID)

	// Send current lobby state to the joining user
	lobby.SendLobbyState(userID)

	return nil
}

// RemoveUser ephemeral: remove from Users, Connections, ReadyStates. If empty => call OnEmpty callback.
func (lobby *Lobby) RemoveUser(userID uuid.UUID) {
	_, connExists := lobby.Connections[userID]

	delete(lobby.Users, userID)
	delete(lobby.Connections, userID)
	delete(lobby.ReadyStates, userID)

	log.Printf("Lobby %s: User %s removed.", lobby.ID, userID)

	if connExists {
		lobby.BroadcastLeave(userID) // Notify others only if they were connected
	}

	lobby.CancelCountdown() // Stop countdown if user leaves

	// Re-evaluate readiness if needed (e.g., if countdown was active)
	if lobby.AreAllReady() && lobby.LobbySettings.AutoStart && !lobby.InGame {
		lobby.StartCountdown(10, func(l *Lobby) { // Restart countdown if still all ready
			// Game start logic (defined elsewhere, e.g., in handlers)
			log.Printf("Lobby %s: Countdown finished after user removal, initiating game start.", l.ID)
			// Placeholder: Need access to GameServer or similar to actually start
			// gameServer.NewCambiaGameFromLobby(context.Background(), l)
		})
	}

	if len(lobby.Connections) == 0 && lobby.OnEmpty != nil { // Check Connections count now
		log.Printf("Lobby %s is now empty. Triggering OnEmpty callback.", lobby.ID)
		lobby.OnEmpty(lobby.ID)
	}
}

// StartCountdown begins a countdown if not in a game, not already counting down.
func (lobby *Lobby) StartCountdown(seconds int, callback func(*Lobby)) bool {
	if lobby.InGame || lobby.CountdownTimer != nil {
		log.Printf("Lobby %s: Cannot start countdown (InGame: %v, TimerExists: %v)", lobby.ID, lobby.InGame, lobby.CountdownTimer != nil)
		return false
	}
	if len(lobby.Connections) < 2 { // Don't start countdown with fewer than 2 players
		log.Printf("Lobby %s: Cannot start countdown with fewer than 2 players.", lobby.ID)
		return false
	}

	log.Printf("Lobby %s: Starting %d second countdown.", lobby.ID, seconds)
	lobby.BroadcastAll(map[string]interface{}{
		"type":    "lobby_countdown_start",
		"seconds": seconds,
	})
	lobby.CountdownTimer = time.AfterFunc(time.Duration(seconds)*time.Second, func() {
		log.Printf("Lobby %s: Countdown finished. Executing callback.", lobby.ID)
		lobby.CountdownTimer = nil // Clear timer ref before callback
		callback(lobby)
	})
	return true
}

// CancelCountdown stops any existing countdown.
func (lobby *Lobby) CancelCountdown() {
	if lobby.CountdownTimer != nil {
		log.Printf("Lobby %s: Cancelling countdown.", lobby.ID)
		lobby.CountdownTimer.Stop()
		lobby.CountdownTimer = nil
		// Broadcast cancellation
		lobby.BroadcastAll(map[string]interface{}{
			"type": "lobby_countdown_cancel",
		})
	}
}

// MarkUserReady ephemeral sets a user's ready state to true, then broadcasts.
func (lobby *Lobby) MarkUserReady(userID uuid.UUID) {
	if _, ok := lobby.Connections[userID]; !ok {
		log.Printf("Lobby %s: Cannot mark non-connected user %s as ready.", lobby.ID, userID)
		return
	}
	if !lobby.ReadyStates[userID] { // Only update and broadcast if state changes
		lobby.ReadyStates[userID] = true
		log.Printf("Lobby %s: User %s marked as READY.", lobby.ID, userID)
		lobby.BroadcastReadyState(userID, true)
	}
}

// MarkUserUnready ephemeral sets a user's ready state to false, then cancels countdown.
func (lobby *Lobby) MarkUserUnready(userID uuid.UUID) {
	if _, ok := lobby.Connections[userID]; !ok {
		log.Printf("Lobby %s: Cannot mark non-connected user %s as unready.", lobby.ID, userID)
		return
	}
	if lobby.ReadyStates[userID] { // Only update and broadcast if state changes
		lobby.ReadyStates[userID] = false
		log.Printf("Lobby %s: User %s marked as UNREADY.", lobby.ID, userID)
		lobby.BroadcastReadyState(userID, false)
		lobby.CancelCountdown() // Always cancel countdown if someone becomes unready
	}
}

// AreAllReady returns true if all *connected* users are ready. If no users, returns false.
func (lobby *Lobby) AreAllReady() bool {
	if len(lobby.Connections) == 0 { // Check connected users
		return false
	}
	for userID := range lobby.Connections { // Iterate over connected users
		if !lobby.ReadyStates[userID] { // Check their ready state
			return false
		}
	}
	return true // All connected users are ready
}

// BroadcastAll sends msg to every connected user in this lobby.
func (lobby *Lobby) BroadcastAll(msg map[string]interface{}) {
	// Marshal once outside the loop
	jsonData, err := json.Marshal(msg)
	if err != nil {
		log.Printf("Lobby %s: Error marshaling message type '%s' for broadcast: %v", lobby.ID, msg["type"], err)
		return
	}

	log.Printf("Lobby %s: Broadcasting message type '%s' to %d connections.", lobby.ID, msg["type"], len(lobby.Connections))

	for _, conn := range lobby.Connections {
		// Send pre-marshaled data
		// Create a temporary variable for the map to send to Write
		msgToSend := make(map[string]interface{})
		if err := json.Unmarshal(jsonData, &msgToSend); err != nil {
			log.Printf("Lobby %s: Error unmarshaling back to map for user %s: %v", lobby.ID, conn.UserID, err)
			continue // Skip sending to this user if unmarshal fails
		}
		conn.Write(msgToSend) // Write expects map[string]interface{}
	}
}

// BroadcastJoin notifies that a user joined.
func (lobby *Lobby) BroadcastJoin(userID uuid.UUID) {
	// Ensure connection exists before accessing IsHost
	isHost := false
	if conn, ok := lobby.Connections[userID]; ok {
		isHost = conn.IsHost
	}

	lobby.BroadcastAll(map[string]interface{}{
		"type":         "lobby_update",
		"user_join":    userID.String(),
		"is_host":      isHost,                        // Include host status
		"lobby_status": lobby.getLobbyStatusPayload(), // Include full status
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
		"type":         "lobby_update",
		"user_left":    userID.String(),
		"lobby_status": lobby.getLobbyStatusPayload(), // Include full status
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

// getLobbyStatusPayload gathers current user IDs and ready states for broadcasting.
func (lobby *Lobby) getLobbyStatusPayload() map[string]interface{} {
	users := []map[string]interface{}{}
	for userID, conn := range lobby.Connections {
		// Check if user is still in ReadyStates map before accessing
		isReady := false
		if ready, ok := lobby.ReadyStates[userID]; ok {
			isReady = ready
		}
		users = append(users, map[string]interface{}{
			"id":       userID.String(),
			"is_host":  conn.IsHost,
			"is_ready": isReady,
		})
	}
	return map[string]interface{}{
		"users": users,
		// Add other relevant status info like HouseRules maybe?
	}
}

// SendLobbyState sends the full current lobby state to a specific user.
func (lobby *Lobby) SendLobbyState(userID uuid.UUID) {
	conn, ok := lobby.Connections[userID]
	if !ok {
		return // User not connected
	}

	stateMsg := map[string]interface{}{
		"type":         "lobby_state",
		"lobby_id":     lobby.ID.String(),
		"host_id":      lobby.HostUserID.String(),
		"your_id":      userID.String(),
		"your_is_host": conn.IsHost,
		"lobby_type":   lobby.Type,
		"game_mode":    lobby.GameMode,
		"in_game":      lobby.InGame,
		"game_id":      lobby.GameID.String(), // Include game ID if applicable
		"house_rules":  lobby.HouseRules,
		"circuit":      lobby.Circuit,
		"settings":     lobby.LobbySettings,
		"lobby_status": lobby.getLobbyStatusPayload(),
	}
	conn.Write(stateMsg)
}

// BroadcastRulesUpdate notifies all users about updated house rules.
func (lobby *Lobby) BroadcastRulesUpdate() {
	lobby.BroadcastAll(map[string]interface{}{
		"type":        "lobby_rules_updated",
		"house_rules": lobby.HouseRules,
		"circuit":     lobby.Circuit, // Include circuit rules too
	})
}
