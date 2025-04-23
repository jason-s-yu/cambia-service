// internal/game/game_test.go
package game

import (
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBroadcaster captures game events for testing assertions.
type mockBroadcaster struct {
	mu           sync.Mutex
	allEvents    []GameEvent               // Events broadcast to all players.
	playerEvents map[uuid.UUID][]GameEvent // Events sent privately to specific players.
}

// newMockBroadcaster creates an instance of the mock broadcaster.
func newMockBroadcaster() *mockBroadcaster {
	return &mockBroadcaster{
		playerEvents: make(map[uuid.UUID][]GameEvent),
	}
}

// broadcastFn simulates broadcasting an event to all players.
func (mb *mockBroadcaster) broadcastFn(ev GameEvent) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.allEvents = append(mb.allEvents, ev)
}

// broadcastToPlayerFn simulates sending a private event to a specific player.
func (mb *mockBroadcaster) broadcastToPlayerFn(playerID uuid.UUID, ev GameEvent) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.playerEvents[playerID] = append(mb.playerEvents[playerID], ev)
}

// clear resets the captured events.
func (mb *mockBroadcaster) clear() {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.allEvents = []GameEvent{}
	mb.playerEvents = make(map[uuid.UUID][]GameEvent)
}

// getLastEvent retrieves the last event broadcast to all players.
func (mb *mockBroadcaster) getLastEvent() *GameEvent {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if len(mb.allEvents) == 0 {
		return nil
	}
	return &mb.allEvents[len(mb.allEvents)-1]
}

// getLastPlayerEvent retrieves the last private event sent to a specific player.
func (mb *mockBroadcaster) getLastPlayerEvent(playerID uuid.UUID) *GameEvent {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	events, ok := mb.playerEvents[playerID]
	if !ok || len(events) == 0 {
		return nil
	}
	return &events[len(events)-1]
}

// setupTestGame initializes a CambiaGame instance with mock players and broadcasters for testing.
// It starts the game, bypassing the pre-game timer delay.
func setupTestGame(t *testing.T, numPlayers int, rules *HouseRules) (*CambiaGame, []*models.Player, *mockBroadcaster) {
	g := NewCambiaGame()
	mb := newMockBroadcaster()
	g.BroadcastFn = mb.broadcastFn
	g.BroadcastToPlayerFn = mb.broadcastToPlayerFn

	if rules != nil {
		g.HouseRules = *rules // Apply custom rules if provided.
	}
	// Use a very short turn duration for timeout tests, but allow disabling.
	if g.HouseRules.TurnTimerSec > 0 {
		g.TurnDuration = 100 * time.Millisecond
	} else {
		g.TurnDuration = 0
	}

	players := make([]*models.Player, numPlayers)
	for i := 0; i < numPlayers; i++ {
		player := &models.Player{
			ID:        uuid.New(),
			Connected: true,
			Conn:      nil, // Mock connections are usually not needed for game logic tests.
			// Assign a simple user model for username access.
			User: &models.User{ID: uuid.New(), Username: "Player" + string(rune('A'+i))},
		}
		players[i] = player
		g.AddPlayer(player)
	}

	// Start the game flow.
	g.BeginPreGame()
	// In tests, we often want to skip the pre-game wait.
	// Ensure StartGame is callable directly after BeginPreGame sets the state.
	require.True(t, g.PreGameActive, "PreGame should be active after BeginPreGame")
	g.StartGame() // Immediately transition to started state.
	require.True(t, g.Started, "Game should be marked as started")
	require.False(t, g.PreGameActive, "PreGame should be inactive after StartGame")

	mb.clear() // Clear events generated during setup.

	return g, players, mb
}

// getPlayerIndex finds the index of a player within the game's Players slice.
func getPlayerIndex(g *CambiaGame, playerID uuid.UUID) int {
	for i, p := range g.Players {
		if p.ID == playerID {
			return i
		}
	}
	return -1
}

