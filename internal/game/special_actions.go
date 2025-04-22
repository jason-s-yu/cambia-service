// internal/game/special_actions.go
package game

import (
	"fmt" // Added import
	"log"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/models"
)

// ProcessSpecialAction is the main entry point for multi-step special actions.
// This function handles "peek_self", "peek_other", "swap_blind", "swap_peek",
// "swap_peek_swap", or "skip" as well as verifying the correct rank ("K", "Q", etc.).
// Assumes the game lock is held by the caller (e.g., the WS handler calling this).
// It's now a method on *CambiaGame.
func (g *CambiaGame) ProcessSpecialAction(
	userID uuid.UUID,
	special string, // The sub-action requested (e.g., "peek_self", "skip")
	card1Data map[string]interface{}, // Raw map from client payload for card 1 target
	card2Data map[string]interface{}, // Raw map from client payload for card 2 target
) {
	// NOTE: Lock is assumed to be HELD by the caller.

	// Verify special action state
	if !g.SpecialAction.Active || g.SpecialAction.PlayerID != userID {
		log.Printf("Game %s: ProcessSpecialAction called by player %s, but no matching special action is active. Ignoring.", g.ID, userID)
		// Send fail event without modifying state or advancing turn
		g.FireEventPrivateSpecialActionFail(userID, "No special action in progress for you.", special, nil, nil)
		return
	}

	rank := g.SpecialAction.CardRank // The rank that triggered the action
	g.logAction(userID, "action_special_received", map[string]interface{}{"special": special, "rank": rank, "card1": card1Data, "card2": card2Data})

	// Handle "skip" universally
	if special == "skip" {
		g.processSkipSpecialAction(userID)
		return
	}

	// Route based on the rank of the card that triggered the special action
	switch rank {
	case "7", "8":
		if special != "peek_self" {
			g.FailSpecialAction(userID, fmt.Sprintf("Invalid step '%s' for 7/8 special action.", special))
			return
		}
		g.doPeekSelf(userID, card1Data) // Pass card1Data for target card info
		// Turn advanced within doPeekSelf if successful

	case "9", "T": // 9 and 10 (T)
		if special != "peek_other" {
			g.FailSpecialAction(userID, fmt.Sprintf("Invalid step '%s' for 9/T special action.", special))
			return
		}
		g.doPeekOther(userID, card1Data) // Pass card1Data for target card info
		// Turn advanced within doPeekOther if successful

	case "J", "Q": // Assuming J/Q use swap_blind
		if special != "swap_blind" {
			g.FailSpecialAction(userID, fmt.Sprintf("Invalid step '%s' for J/Q special action.", special))
			return
		}
		g.doSwapBlind(userID, card1Data, card2Data)
		// Turn advanced within doSwapBlind if successful

	case "K":
		// King has a multi-step flow
		if special == "swap_peek" {
			// Initial step: Player selects two cards to peek
			if g.SpecialAction.FirstStepDone {
				g.FailSpecialAction(userID, "Invalid step 'swap_peek' for King action - reveal already done.")
				return
			}
			g.doKingFirstStep(userID, card1Data, card2Data)
			// Does NOT advance turn; waits for swap_peek_swap or skip
		} else if special == "swap_peek_swap" {
			// Second step: Player decides whether to swap the peeked cards
			if !g.SpecialAction.FirstStepDone {
				g.FailSpecialAction(userID, "Invalid step 'swap_peek_swap' for King action - must peek first.")
				return
			}
			g.doKingSwapDecision(userID, card1Data, card2Data) // cardData validates against stored state
			// Turn advanced within doKingSwapDecision if successful
		} else {
			g.FailSpecialAction(userID, fmt.Sprintf("Invalid 'special' value '%s' for King action.", special))
		}

	default:
		// Should not happen if rank check was done before activating
		g.FailSpecialAction(userID, fmt.Sprintf("Unsupported card rank '%s' for special action.", rank))
	}
}

