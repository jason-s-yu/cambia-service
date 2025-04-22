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

// mockBroadcaster collects events instead of sending them over WS.
type mockBroadcaster struct {
	mu           sync.Mutex
	allEvents    []GameEvent               // Events sent to everyone
	playerEvents map[uuid.UUID][]GameEvent // Events sent to specific players
}

func newMockBroadcaster() *mockBroadcaster {
	return &mockBroadcaster{
		playerEvents: make(map[uuid.UUID][]GameEvent),
	}
}

func (mb *mockBroadcaster) broadcastFn(ev GameEvent) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.allEvents = append(mb.allEvents, ev)
}

func (mb *mockBroadcaster) broadcastToPlayerFn(playerID uuid.UUID, ev GameEvent) {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.playerEvents[playerID] = append(mb.playerEvents[playerID], ev)
}

func (mb *mockBroadcaster) clear() {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	mb.allEvents = []GameEvent{}
	mb.playerEvents = make(map[uuid.UUID][]GameEvent)
}

func (mb *mockBroadcaster) getLastEvent() *GameEvent {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	if len(mb.allEvents) == 0 {
		return nil
	}
	return &mb.allEvents[len(mb.allEvents)-1]
}

func (mb *mockBroadcaster) getLastPlayerEvent(playerID uuid.UUID) *GameEvent {
	mb.mu.Lock()
	defer mb.mu.Unlock()
	events, ok := mb.playerEvents[playerID]
	if !ok || len(events) == 0 {
		return nil
	}
	return &events[len(events)-1]
}

// setupTestGame initializes a game with players and mock broadcasters.
func setupTestGame(t *testing.T, numPlayers int, rules *HouseRules) (*CambiaGame, []*models.Player, *mockBroadcaster) {
	g := NewCambiaGame()
	mb := newMockBroadcaster()
	g.BroadcastFn = mb.broadcastFn
	g.BroadcastToPlayerFn = mb.broadcastToPlayerFn

	if rules != nil {
		g.HouseRules = *rules
	}
	g.TurnDuration = 100 * time.Millisecond // Short timer for testing timeouts

	players := make([]*models.Player, numPlayers)
	for i := 0; i < numPlayers; i++ {
		// Using mock connections for testing purposes
		player := &models.Player{
			ID:        uuid.New(),
			Connected: true,
			// Mock websocket.Conn if needed for specific tests, otherwise nil is fine
			Conn: nil, // Mocking conn behavior is complex, focus on game logic
		}
		players[i] = player
		g.AddPlayer(player) // Adds player to game
	}

	// Manually start pregame for testing control, wait briefly for timer setup
	g.BeginPreGame()
	time.Sleep(10 * time.Millisecond) // Give timer a moment

	// Directly call StartGame to bypass pregame timer wait in tests
	g.StartGame()
	require.True(t, g.Started, "Game should be marked as started")
	require.False(t, g.PreGameActive, "Pregame should be inactive after StartGame")

	mb.clear() // Clear events from setup phase

	return g, players, mb
}

// Helper to get player index
func getPlayerIndex(g *CambiaGame, playerID uuid.UUID) int {
	for i, p := range g.Players {
		if p.ID == playerID {
			return i
		}
	}
	return -1
}