// TestBasicDrawDiscard verifies the standard draw from stockpile -> discard flow.
func TestBasicDrawDiscard(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, nil)
	playerA := players[0]
	playerB := players[1]

	// --- Turn 1: Player A ---
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID, "Should be Player A's turn initially")

	// Action: A draws from stockpile.
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	require.NotNil(t, playerA.DrawnCard, "Player A should hold a drawn card")

	// Assert Events: Public draw, Private draw.
	lastPublicEvent := mb.getLastEvent()
	require.NotNil(t, lastPublicEvent, "Expected public draw event")
	assert.Equal(t, EventPlayerDrawStockpile, lastPublicEvent.Type)
	assert.Equal(t, playerA.ID, lastPublicEvent.User.ID)
	require.NotNil(t, lastPublicEvent.Card, "Public draw event card missing")
	assert.NotEqual(t, uuid.Nil, lastPublicEvent.Card.ID, "Public draw event card ID missing")

	lastPrivateEvent := mb.getLastPlayerEvent(playerA.ID)
	require.NotNil(t, lastPrivateEvent, "Expected private draw event")
	assert.Equal(t, EventPrivateDrawStockpile, lastPrivateEvent.Type)
	require.NotNil(t, lastPrivateEvent.Card, "Private draw event card missing")
	assert.NotEmpty(t, lastPrivateEvent.Card.Rank, "Private draw event should reveal rank")
	assert.Equal(t, playerA.DrawnCard.ID, lastPrivateEvent.Card.ID) // Verify it's the same card.

	// Action: A discards the drawn card.
	drawnCardID := playerA.DrawnCard.ID
	drawnCardRank := playerA.DrawnCard.Rank
	discardAction := models.GameAction{
		ActionType: "action_discard",
		Payload:    map[string]interface{}{"id": drawnCardID.String()},
	}
	g.HandlePlayerAction(playerA.ID, discardAction)
	require.Nil(t, playerA.DrawnCard, "Player A's drawn card should be cleared after discard")
	require.NotEmpty(t, g.DiscardPile, "Discard pile should not be empty")
	assert.Equal(t, drawnCardID, g.DiscardPile[len(g.DiscardPile)-1].ID, "Discarded card should be top of discard pile")

	// Assert Events: Public discard (details revealed).
	lastPublicEvent = mb.getLastEvent()
	require.NotNil(t, lastPublicEvent, "Expected public discard event")
	assert.Equal(t, EventPlayerDiscard, lastPublicEvent.Type)
	assert.Equal(t, playerA.ID, lastPublicEvent.User.ID)
	require.NotNil(t, lastPublicEvent.Card, "Public discard event card missing")
	assert.Equal(t, drawnCardID, lastPublicEvent.Card.ID)
	assert.NotEmpty(t, lastPublicEvent.Card.Rank, "Public discard event should reveal card details")
	assert.Nil(t, lastPublicEvent.Card.Idx, "Public discard event for drawn card should not have index")

	// Assert State: Check if special action triggered OR turn advanced.
	g.Mu.Lock()
	specialActive := g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID
	currentTurnPlayerIDAfterAction := g.Players[g.CurrentPlayerIndex].ID // Get ID after potential advance.
	g.Mu.Unlock()

	if specialType := rankToSpecial(drawnCardRank); specialType != "" {
		// Expect special action to be active.
		require.True(t, specialActive, "Special action should be active for rank %s", drawnCardRank)
		lastPublicEvent = mb.getLastEvent() // Check for special choice event.
		require.NotNil(t, lastPublicEvent, "Expected special choice event")
		assert.Equal(t, EventPlayerSpecialChoice, lastPublicEvent.Type)
		assert.Equal(t, specialType, lastPublicEvent.Special)
		assert.Equal(t, playerA.ID, currentTurnPlayerIDAfterAction, "Turn should NOT advance yet if special action triggered")

		// Manually skip the special action for test progression.
		g.ProcessSpecialAction(playerA.ID, "skip", nil, nil)

		g.Mu.Lock()
		currentTurnPlayerIDAfterSkip := g.Players[g.CurrentPlayerIndex].ID
		g.Mu.Unlock()
		assert.Equal(t, playerB.ID, currentTurnPlayerIDAfterSkip, "Turn should advance to Player B after skipping special")

	} else {
		// Expect no special action and turn advanced.
		require.False(t, specialActive, "Special action should not be active for rank %s", drawnCardRank)
		assert.Equal(t, playerB.ID, currentTurnPlayerIDAfterAction, "Turn should have advanced directly to Player B")
	}
}