// processSkipSpecialAction handles the "skip" action.
// Assumes lock is held by caller.
func (g *CambiaGame) processSkipSpecialAction(userID uuid.UUID) {
	if !g.SpecialAction.Active || g.SpecialAction.PlayerID != userID {
		// Log warning but proceed to clear state and advance turn defensively
		log.Printf("Warning: processSkipSpecialAction called for player %s but state mismatch (Active:%v, Player:%s)", userID, g.SpecialAction.Active, g.SpecialAction.PlayerID)
	}
	rank := g.SpecialAction.CardRank // Get rank before clearing
	log.Printf("Game %s: Player %s chose to skip special action for rank %s.", g.ID, userID, rank)
	g.logAction(userID, "action_special_skip", map[string]interface{}{"rank": rank})

	// Broadcast public skip event? Spec doesn't explicitly list one, but might be useful.
	// Example:
	// g.FireEventPlayerSpecialAction(userID, "skip", nil, nil)

	g.SpecialAction = SpecialActionState{} // Clear state
	g.advanceTurn()                        // Advance turn after skipping
}

// Helper functions (assume lock is held by caller)

// parseCardTarget extracts card ID, user ID, and index from a client payload map.
// Returns cardID, ownerID, index (-1 if not provided), ok (bool for basic success).
func parseCardTarget(data map[string]interface{}) (cardID uuid.UUID, ownerID uuid.UUID, idx int, ok bool) {
	idx = -1 // Default index if not provided
	ok = false
	cardID = uuid.Nil
	ownerID = uuid.Nil

	if data == nil {
		// log.Printf("Debug: parseCardTarget received nil data") // Reduce log noise
		return
	}

	// Card ID (required)
	cardIDStr, idOk := data["id"].(string)
	if !idOk || cardIDStr == "" {
		log.Printf("Debug: parseCardTarget missing or invalid 'id': %v", data["id"])
		return // Missing card ID
	}
	var err error
	cardID, err = uuid.Parse(cardIDStr)
	if err != nil {
		log.Printf("Debug: parseCardTarget failed to parse 'id' %s: %v", cardIDStr, err)
		cardID = uuid.Nil // Ensure it's Nil on error
		return            // Invalid card ID format
	}

	// Index (optional)
	idxFloat, idxProvided := data["idx"].(float64) // JSON numbers are float64
	if idxProvided {
		idx = int(idxFloat)
		if idx < 0 { // Basic validation
			log.Printf("Debug: parseCardTarget received invalid negative index: %d", idx)
			idx = -1 // Treat invalid index as not provided
		}
	}

	// Owner User ID (required for targeting others)
	userMap, userProvided := data["user"].(map[string]interface{})
	if userProvided && userMap != nil { // Check if user field exists and is a map
		userIDStr, uidOk := userMap["id"].(string)
		if uidOk && userIDStr != "" {
			ownerID, err = uuid.Parse(userIDStr)
			if err != nil {
				log.Printf("Debug: parseCardTarget failed to parse 'user.id' %s: %v", userIDStr, err)
				ownerID = uuid.Nil // Set to Nil on parse error
			}
		} else {
			log.Printf("Debug: parseCardTarget missing or invalid 'user.id' within user map: %v", userMap["id"])
			// Keep ownerID as Nil if user.id is missing/invalid
		}
	} else {
		// User field not provided or not a map. Owner remains Nil.
		// Validation if owner is *required* happens in the specific action handler.
	}

	// If we reached here and got a valid cardID, basic parsing succeeded.
	if cardID != uuid.Nil {
		ok = true
	}
	return
}

// findCardByID locates a card in a player's hand by ID and returns the card and its index.
// Assumes lock is held.
func (g *CambiaGame) findCardByID(playerID uuid.UUID, cardID uuid.UUID) (*models.Card, int) {
	player := g.getPlayerByID(playerID) // Use helper to get player
	if player == nil {
		return nil, -1
	}
	for i, c := range player.Hand {
		if c.ID == cardID {
			return c, i
		}
	}
	return nil, -1
}

// buildEventCard creates an EventCard struct for event payloads.
func buildEventCard(card *models.Card, idx *int, ownerID uuid.UUID, includePrivate bool) *EventCard {
	if card == nil {
		return nil
	}
	ec := &EventCard{
		ID:  card.ID,
		Idx: idx, // Pass pointer
	}
	if ownerID != uuid.Nil {
		ec.User = &EventUser{ID: ownerID}
	}
	// Only include details if includePrivate is true
	if includePrivate {
		ec.Rank = card.Rank
		ec.Suit = card.Suit
		ec.Value = card.Value
	}
	return ec
}

