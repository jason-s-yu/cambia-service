// internal/game/special_actions.go
package game

import (
	"fmt"
	"log"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/models"
)

// ProcessSpecialAction handles player requests to use special card abilities (peek, swap)
// or to skip the ability. It routes the request based on the card rank that triggered
// the special action state.
// This function assumes the game lock is HELD by the caller.
func (g *CambiaGame) ProcessSpecialAction(
	userID uuid.UUID,
	special string, // The sub-action requested (e.g., "peek_self", "skip").
	card1Data map[string]interface{}, // Raw map from client payload for card 1 target.
	card2Data map[string]interface{}, // Raw map from client payload for card 2 target.
) {
	// Caller must hold the lock.

	// Verify special action state is active for this player.
	if !g.SpecialAction.Active || g.SpecialAction.PlayerID != userID {
		log.Printf("Game %s: ProcessSpecialAction called by player %s, but no matching special action is active. Ignoring.", g.ID, userID)
		g.FireEventPrivateSpecialActionFail(userID, "No special action in progress for you.", special, nil, nil)
		return
	}

	rank := g.SpecialAction.CardRank // The rank that triggered the action.
	g.logAction(userID, "action_special_received", map[string]interface{}{"special": special, "rank": rank, "card1": card1Data, "card2": card2Data})

	// Handle "skip" universally.
	if special == "skip" {
		g.processSkipSpecialAction(userID)
		return
	}

	// Route based on the triggering rank.
	switch rank {
	case "7", "8":
		if special != "peek_self" {
			g.FailSpecialAction(userID, fmt.Sprintf("Invalid step '%s' for 7/8 special action.", special))
			return
		}
		g.doPeekSelf(userID, card1Data)
		// Turn advanced within doPeekSelf if successful.

	case "9", "T": // 9 and 10 (T).
		if special != "peek_other" {
			g.FailSpecialAction(userID, fmt.Sprintf("Invalid step '%s' for 9/T special action.", special))
			return
		}
		g.doPeekOther(userID, card1Data)
		// Turn advanced within doPeekOther if successful.

	case "J", "Q": // Jack and Queen: Blind Swap.
		if special != "swap_blind" {
			g.FailSpecialAction(userID, fmt.Sprintf("Invalid step '%s' for J/Q special action.", special))
			return
		}
		g.doSwapBlind(userID, card1Data, card2Data)
		// Turn advanced within doSwapBlind if successful.

	case "K": // King: Peek then Swap.
		if special == "swap_peek" {
			// Initial step: Player selects two cards to peek.
			if g.SpecialAction.FirstStepDone {
				g.FailSpecialAction(userID, "Invalid step 'swap_peek' for King action - reveal already done.")
				return
			}
			g.doKingFirstStep(userID, card1Data, card2Data)
			// Does NOT advance turn; waits for swap_peek_swap or skip.
		} else if special == "swap_peek_swap" {
			// Second step: Player decides whether to swap the peeked cards.
			if !g.SpecialAction.FirstStepDone {
				g.FailSpecialAction(userID, "Invalid step 'swap_peek_swap' for King action - must peek first.")
				return
			}
			g.doKingSwapDecision(userID, card1Data, card2Data)
			// Turn advanced within doKingSwapDecision if successful.
		} else {
			g.FailSpecialAction(userID, fmt.Sprintf("Invalid 'special' value '%s' for King action.", special))
		}

	default:
		// Should not happen if rank check was done before activating.
		g.FailSpecialAction(userID, fmt.Sprintf("Unsupported card rank '%s' for special action.", rank))
	}
}

// processSkipSpecialAction handles the "skip" sub-action for any pending special ability.
// Assumes lock is held by caller.
func (g *CambiaGame) processSkipSpecialAction(userID uuid.UUID) {
	if !g.SpecialAction.Active || g.SpecialAction.PlayerID != userID {
		log.Printf("Warning: processSkipSpecialAction called for player %s but state mismatch (Active:%v, Player:%s)", userID, g.SpecialAction.Active, g.SpecialAction.PlayerID)
	}
	rank := g.SpecialAction.CardRank // Get rank before clearing.
	log.Printf("Game %s: Player %s chose to skip special action for rank %s.", g.ID, userID, rank)
	g.logAction(userID, "action_special_skip", map[string]interface{}{"rank": rank})

	// Optionally broadcast a public skip event.
	// g.FireEventPlayerSpecialAction(userID, "skip", nil, nil)

	g.SpecialAction = SpecialActionState{} // Clear the pending action state.
	g.advanceTurn()                        // Advance turn after skipping.
}

