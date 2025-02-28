// internal/handlers/game_ws.go
package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/game"
	"github.com/jason-s-yu/cambia/internal/models"
	"github.com/sirupsen/logrus"
)

// GameWSHandler sets up the WebSocket at /game/ws/{game_id}, subprotocol "game".
func GameWSHandler(logger *logrus.Logger, gs *GameServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// parse game_id from path
		pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/game/ws/"), "/")
		if len(pathParts) < 1 {
			http.Error(w, "missing game_id", http.StatusBadRequest)
			return
		}
		gameIDStr := pathParts[0]
		gameID, err := uuid.Parse(gameIDStr)
		if err != nil {
			http.Error(w, "invalid game_id", http.StatusBadRequest)
			return
		}

		g, ok := gs.GameStore.GetGame(gameID)
		if !ok {
			http.Error(w, "game not found", http.StatusNotFound)
			return
		}

		// if not set, set the broadcast callback
		if g.BroadcastFn == nil {
			g.BroadcastFn = func(ev game.GameEvent) {
				// broadcast to all players
				for _, pl := range g.Players {
					if pl.Conn != nil {
						data, _ := json.Marshal(ev)
						pl.Conn.Write(context.Background(), websocket.MessageText, data)
					}
				}
			}
		}

		// upgrade to websocket with subprotocol=game
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols: []string{"game"},
		})
		if err != nil {
			logger.Warnf("websocket accept error: %v", err)
			return
		}
		if c.Subprotocol() != "game" {
			c.Close(websocket.StatusPolicyViolation, "client must speak the game subprotocol")
			return
		}

		// authenticate user by cookie
		cookieToken := extractCookieToken(r.Header.Get("Cookie"), "auth_token")
		userIDStr, err := auth.AuthenticateJWT(cookieToken)
		if err != nil {
			logger.Warnf("invalid token: %v", err)
			c.Close(websocket.StatusPolicyViolation, "invalid auth_token")
			return
		}
		userUUID, err := uuid.Parse(userIDStr)
		if err != nil {
			logger.Warnf("invalid user UUID parse: %v", err)
			c.Close(websocket.StatusPolicyViolation, "invalid user ID")
			return
		}

		// attach player
		p := &models.Player{
			ID:        userUUID,
			Hand:      []*models.Card{},
			Connected: true,
			Conn:      c,
		}
		g.AddPlayer(p)
		logger.Infof("User %v joined game %v via WS", userUUID, gameID)

		ctx, _ := context.WithCancel(r.Context())
		go readGameMessages(ctx, g, p, logger)
	}
}

// readGameMessages listens for JSON messages from a single player's WS.
// We parse the "type" field and any "payload" to handle "action_*" commands.
func readGameMessages(ctx context.Context, g *game.CambiaGame, p *models.Player, logger *logrus.Logger) {
	defer func() {
		p.Conn.Close(websocket.StatusNormalClosure, "closing")
		g.HandleDisconnect(p.ID)
	}()

	for {
		typ, data, err := p.Conn.Read(ctx)
		if err != nil {
			logger.Infof("user %v read err: %v", p.ID, err)
			return
		}
		if typ != websocket.MessageText {
			continue
		}

		var msg GameMessage
		if e := json.Unmarshal(data, &msg); e != nil {
			logger.Warnf("invalid json from user %v: %v", p.ID, e)
			continue
		}

		switch msg.Type {
		case "action_snap", "action_draw_stockpile", "action_draw_discard",
			"action_discard", "action_replace", "action_cambia":
			handleSimpleAction(g, p.ID, GameMessage(msg), logger)

		case "action_special":
			handleSpecialAction(g, p.ID, msg, logger)

		case "ping":
			_ = p.Conn.Write(ctx, websocket.MessageText, []byte(`{"action":"pong"}`))

		default:
			logger.Warnf("Unknown game action '%s' from user %v", msg.Type, p.ID)
		}
	}
}

// handleSimpleAction processes one-step actions: snap, draw, discard, replace, cambia.
type GameMessage struct {
	Type    string
	Payload map[string]interface{}
	Card    map[string]interface{}
	Card1   map[string]interface{}
	Card2   map[string]interface{}
	Special string
}