// doPeekSelf handles the 7/8 special action.
// Assumes lock is held by caller.
func (g *CambiaGame) doPeekSelf(playerID uuid.UUID, card1Data map[string]interface{}) {
	cardID, _, reqIdx, ok := parseCardTarget(card1Data) // Owner ID not needed/validated for self-peek
	if !ok || cardID == uuid.Nil {
		g.FailSpecialAction(playerID, "Invalid card specified for peek_self.")
		return
	}

	targetCard, actualIdx := g.findCardByID(playerID, cardID) // Use game method
	if targetCard == nil {
		g.FailSpecialAction(playerID, "Specified card not found in your hand for peek_self.")
		return
	}

	// Optional index validation
	if reqIdx != -1 && reqIdx != actualIdx {
		log.Printf("Warning: peek_self index mismatch (req: %d, actual: %d) for card %s", reqIdx, actualIdx, cardID)
		// Proceed using actualIdx
	}

	g.logAction(playerID, "action_special_peek_self", map[string]interface{}{"cardId": targetCard.ID, "idx": actualIdx})

	// Private success event with full card details
	eventIdx := actualIdx // Capture for pointer
	g.FireEventPrivateSuccess(playerID, "peek_self",
		buildEventCard(targetCard, &eventIdx, playerID, true), // includePrivate = true
		nil)

	// Public action event (obfuscated)
	g.FireEventPlayerSpecialAction(playerID, "peek_self",
		buildEventCard(targetCard, &eventIdx, playerID, false), // includePrivate = false
		nil)

	g.SpecialAction = SpecialActionState{} // Clear state
	g.advanceTurn()                        // Advance turn
}

// doPeekOther handles the 9/T special action.
// Assumes lock is held by caller.
func (g *CambiaGame) doPeekOther(playerID uuid.UUID, card1Data map[string]interface{}) {
	cardID, ownerID, reqIdx, ok := parseCardTarget(card1Data) // Use reqIdx for validation
	if !ok || cardID == uuid.Nil || ownerID == uuid.Nil {
		g.FailSpecialAction(playerID, "Invalid card or target user specified for peek_other.")
		return
	}
	if ownerID == playerID {
		g.FailSpecialAction(playerID, "Cannot use peek_other on yourself.")
		return
	}

	targetCard, actualIdx := g.findCardByID(ownerID, cardID) // Find card in target's hand
	if targetCard == nil {
		g.FailSpecialAction(playerID, "Specified card not found in target player's hand.")
		return
	}

	// Optional index validation
	if reqIdx != -1 && reqIdx != actualIdx {
		log.Printf("Warning: peek_other index mismatch (req: %d, actual: %d) for card %s", reqIdx, actualIdx, cardID)
		// Proceed using actualIdx
	}

	g.logAction(playerID, "action_special_peek_other", map[string]interface{}{"targetPlayerId": ownerID, "cardId": targetCard.ID, "idx": actualIdx})

	// Private success event to the peeker with full details
	eventIdx := actualIdx // Capture for pointer
	g.FireEventPrivateSuccess(playerID, "peek_other",
		buildEventCard(targetCard, &eventIdx, ownerID, true), // includePrivate = true
		nil)

	// Public action event (obfuscated card, reveals target user)
	g.FireEventPlayerSpecialAction(playerID, "peek_other",
		buildEventCard(targetCard, &eventIdx, ownerID, false), // includePrivate = false
		nil)

	g.SpecialAction = SpecialActionState{} // Clear state
	g.advanceTurn()                        // Advance turn
}