// TestBasicDrawDiscard tests the standard draw->discard flow.
func TestBasicDrawDiscard(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, nil)
	playerA := players[0]
	playerB := players[1] // Keep track of player B for turn check

	// A's turn
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID, "Should be Player A's turn")

	// A draws from stockpile
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	require.NotNil(t, playerA.DrawnCard, "Player A should have a drawn card")

	// Check events
	lastPublicEvent := mb.getLastEvent()
	require.NotNil(t, lastPublicEvent)
	assert.Equal(t, EventPlayerDrawStockpile, lastPublicEvent.Type)
	assert.Equal(t, playerA.ID, lastPublicEvent.User.ID)
	assert.NotNil(t, lastPublicEvent.Card) // Public event has card ID

	lastPrivateEvent := mb.getLastPlayerEvent(playerA.ID)
	require.NotNil(t, lastPrivateEvent)
	assert.Equal(t, EventPrivateDrawStockpile, lastPrivateEvent.Type)
	require.NotNil(t, lastPrivateEvent.Card)
	assert.NotEmpty(t, lastPrivateEvent.Card.Rank, "Private event should have card rank") // Check private details

	// A discards the drawn card
	drawnCardID := playerA.DrawnCard.ID
	drawnCardRank := playerA.DrawnCard.Rank // Store rank for special check later
	discardAction := models.GameAction{
		ActionType: "action_discard",
		Payload:    map[string]interface{}{"id": drawnCardID.String()},
	}
	g.HandlePlayerAction(playerA.ID, discardAction)
	require.Nil(t, playerA.DrawnCard, "Player A's drawn card should be nil after discard")
	assert.Equal(t, drawnCardID, g.DiscardPile[len(g.DiscardPile)-1].ID, "Discarded card should be top of discard pile")

	// Check discard event
	lastPublicEvent = mb.getLastEvent()
	require.NotNil(t, lastPublicEvent)
	assert.Equal(t, EventPlayerDiscard, lastPublicEvent.Type)
	assert.Equal(t, playerA.ID, lastPublicEvent.User.ID)
	require.NotNil(t, lastPublicEvent.Card)
	assert.Equal(t, drawnCardID, lastPublicEvent.Card.ID)
	assert.NotEmpty(t, lastPublicEvent.Card.Rank, "Public discard event should reveal card details")

	// Check if special action triggered OR turn advanced
	g.Mu.Lock()
	specialActive := g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID
	currentTurnPlayerID := g.Players[g.CurrentPlayerIndex].ID
	g.Mu.Unlock()

	if specialType := rankToSpecial(drawnCardRank); specialType != "" {
		// Special action should be active
		require.True(t, specialActive, "Special action should be active for rank %s", drawnCardRank)
		lastPublicEvent = mb.getLastEvent() // Special choice event
		require.NotNil(t, lastPublicEvent)
		assert.Equal(t, EventPlayerSpecialChoice, lastPublicEvent.Type)
		assert.Equal(t, specialType, lastPublicEvent.Special)

		// Manually skip the special action for the test
		skipAction := models.GameAction{ActionType: "action_special", Payload: map[string]interface{}{"special": "skip"}}
		g.ProcessSpecialAction(playerA.ID, "skip", nil, nil) // Call ProcessSpecialAction directly

		g.Mu.Lock()
		currentTurnPlayerID = g.Players[g.CurrentPlayerIndex].ID
		g.Mu.Unlock()
		assert.Equal(t, playerB.ID, currentTurnPlayerID, "Turn should advance to Player B after skipping special")

	} else {
		// No special action, turn should have advanced directly
		require.False(t, specialActive, "Special action should not be active for rank %s", drawnCardRank)
		assert.Equal(t, playerB.ID, currentTurnPlayerID, "Turn should advance to Player B")
	}
}