func handleSimpleAction(g *game.CambiaGame, playerID uuid.UUID, msg GameMessage, logger *logrus.Logger) {
	a := models.GameAction{ActionType: msg.Type, Payload: map[string]interface{}{}}
	if msg.Card != nil {
		if idStr, ok := msg.Card["id"].(string); ok && idStr != "" {
			a.Payload["id"] = idStr
		}
		if idxVal, ok := msg.Card["idx"].(float64); ok {
			a.Payload["idx"] = idxVal
		}
	}
	g.HandlePlayerAction(playerID, a)
}

// handleSpecialAction processes multi-step logic for K, Q, J, 7,8,9,10
func handleSpecialAction(g *game.CambiaGame, playerID uuid.UUID, msg struct {
	Type    string
	Payload map[string]interface{}
	Card    map[string]interface{}
	Card1   map[string]interface{}
	Card2   map[string]interface{}
	Special string
}, logger *logrus.Logger) {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	// verify there's a special in progress for this player
	if !g.SpecialAction.Active || g.SpecialAction.PlayerID != playerID {
		logger.Infof("Player %v tried special action but none in progress.", playerID)
		g.FireEventPrivateSpecialActionFail(playerID, "No special action in progress")
		return
	}

	rank := g.SpecialAction.CardRank
	curStep := msg.Special
	if curStep == "skip" {
		logger.Infof("Player %v skipped special card %v", playerID, rank)
		g.SpecialAction = game.SpecialActionState{}
		g.AdvanceTurn() // use exported function
		return
	}

	switch rank {
	case "7", "8":
		if curStep != "peek_self" {
			g.FailSpecialAction(playerID, "invalid special step for 7/8")
			return
		}
		doPeekSelf(g, playerID)
		g.AdvanceTurn()
		return

	case "9", "10":
		if curStep != "peek_other" {
			g.FailSpecialAction(playerID, "invalid special step for 9/10")
			return
		}
		doPeekOther(g, playerID, msg.Card1)
		g.AdvanceTurn()
		return

	case "Q", "J":
		if curStep != "swap_blind" {
			g.FailSpecialAction(playerID, "invalid special step for Q/J")
			return
		}
		doSwapBlind(g, playerID, msg.Card1, msg.Card2)
		g.AdvanceTurn()
		return

	case "K":
		if curStep == "swap_peek" {
			doKingFirstStep(g, playerID, msg.Card1, msg.Card2)
		} else if curStep == "swap_peek_swap" {
			doKingSwapDecision(g, playerID, msg.Card1, msg.Card2)
		} else {
			g.FailSpecialAction(playerID, "invalid special step for K")
		}
		return

	default:
		g.FailSpecialAction(playerID, "unsupported rank")
	}
}

// doPeekSelf ...
func doPeekSelf(g *game.CambiaGame, playerID uuid.UUID) {
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
	g.SpecialAction = game.SpecialActionState{}
}

// doPeekOther ...
func doPeekOther(g *game.CambiaGame, playerID uuid.UUID, card1 map[string]interface{}) {
	var targetUserID uuid.UUID
	if card1 != nil {
		if userMap, ok := card1["user"].(map[string]interface{}); ok {
			uidStr, _ := userMap["id"].(string)
			if uid, err := uuid.Parse(uidStr); err == nil {
				targetUserID = uid
			}
		}
	}
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
	// private reveal to action taker
	g.FireEventPrivateSuccess(playerID, "peek_other", reveal, nil)
	// broadcast partial
	g.FireEventPlayerSpecialAction(playerID, "peek_other", &models.Card{ID: reveal.ID}, nil, map[string]interface{}{
		"user": targetUserID.String(),
	})
	g.SpecialAction = game.SpecialActionState{}
}