// doSwapBlind handles the J/Q special action.
// Assumes lock is held by caller.
func (g *CambiaGame) doSwapBlind(playerID uuid.UUID, card1Data, card2Data map[string]interface{}) {
	card1ID, owner1ID, idx1, ok1 := parseCardTarget(card1Data)
	card2ID, owner2ID, idx2, ok2 := parseCardTarget(card2Data)

	if !ok1 || !ok2 || card1ID == uuid.Nil || owner1ID == uuid.Nil || card2ID == uuid.Nil || owner2ID == uuid.Nil {
		g.FailSpecialAction(playerID, "Invalid card or user specification for swap_blind.")
		return
	}
	if card1ID == card2ID { // Simpler check: cannot target the exact same card ID
		g.FailSpecialAction(playerID, "Cannot swap a card with itself.")
		return
	}

	player1 := g.getPlayerByID(owner1ID)
	player2 := g.getPlayerByID(owner2ID)
	if player1 == nil || player2 == nil {
		g.FailSpecialAction(playerID, "One or both target players not found.")
		return
	}

	// Check if trying to swap with a player who called Cambia
	if player1.HasCalledCambia || player2.HasCalledCambia {
		log.Printf("Game %s: Player %s attempted blind swap involving a player (%s or %s) who called Cambia. Failing.", g.ID, playerID, owner1ID, owner2ID)

		var eventIdx1Ptr, eventIdx2Ptr *int
		if idx1 != -1 {
			eventIdx1 := idx1
			eventIdx1Ptr = &eventIdx1
		}
		if idx2 != -1 {
			eventIdx2 := idx2
			eventIdx2Ptr = &eventIdx2
		}

		g.FireEventPrivateSpecialActionFail(playerID, "Cannot swap cards with a player who has called Cambia.", "swap_blind",
			buildEventCard(&models.Card{ID: card1ID}, eventIdx1Ptr, owner1ID, false), // Use card ID from payload
			buildEventCard(&models.Card{ID: card2ID}, eventIdx2Ptr, owner2ID, false)) // Use card ID from payload

		// Per spec: If a player attempts to make this action, they receive a private payload
		// from the server, and must issue a new action_special.
		// So, we should NOT clear the special action state here and NOT advance turn.
		// The player must send "skip" or a valid target next.
		// Reset the turn timer to give them time.
		g.ResetTurnTimer()
		return
	}

	card1, actualIdx1 := g.findCardByID(owner1ID, card1ID)
	card2, actualIdx2 := g.findCardByID(owner2ID, card2ID)
	if card1 == nil || card2 == nil {
		g.FailSpecialAction(playerID, "One or both specified cards not found in target hands.")
		return
	}

	// Validate index match if provided (optional but good)
	if idx1 != -1 && idx1 != actualIdx1 {
		log.Printf("Warning: swap_blind index mismatch card1 (req: %d, actual: %d)", idx1, actualIdx1)
	}
	if idx2 != -1 && idx2 != actualIdx2 {
		log.Printf("Warning: swap_blind index mismatch card2 (req: %d, actual: %d)", idx2, actualIdx2)
	}

	// Perform the swap
	player1.Hand[actualIdx1], player2.Hand[actualIdx2] = player2.Hand[actualIdx2], player1.Hand[actualIdx1]
	log.Printf("Game %s: Player %s performed blind swap between %s (Player %s, Idx %d) and %s (Player %s, Idx %d)",
		g.ID, playerID, card1ID, owner1ID, actualIdx1, card2ID, owner2ID, actualIdx2)
	g.logAction(playerID, "action_special_swap_blind", map[string]interface{}{
		"card1Id": card1ID, "owner1Id": owner1ID, "idx1": actualIdx1,
		"card2Id": card2ID, "owner2Id": owner2ID, "idx2": actualIdx2,
	})

	// Public action event (obfuscated cards, reveals users and indices)
	// The event shows the cards *after* the swap, associated with their new owners/indices
	eventIdx1 := actualIdx1 // Capture for pointer
	eventIdx2 := actualIdx2 // Capture for pointer
	g.FireEventPlayerSpecialAction(playerID, "swap_blind",
		buildEventCard(card2, &eventIdx1, owner1ID, false), // Card 2 now at P1's index
		buildEventCard(card1, &eventIdx2, owner2ID, false)) // Card 1 now at P2's index

	// No private success event for blind swap

	g.SpecialAction = SpecialActionState{} // Clear state
	g.advanceTurn()                        // Advance turn
}

