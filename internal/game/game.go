// internal/game/go
package game

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/models"
)

// OnGameEndFunc is a function signature that can handle a finished game, broadcasting results to the lobby, etc.
type OnGameEndFunc func(lobbyID uuid.UUID, winner uuid.UUID, scores map[uuid.UUID]int)

// GameEventType is an enum-like type for broadcasting game actions.
type GameEventType string

const (
	EventSnapSuccess      GameEventType = "player_snap_success"
	EventSnapFail         GameEventType = "player_snap_fail"
	EventSnapPenalty      GameEventType = "player_snap_penalty"
	EventReshuffle        GameEventType = "reshuffle_stockpile"
	EventPlayerDrawStock  GameEventType = "player_draw_stockpile"
	EventPrivateDrawStock GameEventType = "private_draw_stockpile"
	EventPlayerDiscard    GameEventType = "player_discard"
	EventPlayerReplace    GameEventType = "player_replace"

	EventPlayerSpecialChoice      GameEventType = "player_special_choice"
	EventPlayerSpecialAction      GameEventType = "player_special_action"
	EventPrivateSpecialAction     GameEventType = "private_special_action_success"
	EventPrivateSpecialActionFail GameEventType = "private_special_action_fail"

	EventPlayerCambia GameEventType = "player_cambia"
	EventPlayerTurn   GameEventType = "player_turn"
)

// GameEvent holds data about an event that can be broadcast to the clients in a consistent format.
type GameEvent struct {
	Type   GameEventType          `json:"type"`
	UserID uuid.UUID              `json:"user,omitempty"`
	Card   *models.Card           `json:"card,omitempty"`
	Card2  *models.Card           `json:"card2,omitempty"`
	Other  map[string]interface{} `json:"other,omitempty"`
}

// SpecialActionState holds temporary info about a pending special action.
// e.g. a King might be in a multi-step: first peek, then decide to swap or skip.
type SpecialActionState struct {
	Active        bool
	PlayerID      uuid.UUID
	CardRank      string // "K", "Q", "J", "7", "8", "9", "10"
	FirstStepDone bool   // used for K to track if we've revealed 2 cards
	Card1         *models.Card
	Card1Owner    uuid.UUID
	Card2         *models.Card
	Card2Owner    uuid.UUID
}

// CambiaGame holds the entire state for a single game instance in memory.
type CambiaGame struct {
	ID      uuid.UUID
	LobbyID uuid.UUID // references the lobby that spawned this game

	Players     []*models.Player
	Deck        []*models.Card
	DiscardPile []*models.Card

	HouseRules models.HouseRules

	CurrentPlayerIndex int
	Started            bool
	GameOver           bool

	lastSeen     map[uuid.UUID]time.Time
	turnTimer    *time.Timer
	TurnDuration time.Duration

	OnGameEnd   OnGameEndFunc
	BroadcastFn func(ev GameEvent) // callback to broadcast game events

	// SpecialAction is used for multi-step card logic (K, Q, J, etc.)
	SpecialAction SpecialActionState

	Mu sync.Mutex

	// CambiaCalled tracks if a player has invoked "Cambia". If so, we do a final round logic.
	CambiaCalled       bool
	CambiaCallerID     uuid.UUID
	CambiaFinalCounter int // how many "other players" have taken their final turn
}

// NewCambiaGame builds an empty instance with a newly shuffled deck.
func NewCambiaGame() *CambiaGame {
	id, _ := uuid.NewRandom()
	g := &CambiaGame{
		ID:                 id,
		Deck:               []*models.Card{},
		DiscardPile:        []*models.Card{},
		lastSeen:           make(map[uuid.UUID]time.Time),
		CurrentPlayerIndex: 0,
		Started:            false,
		GameOver:           false,
		TurnDuration:       15 * time.Second,
		SpecialAction:      SpecialActionState{},
		CambiaCalled:       false,
		CambiaFinalCounter: 0,
	}
	g.initializeDeck()
	return g
}