// doSwapBlind ...
func doSwapBlind(g *game.CambiaGame, playerID uuid.UUID, c1, c2 map[string]interface{}) {
	cardA, userA := pickCardFromMessage(g, c1)
	cardB, userB := pickCardFromMessage(g, c2)
	if cardA == nil || cardB == nil {
		g.FailSpecialAction(playerID, "invalid blind swap targets")
		return
	}
	// if either is in locked Cambia caller => skip
	if g.CambiaCalled && (userA == g.CambiaCallerID || userB == g.CambiaCallerID) {
		// cannot swap locked
		g.FailSpecialAction(playerID, "target card belongs to Cambia caller, locked for swap")
		return
	}
	swapTwoCards(g, userA, cardA.ID, userB, cardB.ID)
	g.FireEventPlayerSpecialAction(playerID, "swap_blind", &models.Card{ID: cardA.ID}, &models.Card{ID: cardB.ID}, map[string]interface{}{
		"userA": userA.String(),
		"userB": userB.String(),
	})
	g.SpecialAction = game.SpecialActionState{}
}

// doKingFirstStep is "swap_peek" => reveal two chosen cards privately
func doKingFirstStep(g *game.CambiaGame, playerID uuid.UUID, c1, c2 map[string]interface{}) {
	cardA, userA := pickCardFromMessage(g, c1)
	cardB, userB := pickCardFromMessage(g, c2)
	if cardA == nil || cardB == nil {
		g.FailSpecialAction(playerID, "invalid king step targets")
		return
	}
	// store
	g.SpecialAction.FirstStepDone = true
	g.SpecialAction.Card1 = cardA
	g.SpecialAction.Card1Owner = userA
	g.SpecialAction.Card2 = cardB
	g.SpecialAction.Card2Owner = userB

	// broadcast partial reveal
	g.FireEventPlayerSpecialAction(playerID, "swap_peek_reveal", &models.Card{ID: cardA.ID}, &models.Card{ID: cardB.ID}, map[string]interface{}{
		"userA": userA.String(),
		"userB": userB.String(),
	})
	// private detail
	g.FireEventPrivateSuccess(playerID, "swap_peek_reveal", cardA, cardB)
	g.ResetTurnTimer()
}

// doKingSwapDecision is "swap_peek_swap" => optionally swap
func doKingSwapDecision(g *game.CambiaGame, playerID uuid.UUID, c1, c2 map[string]interface{}) {
	cardA := g.SpecialAction.Card1
	cardB := g.SpecialAction.Card2
	userA := g.SpecialAction.Card1Owner
	userB := g.SpecialAction.Card2Owner
	if cardA == nil || cardB == nil {
		g.FailSpecialAction(playerID, "missing stored king cards")
		return
	}
	// if either is the Cambia caller => cannot swap, but we can peek
	if g.CambiaCalled && (userA == g.CambiaCallerID || userB == g.CambiaCallerID) {
		g.FailSpecialAction(playerID, "cannot swap locked Cambia caller's cards")
		return
	}
	swapTwoCards(g, userA, cardA.ID, userB, cardB.ID)
	g.FireEventPlayerSpecialAction(playerID, "swap_peek_swap", &models.Card{ID: cardA.ID}, &models.Card{ID: cardB.ID}, map[string]interface{}{
		"userA": userA.String(),
		"userB": userB.String(),
	})
	g.SpecialAction = game.SpecialActionState{}
	g.AdvanceTurn()
}

// pickCardFromMessage ...
func pickCardFromMessage(g *game.CambiaGame, cardMap map[string]interface{}) (*models.Card, uuid.UUID) {
	if cardMap == nil {
		return nil, uuid.Nil
	}
	cardIDStr, _ := cardMap["id"].(string)
	if cardIDStr == "" {
		return nil, uuid.Nil
	}
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		return nil, uuid.Nil
	}
	var ownerID uuid.UUID
	if uMap, ok := cardMap["user"].(map[string]interface{}); ok {
		if uidStr, ok2 := uMap["id"].(string); ok2 {
			if uid, e2 := uuid.Parse(uidStr); e2 == nil {
				ownerID = uid
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

// swapTwoCards ...
func swapTwoCards(g *game.CambiaGame, userA uuid.UUID, cardAID uuid.UUID, userB uuid.UUID, cardBID uuid.UUID) {
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
