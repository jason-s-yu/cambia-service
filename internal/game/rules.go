package game

import "fmt"

type HouseRules struct {
	AllowDrawFromDiscardPile bool `json:"allowDrawFromDiscardPile"` // allow players to draw from the discard pile
	AllowReplaceAbilities    bool `json:"allowReplaceAbilities"`    // allow cards discarded from a draw and replace to use their special abilities
	SnapRace                 bool `json:"snapRace"`                 // only allow the first card snapped to succeed; all others get penalized
	ForfeitOnDisconnect      bool `json:"forfeitOnDisconnect"`      // if a player disconnects, forfeit their game; if false, players can rejoin
	PenaltyDrawCount         int  `json:"penaltyDrawCount"`         // num cards to draw on false snap
	AutoKickTurnCount        int  `json:"autoKickTurnCount"`        // number of Cambia rounds to wait before auto-forfeiting a player that is nonresponsive
	TurnTimerSec             int  `json:"turnTimerSec"`             // number of seconds to wait for a player to make a move; default is 15 sec
}

// Update will update the house rules with the new rules provided.
// If a rule is not set or defined, it will be ignored, and the old value will persist.
func (rules *HouseRules) Update(newRules map[string]interface{}) error {
	var ok bool

	if val, exists := newRules["allowDrawFromDiscardPile"]; exists && val != nil {
		if rules.AllowDrawFromDiscardPile, ok = val.(bool); !ok {
			return fmt.Errorf("invalid type for allowDrawFromDiscardPile")
		}
		rules.AllowDrawFromDiscardPile = val.(bool)
	}
	if val, exists := newRules["allowReplaceAbilities"]; exists && val != nil {
		if rules.AllowReplaceAbilities, ok = val.(bool); !ok {
			return fmt.Errorf("invalid type for allowReplaceAbilities")
		}
		rules.AllowReplaceAbilities = val.(bool)
	}
	if val, exists := newRules["snapRace"]; exists && val != nil {
		if rules.SnapRace, ok = val.(bool); !ok {
			return fmt.Errorf("invalid type for snapRace")
		}
		rules.SnapRace = val.(bool)
	}
	if val, exists := newRules["forfeitOnDisconnect"]; exists && val != nil {
		if rules.ForfeitOnDisconnect, ok = val.(bool); !ok {
			return fmt.Errorf("invalid type for forfeitOnDisconnect")
		}
		rules.ForfeitOnDisconnect = val.(bool)
	}
	if val, exists := newRules["penaltyDrawCount"]; exists && val != nil {
		if rules.PenaltyDrawCount, ok = val.(int); !ok {
			return fmt.Errorf("invalid type for penaltyDrawCount")
		}
		if val.(int) < 0 {
			return fmt.Errorf("penaltyDrawCount must be greater than or equal to 0; set to 0 for no penalty")
		}
		rules.PenaltyDrawCount = val.(int)
	}
	if val, exists := newRules["autoKickTurnCount"]; exists && val != nil {
		if rules.AutoKickTurnCount, ok = val.(int); !ok {
			return fmt.Errorf("invalid type for autoKickTurnCount")
		}
		if val.(int) < 0 {
			return fmt.Errorf("autoKickTurnCount must be at least 0; set to 0 to disable auto-kick")
		}
		rules.AutoKickTurnCount = val.(int)
	}
	if val, exists := newRules["turnTimerSec"]; exists && val != nil {
		if rules.TurnTimerSec, ok = val.(int); !ok {
			return fmt.Errorf("invalid type for turnTimerSec")
		}
		if val.(int) < 0 {
			return fmt.Errorf("turnTimerSec must be at least 0; set to 0 to disable turn timer")
		}
		rules.TurnTimerSec = val.(int)
	}

	return nil
}

// ParseRules converts a map of rules to a HouseRules struct. It will ensure the types are valid.
func ParseRules(rules map[string]interface{}, current HouseRules) (HouseRules, error) {
	houseRules := current
	var ok bool

	if val, exists := rules["allowDrawFromDiscardPile"]; exists && val != nil {
		if houseRules.AllowDrawFromDiscardPile, ok = val.(bool); !ok {
			return houseRules, fmt.Errorf("invalid type for allowDrawFromDiscardPile")
		}
	}
	if val, exists := rules["allowReplaceAbilities"]; exists && val != nil {
		if houseRules.AllowReplaceAbilities, ok = val.(bool); !ok {
			return houseRules, fmt.Errorf("invalid type for allowReplaceAbilities")
		}
	}
	if val, exists := rules["snapRace"]; exists && val != nil {
		if houseRules.SnapRace, ok = val.(bool); !ok {
			return houseRules, fmt.Errorf("invalid type for snapRace")
		}
	}
	if val, exists := rules["forfeitOnDisconnect"]; exists && val != nil {
		if houseRules.ForfeitOnDisconnect, ok = val.(bool); !ok {
			return houseRules, fmt.Errorf("invalid type for forfeitOnDisconnect")
		}
	}
	if val, exists := rules["penaltyDrawCount"]; exists && val != nil {
		if houseRules.PenaltyDrawCount, ok = val.(int); !ok {
			return houseRules, fmt.Errorf("invalid type for penaltyDrawCount")
		}
	}
	if val, exists := rules["autoKickTurnCount"]; exists && val != nil {
		if houseRules.AutoKickTurnCount, ok = val.(int); !ok {
			return houseRules, fmt.Errorf("invalid type for autoKickTurnCount")
		}
	}
	if val, exists := rules["turnTimerSec"]; exists && val != nil {
		if houseRules.TurnTimerSec, ok = val.(int); !ok {
			return houseRules, fmt.Errorf("invalid type for turnTimerSec")
		}
	}

	return houseRules, nil
}