// TestBasicDrawReplace verifies the draw -> replace card flow.
func TestBasicDrawReplace(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, nil)
	playerA := players[0]
	playerB := players[1]
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID, "Should be Player A's turn")

	// Ensure player A has cards.
	require.NotEmpty(t, playerA.Hand, "Player A must have cards in hand")
	originalCardInHand := playerA.Hand[0] // Card at index 0 will be replaced.
	originalHandSize := len(playerA.Hand)

	// Action: A draws.
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	require.NotNil(t, playerA.DrawnCard, "Player A should hold a drawn card")
	drawnCard := playerA.DrawnCard

	mb.clear() // Clear draw events for clarity.

	// Action: A replaces card at index 0 with the drawn card.
	replaceAction := models.GameAction{
		ActionType: "action_replace",
		Payload: map[string]interface{}{
			"id":  originalCardInHand.ID.String(), // ID of the card being replaced.
			"idx": float64(0),                     // Index to replace at (as float64 from JSON).
		},
	}
	g.HandlePlayerAction(playerA.ID, replaceAction)

	// Assert State Changes:
	require.Nil(t, playerA.DrawnCard, "Drawn card should be cleared after replace")
	require.Len(t, playerA.Hand, originalHandSize, "Hand size should remain the same")
	assert.Equal(t, drawnCard.ID, playerA.Hand[0].ID, "Drawn card should now be at index 0 in hand")
	require.NotEmpty(t, g.DiscardPile, "Discard pile should not be empty")
	assert.Equal(t, originalCardInHand.ID, g.DiscardPile[len(g.DiscardPile)-1].ID, "Original card should be on discard pile")

	// Assert Events: Expecting player_discard for the replaced card.
	lastPublicEvent := mb.getLastEvent()
	require.NotNil(t, lastPublicEvent, "Expected public discard event for replaced card")
	assert.Equal(t, EventPlayerDiscard, lastPublicEvent.Type)
	assert.Equal(t, playerA.ID, lastPublicEvent.User.ID)
	require.NotNil(t, lastPublicEvent.Card, "Discard event card missing")
	assert.Equal(t, originalCardInHand.ID, lastPublicEvent.Card.ID)
	require.NotNil(t, lastPublicEvent.Card.Idx, "Discard event for replaced card should include index")
	assert.Equal(t, 0, *lastPublicEvent.Card.Idx) // Verify index.

	// Assert State: Check if special action triggered (based on replaced card) OR turn advanced.
	g.Mu.Lock()
	specialActive := g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID
	currentTurnPlayerIDAfterAction := g.Players[g.CurrentPlayerIndex].ID
	g.Mu.Unlock()

	// Check based on HouseRules.AllowReplaceAbilities (default is false).
	if g.HouseRules.AllowReplaceAbilities {
		if specialType := rankToSpecial(originalCardInHand.Rank); specialType != "" {
			require.True(t, specialActive, "Special action should be active for replaced rank %s if AllowReplaceAbilities is true", originalCardInHand.Rank)
			assert.Equal(t, playerA.ID, currentTurnPlayerIDAfterAction, "Turn should NOT advance yet if special action triggered on replace")
			// Manually skip for test progression.
			g.ProcessSpecialAction(playerA.ID, "skip", nil, nil)
			g.Mu.Lock()
			currentTurnPlayerIDAfterSkip := g.Players[g.CurrentPlayerIndex].ID
			g.Mu.Unlock()
			assert.Equal(t, playerB.ID, currentTurnPlayerIDAfterSkip, "Turn should advance to Player B after skipping special on replace")
		} else {
			require.False(t, specialActive, "Special action should not trigger if replaced card has no ability")
			assert.Equal(t, playerB.ID, currentTurnPlayerIDAfterAction, "Turn should advance if replaced card has no special ability")
		}
	} else {
		// Default behavior: no special ability on replace.
		require.False(t, specialActive, "Special action should NOT be active if AllowReplaceAbilities is false")
		assert.Equal(t, playerB.ID, currentTurnPlayerIDAfterAction, "Turn should advance if AllowReplaceAbilities is false")
	}
}