// parseCardTarget extracts card ID, owner ID, and index from a client payload map.
// Returns cardID, ownerID, index (-1 if not provided/invalid), ok (bool for basic success).
// Assumes lock is held by caller.
func parseCardTarget(data map[string]interface{}) (cardID uuid.UUID, ownerID uuid.UUID, idx int, ok bool) {
	idx = -1 // Default index.
	cardID = uuid.Nil
	ownerID = uuid.Nil

	if data == nil {
		return // Return default nils and false ok.
	}

	// Card ID (required).
	cardIDStr, idOk := data["id"].(string)
	if !idOk || cardIDStr == "" {
		log.Printf("Debug: parseCardTarget missing or invalid 'id': %v", data["id"])
		return // Missing/invalid card ID.
	}
	var err error
	cardID, err = uuid.Parse(cardIDStr)
	if err != nil {
		log.Printf("Debug: parseCardTarget failed to parse 'id' %s: %v", cardIDStr, err)
		cardID = uuid.Nil
		return // Invalid card ID format.
	}

	// Index (optional).
	idxFloat, idxProvided := data["idx"].(float64) // JSON numbers are float64.
	if idxProvided {
		// Check for fractional part before converting.
		if idxFloat != float64(int(idxFloat)) || idxFloat < 0 {
			log.Printf("Debug: parseCardTarget received invalid index value: %f", idxFloat)
			// Keep idx = -1.
		} else {
			idx = int(idxFloat)
		}
	}

	// Owner User ID (required for targeting others).
	userMap, userProvided := data["user"].(map[string]interface{})
	if userProvided && userMap != nil {
		userIDStr, uidOk := userMap["id"].(string)
		if uidOk && userIDStr != "" {
			ownerID, err = uuid.Parse(userIDStr)
			if err != nil {
				log.Printf("Debug: parseCardTarget failed to parse 'user.id' %s: %v", userIDStr, err)
				ownerID = uuid.Nil // Set to Nil on parse error.
			}
		} else {
			log.Printf("Debug: parseCardTarget missing or invalid 'user.id' within user map: %v", userMap["id"])
		}
	} // If user field not provided, ownerID remains Nil.

	// Basic success if a valid cardID was parsed.
	ok = (cardID != uuid.Nil)
	return
}