// AddPlayer merges the logic from old AddPlayer. If the player already exists, update the conn.
func (g *CambiaGame) AddPlayer(p *models.Player) {
	g.Mu.Lock()
	defer g.Mu.Unlock()
	for i, pl := range g.Players {
		if pl.ID == p.ID {
			// reconnect
			g.Players[i].Conn = p.Conn
			g.Players[i].Connected = true
			g.lastSeen[p.ID] = time.Now()
			return
		}
	}
	g.Players = append(g.Players, p)
	g.lastSeen[p.ID] = time.Now()
}

// initializeDeck sets up a standard Cambia deck, including jokers, red kings = -1, etc.
func (g *CambiaGame) initializeDeck() {
	suits := []string{"Hearts", "Diamonds", "Clubs", "Spades"}
	ranks := []string{"A", "2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K"}
	values := map[string]int{
		"A": 1, "2": 2, "3": 3, "4": 4,
		"5": 5, "6": 6, "7": 7, "8": 8,
		"9": 9, "10": 10, "J": 11, "Q": 12,
		"K": 13,
	}

	var deck []*models.Card
	for _, suit := range suits {
		for _, rank := range ranks {
			val := values[rank]
			if rank == "K" && (suit == "Hearts" || suit == "Diamonds") {
				val = -1 // set red kings to -1
			}
			cid, _ := uuid.NewRandom()
			card := &models.Card{
				ID:    cid,
				Suit:  suit,
				Rank:  rank,
				Value: val,
			}
			deck = append(deck, card)
		}
	}
	// 2 jokers
	for i := 0; i < 2; i++ {
		cid, _ := uuid.NewRandom()
		deck = append(deck, &models.Card{
			ID:    cid,
			Suit:  "Joker",
			Rank:  "Joker",
			Value: 0,
		})
	}

	rand.Shuffle(len(deck), func(i, j int) {
		deck[i], deck[j] = deck[j], deck[i]
	})
	g.Deck = deck
}

// Start sets up the game state: deal initial cards, start turn timers, etc.
func (g *CambiaGame) Start() {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	if g.Started || g.GameOver {
		return
	}
	g.Started = true

	if g.HouseRules.TurnTimeoutSeconds() > 0 {
		g.TurnDuration = time.Duration(g.HouseRules.TurnTimeoutSeconds()) * time.Second
	} else {
		g.TurnDuration = 0
	}

	// deal 4 cards each
	for _, p := range g.Players {
		p.Hand = []*models.Card{}
		for i := 0; i < 4; i++ {
			card := g.drawTopStockpile(false)
			if card == nil {
				break
			}
			p.Hand = append(p.Hand, card)
		}
	}
	g.scheduleNextTurnTimer()
	g.broadcastPlayerTurn()
}

// drawTopStockpile draws the top card from the stockpile, re-shuffling discard if needed.
// If broadcast is true, we send a "player_draw_stockpile" event, else skip that.
func (g *CambiaGame) drawTopStockpile(broadcast bool) *models.Card {
	if len(g.Deck) == 0 {
		if len(g.DiscardPile) == 0 {
			// no cards left => forced game end?
			g.EndGame()
			return nil
		}

		// reshuffle discard
		g.Deck = append(g.Deck, g.DiscardPile...)
		g.DiscardPile = []*models.Card{}
		rand.Shuffle(len(g.Deck), func(i, j int) {
			g.Deck[i], g.Deck[j] = g.Deck[j], g.Deck[i]
		})

		// broadcast reshuffle
		g.fireEvent(GameEvent{
			Type: EventReshuffle,
			Other: map[string]interface{}{
				"stockpileSize": len(g.Deck),
			},
		})
	}

	if len(g.Deck) == 0 {
		return nil
	}

	card := g.Deck[0]
	g.Deck = g.Deck[1:]
	if broadcast {
		curPID := g.Players[g.CurrentPlayerIndex].ID
		g.fireEvent(GameEvent{
			Type:   EventPlayerDrawStock,
			UserID: curPID,
			Card:   &models.Card{ID: card.ID},
			Other: map[string]interface{}{
				"stockpileSize": len(g.Deck),
			},
		})
	}
	return card
}

// drawTopDiscard draws from the top of the discard if allowed and non-empty.
func (g *CambiaGame) drawTopDiscard() *models.Card {
	if !g.HouseRules.AllowDrawFromDiscardPile {
		return nil
	}
	if len(g.DiscardPile) == 0 {
		return nil
	}
	idx := len(g.DiscardPile) - 1
	card := g.DiscardPile[idx]
	g.DiscardPile = g.DiscardPile[:idx]
	return card
}