// TestSnapSuccess verifies a correct snap action.
func TestSnapSuccess(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, nil)
	// playerA := players[0] // Not used directly, but context for discard.
	playerB := players[1] // The snapping player.

	// Setup: Simulate a card (e.g., a '7') being on top of the discard pile.
	cardOnDiscard := &models.Card{ID: uuid.New(), Rank: "7", Suit: "Hearts", Value: 7}
	g.DiscardPile = append(g.DiscardPile, cardOnDiscard)
	g.snapUsedForThisDiscard = false // Ensure snap is allowed for this discard event.

	// Setup: Give Player B a matching '7' in their hand.
	cardToSnap := &models.Card{ID: uuid.New(), Rank: "7", Suit: "Spades", Value: 7}
	playerB.Hand = append(playerB.Hand, cardToSnap)
	initialHandSizeB := len(playerB.Hand)
	initialDiscardSize := len(g.DiscardPile)

	mb.clear() // Clear any setup events.

	// Action: Player B snaps their '7'.
	snapAction := models.GameAction{
		ActionType: "action_snap",
		Payload:    map[string]interface{}{"id": cardToSnap.ID.String()},
	}
	g.HandlePlayerAction(playerB.ID, snapAction) // Snap can happen out of turn.

	// Assert State Changes:
	assert.Len(t, playerB.Hand, initialHandSizeB-1, "Player B hand size should decrease by 1")
	assert.Len(t, g.DiscardPile, initialDiscardSize+1, "Discard pile size should increase by 1")
	assert.Equal(t, cardToSnap.ID, g.DiscardPile[len(g.DiscardPile)-1].ID, "Snapped card should now be top of discard")
	// Check that the card is actually removed from player B's hand.
	_, foundIdx := g.findCardByID(playerB.ID, cardToSnap.ID)
	assert.Equal(t, -1, foundIdx, "Snapped card should no longer be in player B's hand")

	// Assert Events: Expecting player_snap_success.
	lastPublicEvent := mb.getLastEvent()
	require.NotNil(t, lastPublicEvent, "Expected public snap success event")
	assert.Equal(t, EventPlayerSnapSuccess, lastPublicEvent.Type)
	assert.Equal(t, playerB.ID, lastPublicEvent.User.ID)
	require.NotNil(t, lastPublicEvent.Card, "Snap success event card missing")
	assert.Equal(t, cardToSnap.ID, lastPublicEvent.Card.ID)
	assert.Equal(t, cardToSnap.Rank, lastPublicEvent.Card.Rank)
	require.NotNil(t, lastPublicEvent.Card.Idx, "Snap success event should include the original index")
	// The index should be the original index in hand before removal.
	assert.Equal(t, initialHandSizeB-1, *lastPublicEvent.Card.Idx) // Assuming it was the last card added.
}

