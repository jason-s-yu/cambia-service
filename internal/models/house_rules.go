package models

// HouseRules captures the final game-time configuration from the lobby
type HouseRules struct {
	FreezeOnDisconnect      bool
	ForfeitOnDisconnect     bool
	MissedRoundThreshold    int
	PenaltyCardCount        int
	AllowDiscardAbilities   bool
	DisconnectionRoundLimit int

	// If 0, no turn timeout
	TurnTimeoutSec int
}

// TurnTimeoutSeconds returns the configured turn timeout or 0 if no limit
func (h HouseRules) TurnTimeoutSeconds() int {
	return h.TurnTimeoutSec
}
