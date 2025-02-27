// internal/models/house_rules.go
package models

// HouseRules captures the final game-time configuration from the lobby
// including disconnection policies, special card rules, turn timeouts, etc.
type HouseRules struct {
	// FreezeOnDisconnect indicates if the lobby will freeze the game if a player disconnects.
	FreezeOnDisconnect bool

	// ForfeitOnDisconnect indicates if a player should immediately forfeit upon disconnect.
	ForfeitOnDisconnect bool

	// MissedRoundThreshold indicates how many rounds a user can miss before penalty or removal.
	MissedRoundThreshold int

	// PenaltyCardCount is the default penalty for failed snaps, etc.
	PenaltyCardCount int

	// AllowDiscardAbilities indicates if replaced discards can trigger special abilities.
	AllowDiscardAbilities bool

	// DisconnectionRoundLimit indicates how many consecutive turns a user can be disconnected before penalty.
	DisconnectionRoundLimit int

	// TurnTimeoutSec is how many seconds each turn lasts before auto-skipping (0 => no limit).
	TurnTimeoutSec int

	// AutoStart indicates if the lobby automatically starts the countdown once all players are ready.
	AutoStart bool
}

// TurnTimeoutSeconds returns the configured turn timeout or 0 if no limit.
func (h HouseRules) TurnTimeoutSeconds() int {
	return h.TurnTimeoutSec
}