// scheduleNextTurnTimer restarts a turn timer for the current player if turnDuration > 0
func (g *CambiaGame) scheduleNextTurnTimer() {
	if g.TurnDuration == 0 {
		return
	}
	if g.turnTimer != nil {
		g.turnTimer.Stop()
	}
	curPID := g.Players[g.CurrentPlayerIndex].ID
	g.turnTimer = time.AfterFunc(g.TurnDuration, func() {
		g.Mu.Lock()
		defer g.Mu.Unlock()
		g.handleTimeout(curPID)
	})
}

// handleTimeout forcibly draws & discards for the current player if they time out.
func (g *CambiaGame) handleTimeout(playerID uuid.UUID) {
	log.Printf("Player %v timed out. Force draw & discard.\n", playerID)
	// If there's a special action in progress for them, skip it
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerID {
		log.Printf("Timeout skipping special action for player %v", playerID)
		g.SpecialAction = SpecialActionState{}
	}

	// forcibly draw top of stock
	card := g.drawTopStockpile(true)
	if card != nil {
		g.fireEvent(GameEvent{
			Type:   EventPrivateDrawStock,
			UserID: playerID,
			Card:   &models.Card{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
		})
		// discard immediately
		g.DiscardPile = append(g.DiscardPile, card)
		g.fireEvent(GameEvent{
			Type:   EventPlayerDiscard,
			UserID: playerID,
			Card:   card,
		})
	}
	g.advanceTurn()
}

// broadcastPlayerTurn notifies all players whose turn it is now.
func (g *CambiaGame) broadcastPlayerTurn() {
	currentPID := g.Players[g.CurrentPlayerIndex].ID
	g.fireEvent(GameEvent{
		Type:   EventPlayerTurn,
		UserID: currentPID,
	})
}

// fireEvent is a helper that calls BroadcastFn if non-nil
func (g *CambiaGame) fireEvent(ev GameEvent) {
	if g.BroadcastFn != nil {
		g.BroadcastFn(ev)
	}
}

// Advance turn to next player
func (g *CambiaGame) advanceTurn() {
	if g.GameOver {
		return
	}
	// If Cambia is called, we let the other players get final turns.
	// If the current player was the Cambia caller, do nothing special here (they've ended their turn).
	// If the current player is not the Cambia caller, we may increment cambiaFinalCounter if the turn is done.
	if g.CambiaCalled {
		// if the current player wasn't the caller, then they've just finished a final turn
		curPID := g.Players[g.CurrentPlayerIndex].ID
		if curPID != g.CambiaCallerID {
			g.CambiaFinalCounter++
			// If all others have played => end now
			if g.CambiaFinalCounter >= len(g.Players)-1 {
				g.EndGame()
				return
			}
		}
	}

	g.CurrentPlayerIndex = (g.CurrentPlayerIndex + 1) % len(g.Players)
	g.scheduleNextTurnTimer()
	g.broadcastPlayerTurn()
}

// HandleDisconnect logic
func (g *CambiaGame) HandleDisconnect(playerID uuid.UUID) {
	g.Mu.Lock()
	defer g.Mu.Unlock()
	if g.HouseRules.ForfeitOnDisconnect {
		g.markPlayerAsDisconnected(playerID)
	} else {
		g.lastSeen[playerID] = time.Now()
	}
}

// HandleReconnect sets the player as reconnected
func (g *CambiaGame) HandleReconnect(playerID uuid.UUID) {
	g.Mu.Lock()
	defer g.Mu.Unlock()
	g.lastSeen[playerID] = time.Now()
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			g.Players[i].Connected = true
			break
		}
	}
}

// markPlayerAsDisconnected forcibly sets them as disconnected
func (g *CambiaGame) markPlayerAsDisconnected(playerID uuid.UUID) {
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			g.Players[i].Connected = false
			break
		}
	}
}