// doKingFirstStep handles the initial "peek" part of the King's `swap_peek` action.
// Assumes lock is held by caller.
func (g *CambiaGame) doKingFirstStep(playerID uuid.UUID, card1Data, card2Data map[string]interface{}) {
	card1ID, owner1ID, idx1, ok1 := parseCardTarget(card1Data)
	card2ID, owner2ID, idx2, ok2 := parseCardTarget(card2Data)

	if !ok1 || !ok2 || card1ID == uuid.Nil || owner1ID == uuid.Nil || card2ID == uuid.Nil || owner2ID == uuid.Nil {
		g.FailSpecialAction(playerID, "Invalid card or user specification for King peek.")
		return
	}
	if card1ID == card2ID { // Cannot peek the same card instance twice
		g.FailSpecialAction(playerID, "Cannot peek the same card twice.")
		return
	}

	player1 := g.getPlayerByID(owner1ID)
	player2 := g.getPlayerByID(owner2ID)
	if player1 == nil || player2 == nil {
		g.FailSpecialAction(playerID, "One or both target players not found for King peek.")
		return
	}

	card1, actualIdx1 := g.findCardByID(owner1ID, card1ID)
	card2, actualIdx2 := g.findCardByID(owner2ID, card2ID)
	if card1 == nil || card2 == nil {
		g.FailSpecialAction(playerID, "One or both specified cards not found for King peek.")
		return
	}

	// Optional index validation
	if idx1 != -1 && idx1 != actualIdx1 {
		log.Printf("Warning: king_peek index mismatch card1")
	}
	if idx2 != -1 && idx2 != actualIdx2 {
		log.Printf("Warning: king_peek index mismatch card2")
	}

	// Store targets for the second step (swap decision)
	g.SpecialAction.FirstStepDone = true
	g.SpecialAction.Card1 = card1
	g.SpecialAction.Card1Owner = owner1ID
	g.SpecialAction.Card2 = card2
	g.SpecialAction.Card2Owner = owner2ID

	log.Printf("Game %s: Player %s initiated King peek on %s (Player %s, Idx %d) and %s (Player %s, Idx %d)",
		g.ID, playerID, card1ID, owner1ID, actualIdx1, card2ID, owner2ID, actualIdx2)
	g.logAction(playerID, "action_special_swap_peek_reveal", map[string]interface{}{
		"card1Id": card1ID, "owner1Id": owner1ID, "idx1": actualIdx1,
		"card2Id": card2ID, "owner2Id": owner2ID, "idx2": actualIdx2,
	})

	// Private success event revealing card details to the King user
	eventIdx1 := actualIdx1                                 // Capture for pointer
	eventIdx2 := actualIdx2                                 // Capture for pointer
	g.FireEventPrivateSuccess(playerID, "swap_peek_reveal", // Use specific type from spec
		buildEventCard(card1, &eventIdx1, owner1ID, true),
		buildEventCard(card2, &eventIdx2, owner2ID, true))

	// Public event indicating which cards were targeted for the peek
	g.FireEventPlayerSpecialAction(playerID, "swap_peek_reveal",
		buildEventCard(card1, &eventIdx1, owner1ID, false),
		buildEventCard(card2, &eventIdx2, owner2ID, false))

	// Check if swap is impossible due to Cambia lock *after* revealing
	// REMOVED unused swapIsPossible
	if player1.HasCalledCambia || player2.HasCalledCambia {
		log.Printf("Game %s: King peek revealed cards, but swap is impossible as target player (%s or %s) called Cambia.", g.ID, owner1ID, owner2ID)
		g.fireEventToPlayer(playerID, GameEvent{
			Type:    EventPrivateSpecialFail, // Use generic fail
			Special: "swap_peek_swap",        // Indicate the swap part failed implicitly
			Payload: map[string]interface{}{"message": "Cannot swap cards with a player who has called Cambia."},
			Card1:   buildEventCard(card1, &eventIdx1, owner1ID, false), // Show which cards were involved
			Card2:   buildEventCard(card2, &eventIdx2, owner2ID, false),
		})
		// End the special action here and advance turn because swap isn't possible
		g.SpecialAction = SpecialActionState{}
		g.advanceTurn()
		return // Stop processing this action
	}

	// Reset timer to allow player time to decide swap/skip
	g.ResetTurnTimer()
	// Do NOT advance turn yet. Wait for 'swap_peek_swap' or 'skip'.
}

