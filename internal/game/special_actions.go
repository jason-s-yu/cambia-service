// internal/game/special_actions.go
package game

import (
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/models"
)

// ProcessSpecialAction is the main entry point for multi-step special actions.
// This function handles "peek_self", "peek_other", "swap_blind", "swap_peek",
// "swap_peek_swap", or "skip" as well as verifying the correct rank ("K", "Q", etc.).
//
// To keep WebSocket logic separate, we do not parse JSON here; the WS layer or tests
// should pass `special`, plus optional card1 and card2 maps. We lock the game,
// check if the correct user is in a special action, handle the logic, then unlock.
func (g *CambiaGame) ProcessSpecialAction(
	userID uuid.UUID,
	special string,
	card1 map[string]interface{},
	card2 map[string]interface{},
) {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	if !g.SpecialAction.Active || g.SpecialAction.PlayerID != userID {
		g.FireEventPrivateSpecialActionFail(userID, "No special action in progress")
		return
	}

	rank := g.SpecialAction.CardRank

	// handle "skip"
	if special == "skip" {
		g.SpecialAction = SpecialActionState{}
		g.AdvanceTurn()
		return
	}

	switch rank {
	case "7", "8":
		if special != "peek_self" {
			g.FailSpecialAction(userID, "invalid step for 7/8")
			return
		}
		doPeekSelf(g, userID)
		g.AdvanceTurn()

	case "9", "10":
		if special != "peek_other" {
			g.FailSpecialAction(userID, "invalid step for 9/10")
			return
		}
		doPeekOther(g, userID, card1)
		g.AdvanceTurn()

	case "Q", "J":
		if special != "swap_blind" {
			g.FailSpecialAction(userID, "invalid step for Q/J")
			return
		}
		doSwapBlind(g, userID, card1, card2)
		g.AdvanceTurn()

	case "K":
		if special == "swap_peek" {
			doKingFirstStep(g, userID, card1, card2)
			// No turn advance yet, since it's a two-step flow
		} else if special == "swap_peek_swap" {
			doKingSwapDecision(g, userID, card1, card2)
		} else {
			g.FailSpecialAction(userID, "invalid step for K")
		}

	default:
		g.FailSpecialAction(userID, "unsupported rank")
	}
}

// Below are the same helper methods used by ProcessSpecialAction. They remain private
// and only rely on CambiaGame references, so they're purely "game logic," not WS code.

func doPeekSelf(g *CambiaGame, playerID uuid.UUID) {
	var reveal *models.Card
	for i := range g.Players {
		if g.Players[i].ID == playerID && len(g.Players[i].Hand) > 0 {
			reveal = g.Players[i].Hand[0]
			break
		}
	}
	if reveal == nil {
		g.FailSpecialAction(playerID, "No card in own hand to peek")
		return
	}
	g.FireEventPrivateSuccess(playerID, "peek_self", reveal, nil)
	g.FireEventPlayerSpecialAction(playerID, "peek_self", reveal, nil, nil)
	g.SpecialAction = SpecialActionState{}
}

func doPeekOther(g *CambiaGame, playerID uuid.UUID, card1 map[string]interface{}) {
	targetUserID := parseUserIDFromCard(card1)
	if targetUserID == uuid.Nil {
		g.FailSpecialAction(playerID, "No valid target user for peek_other")
		return
	}
	var reveal *models.Card
	for i := range g.Players {
		if g.Players[i].ID == targetUserID && len(g.Players[i].Hand) > 0 {
			reveal = g.Players[i].Hand[0]
			break
		}
	}
	if reveal == nil {
		g.FailSpecialAction(playerID, "No card in target's hand to peek")
		return
	}
	g.FireEventPrivateSuccess(playerID, "peek_other", reveal, nil)
	g.FireEventPlayerSpecialAction(playerID, "peek_other", &models.Card{ID: reveal.ID}, nil, map[string]interface{}{
		"user": targetUserID.String(),
	})
	g.SpecialAction = SpecialActionState{}
}

func doSwapBlind(g *CambiaGame, playerID uuid.UUID, c1, c2 map[string]interface{}) {
	cardA, userA := pickCardFromMessage(g, c1)
	cardB, userB := pickCardFromMessage(g, c2)
	if cardA == nil || cardB == nil {
		g.FailSpecialAction(playerID, "invalid blind swap targets")
		return
	}
	if g.CambiaCalled && (userA == g.CambiaCallerID || userB == g.CambiaCallerID) {
		g.FailSpecialAction(playerID, "target card belongs to Cambia caller, locked for swap")
		return
	}
	swapTwoCards(g, userA, cardA.ID, userB, cardB.ID)
	g.FireEventPlayerSpecialAction(playerID, "swap_blind",
		&models.Card{ID: cardA.ID}, &models.Card{ID: cardB.ID},
		map[string]interface{}{"userA": userA.String(), "userB": userB.String()})
	g.SpecialAction = SpecialActionState{}
}