// drawCardFromLocation picks stockpile or discard if allowed
func (g *CambiaGame) drawCardFromLocation(playerID uuid.UUID, location string) *models.Card {
	if location == "stockpile" {
		card := g.drawTopStockpile(true)
		// private reveal to drawer
		if card != nil {
			g.fireEvent(GameEvent{
				Type:   EventPrivateDrawStock,
				UserID: playerID,
				Card:   &models.Card{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
			})
		}
		return card
	} else if location == "discardpile" && g.HouseRules.AllowDrawFromDiscardPile {
		card := g.drawTopDiscard()
		if card != nil {
			// broadcast to all
			g.fireEvent(GameEvent{
				Type:   EventPlayerDrawStock,
				UserID: playerID,
				Card:   &models.Card{ID: card.ID},
				Other: map[string]interface{}{
					"discardSize": len(g.DiscardPile),
				},
			})
			// private reveal
			g.fireEvent(GameEvent{
				Type:   EventPrivateDrawStock,
				UserID: playerID,
				Card:   &models.Card{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
			})
		}
		return card
	}
	return nil
}

// HandlePlayerAction interprets draw, discard, snap, cambia, replace, etc.
func (g *CambiaGame) HandlePlayerAction(playerID uuid.UUID, action models.GameAction) {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	if g.GameOver {
		return
	}
	currentPID := g.Players[g.CurrentPlayerIndex].ID

	// NB: snap can be played out of turn
	if action.ActionType != "action_snap" && playerID != currentPID {
		return
	}

	switch action.ActionType {
	case "action_snap":
		g.handleSnap(playerID, action.Payload)
	case "action_draw_stockpile":
		g.handleDrawFrom(playerID, "stockpile")
	case "action_draw_discard":
		g.handleDrawFrom(playerID, "discardpile")
	case "action_discard":
		g.handleDiscard(playerID, action.Payload)
	case "action_replace":
		g.handleReplace(playerID, action.Payload)
	case "action_cambia":
		g.handleCallCambia(playerID)
	default:
		log.Printf("Unknown action %s by player %v\n", action.ActionType, playerID)
	}
}

// handleDrawFrom draws from either stockpile or discard pile.
func (g *CambiaGame) handleDrawFrom(playerID uuid.UUID, location string) {
	// if there's a special in progress for this player, ignore
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerID {
		log.Printf("Player %v tried to draw while special in progress.\n", playerID)
		return
	}
	card := g.drawCardFromLocation(playerID, location)
	if card == nil {
		// invalid draw location i.e. deck or card is empty/nil
		return
	}
	// store card in player's temp hand
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			g.Players[i].DrawnCard = card
			break
		}
	}
}

// handleDiscard discards the drawnCard or a card from the player's hand
func (g *CambiaGame) handleDiscard(playerID uuid.UUID, payload map[string]interface{}) {
	// if there's a special in progress, ignore
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerID {
		log.Printf("Player %v tried to discard while special in progress.\n", playerID)
		return
	}

	cardIDStr, _ := payload["id"].(string)
	if cardIDStr == "" {
		return
	}
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		return
	}
	var discarded *models.Card
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			p := g.Players[i]
			if p.DrawnCard != nil && p.DrawnCard.ID == cardID {
				discarded = p.DrawnCard
				p.DrawnCard = nil
				break
			} else {
				for h := 0; h < len(p.Hand); h++ {
					if p.Hand[h].ID == cardID {
						discarded = p.Hand[h]
						// remove from hand
						p.Hand = append(p.Hand[:h], p.Hand[h+1:]...)
						break
					}
				}
			}
			break
		}
	}
	if discarded == nil {
		return
	}
	g.DiscardPile = append(g.DiscardPile, discarded)

	g.fireEvent(GameEvent{
		Type:   EventPlayerDiscard,
		UserID: playerID,
		Card:   &models.Card{ID: discarded.ID, Rank: discarded.Rank, Suit: discarded.Suit, Value: discarded.Value},
	})

	g.applySpecialAbilityIfFreshlyDrawn(discarded, playerID)
}