// TestSnapFailPenalty verifies penalties for incorrect snaps (wrong rank).
func TestSnapFailPenalty(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, &HouseRules{PenaltyDrawCount: 2}) // Use default rules + penalty=2.
	// playerA := players[0] // Context for discard.
	playerB := players[1] // Player attempting snap.
	penaltyCount := g.HouseRules.PenaltyDrawCount
	require.Equal(t, 2, penaltyCount, "Test assumes penalty count is 2")

	// Setup: Card on discard is a '7'.
	discardTop := &models.Card{ID: uuid.New(), Rank: "7", Suit: "Hearts", Value: 7}
	g.DiscardPile = append(g.DiscardPile, discardTop)

	// Setup: Player B has a non-matching card ('8') to attempt snap with.
	cardToAttemptSnap := &models.Card{ID: uuid.New(), Rank: "8", Suit: "Spades", Value: 8}
	playerB.Hand = append(playerB.Hand, cardToAttemptSnap)
	initialHandSizeB := len(playerB.Hand)
	initialStockSize := len(g.Deck) // Track stockpile size.

	mb.clear() // Clear setup events.

	// Action: Player B snaps incorrectly with their '8'.
	snapAction := models.GameAction{
		ActionType: "action_snap",
		Payload:    map[string]interface{}{"id": cardToAttemptSnap.ID.String()},
	}
	g.HandlePlayerAction(playerB.ID, snapAction)

	// Assert State Changes:
	assert.Len(t, playerB.Hand, initialHandSizeB+penaltyCount, "Player B hand size should increase by penalty count")
	assert.Equal(t, initialStockSize-penaltyCount, len(g.Deck), "Stockpile size should decrease by penalty count")
	// Verify original card is still in hand.
	foundCard, _ := g.findCardByID(playerB.ID, cardToAttemptSnap.ID)
	require.NotNil(t, foundCard, "Original card should still be in player B's hand after failed snap")

	// Assert Events: SnapFail (public), SnapPenalty (public) x2, PrivateSnapPenalty (private) x2.
	publicEvents := mb.allEvents
	privateEventsB := mb.playerEvents[playerB.ID]

	// Check public events.
	require.GreaterOrEqual(t, len(publicEvents), 1+penaltyCount, "Expected at least 1 snap fail + penalty public events")
	// 1. Snap Fail Event
	assert.Equal(t, EventPlayerSnapFail, publicEvents[0].Type, "First event should be snap fail")
	assert.Equal(t, playerB.ID, publicEvents[0].User.ID)
	require.NotNil(t, publicEvents[0].Card, "Snap fail event should include attempted card details")
	assert.Equal(t, cardToAttemptSnap.ID, publicEvents[0].Card.ID)
	assert.Equal(t, cardToAttemptSnap.Rank, publicEvents[0].Card.Rank) // Verify details shown on fail.

	// 2. Public Penalty Draw Events
	publicPenaltyEventCount := 0
	for i := 1; i < len(publicEvents); i++ {
		if publicEvents[i].Type == EventPlayerSnapPenalty {
			publicPenaltyEventCount++
			assert.Equal(t, playerB.ID, publicEvents[i].User.ID)
			require.NotNil(t, publicEvents[i].Card, "Public penalty event card missing")
			assert.NotEqual(t, uuid.Nil, publicEvents[i].Card.ID, "Public penalty event card ID missing")
			// Check payload count indicators.
			require.NotNil(t, publicEvents[i].Payload)
			assert.Equal(t, float64(publicPenaltyEventCount), publicEvents[i].Payload["count"]) // JSON numbers are float64.
			assert.Equal(t, float64(penaltyCount), publicEvents[i].Payload["total"])
		}
	}
	assert.Equal(t, penaltyCount, publicPenaltyEventCount, "Expected correct number of public penalty events")

	// Check private events for Player B.
	require.Len(t, privateEventsB, penaltyCount, "Expected correct number of private penalty events for player B")
	for i := 0; i < penaltyCount; i++ {
		assert.Equal(t, EventPrivateSnapPenalty, privateEventsB[i].Type)
		require.NotNil(t, privateEventsB[i].Card, "Private penalty event card missing")
		require.NotNil(t, privateEventsB[i].Card.Idx, "Private penalty event index missing")
		// Index should be the index where the card was added in the hand.
		assert.Equal(t, initialHandSizeB+i, *privateEventsB[i].Card.Idx)
		assert.NotEmpty(t, privateEventsB[i].Card.Rank, "Private penalty event should reveal card details")
		// Check payload count indicators.
		require.NotNil(t, privateEventsB[i].Payload)
		assert.Equal(t, float64(i+1), privateEventsB[i].Payload["count"])
		assert.Equal(t, float64(penaltyCount), privateEventsB[i].Payload["total"])
	}
}