// findCardByID locates a card in a specific player's hand by ID.
// Returns the card and its index, or nil and -1 if not found.
// Assumes lock is held by caller.
func (g *CambiaGame) findCardByID(playerID uuid.UUID, cardID uuid.UUID) (*models.Card, int) {
	player := g.getPlayerByID(playerID)
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

// buildEventCard creates an EventCard struct used in event payloads.
// Optionally includes private details (rank, suit, value).
func buildEventCard(card *models.Card, idx *int, ownerID uuid.UUID, includePrivate bool) *EventCard {
	if card == nil {
		return nil
	}
	ec := &EventCard{
		ID:  card.ID,
		Idx: idx, // Pass pointer.
	}
	if ownerID != uuid.Nil {
		ec.User = &EventUser{ID: ownerID}
	}
	if includePrivate {
		ec.Rank = card.Rank
		ec.Suit = card.Suit
		ec.Value = card.Value
	}
	return ec
}

// doPeekSelf handles the 7/8 "Peek Self" action.
// Assumes lock is held by caller.
func (g *CambiaGame) doPeekSelf(playerID uuid.UUID, card1Data map[string]interface{}) {
	cardID, _, reqIdx, ok := parseCardTarget(card1Data) // Owner ID is implicitly self.
	if !ok || cardID == uuid.Nil {
		g.FailSpecialAction(playerID, "Invalid card specified for peek_self.")
		return
	}

	targetCard, actualIdx := g.findCardByID(playerID, cardID)
	if targetCard == nil {
		g.FailSpecialAction(playerID, "Specified card not found in your hand for peek_self.")
		return
	}
	if reqIdx != -1 && reqIdx != actualIdx {
		log.Printf("Warning: peek_self index mismatch (req: %d, actual: %d) for card %s", reqIdx, actualIdx, cardID)
	}

	g.logAction(playerID, "action_special_peek_self", map[string]interface{}{"cardId": targetCard.ID, "idx": actualIdx})

	// Private success event revealing the card.
	eventIdx := actualIdx
	g.FireEventPrivateSuccess(playerID, "peek_self", buildEventCard(targetCard, &eventIdx, playerID, true), nil)

	// Public event showing which card was targeted.
	g.FireEventPlayerSpecialAction(playerID, "peek_self", buildEventCard(targetCard, &eventIdx, playerID, false), nil)

	g.SpecialAction = SpecialActionState{} // Clear state.
	g.advanceTurn()                        // Advance turn.
}

// doPeekOther handles the 9/T "Peek Other" action.
// Assumes lock is held by caller.
func (g *CambiaGame) doPeekOther(playerID uuid.UUID, card1Data map[string]interface{}) {
	cardID, ownerID, reqIdx, ok := parseCardTarget(card1Data)
	if !ok || cardID == uuid.Nil || ownerID == uuid.Nil {
		g.FailSpecialAction(playerID, "Invalid card or target user specified for peek_other.")
		return
	}
	if ownerID == playerID {
		g.FailSpecialAction(playerID, "Cannot use peek_other on yourself.")
		return
	}

	targetCard, actualIdx := g.findCardByID(ownerID, cardID)
	if targetCard == nil {
		g.FailSpecialAction(playerID, "Specified card not found in target player's hand.")
		return
	}
	if reqIdx != -1 && reqIdx != actualIdx {
		log.Printf("Warning: peek_other index mismatch (req: %d, actual: %d) for card %s", reqIdx, actualIdx, cardID)
	}

	g.logAction(playerID, "action_special_peek_other", map[string]interface{}{"targetPlayerId": ownerID, "cardId": targetCard.ID, "idx": actualIdx})

	// Private success event revealing card details to the peeker.
	eventIdx := actualIdx
	g.FireEventPrivateSuccess(playerID, "peek_other", buildEventCard(targetCard, &eventIdx, ownerID, true), nil)

	// Public event showing which card/player was targeted.
	g.FireEventPlayerSpecialAction(playerID, "peek_other", buildEventCard(targetCard, &eventIdx, ownerID, false), nil)

	g.SpecialAction = SpecialActionState{} // Clear state.
	g.advanceTurn()                        // Advance turn.
}

// doSwapBlind handles the J/Q "Blind Swap" action.
// Assumes lock is held by caller.
func (g *CambiaGame) doSwapBlind(playerID uuid.UUID, card1Data, card2Data map[string]interface{}) {
	card1ID, owner1ID, idx1, ok1 := parseCardTarget(card1Data)
	card2ID, owner2ID, idx2, ok2 := parseCardTarget(card2Data)

	if !ok1 || !ok2 || card1ID == uuid.Nil || owner1ID == uuid.Nil || card2ID == uuid.Nil || owner2ID == uuid.Nil {
		g.FailSpecialAction(playerID, "Invalid card or user specification for swap_blind.")
		return
	}
	if card1ID == card2ID {
		g.FailSpecialAction(playerID, "Cannot swap a card with itself.")
		return
	}

	player1 := g.getPlayerByID(owner1ID)
	player2 := g.getPlayerByID(owner2ID)
	if player1 == nil || player2 == nil {
		g.FailSpecialAction(playerID, "One or both target players not found.")
		return
	}

	// Check Cambia lock.
	if player1.HasCalledCambia || player2.HasCalledCambia {
		log.Printf("Game %s: Player %s attempted blind swap involving a player (%s or %s) who called Cambia. Failing.", g.ID, playerID, owner1ID, owner2ID)
		eventIdx1 := idx1 // Use requested indices for failure event.
		eventIdx2 := idx2
		g.FireEventPrivateSpecialActionFail(playerID, "Cannot swap cards with a player who has called Cambia.", "swap_blind",
			buildEventCard(&models.Card{ID: card1ID}, &eventIdx1, owner1ID, false),
			buildEventCard(&models.Card{ID: card2ID}, &eventIdx2, owner2ID, false))
		// Do NOT clear state or advance turn; player must issue a new command (e.g., skip or valid target).
		g.ResetTurnTimer()
		return
	}

	card1, actualIdx1 := g.findCardByID(owner1ID, card1ID)
	card2, actualIdx2 := g.findCardByID(owner2ID, card2ID)
	if card1 == nil || card2 == nil {
		g.FailSpecialAction(playerID, "One or both specified cards not found in target hands.")
		return
	}
	if idx1 != -1 && idx1 != actualIdx1 {
		log.Printf("Warning: swap_blind index mismatch card1 (req: %d, actual: %d)", idx1, actualIdx1)
	}
	if idx2 != -1 && idx2 != actualIdx2 {
		log.Printf("Warning: swap_blind index mismatch card2 (req: %d, actual: %d)", idx2, actualIdx2)
	}

	// Perform the swap using actual indices.
	player1.Hand[actualIdx1], player2.Hand[actualIdx2] = player2.Hand[actualIdx2], player1.Hand[actualIdx1]
	log.Printf("Game %s: Player %s performed blind swap between %s (Player %s, Idx %d) and %s (Player %s, Idx %d)",
		g.ID, playerID, card1ID, owner1ID, actualIdx1, card2ID, owner2ID, actualIdx2)
	g.logAction(playerID, "action_special_swap_blind", map[string]interface{}{
		"card1Id": card1ID, "owner1Id": owner1ID, "idx1": actualIdx1,
		"card2Id": card2ID, "owner2Id": owner2ID, "idx2": actualIdx2,
	})

	// Public event showing which cards/indices/owners were involved.
	eventIdx1 := actualIdx1
	eventIdx2 := actualIdx2
	// The event shows the cards *after* the swap, associated with their new owners/indices.
	g.FireEventPlayerSpecialAction(playerID, "swap_blind",
		buildEventCard(card2, &eventIdx1, owner1ID, false), // Card 2 now at P1's index.
		buildEventCard(card1, &eventIdx2, owner2ID, false)) // Card 1 now at P2's index.

	// No private success event needed for blind swap.

	g.SpecialAction = SpecialActionState{} // Clear state.
	g.advanceTurn()                        // Advance turn.
}

// doKingFirstStep handles the initial "peek" part of the King's action.
// It reveals the two chosen cards to the player and stores them for the potential swap step.
// Assumes lock is held by caller.
func (g *CambiaGame) doKingFirstStep(playerID uuid.UUID, card1Data, card2Data map[string]interface{}) {
	card1ID, owner1ID, idx1, ok1 := parseCardTarget(card1Data)
	card2ID, owner2ID, idx2, ok2 := parseCardTarget(card2Data)

	if !ok1 || !ok2 || card1ID == uuid.Nil || owner1ID == uuid.Nil || card2ID == uuid.Nil || owner2ID == uuid.Nil {
		g.FailSpecialAction(playerID, "Invalid card or user specification for King peek.")
		return
	}
	if card1ID == card2ID {
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
	if idx1 != -1 && idx1 != actualIdx1 {
		log.Printf("Warning: king_peek index mismatch card1 (req: %d, actual: %d)", idx1, actualIdx1)
	}
	if idx2 != -1 && idx2 != actualIdx2 {
		log.Printf("Warning: king_peek index mismatch card2 (req: %d, actual: %d)", idx2, actualIdx2)
	}

	// Store targets for the second step (swap decision).
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

	// Private success event revealing card details to the King user.
	eventIdx1 := actualIdx1
	eventIdx2 := actualIdx2
	g.FireEventPrivateSuccess(playerID, "swap_peek_reveal",
		buildEventCard(card1, &eventIdx1, owner1ID, true),
		buildEventCard(card2, &eventIdx2, owner2ID, true))

	// Public event indicating which cards were targeted for the peek.
	g.FireEventPlayerSpecialAction(playerID, "swap_peek_reveal",
		buildEventCard(card1, &eventIdx1, owner1ID, false),
		buildEventCard(card2, &eventIdx2, owner2ID, false))

	// Check if swap is impossible due to Cambia lock *after* revealing.
	if player1.HasCalledCambia || player2.HasCalledCambia {
		log.Printf("Game %s: King peek revealed cards, but swap is impossible as target player (%s or %s) called Cambia.", g.ID, owner1ID, owner2ID)
		g.fireEventToPlayer(playerID, GameEvent{
			Type:    EventPrivateSpecialFail,
			Special: "swap_peek_swap", // Indicate the swap part failed implicitly.
			Payload: map[string]interface{}{"message": "Cannot swap cards with a player who has called Cambia."},
			Card1:   buildEventCard(card1, &eventIdx1, owner1ID, false),
			Card2:   buildEventCard(card2, &eventIdx2, owner2ID, false),
		})
		// End the special action here and advance turn because swap isn't possible.
		g.SpecialAction = SpecialActionState{}
		g.advanceTurn()
		return
	}

	// Reset timer; wait for 'swap_peek_swap' or 'skip'.
	g.ResetTurnTimer()
}

// doKingSwapDecision handles the second "swap" step of the King's action.
// Validates the request against the stored peeked cards and performs the swap if confirmed.
// Assumes lock is held by caller.
func (g *CambiaGame) doKingSwapDecision(playerID uuid.UUID, card1Data, card2Data map[string]interface{}) {
	// Retrieve stored cards from the first step.
	card1Stored := g.SpecialAction.Card1
	owner1IDStored := g.SpecialAction.Card1Owner
	card2Stored := g.SpecialAction.Card2
	owner2IDStored := g.SpecialAction.Card2Owner

	if card1Stored == nil || card2Stored == nil {
		log.Printf("Error: King swap decision called, but stored cards are nil for player %s", playerID)
		g.FailSpecialAction(playerID, "Internal error during King swap decision.")
		return
	}

	// Validate the incoming card data matches the stored state.
	reqCard1ID, reqOwner1ID, idx1, ok1 := parseCardTarget(card1Data)
	reqCard2ID, reqOwner2ID, idx2, ok2 := parseCardTarget(card2Data)

	if !ok1 || !ok2 || reqCard1ID != card1Stored.ID || reqOwner1ID != owner1IDStored ||
		reqCard2ID != card2Stored.ID || reqOwner2ID != owner2IDStored {
		log.Printf("Game %s: King swap decision payload mismatch for player %s. Stored: C1(%s,%s), C2(%s,%s). Received: C1(%s,%s), C2(%s,%s). Failing.",
			g.ID, playerID, card1Stored.ID, owner1IDStored, card2Stored.ID, owner2IDStored, reqCard1ID, reqOwner1ID, reqCard2ID, reqOwner2ID)
		g.FailSpecialAction(playerID, "Payload mismatch during King swap decision.")
		return
	}

	// Find players and indices again in case state changed (though unlikely with lock held).
	player1 := g.getPlayerByID(owner1IDStored)
	player2 := g.getPlayerByID(owner2IDStored)
	if player1 == nil || player2 == nil {
		g.FailSpecialAction(playerID, "Target player(s) not found during King swap decision.")
		return
	}

	// Double-check Cambia lock.
	if player1.HasCalledCambia || player2.HasCalledCambia {
		log.Printf("Game %s: Player %s attempted King swap, but target player (%s or %s) called Cambia since peek. Failing.", g.ID, playerID, owner1IDStored, owner2IDStored)
		eventIdx1 := idx1 // Use requested indices for fail event.
		eventIdx2 := idx2
		g.FireEventPrivateSpecialActionFail(playerID, "Cannot swap cards with a player who has called Cambia.", "swap_peek_swap",
			buildEventCard(card1Stored, &eventIdx1, owner1IDStored, false),
			buildEventCard(card2Stored, &eventIdx2, owner2IDStored, false))
		g.SpecialAction = SpecialActionState{}
		g.advanceTurn()
		return
	}

	// Find actual indices in current hands.
	actualIdx1, actualIdx2 := -1, -1
	found1, found2 := false, false
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
	if idx1 != -1 && idx1 != actualIdx1 {
		log.Printf("Warning: king_swap index mismatch card1 (req: %d, actual: %d)", idx1, actualIdx1)
	}
	if idx2 != -1 && idx2 != actualIdx2 {
		log.Printf("Warning: king_swap index mismatch card2 (req: %d, actual: %d)", idx2, actualIdx2)
	}

	// Perform the swap using actual indices.
	player1.Hand[actualIdx1], player2.Hand[actualIdx2] = player2.Hand[actualIdx2], player1.Hand[actualIdx1]
	log.Printf("Game %s: Player %s confirmed King swap between %s (Player %s, Idx %d) and %s (Player %s, Idx %d)",
		g.ID, playerID, card1Stored.ID, owner1IDStored, actualIdx1, card2Stored.ID, owner2IDStored, actualIdx2)
	g.logAction(playerID, "action_special_swap_peek_swap", map[string]interface{}{
		"card1Id": card1Stored.ID, "owner1Id": owner1IDStored, "idx1": actualIdx1,
		"card2Id": card2Stored.ID, "owner2Id": owner2IDStored, "idx2": actualIdx2,
	})

	// Public event indicating the swap occurred.
	eventIdx1 := actualIdx1
	eventIdx2 := actualIdx2
	// Event shows cards *after* swap, associated with new owners/indices.
	g.FireEventPlayerSpecialAction(playerID, "swap_peek_swap",
		buildEventCard(card2Stored, &eventIdx1, owner1IDStored, false), // Card 2 now at P1's index.
		buildEventCard(card1Stored, &eventIdx2, owner2IDStored, false)) // Card 1 now at P2's index.

	// No private success event needed for the swap confirmation.

	g.SpecialAction = SpecialActionState{} // Clear state.
	g.advanceTurn()                        // Advance turn.
}