// handleReplace means the player is swapping their drawnCard with a card in their hand
func (g *CambiaGame) handleReplace(playerID uuid.UUID, payload map[string]interface{}) {
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerID {
		log.Printf("Player %v tried to replace while special in progress.\n", playerID)
		return
	}

	idxFloat, _ := payload["idx"].(float64)
	idx := int(idxFloat)
	var replaced *models.Card
	var fresh *models.Card
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			p := g.Players[i]
			if p.DrawnCard != nil {
				fresh = p.DrawnCard
				p.DrawnCard = nil
			}
			if idx >= 0 && idx < len(p.Hand) && fresh != nil {
				replaced = p.Hand[idx]
				p.Hand[idx] = fresh
			}
			break
		}
	}
	if fresh == nil || replaced == nil {
		return
	}
	// replaced card goes to discard pile
	g.DiscardPile = append(g.DiscardPile, replaced)
	g.fireEvent(GameEvent{
		Type:   EventPlayerReplace,
		UserID: playerID,
		Card:   &models.Card{ID: fresh.ID, Rank: fresh.Rank, Suit: fresh.Suit, Value: fresh.Value},
		Other: map[string]interface{}{
			"replaceIdx": idx,
		},
	})
	g.fireEvent(GameEvent{
		Type:   EventPlayerDiscard,
		UserID: playerID,
		Card:   &models.Card{ID: replaced.ID, Rank: replaced.Rank, Suit: replaced.Suit, Value: replaced.Value},
	})

	g.applySpecialAbilityIfFreshlyDrawn(replaced, playerID)
}

// handleSnap processes an out-of-turn snap/burn. If a snap is done incorrectly, the player is penalized with
// an additional forced-draw, depending on house rules.
// Invokes penalizeSnapFail() to draw penalty cards.
func (g *CambiaGame) handleSnap(playerID uuid.UUID, payload map[string]interface{}) {
	cardIDStr, _ := payload["id"].(string)
	if cardIDStr == "" {
		return
	}
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		return
	}
	if len(g.DiscardPile) == 0 {
		g.penalizeSnapFail(playerID, nil)
		return
	}
	lastDiscard := g.DiscardPile[len(g.DiscardPile)-1]
	var snapCard *models.Card

playerloop:
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			for h := 0; h < len(g.Players[i].Hand); h++ {
				if g.Players[i].Hand[h].ID == cardID {
					snapCard = g.Players[i].Hand[h]
					break playerloop
				}
			}
		}
	}
	if snapCard == nil {
		g.penalizeSnapFail(playerID, nil)
		return
	}
	if snapCard.Rank == lastDiscard.Rank {
		log.Printf("Player %v snap success with rank %s", playerID, snapCard.Rank)
		g.removeCardFromPlayerHand(playerID, cardID)
		g.DiscardPile = append(g.DiscardPile, snapCard)
		g.fireEvent(GameEvent{
			Type:   EventSnapSuccess,
			UserID: playerID,
			Card:   &models.Card{ID: snapCard.ID, Rank: snapCard.Rank, Suit: snapCard.Suit, Value: snapCard.Value},
		})
	} else {
		g.penalizeSnapFail(playerID, snapCard)
	}
}