// TestBasicDrawReplace tests draw -> replace flow.
func TestBasicDrawReplace(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, nil)
	playerA := players[0]
	playerB := players[1]
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID) // Verify A's turn

	// Ensure player A has cards
	require.NotEmpty(t, playerA.Hand, "Player A must have cards in hand")
	originalCardInHand := playerA.Hand[0]
	originalHandSize := len(playerA.Hand)

	// A draws
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	require.NotNil(t, playerA.DrawnCard)
	drawnCard := playerA.DrawnCard

	mb.clear() // Clear draw events

	// A replaces card at index 0
	replaceAction := models.GameAction{
		ActionType: "action_replace",
		Payload: map[string]interface{}{
			"id":  originalCardInHand.ID.String(), // ID of the card being replaced
			"idx": float64(0),                     // Index to replace at
		},
	}
	g.HandlePlayerAction(playerA.ID, replaceAction)

	// Verify state changes
	require.Nil(t, playerA.DrawnCard, "Drawn card should be nil after replace")
	require.Len(t, playerA.Hand, originalHandSize, "Hand size should remain the same")
	assert.Equal(t, drawnCard.ID, playerA.Hand[0].ID, "Drawn card should now be at index 0")
	require.NotEmpty(t, g.DiscardPile)
	assert.Equal(t, originalCardInHand.ID, g.DiscardPile[len(g.DiscardPile)-1].ID, "Original card should be on discard pile")

	// Check events: Expecting player_discard for the replaced card
	lastPublicEvent := mb.getLastEvent()
	require.NotNil(t, lastPublicEvent)
	assert.Equal(t, EventPlayerDiscard, lastPublicEvent.Type)
	assert.Equal(t, playerA.ID, lastPublicEvent.User.ID)
	require.NotNil(t, lastPublicEvent.Card)
	assert.Equal(t, originalCardInHand.ID, lastPublicEvent.Card.ID)
	require.NotNil(t, lastPublicEvent.Card.Idx, "Discard event for replaced card should include index")
	assert.Equal(t, 0, *lastPublicEvent.Card.Idx)

	// Check if special action triggered (based on replaced card) OR turn advanced
	g.Mu.Lock()
	specialActive := g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID
	currentTurnPlayerID := g.Players[g.CurrentPlayerIndex].ID
	g.Mu.Unlock()

	// Check based on HouseRules.AllowReplaceAbilities (default is false)
	if g.HouseRules.AllowReplaceAbilities {
		if specialType := rankToSpecial(originalCardInHand.Rank); specialType != "" {
			require.True(t, specialActive, "Special action should be active for replaced rank %s if AllowReplaceAbilities is true", originalCardInHand.Rank)
			// ... handle skipping special action ...
			g.ProcessSpecialAction(playerA.ID, "skip", nil, nil)
			g.Mu.Lock()
			currentTurnPlayerID = g.Players[g.CurrentPlayerIndex].ID
			g.Mu.Unlock()
			assert.Equal(t, playerB.ID, currentTurnPlayerID, "Turn should advance to Player B after skipping special on replace")
		} else {
			require.False(t, specialActive)
			assert.Equal(t, playerB.ID, currentTurnPlayerID, "Turn should advance if replaced card has no special ability")
		}
	} else {
		// Default behavior: no special ability on replace
		require.False(t, specialActive, "Special action should NOT be active if AllowReplaceAbilities is false")
		assert.Equal(t, playerB.ID, currentTurnPlayerID, "Turn should advance if AllowReplaceAbilities is false")
	}
}

// TestSnapSuccess tests a successful snap.
func TestSnapSuccess(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, nil)
	playerA := players[0]
	playerB := players[1]

	// Setup: Player A discards a card (e.g., a '7')
	cardToDiscard := &models.Card{ID: uuid.New(), Rank: "7", Suit: "Hearts", Value: 7}
	g.DiscardPile = append(g.DiscardPile, cardToDiscard)
	g.snapUsedForThisDiscard = false // Ensure snap is allowed

	// Setup: Player B has a matching card ('7')
	cardToSnap := &models.Card{ID: uuid.New(), Rank: "7", Suit: "Spades", Value: 7}
	playerB.Hand = append(playerB.Hand, cardToSnap)
	initialHandSizeB := len(playerB.Hand)
	initialDiscardSize := len(g.DiscardPile)

	mb.clear() // Clear setup events

	// Player B snaps
	snapAction := models.GameAction{
		ActionType: "action_snap",
		Payload:    map[string]interface{}{"id": cardToSnap.ID.String()},
	}
	g.HandlePlayerAction(playerB.ID, snapAction) // Snap is out of turn

	// Verify state
	assert.Len(t, playerB.Hand, initialHandSizeB-1, "Player B hand size should decrease")
	assert.Len(t, g.DiscardPile, initialDiscardSize+1, "Discard pile size should increase")
	assert.Equal(t, cardToSnap.ID, g.DiscardPile[len(g.DiscardPile)-1].ID, "Snapped card should be top of discard")

	// Verify event
	lastPublicEvent := mb.getLastEvent()
	require.NotNil(t, lastPublicEvent)
	assert.Equal(t, EventPlayerSnapSuccess, lastPublicEvent.Type)
	assert.Equal(t, playerB.ID, lastPublicEvent.User.ID)
	require.NotNil(t, lastPublicEvent.Card)
	assert.Equal(t, cardToSnap.ID, lastPublicEvent.Card.ID)
	assert.Equal(t, cardToSnap.Rank, lastPublicEvent.Card.Rank)
	require.NotNil(t, lastPublicEvent.Card.Idx, "Snap success event should have index")
}

