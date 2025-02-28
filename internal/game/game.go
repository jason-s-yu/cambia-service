// internal/game/game.go
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
	EventSnapSuccess              GameEventType = "player_snap_success"
	EventSnapFail                 GameEventType = "player_snap_fail"
	EventSnapPenalty              GameEventType = "player_snap_penalty"
	EventReshuffle                GameEventType = "reshuffle_stockpile"
	EventPlayerDrawStock          GameEventType = "player_draw_stockpile"
	EventPrivateDrawStock         GameEventType = "private_draw_stockpile"
	EventPlayerDiscard            GameEventType = "player_discard"
	EventPlayerReplace            GameEventType = "player_replace"
	EventPlayerSpecialChoice      GameEventType = "player_special_choice"
	EventPlayerSpecialAction      GameEventType = "player_special_action"
	EventPrivateSpecialAction     GameEventType = "private_special_action_success"
	EventPrivateSpecialActionFail GameEventType = "private_special_action_fail"
	EventPlayerCambia             GameEventType = "player_cambia"
	EventPlayerTurn               GameEventType = "player_turn"
)

// GameEvent holds data about an event that can be broadcast to the clients in a consistent format.
type GameEvent struct {
	Type   GameEventType          `json:"type"`
	UserID uuid.UUID              `json:"user,omitempty"`
	Card   *models.Card           `json:"card,omitempty"`
	Card2  *models.Card           `json:"card2,omitempty"`
	Other  map[string]interface{} `json:"other,omitempty"`
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

	lastSeen map[uuid.UUID]time.Time

	turnTimer    *time.Timer
	turnDuration time.Duration

	OnGameEnd   OnGameEndFunc
	BroadcastFn func(ev GameEvent) // callback to broadcast game events (set from outside)

	mu sync.Mutex
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
		turnDuration:       15 * time.Second,
	}
	g.initializeDeck()
	return g
}

// AddPlayer merges the logic from old AddPlayer. If the player already exists, update the conn.
func (g *CambiaGame) AddPlayer(p *models.Player) {
	g.mu.Lock()
	defer g.mu.Unlock()
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
		"K": 13, // black kings are 13, red kings => -1
	}

	var deck []*models.Card
	for _, suit := range suits {
		for _, rank := range ranks {
			val := values[rank]
			if rank == "K" && (suit == "Hearts" || suit == "Diamonds") {
				val = -1
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
	// 2 jokers, value=0
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
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.Started || g.GameOver {
		return
	}
	g.Started = true

	if g.HouseRules.TurnTimeoutSeconds() > 0 {
		g.turnDuration = time.Duration(g.HouseRules.TurnTimeoutSeconds()) * time.Second
	} else {
		g.turnDuration = 0
	}

	// Deal 4 cards to each player
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
		g.fireEvent(GameEvent{
			Type:   EventPlayerDrawStock,
			UserID: g.Players[g.CurrentPlayerIndex].ID,
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
	if g.turnDuration == 0 {
		return
	}
	if g.turnTimer != nil {
		g.turnTimer.Stop()
	}
	g.turnTimer = time.AfterFunc(g.turnDuration, func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.handleTimeout(g.Players[g.CurrentPlayerIndex].ID)
	})
}

// handleTimeout applies a skip/pass if a player times out
func (g *CambiaGame) handleTimeout(playerID uuid.UUID) {
	log.Printf("Player %v timed out. Force draw & discard.\n", playerID)
	// default action: draw top of stock, discard it, ignoring special abilities
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

// broadcastPlayerTurn notifies all players whose turn it is now
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
	g.CurrentPlayerIndex = (g.CurrentPlayerIndex + 1) % len(g.Players)
	g.scheduleNextTurnTimer()
	g.broadcastPlayerTurn()
}

// HandleDisconnect logic
func (g *CambiaGame) HandleDisconnect(playerID uuid.UUID) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.HouseRules.ForfeitOnDisconnect {
		g.markPlayerAsDisconnected(playerID)
	} else {
		g.lastSeen[playerID] = time.Now()
	}
}

// HandleReconnect sets the player as reconnected
func (g *CambiaGame) HandleReconnect(playerID uuid.UUID) {
	g.mu.Lock()
	defer g.mu.Unlock()
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

// HandlePlayerAction processes draw, discard, snap, or cambia.
// We add "action_draw_stockpile", "action_draw_discard", etc. with the new approach.
func (g *CambiaGame) HandlePlayerAction(playerID uuid.UUID, action models.GameAction) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.GameOver {
		return
	}
	currentPID := g.Players[g.CurrentPlayerIndex].ID

	// note: snap doesn't require it to be that player's turn
	if action.ActionType != "snap" && playerID != currentPID {
		// not your turn
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
	card := g.drawCardFromLocation(playerID, location)
	if card == nil {
		// invalid or deck empty => do nothing
		return
	}
	// store the drawn card in player's DrawnCard
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			g.Players[i].DrawnCard = card
			break
		}
	}
}

// handleDiscard discards the drawnCard or a card from hand
func (g *CambiaGame) handleDiscard(playerID uuid.UUID, payload map[string]interface{}) {
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
				// search in hand
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
		// no such card
		return
	}
	g.DiscardPile = append(g.DiscardPile, discarded)

	// broadcast
	g.fireEvent(GameEvent{
		Type:   EventPlayerDiscard,
		UserID: playerID,
		Card: &models.Card{
			ID:    discarded.ID,
			Rank:  discarded.Rank,
			Suit:  discarded.Suit,
			Value: discarded.Value,
		},
	})

	// check special
	if g.HouseRules.AllowDiscardAbilities {
		g.applySpecialAbilityIfFreshlyDrawn(discarded, playerID)
	}
	g.advanceTurn()
}

