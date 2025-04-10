// internal/game/game_test.go
package game

import (
	"testing"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/models"
)

// simulateClientAction is a helper that mimics real server flow in minimal form.
// - For single-step actions (draw, discard, replace, snap, cambia), call g.HandlePlayerAction
// - For multi-step actions ("action_special"), call g.ProcessSpecialAction
// This allows test code to unify how we "dispatch" actions, without referencing any WebSocket logic.
func simulateClientAction(g *CambiaGame, userID uuid.UUID, act models.GameAction) {
	switch act.ActionType {
	case "action_special":
		var card1, card2 map[string]interface{}
		if c1, ok := act.Payload["card1"].(map[string]interface{}); ok {
			card1 = c1
		}
		if c2, ok := act.Payload["card2"].(map[string]interface{}); ok {
			card2 = c2
		}
		specialStr, _ := act.Payload["special"].(string)
		g.ProcessSpecialAction(userID, specialStr, card1, card2)

	default:
		// normal single-step
		g.HandlePlayerAction(userID, act)
	}
}

// TestBasicNonCircuitFlow ensures we can create a 2-player game, draw, discard, skip specials, and see correct turn advancement.
func TestBasicNonCircuitFlow(t *testing.T) {
	g := NewCambiaGame()
	playerA := &models.Player{ID: uuid.New(), Connected: true}
	playerB := &models.Player{ID: uuid.New(), Connected: true}
	g.AddPlayer(playerA)
	g.AddPlayer(playerB)

	if len(g.Players) != 2 {
		t.Fatalf("Expected 2 players, got %d", len(g.Players))
	}

	g.Start()
	if !g.Started {
		t.Fatal("Game should be started after g.Start()")
	}
	if len(playerA.Hand) != 4 || len(playerB.Hand) != 4 {
		t.Fatalf("Each player should have 4 cards: A=%d, B=%d", len(playerA.Hand), len(playerB.Hand))
	}

	// Player A draws from the stockpile
	simulateClientAction(g, playerA.ID, models.GameAction{ActionType: "action_draw_stockpile"})
	if playerA.DrawnCard == nil {
		t.Fatal("Player A should have a drawn card after drawing")
	}

	// Player A discards the drawn card
	discardAction := models.GameAction{
		ActionType: "action_discard",
		Payload: map[string]interface{}{
			"id": playerA.DrawnCard.ID.String(),
		},
	}
	simulateClientAction(g, playerA.ID, discardAction)

	// If that discard was a special (7,8,9,10,J,Q,K), the game is waiting for a special action.
	// We'll skip it to guarantee the turn advances.
	// Normally you'd do doPeekSelf, doSwapBlind, etc. but for test determinism we skip.
	g.Mu.Lock()
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerA.ID {
		g.Mu.Unlock()
		skipSpecial := models.GameAction{
			ActionType: "action_special",
			Payload:    map[string]interface{}{"special": "skip"},
		}
		simulateClientAction(g, playerA.ID, skipSpecial)
	} else {
		g.Mu.Unlock()
	}

	// Now the turn should be with player B
	if g.CurrentPlayerIndex != 1 {
		t.Fatalf("Expected CurrentPlayerIndex=1 (playerB). Got %d", g.CurrentPlayerIndex)
	}

	// Player B calls Cambia to end quickly
	cambiaAction := models.GameAction{ActionType: "action_cambia"}
	simulateClientAction(g, playerB.ID, cambiaAction)
	if !g.CambiaCalled {
		t.Fatal("Expected CambiaCalled to be true after B calls cambia")
	}

	// Force end
	g.EndGame()
	if !g.GameOver {
		t.Fatal("GameOver should be true after EndGame")
	}
}

// TestSnapRace confirms that if SnapRace is enabled, only the first snap success is allowed per discard.
func TestSnapRace(t *testing.T) {
	g := NewCambiaGame()
	g.HouseRules.SnapRace = true
	playerA := &models.Player{ID: uuid.New(), Connected: true}
	playerB := &models.Player{ID: uuid.New(), Connected: true}
	g.AddPlayer(playerA)
	g.AddPlayer(playerB)
	g.Start()

	if len(playerA.Hand) == 0 {
		t.Fatal("Expected playerA to have starting cards")
	}
	// Force a known discard
	cardToDiscard := playerA.Hand[0]
	playerA.Hand = playerA.Hand[1:]
	g.DiscardPile = append(g.DiscardPile, cardToDiscard)

	// Give playerB a matching rank to snap
	cardMatch := *cardToDiscard
	cardMatch.ID = uuid.New() // new ID
	playerB.Hand = append(playerB.Hand, &cardMatch)

	// Snap success by B
	snapB := models.GameAction{
		ActionType: "action_snap",
		Payload: map[string]interface{}{
			"id": cardMatch.ID.String(),
		},
	}
	simulateClientAction(g, playerB.ID, snapB)
	if len(g.DiscardPile) != 2 {
		t.Fatalf("Discard pile should be 2 after snap success, got %d", len(g.DiscardPile))
	}

	// Next snap from A with same rank fails automatically (SnapRace is true)
	prevSize := len(g.DiscardPile)
	snapA := models.GameAction{
		ActionType: "action_snap",
		Payload: map[string]interface{}{
			"id": cardToDiscard.ID.String(),
		},
	}
	simulateClientAction(g, playerA.ID, snapA)
	if len(g.DiscardPile) != prevSize {
		t.Fatalf("Discard pile should remain %d after failed snap race attempt", prevSize)
	}
}