// TestSnapFail tests a failed snap (wrong rank) and penalty.
func TestSnapFailPenalty(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, nil)
	playerA := players[0]
	playerB := players[1]
	defaultPenalty := 2 // Assuming default penalty

	// Setup: Discard top is '7'
	discardTop := &models.Card{ID: uuid.New(), Rank: "7", Suit: "Hearts", Value: 7}
	g.DiscardPile = append(g.DiscardPile, discardTop)

	// Setup: Player B has a non-matching card ('8')
	cardToSnap := &models.Card{ID: uuid.New(), Rank: "8", Suit: "Spades", Value: 8}
	playerB.Hand = append(playerB.Hand, cardToSnap)
	initialHandSizeB := len(playerB.Hand)
	initialStockSize := len(g.Deck)

	mb.clear() // Clear setup events

	// Player B snaps incorrectly
	snapAction := models.GameAction{
		ActionType: "action_snap",
		Payload:    map[string]interface{}{"id": cardToSnap.ID.String()},
	}
	g.HandlePlayerAction(playerB.ID, snapAction)

	// Verify state: Card remains in hand, penalty cards drawn
	assert.Len(t, playerB.Hand, initialHandSizeB+defaultPenalty, "Player B hand size should increase by penalty count")
	assert.Equal(t, initialStockSize-defaultPenalty, len(g.Deck), "Stockpile size should decrease by penalty count")
	foundOriginalCard := false
	for _, c := range playerB.Hand {
		if c.ID == cardToSnap.ID {
			foundOriginalCard = true
			break
		}
	}
	assert.True(t, foundOriginalCard, "Original card should still be in player B's hand")

	// Verify events: SnapFail, SnapPenalty (public), SnapPenalty (private)
	publicEvents := mb.allEvents
	privateEventsB := mb.playerEvents[playerB.ID]

	require.GreaterOrEqual(t, len(publicEvents), 1+defaultPenalty, "Expected at least 1 snap fail + penalty public events")
	assert.Equal(t, EventPlayerSnapFail, publicEvents[0].Type, "First event should be snap fail")
	assert.Equal(t, playerB.ID, publicEvents[0].User.ID)
	require.NotNil(t, publicEvents[0].Card, "Snap fail event should include attempted card details")
	assert.Equal(t, cardToSnap.ID, publicEvents[0].Card.ID)

	penaltyEventCount := 0
	for i := 1; i < len(publicEvents); i++ {
		if publicEvents[i].Type == EventPlayerSnapPenalty {
			penaltyEventCount++
			assert.Equal(t, playerB.ID, publicEvents[i].User.ID) // Should be User, not Player field
			assert.NotNil(t, publicEvents[i].Card)               // Public penalty reveals card ID
		}
	}
	assert.Equal(t, defaultPenalty, penaltyEventCount, "Expected correct number of public penalty events")

	require.Len(t, privateEventsB, defaultPenalty, "Expected correct number of private penalty events for player B")
	for i := 0; i < defaultPenalty; i++ {
		assert.Equal(t, EventPrivateSnapPenalty, privateEventsB[i].Type)
		require.NotNil(t, privateEventsB[i].Card)
		require.NotNil(t, privateEventsB[i].Card.Idx)
		assert.Equal(t, initialHandSizeB-1+i, *privateEventsB[i].Card.Idx, "Private penalty event index mismatch") // Index should be relative to hand *before* adding penalties? No, absolute index.
		assert.NotEmpty(t, privateEventsB[i].Card.Rank, "Private penalty event should reveal card details")
	}
}

