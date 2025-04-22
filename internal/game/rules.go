// internal/game/rules.go
package game

import "fmt"

// HouseRules defines optional game rules that can modify standard play.
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
	var err error // Declare error variable

	// Helper function to handle type assertion and assignment
	assignBool := func(field *bool, key string) error {
		if val, exists := newRules[key]; exists && val != nil {
			*field, ok = val.(bool)
			if !ok {
				return fmt.Errorf("invalid type for %s", key)
			}
		}
		return nil
	}

	assignInt := func(field *int, key string, minVal int, validationMsg string) error {
		if val, exists := newRules[key]; exists && val != nil {
			// JSON numbers are often float64, handle conversion
			var floatVal float64
			floatVal, ok = val.(float64)
			if !ok {
				// Try int if float64 fails
				var intVal int
				intVal, ok = val.(int)
				if !ok {
					return fmt.Errorf("invalid type for %s", key)
				}
				*field = intVal
			} else {
				*field = int(floatVal)
			}

			if *field < minVal {
				return fmt.Errorf(validationMsg)
			}
		}
		return nil
	}

	// Apply updates using helpers
	if err = assignBool(&rules.AllowDrawFromDiscardPile, "allowDrawFromDiscardPile"); err != nil {
		return err
	}
	if err = assignBool(&rules.AllowReplaceAbilities, "allowReplaceAbilities"); err != nil {
		return err
	}
	if err = assignBool(&rules.SnapRace, "snapRace"); err != nil {
		return err
	}
	if err = assignBool(&rules.ForfeitOnDisconnect, "forfeitOnDisconnect"); err != nil {
		return err
	}
	if err = assignInt(&rules.PenaltyDrawCount, "penaltyDrawCount", 0, "penaltyDrawCount must be non-negative"); err != nil {
		return err
	}
	if err = assignInt(&rules.AutoKickTurnCount, "autoKickTurnCount", 0, "autoKickTurnCount must be non-negative"); err != nil {
		return err
	}
	if err = assignInt(&rules.TurnTimerSec, "turnTimerSec", 0, "turnTimerSec must be non-negative"); err != nil {
		return err
	}

	return nil
}

// ParseRules converts a map of rules to a HouseRules struct. It will ensure the types are valid.
func ParseRules(rules map[string]interface{}, current HouseRules) (HouseRules, error) {
	// Create a copy to modify
	houseRules := current
	// Use the Update method on the copy
	err := houseRules.Update(rules)
	return houseRules, err
}