// handleReplace means the player is swapping their drawnCard with a card in their hand
func (g *CambiaGame) handleReplace(playerID uuid.UUID, payload map[string]interface{}) {
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
			if idx >= 0 && idx < len(p.Hand) {
				replaced = p.Hand[idx]
				p.Hand[idx] = fresh
			}
			break
		}
	}
	if fresh == nil || replaced == nil {
		// invalid
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
	// also broadcast the discard
	g.fireEvent(GameEvent{
		Type:   EventPlayerDiscard,
		UserID: playerID,
		Card:   &models.Card{ID: replaced.ID, Rank: replaced.Rank, Suit: replaced.Suit, Value: replaced.Value},
	})

	if g.HouseRules.AllowDiscardAbilities {
		g.applySpecialAbilityIfFreshlyDrawn(replaced, playerID)
	}
	g.advanceTurn()
}

// handleSnap ...
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
	var snapPlayer *models.Player
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			snapPlayer = g.Players[i]
			for h := 0; h < len(snapPlayer.Hand); h++ {
				if snapPlayer.Hand[h].ID == cardID {
					snapCard = snapPlayer.Hand[h]
					break
				}
			}
			break
		}
	}
	if snapCard == nil {
		g.penalizeSnapFail(playerID, nil)
		return
	}
	if snapCard.Rank == lastDiscard.Rank {
		// success
		log.Printf("Player %v snap success with rank %s", playerID, snapCard.Rank)
		g.removeCardFromPlayerHand(playerID, cardID)
		g.DiscardPile = append(g.DiscardPile, snapCard)
		// broadcast success
		g.fireEvent(GameEvent{
			Type:   EventSnapSuccess,
			UserID: playerID,
			Card:   &models.Card{ID: snapCard.ID, Rank: snapCard.Rank, Suit: snapCard.Suit, Value: snapCard.Value},
		})
	} else {
		g.penalizeSnapFail(playerID, snapCard)
	}
}

func (g *CambiaGame) penalizeSnapFail(playerID uuid.UUID, attemptedCard *models.Card) {
	log.Printf("Player %v snap fail", playerID)
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
	// draw penalty
	penalty := g.HouseRules.PenaltyCardCount
	if penalty < 1 {
		penalty = 2
	}
	for i := 0; i < penalty; i++ {
		card := g.drawTopStockpile(false)
		if card == nil {
			break
		}
		// broadcast public
		g.fireEvent(GameEvent{
			Type:   EventSnapPenalty,
			UserID: playerID,
			Card:   &models.Card{ID: card.ID},
		})
		// private
		g.fireEvent(GameEvent{
			Type:   EventPrivateDrawStock,
			UserID: playerID,
			Card:   &models.Card{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
			Other: map[string]interface{}{
				"idx": i,
			},
		})
		// add to player's hand
		for j := range g.Players {
			if g.Players[j].ID == playerID {
				g.Players[j].Hand = append(g.Players[j].Hand, card)
				break
			}
		}
	}
}

// handleCallCambia => end game after other players get 1 more turn
func (g *CambiaGame) handleCallCambia(playerID uuid.UUID) {
	log.Printf("Player %v calls Cambia", playerID)
	// broadcast
	g.fireEvent(GameEvent{
		Type:   EventPlayerCambia,
		UserID: playerID,
	})
	g.advanceTurn()
	// in real cambia, we let each other player have one more turn, then end
	// for brevity, we won't implement that right now
}

// applySpecialAbilityIfFreshlyDrawn handles the immediate discard actions
func (g *CambiaGame) applySpecialAbilityIfFreshlyDrawn(c *models.Card, playerID uuid.UUID) {
	// Placeholder: you'd handle Q, J, K, 7,8,9,10 logic here.
	// Then you'd broadcast e.g. "player_special_choice", etc.
}

// EndGame finalizes scoring, sets GameOver, and calls OnGameEnd if present.
func (g *CambiaGame) EndGame() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.GameOver {
		return
	}
	g.GameOver = true
	log.Printf("Ending game %v, computing final scores...", g.ID)

	finalScores := g.computeScores()
	winners := findWinners(finalScores)
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

// findWinners picks the lowest score as winner. Ties => multiple winners
func findWinners(scores map[uuid.UUID]int) []uuid.UUID {
	var best int
	var first = true
	for _, s := range scores {
		if first {
			best = s
			first = false
		} else if s < best {
			best = s
		}
	}
	var winners []uuid.UUID
	for pid, s := range scores {
		if s == best {
			winners = append(winners, pid)
		}
	}
	return winners
}

// persistResults is called optionally if you want to store final results in DB
func (g *CambiaGame) persistResults(finalScores map[uuid.UUID]int, winners []uuid.UUID) {
	ctx := context.Background()
	err := database.RecordGameAndResults(ctx, g.ID, g.Players, finalScores, winners)
	if err != nil {
		log.Printf("Error persisting results: %v", err)
	}
}

// removeCardFromPlayerHand convenience
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