// doKingSwapDecision handles the second "swap" part of the King's action.
// Assumes lock is held by caller.
func (g *CambiaGame) doKingSwapDecision(playerID uuid.UUID, card1Data, card2Data map[string]interface{}) {
	// Retrieve stored cards from the first step
	card1Stored := g.SpecialAction.Card1
	owner1IDStored := g.SpecialAction.Card1Owner
	card2Stored := g.SpecialAction.Card2
	owner2IDStored := g.SpecialAction.Card2Owner

	if card1Stored == nil || card2Stored == nil {
		// This indicates an internal state error
		log.Printf("Error: King swap decision called, but stored cards are nil for player %s", playerID)
		g.FailSpecialAction(playerID, "Internal error during King swap decision.")
		return
	}

	// Validate the incoming card data matches the stored state
	reqCard1ID, reqOwner1ID, idx1, ok1 := parseCardTarget(card1Data)
	reqCard2ID, reqOwner2ID, idx2, ok2 := parseCardTarget(card2Data)

	// Check if the request matches the stored peeked cards (IDs and Owners must match)
	if !ok1 || !ok2 || reqCard1ID != card1Stored.ID || reqOwner1ID != owner1IDStored ||
		reqCard2ID != card2Stored.ID || reqOwner2ID != owner2IDStored {
		log.Printf("Game %s: King swap decision payload mismatch for player %s. Stored: C1(%s,%s), C2(%s,%s). Received: C1(%s,%s), C2(%s,%s). Failing.",
			g.ID, playerID, card1Stored.ID, owner1IDStored, card2Stored.ID, owner2IDStored, reqCard1ID, reqOwner1ID, reqCard2ID, reqOwner2ID)
		g.FailSpecialAction(playerID, "Payload mismatch during King swap decision.")
		return
	}

	// Find players and indices again (necessary if game state could change)
	player1 := g.getPlayerByID(owner1IDStored)
	player2 := g.getPlayerByID(owner2IDStored)
	if player1 == nil || player2 == nil {
		g.FailSpecialAction(playerID, "Target player(s) not found during King swap decision.")
		return
	}

	// Double-check Cambia lock
	if player1.HasCalledCambia || player2.HasCalledCambia {
		log.Printf("Game %s: Player %s attempted King swap, but target player (%s or %s) called Cambia since peek. Failing.", g.ID, playerID, owner1IDStored, owner2IDStored)

		var eventIdx1Ptr, eventIdx2Ptr *int
		if idx1 != -1 {
			eventIdx1 := idx1
			eventIdx1Ptr = &eventIdx1
		} // Use indices from request
		if idx2 != -1 {
			eventIdx2 := idx2
			eventIdx2Ptr = &eventIdx2
		}

		g.FireEventPrivateSpecialActionFail(playerID, "Cannot swap cards with a player who has called Cambia.", "swap_peek_swap",
			buildEventCard(card1Stored, eventIdx1Ptr, owner1IDStored, false), // Use stored cards for event
			buildEventCard(card2Stored, eventIdx2Ptr, owner2IDStored, false))

		// Fail and advance turn as swap is impossible.
		g.SpecialAction = SpecialActionState{}
		g.advanceTurn()
		return
	}

	// Find actual indices in current hands
	actualIdx1, actualIdx2 := -1, -1
	var found1, found2 bool
	for i, c := range player1.Hand {
		if c.ID == card1Stored.ID {
			actualIdx1 = i
			found1 = true
			break
		}
	}
	for i, c := range player2.Hand {
		if c.ID == card2Stored.ID {
			actualIdx2 = i
			found2 = true
			break
		}
	}

	if !found1 || !found2 || actualIdx1 < 0 || actualIdx2 < 0 {
		log.Printf("Error: Stored cards C1(%s) or C2(%s) not found in hands during King swap decision.", card1Stored.ID, card2Stored.ID)
		g.FailSpecialAction(playerID, "Stored cards not found in hands during King swap decision.")
		return
	}

	// Validate requested indices match actual indices (if provided)
	if idx1 != -1 && idx1 != actualIdx1 {
		log.Printf("Warning: king_swap index mismatch card1")
	}
	if idx2 != -1 && idx2 != actualIdx2 {
		log.Printf("Warning: king_swap index mismatch card2")
	}

	// Perform the swap using actual indices
	player1.Hand[actualIdx1], player2.Hand[actualIdx2] = player2.Hand[actualIdx2], player1.Hand[actualIdx1]
	log.Printf("Game %s: Player %s confirmed King swap between %s (Player %s, Idx %d) and %s (Player %s, Idx %d)",
		g.ID, playerID, card1Stored.ID, owner1IDStored, actualIdx1, card2Stored.ID, owner2IDStored, actualIdx2)
	g.logAction(playerID, "action_special_swap_peek_swap", map[string]interface{}{
		"card1Id": card1Stored.ID, "owner1Id": owner1IDStored, "idx1": actualIdx1,
		"card2Id": card2Stored.ID, "owner2Id": owner2IDStored, "idx2": actualIdx2,
	})

	// Public event indicating the swap occurred
	eventIdx1 := actualIdx1 // Capture for pointer
	eventIdx2 := actualIdx2 // Capture for pointer
	g.FireEventPlayerSpecialAction(playerID, "swap_peek_swap",
		buildEventCard(card2Stored, &eventIdx1, owner1IDStored, false), // Card 2 now at P1's index
		buildEventCard(card1Stored, &eventIdx2, owner2IDStored, false)) // Card 1 now at P2's index

	// No private success event needed for the swap confirmation

	g.SpecialAction = SpecialActionState{} // Clear state
	g.advanceTurn()                        // Advance turn
}