// TestCambiaCallAndEndgame verifies calling Cambia and the subsequent final round logic.
func TestCambiaCallAndEndgame(t *testing.T) {
	g, players, mb := setupTestGame(t, 3, nil) // Use 3 players for realistic final round.
	playerA := players[0]
	playerB := players[1]
	playerC := players[2]

	// --- Turn 1: Player A ---
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID)
	// Simulate A taking a simple turn (draw/discard).
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	if playerA.DrawnCard != nil {
		discardActionA := models.GameAction{ActionType: "action_discard", Payload: map[string]interface{}{"id": playerA.DrawnCard.ID.String()}}
		g.HandlePlayerAction(playerA.ID, discardActionA)
		if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID { // Skip specials.
			g.ProcessSpecialAction(playerA.ID, "skip", nil, nil)
		}
	} else {
		g.advanceTurn() // Advance if draw failed.
	}

	// --- Turn 2: Player B ---
	require.Equal(t, playerB.ID, g.Players[g.CurrentPlayerIndex].ID)
	// Action: B calls Cambia.
	// Need enough turns passed if rule active (default check is simple: TurnID >= num players).
	g.TurnID = len(g.Players) // Force TurnID to be sufficient for Cambia call.
	cambiaAction := models.GameAction{ActionType: "action_cambia"}
	g.HandlePlayerAction(playerB.ID, cambiaAction)

	// Assert State: Cambia called.
	assert.True(t, g.CambiaCalled, "CambiaCalled flag should be true")
	assert.Equal(t, playerB.ID, g.CambiaCallerID, "CambiaCallerID should be Player B")
	playerBModel := g.getPlayerByID(playerB.ID)
	require.NotNil(t, playerBModel)
	assert.True(t, playerBModel.HasCalledCambia, "Player B model should have HasCalledCambia true")

	// Assert Events: Cambia broadcast.
	lastPublicEvent := mb.getLastEvent()
	require.NotNil(t, lastPublicEvent)
	assert.Equal(t, EventPlayerCambia, lastPublicEvent.Type)
	assert.Equal(t, playerB.ID, lastPublicEvent.User.ID)

	// Assert State: Turn advanced to C (player after caller).
	require.Equal(t, playerC.ID, g.Players[g.CurrentPlayerIndex].ID)

	// --- Turn 3: Player C (Final Turn) ---
	require.False(t, g.GameOver, "Game should not be over yet (C's turn)")
	// Simulate C taking a simple turn.
	g.HandlePlayerAction(playerC.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	if playerC.DrawnCard != nil {
		discardActionC := models.GameAction{ActionType: "action_discard", Payload: map[string]interface{}{"id": playerC.DrawnCard.ID.String()}}
		g.HandlePlayerAction(playerC.ID, discardActionC)
		if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerC.ID { // Skip specials.
			g.ProcessSpecialAction(playerC.ID, "skip", nil, nil)
		}
	} else {
		g.advanceTurn() // Advance if draw failed.
	}

	// Assert State: Turn advanced to A (player before caller).
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID)
	require.False(t, g.GameOver, "Game should not be over yet (A's turn)")

	// --- Turn 4: Player A (Final Turn) ---
	// Simulate A taking a simple turn.
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	if playerA.DrawnCard != nil {
		discardActionA := models.GameAction{ActionType: "action_discard", Payload: map[string]interface{}{"id": playerA.DrawnCard.ID.String()}}
		g.HandlePlayerAction(playerA.ID, discardActionA)
		if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID { // Skip specials.
			g.ProcessSpecialAction(playerA.ID, "skip", nil, nil)
		}
	} else {
		g.advanceTurn() // Advance if draw failed.
	}

	// Assert State: Game should end now.
	// The turn attempt moves *past* A. The advanceTurn logic detects that the previous player (A)
	// was the one right before the caller (B), triggering game end.
	assert.True(t, g.GameOver, "Game should be over after the player before the caller finishes their turn")

	// Assert Events: Game End broadcast.
	lastPublicEvent = mb.getLastEvent()
	require.NotNil(t, lastPublicEvent, "Expected game end event")
	assert.Equal(t, EventGameEnd, lastPublicEvent.Type)
	require.NotNil(t, lastPublicEvent.Payload, "Game end payload missing")
	assert.Contains(t, lastPublicEvent.Payload, "scores", "Game end payload missing scores")
	assert.Contains(t, lastPublicEvent.Payload, "winner", "Game end payload missing winner") // Winner ID or Nil UUID string.
}

