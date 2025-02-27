// internal/models/lobby.go
package models

import "github.com/google/uuid"

// Lobby represents a row in the lobbies table, referencing a 'lobby_type' and associated HouseRules.
type Lobby struct {
	ID          uuid.UUID `json:"id"`
	HostUserID  uuid.UUID `json:"host_user_id"`
	Type        string    `json:"type"` // 'private', 'public', or 'matchmaking'
	CircuitMode bool      `json:"circuit_mode"`
	Ranked      bool      `json:"ranked"`
	RankingMode string    `json:"ranking_mode"`

	// HouseRules holds the nested object of specific rules, including auto_start, turn_timeout_sec, etc.
	// see internal/models/house_rules.go
	HouseRules HouseRules `json:"house_rules"`
}