// TestCambiaCallAndEndgame tests calling Cambia and the final round logic.
func TestCambiaCallAndEndgame(t *testing.T) {
	g, players, mb := setupTestGame(t, 3, nil) // Use 3 players
	playerA := players[0]
	playerB := players[1]
	playerC := players[2]

	// A's turn, does nothing (advances turn via timeout or action)
	g.advanceTurn() // Manually advance past A
	require.Equal(t, playerB.ID, g.Players[g.CurrentPlayerIndex].ID)

	// B's turn: Calls Cambia
	cambiaAction := models.GameAction{ActionType: "action_cambia"}
	g.HandlePlayerAction(playerB.ID, cambiaAction)

	// Verify state
	assert.True(t, g.CambiaCalled, "CambiaCalled flag should be true")
	assert.Equal(t, playerB.ID, g.CambiaCallerID, "CambiaCallerID should be Player B")
	playerBModel := g.getPlayerByID(playerB.ID)
	require.NotNil(t, playerBModel)
	assert.True(t, playerBModel.HasCalledCambia, "Player B model should have HasCalledCambia true")

	// Verify event
	lastPublicEvent := mb.getLastEvent()
	require.NotNil(t, lastPublicEvent)
	assert.Equal(t, EventPlayerCambia, lastPublicEvent.Type)
	assert.Equal(t, playerB.ID, lastPublicEvent.User.ID)

	// Verify turn advanced to C (player after caller)
	require.Equal(t, playerC.ID, g.Players[g.CurrentPlayerIndex].ID)

	// C takes their final turn (e.g., draw and discard)
	g.HandlePlayerAction(playerC.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	if playerC.DrawnCard != nil { // Need to handle nil card draw
		discardActionC := models.GameAction{
			ActionType: "action_discard",
			Payload:    map[string]interface{}{"id": playerC.DrawnCard.ID.String()},
		}
		g.HandlePlayerAction(playerC.ID, discardActionC)
		// If special action, skip it
		if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerC.ID {
			g.ProcessSpecialAction(playerC.ID, "skip", nil, nil)
		}
	} else {
		g.advanceTurn() // Advance if no card could be drawn/discarded
	}

	// Verify turn advanced to A (player before caller)
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID)
	require.False(t, g.GameOver, "Game should not be over yet")

	// A takes their final turn
	g.HandlePlayerAction(playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	if playerA.DrawnCard != nil { // Need to handle nil card draw
		discardActionA := models.GameAction{
			ActionType: "action_discard",
			Payload:    map[string]interface{}{"id": playerA.DrawnCard.ID.String()},
		}
		g.HandlePlayerAction(playerA.ID, discardActionA)
		// If special action, skip it
		if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID {
			g.ProcessSpecialAction(playerA.ID, "skip", nil, nil)
		}
	} else {
		g.advanceTurn() // Advance if no card could be drawn/discarded
	}

	// Game should end now as the turn passed back to the player *after* the caller (B)
	// because A's turn completion triggers the check.
	assert.True(t, g.GameOver, "Game should be over after final player's turn")

	// Verify game end event
	lastPublicEvent = mb.getLastEvent()
	require.NotNil(t, lastPublicEvent)
	assert.Equal(t, EventGameEnd, lastPublicEvent.Type)
	assert.NotNil(t, lastPublicEvent.Payload)
	assert.Contains(t, lastPublicEvent.Payload, "scores")
	assert.Contains(t, lastPublicEvent.Payload, "winner") // Winner ID or Nil UUID string
}

// TestCambiaLock tests that swapping with a player who called Cambia fails.
func TestCambiaLock(t *testing.T) {
	g, players, mb := setupTestGame(t, 2, nil)
	playerA := players[0] // Will use swap_blind
	playerB := players[1] // Will call Cambia

	// Give A a Jack
	jack := &models.Card{ID: uuid.New(), Rank: "J", Suit: "Clubs", Value: 11}
	g.DiscardPile = append(g.DiscardPile, jack)  // Put Jack on discard
	g.HouseRules.AllowDrawFromDiscardPile = true // Allow drawing it

	// B's turn: Calls Cambia
	g.CurrentPlayerIndex = getPlayerIndex(g, playerB.ID)
	g.HandlePlayerAction(playerB.ID, models.GameAction{ActionType: "action_cambia"})
	require.True(t, g.CambiaCalled)
	require.True(t, playerB.HasCalledCambia)
	require.Equal(t, playerA.ID, g.Players[g.CurrentPlayerIndex].ID) // Turn is now A's

	// A draws the Jack from discard
	drawAction := models.GameAction{ActionType: "action_draw_discardpile"}
	g.HandlePlayerAction(playerA.ID, drawAction)
	require.NotNil(t, playerA.DrawnCard)
	require.Equal(t, "J", playerA.DrawnCard.Rank)

	// A discards the Jack to trigger special action
	discardAction := models.GameAction{ActionType: "action_discard", Payload: map[string]interface{}{"id": playerA.DrawnCard.ID.String()}}
	g.HandlePlayerAction(playerA.ID, discardAction)
	require.True(t, g.SpecialAction.Active)
	require.Equal(t, playerA.ID, g.SpecialAction.PlayerID)
	require.Equal(t, "J", g.SpecialAction.CardRank)

	mb.clear() // Clear previous events

	// A attempts blind swap involving B (who called Cambia)
	cardA := playerA.Hand[0]
	cardB := playerB.Hand[0]
	swapPayload := map[string]interface{}{
		"special": "swap_blind",
		"card1": map[string]interface{}{
			"id":   cardA.ID.String(),
			"idx":  float64(0),
			"user": map[string]interface{}{"id": playerA.ID.String()},
		},
		"card2": map[string]interface{}{
			"id":   cardB.ID.String(),
			"idx":  float64(0),
			"user": map[string]interface{}{"id": playerB.ID.String()},
		},
	}
	g.ProcessSpecialAction(playerA.ID, "swap_blind", swapPayload["card1"].(map[string]interface{}), swapPayload["card2"].(map[string]interface{}))

	// Verify failure: Special action should be cleared, turn advanced, private fail event sent
	assert.False(t, g.SpecialAction.Active, "Special action should be cleared after failed swap attempt")
	// Check if turn advanced (should advance after failed swap)
	// Assuming turn advances back to B (caller), triggering game end. Need careful check here.
	// If the game ends immediately, CurrentPlayerIndex might not be B.
	// Let's check if game ended
	assert.True(t, g.GameOver, "Game should end after A's turn completes (following failed swap)")

	// Verify private fail event
	lastPrivateEvent := mb.getLastPlayerEvent(playerA.ID)
	require.NotNil(t, lastPrivateEvent, "Expected a private event for player A")
	assert.Equal(t, EventPrivateSpecialFail, lastPrivateEvent.Type)
	assert.Equal(t, "swap_blind", lastPrivateEvent.Special)
	assert.Contains(t, lastPrivateEvent.Payload["message"], "called Cambia")
	assert.NotNil(t, lastPrivateEvent.Card1) // Should include attempted cards
	assert.NotNil(t, lastPrivateEvent.Card2)
	assert.Equal(t, cardA.ID, lastPrivateEvent.Card1.ID)
	assert.Equal(t, cardB.ID, lastPrivateEvent.Card2.ID)

	// Verify cards were NOT actually swapped
	assert.Equal(t, cardA.ID, playerA.Hand[0].ID, "Card A should not have been swapped")
	assert.Equal(t, cardB.ID, playerB.Hand[0].ID, "Card B should not have been swapped")
}

// Add tests for King multi-step flow, peek actions, timeouts, etc.