// penalizeSnapFail deals penalty cards to the snapper
func (g *CambiaGame) penalizeSnapFail(playerID uuid.UUID, attemptedCard *models.Card) {
	if attemptedCard != nil {
		g.fireEvent(GameEvent{
			Type:   EventSnapFail,
			UserID: playerID,
			Card:   &models.Card{ID: attemptedCard.ID, Rank: attemptedCard.Rank, Suit: attemptedCard.Suit, Value: attemptedCard.Value},
		})
	} else {
		g.fireEvent(GameEvent{
			Type:   EventSnapFail,
			UserID: playerID,
		})
	}
	pen := g.HouseRules.PenaltyCardCount
	if pen < 1 {
		pen = 2
	}
	for i := 0; i < pen; i++ {
		card := g.drawTopStockpile(false)
		if card == nil {
			break
		}
		g.fireEvent(GameEvent{
			Type:   EventSnapPenalty,
			UserID: playerID,
			Card:   &models.Card{ID: card.ID},
		})
		g.fireEvent(GameEvent{
			Type:   EventPrivateDrawStock,
			UserID: playerID,
			Card:   &models.Card{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
			Other: map[string]interface{}{
				"idx": i,
			},
		})
		for j := range g.Players {
			if g.Players[j].ID == playerID {
				g.Players[j].Hand = append(g.Players[j].Hand, card)
				break
			}
		}
	}
}

// handleCallCambia invokes the end-game phase, after a player calls "Cambia."
// All other players should retain one more turn before tallying scores.
func (g *CambiaGame) handleCallCambia(playerID uuid.UUID) {
	log.Printf("Player %v calls Cambia", playerID)
	g.fireEvent(GameEvent{
		Type:   EventPlayerCambia,
		UserID: playerID,
	})

	// Mark cambia
	if !g.CambiaCalled {
		g.CambiaCalled = true
		g.CambiaCallerID = playerID
		g.CambiaFinalCounter = 0
	}
	// we forcibly end the caller's turn, so next player gets a turn
	g.advanceTurn()
}

// applySpecialAbilityIfFreshlyDrawn checks if the discard is Q, J, K, 7, 8, 9, 10
// and triggers partial-turn logic for the active player.
func (g *CambiaGame) applySpecialAbilityIfFreshlyDrawn(c *models.Card, playerID uuid.UUID) {
	// if the target card's owner is locked (cambia caller), cannot be swapped, but can be peeked
	// we handle that logic in the special action flow. For now we just start the normal partial-turn if rank is special
	if c.Rank == "K" || c.Rank == "Q" || c.Rank == "J" || c.Rank == "9" || c.Rank == "10" || c.Rank == "7" || c.Rank == "8" {
		g.resetTurnTimer()
		g.SpecialAction = SpecialActionState{
			Active:        true,
			PlayerID:      playerID,
			CardRank:      c.Rank,
			FirstStepDone: false,
		}
		// broadcast "player_special_choice"
		g.fireEvent(GameEvent{
			Type:   EventPlayerSpecialChoice,
			UserID: playerID,
			Card:   &models.Card{ID: c.ID, Rank: c.Rank},
			Other:  map[string]interface{}{"special": rankToSpecial(c.Rank)},
		})
	} else {
		// no special
		g.advanceTurn()
	}
}

// rankToSpecial maps card ranks to the "special" string for socket broadcasting.
// Internally, special actions are labeled by one of four enums:
// `peek_self`: 7 or 8 is played. The player can then peek at a card in their hand.
// `peek_other`: 9 or 10 is played. The player can then peek at a card in an opponent's hand.
// `swap_blind`: J or Q is played. The player may choose to blindly swap two cards without peeking.
// `swap_look`: K is played. The player may peek at any two cards and choose to swap them.
// In a swap move, players cannot swap cards from opponents who have already called Cambia (i.e. locked hand).
// However, if a king is played, the player may peek at a locked hand, but may not swap.
func rankToSpecial(rank string) string {
	switch rank {
	case "7", "8":
		return "peek_self"
	case "9", "10":
		return "peek_other"
	case "Q", "J":
		return "swap_blind"
	case "K":
		return "swap_peek"
	default:
		return ""
	}
}

// resetTurnTimer resets the turn timer to the full length
func (g *CambiaGame) resetTurnTimer() {
	if g.turnTimer != nil {
		g.turnTimer.Stop()
		g.turnTimer = nil
	}
	if g.TurnDuration > 0 {
		curPID := g.Players[g.CurrentPlayerIndex].ID
		g.turnTimer = time.AfterFunc(g.TurnDuration, func() {
			g.Mu.Lock()
			defer g.Mu.Unlock()
			g.handleTimeout(curPID)
		})
	}
}

// EndGame finalizes scoring, sets GameOver, and calls OnGameEnd if present.
func (g *CambiaGame) EndGame() {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	if g.GameOver {
		return
	}
	g.GameOver = true
	log.Printf("Ending game %v, computing final scores...", g.ID)

	finalScores := g.computeScores()
	winners := g.findWinnersWithCambiaTiebreak(finalScores)

	var firstWinner uuid.UUID
	if len(winners) > 0 {
		firstWinner = winners[0]
	}
	if g.OnGameEnd != nil {
		g.OnGameEnd(g.LobbyID, firstWinner, finalScores)
	}
}

// computeScores calculates each player's sum of hand
func (g *CambiaGame) computeScores() map[uuid.UUID]int {
	scores := make(map[uuid.UUID]int)
	for _, p := range g.Players {
		sum := 0
		for _, c := range p.Hand {
			sum += c.Value
		}
		scores[p.ID] = sum
	}
	return scores
}

// findWinnersWithCambiaTiebreak is a custom function that returns either the Cambia caller if they tie for best
// or else returns all players who share the best score.
func (g *CambiaGame) findWinnersWithCambiaTiebreak(scores map[uuid.UUID]int) []uuid.UUID {
	var best int
	first := true
	for _, s := range scores {
		if first {
			best = s
			first = false
		} else if s < best {
			best = s
		}
	}
	// gather all who have that best score
	var tied []uuid.UUID
	for pid, s := range scores {
		if s == best {
			tied = append(tied, pid)
		}
	}
	// if cambia caller is in tied => they override
	for _, pid := range tied {
		if pid == g.CambiaCallerID {
			return []uuid.UUID{g.CambiaCallerID}
		}
	}
	// otherwise it's a tie among all
	return tied
}

// persistResults is called optionally to store game results in DB
func (g *CambiaGame) persistResults(finalScores map[uuid.UUID]int, winners []uuid.UUID) {
	ctx := context.Background()
	err := database.RecordGameAndResults(ctx, g.ID, g.Players, finalScores, winners)
	if err != nil {
		log.Printf("Error persisting results: %v", err)
	}
}

// removeCardFromPlayerHand removes a card from a player's hand by ID
func (g *CambiaGame) removeCardFromPlayerHand(playerID, cardID uuid.UUID) {
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			p := g.Players[i]
			newHand := []*models.Card{}
			for _, c := range p.Hand {
				if c.ID != cardID {
					newHand = append(newHand, c)
				}
			}
			p.Hand = newHand
			return
		}
	}
}