func doKingFirstStep(g *CambiaGame, playerID uuid.UUID, c1, c2 map[string]interface{}) {
	cardA, userA := pickCardFromMessage(g, c1)
	cardB, userB := pickCardFromMessage(g, c2)
	if cardA == nil || cardB == nil {
		g.FailSpecialAction(playerID, "invalid king step targets")
		return
	}
	g.SpecialAction.FirstStepDone = true
	g.SpecialAction.Card1 = cardA
	g.SpecialAction.Card1Owner = userA
	g.SpecialAction.Card2 = cardB
	g.SpecialAction.Card2Owner = userB

	g.FireEventPlayerSpecialAction(playerID, "swap_peek_reveal",
		&models.Card{ID: cardA.ID}, &models.Card{ID: cardB.ID},
		map[string]interface{}{"userA": userA.String(), "userB": userB.String()})

	g.FireEventPrivateSuccess(playerID, "swap_peek_reveal", cardA, cardB)
	g.ResetTurnTimer()
}

func doKingSwapDecision(g *CambiaGame, playerID uuid.UUID, c1, c2 map[string]interface{}) {
	cardA := g.SpecialAction.Card1
	cardB := g.SpecialAction.Card2
	userA := g.SpecialAction.Card1Owner
	userB := g.SpecialAction.Card2Owner
	if cardA == nil || cardB == nil {
		g.FailSpecialAction(playerID, "missing stored king cards")
		return
	}
	if g.CambiaCalled && (userA == g.CambiaCallerID || userB == g.CambiaCallerID) {
		g.FailSpecialAction(playerID, "cannot swap locked Cambia caller's cards")
		return
	}
	swapTwoCards(g, userA, cardA.ID, userB, cardB.ID)
	g.FireEventPlayerSpecialAction(playerID, "swap_peek_swap",
		&models.Card{ID: cardA.ID}, &models.Card{ID: cardB.ID},
		map[string]interface{}{"userA": userA.String(), "userB": userB.String()})
	g.SpecialAction = SpecialActionState{}
	g.AdvanceTurn()
}

// parseUserIDFromCard extracts { "user": {"id": "..."} } from card1
func parseUserIDFromCard(c map[string]interface{}) uuid.UUID {
	if c == nil {
		return uuid.Nil
	}
	userObj, _ := c["user"].(map[string]interface{})
	if userObj == nil {
		return uuid.Nil
	}
	uidStr, _ := userObj["id"].(string)
	uid, _ := uuid.Parse(uidStr)
	return uid
}

// pickCardFromMessage and swapTwoCards are identical to your original logic
// but we've placed them here so we can call them from doSwapBlind/doKingFirstStep/etc.

func pickCardFromMessage(g *CambiaGame, cardMap map[string]interface{}) (*models.Card, uuid.UUID) {
	if cardMap == nil {
		return nil, uuid.Nil
	}
	cardIDStr, _ := cardMap["id"].(string)
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil || cardIDStr == "" {
		return nil, uuid.Nil
	}
	var ownerID uuid.UUID
	userData, _ := cardMap["user"].(map[string]interface{})
	if userData != nil {
		if userIDStr, ok := userData["id"].(string); ok {
			if parsed, e2 := uuid.Parse(userIDStr); e2 == nil {
				ownerID = parsed
			}
		}
	}

	var found *models.Card
	for _, pl := range g.Players {
		if pl.ID == ownerID {
			for _, c := range pl.Hand {
				if c.ID == cardID {
					found = c
					break
				}
			}
			break
		}
	}
	return found, ownerID
}

func swapTwoCards(g *CambiaGame, userA uuid.UUID, cardAID uuid.UUID, userB uuid.UUID, cardBID uuid.UUID) {
	var pA, pB *models.Player
	for i := range g.Players {
		if g.Players[i].ID == userA {
			pA = g.Players[i]
		} else if g.Players[i].ID == userB {
			pB = g.Players[i]
		}
	}
	if pA == nil || pB == nil {
		return
	}
	var idxA, idxB = -1, -1
	var cA, cB *models.Card
	for i, c := range pA.Hand {
		if c.ID == cardAID {
			cA = c
			idxA = i
			break
		}
	}
	for j, c := range pB.Hand {
		if c.ID == cardBID {
			cB = c
			idxB = j
			break
		}
	}
	if cA == nil || cB == nil || idxA < 0 || idxB < 0 {
		return
	}
	pA.Hand[idxA], pB.Hand[idxB] = cB, cA
}