// TestCambiaLock verifies that swapping with a player who has called Cambia fails.
func TestCambiaLock(t *testing.T) {
	// Setup: Player B calls Cambia, Player A tries to swap with B using J/Q/K.
	g, players, mb := setupTestGame(t, 2, &HouseRules{AllowDrawFromDiscardPile: true})
	playerA := players[0] // Will attempt the swap.
	playerB := players[1] // Will call Cambia.

	// --- Turn 1: Player A (Setup) ---
	// Give A a Jack (Blind Swap card).
	jack := &models.Card{ID: uuid.New(), Rank: "J", Suit: "Clubs", Value: 11}
	// Simulate A drawing and discarding the Jack.
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"}) // Draw anything.
	require.NotNil(t, playerA.DrawnCard)
	discardedCardA := playerA.DrawnCard
	playerA.DrawnCard = jack // Replace drawn with Jack for discard.
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_discard", Payload: map[string]interface{}{"id": jack.ID.String()}})
	g.DiscardPile = append(g.DiscardPile, discardedCardA) // Put original drawn card on discard.
	// Assert A has special choice pending.
	require.True(t, g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID && g.SpecialAction.CardRank == "J")
	// Manually skip A's special for now, turn advances to B.
	g.ProcessSpecialAction(playerA.ID, "skip", nil, nil)

	// --- Turn 2: Player B ---
	require.Equal(t, playerB.ID, g.Players[g.CurrentPlayerIndex].ID, "Should be Player B's turn")
	// Action: B calls Cambia.
	g.TurnID = len(g.Players) // Ensure TurnID allows Cambia call.
	g.HandlePlayerAction(playerB.ID, models.GameAction{ActionType: "action_cambia"})
	require.True(t, g.CambiaCalled)
	playerBModel := g.getPlayerByID(playerB.ID)
	require.NotNil(t, playerBModel)
	require.True(t, playerBModel.HasCalledCambia) // Verify player model flag.

	// --- Turn 3: Player A (Final Turn) ---
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID, "Should be Player A's turn again (final)")
	// Action: A draws and discards a Jack again to trigger special action.
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	require.NotNil(t, playerA.DrawnCard)
	discardedCardA2 := playerA.DrawnCard
	playerA.DrawnCard = jack // Replace drawn with Jack. Need a *new* Jack instance? No, reuse OK for test.
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_discard", Payload: map[string]interface{}{"id": jack.ID.String()}})
	g.DiscardPile = append(g.DiscardPile, discardedCardA2)
	// Assert A has special choice pending again.
	require.True(t, g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID && g.SpecialAction.CardRank == "J")

	mb.clear() // Clear previous events.

	// Action: A attempts blind swap involving B (who called Cambia).
	require.NotEmpty(t, playerA.Hand, "Player A needs cards for swap target")
	require.NotEmpty(t, playerB.Hand, "Player B needs cards for swap target")
	cardA := playerA.Hand[0]
	cardB := playerB.Hand[0]
	swapPayload := map[string]interface{}{
		"special": "swap_blind", // Correct special string for J/Q.
		"card1":   map[string]interface{}{"id": cardA.ID.String(), "idx": float64(0), "user": map[string]interface{}{"id": playerA.ID.String()}},
		"card2":   map[string]interface{}{"id": cardB.ID.String(), "idx": float64(0), "user": map[string]interface{}{"id": playerB.ID.String()}},
	}
	g.ProcessSpecialAction(playerA.ID, "swap_blind", swapPayload["card1"].(map[string]interface{}), swapPayload["card2"].(map[string]interface{}))

	// Assert Failure: Special action remains (player must skip/retry), turn NOT advanced, private fail event sent.
	g.Mu.Lock()
	specialStillActive := g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID
	currentTurnPlayerIDAfterFail := g.Players[g.CurrentPlayerIndex].ID
	g.Mu.Unlock()

	assert.True(t, specialStillActive, "Special action should still be active after failed swap attempt due to Cambia lock")
	assert.Equal(t, playerA.ID, currentTurnPlayerIDAfterFail, "Turn should NOT advance after failed swap attempt")

	// Assert Events: Private fail event for Player A.
	lastPrivateEvent := mb.getLastPlayerEvent(playerA.ID)
	require.NotNil(t, lastPrivateEvent, "Expected a private event for player A")
	assert.Equal(t, EventPrivateSpecialFail, lastPrivateEvent.Type)
	assert.Equal(t, "swap_blind", lastPrivateEvent.Special)
	require.NotNil(t, lastPrivateEvent.Payload)
	assert.Contains(t, lastPrivateEvent.Payload["message"], "called Cambia", "Failure message should mention Cambia lock")
	require.NotNil(t, lastPrivateEvent.Card1, "Fail event should include card1 info")
	require.NotNil(t, lastPrivateEvent.Card2, "Fail event should include card2 info")
	assert.Equal(t, cardA.ID, lastPrivateEvent.Card1.ID)
	assert.Equal(t, cardB.ID, lastPrivateEvent.Card2.ID)

	// Assert State: Cards were NOT actually swapped.
	assert.Equal(t, cardA.ID, playerA.Hand[0].ID, "Card A should not have been swapped")
	assert.Equal(t, cardB.ID, playerB.Hand[0].ID, "Card B should not have been swapped")
}

// TODO: Add tests for King multi-step flow, peek actions, timeouts, different player counts, edge cases.