// FireEventPrivateSpecialActionFail ...
func (g *CambiaGame) FireEventPrivateSpecialActionFail(userID uuid.UUID, message string) {
	g.fireEvent(GameEvent{
		Type:   EventPrivateSpecialActionFail,
		UserID: userID,
		Other:  map[string]interface{}{"message": message},
	})
}

// FailSpecialAction ...
func (g *CambiaGame) FailSpecialAction(userID uuid.UUID, reason string) {
	g.FireEventPrivateSpecialActionFail(userID, reason)
	g.SpecialAction = SpecialActionState{}
	g.AdvanceTurn()
}

// FireEventPrivateSuccess ...
func (g *CambiaGame) FireEventPrivateSuccess(userID uuid.UUID, special string, c1, c2 *models.Card) {
	ev := GameEvent{
		Type:   EventPrivateSpecialAction,
		UserID: userID,
		Other:  map[string]interface{}{"special": special},
	}
	if c1 != nil {
		ev.Card = &models.Card{ID: c1.ID, Rank: c1.Rank, Suit: c1.Suit, Value: c1.Value}
	}
	if c2 != nil {
		ev.Card2 = &models.Card{ID: c2.ID, Rank: c2.Rank, Suit: c2.Suit, Value: c2.Value}
	}
	g.fireEvent(ev)
}

// FireEventPlayerSpecialAction ...
func (g *CambiaGame) FireEventPlayerSpecialAction(userID uuid.UUID, special string, c1, c2 *models.Card, extra map[string]interface{}) {
	ev := GameEvent{
		Type:   EventPlayerSpecialAction,
		UserID: userID,
		Other:  map[string]interface{}{"special": special},
	}
	if c1 != nil {
		ev.Card = &models.Card{ID: c1.ID}
	}
	if c2 != nil {
		ev.Card2 = &models.Card{ID: c2.ID}
	}
	for k, v := range extra {
		ev.Other[k] = v
	}
	g.fireEvent(ev)
}

// AdvanceTurn calls the CambiaadvanceTurn exported
func (g *CambiaGame) AdvanceTurn() {
	g.Mu.Unlock() // we are inside a locked section, so we must unlock, call the method, and re-lock
	g.advanceTurn()
	g.Mu.Lock()
}

// ResetTurnTimer calls the resetTurnTimer
func (g *CambiaGame) ResetTurnTimer() {
	g.resetTurnTimer()
}
