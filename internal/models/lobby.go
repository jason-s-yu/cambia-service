package models

import "github.com/google/uuid"

// Lobby represents a row in the lobbies table, possibly referencing the new 'lobby_type' enum.
type Lobby struct {
	ID                     uuid.UUID `json:"id"`
	HostUserID             uuid.UUID `json:"host_user_id"`
	Type                   string    `json:"type"` // 'private', 'public', or 'matchmaking'
	CircuitMode            bool      `json:"circuit_mode"`
	Ranked                 bool      `json:"ranked"`
	RankingMode            string    `json:"ranking_mode"`
	DisconnectionThreshold int       `json:"disconnection_threshold"`

	HouseRuleFreezeDisconnect     bool `json:"house_rule_freeze_disconnect"`
	HouseRuleForfeitDisconnect    bool `json:"house_rule_forfeit_disconnect"`
	HouseRuleMissedRoundThreshold int  `json:"house_rule_missed_round_threshold"`
	PenaltyCardCount              int  `json:"penalty_card_count"`
	AllowReplacedDiscardAbilities bool `json:"allow_replaced_discard_abilities"`
}
