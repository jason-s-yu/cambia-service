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

// CambiaGame holds the entire state for a single game instance in memory.
// We'll store all real-time data here and persist final results at game end.
type CambiaGame struct {
	ID      uuid.UUID
	Players []*models.Player

	Deck        []*models.Card
	DiscardPile []*models.Card

	HouseRules models.HouseRules

	CurrentPlayerIndex int
	Started            bool
	GameOver           bool

	// lastSeen tracks last known activity for each player to handle DC
	lastSeen map[uuid.UUID]time.Time

	// turnTimer manages turn timeouts
	turnTimer    *time.Timer
	turnDuration time.Duration

	mu sync.Mutex
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
		turnDuration:       15 * time.Second, // default
	}
	g.initializeDeck()
	return g
}

// initializeDeck sets up a standard Cambia deck, including jokers, red kings = -1, etc.
func (g *CambiaGame) initializeDeck() {
	suits := []string{"Hearts", "Diamonds", "Clubs", "Spades"}
	ranks := []string{"A", "2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K"}
	values := map[string]int{
		"A": 1, "2": 2, "3": 3, "4": 4,
		"5": 5, "6": 6, "7": 7, "8": 8,
		"9": 9, "10": 10, "J": 11, "Q": 12,
		"K": 13, // black kings are 13, red kings are overridden to -1
	}

	var deck []*models.Card
	for _, suit := range suits {
		for _, rank := range ranks {
			val := values[rank]
			// Red King check
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

	rand.Seed(time.Now().UnixNano())
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

	// Set turnDuration from house rules (0 => no limit)
	if g.HouseRules.TurnTimeoutSeconds() > 0 {
		g.turnDuration = time.Duration(g.HouseRules.TurnTimeoutSeconds()) * time.Second
	} else {
		// 0 means no limit
		g.turnDuration = 0
	}

	// Deal 4 cards to each player
	for _, p := range g.Players {
		p.Hand = []*models.Card{}
		for i := 0; i < 4; i++ {
			p.Hand = append(p.Hand, g.drawCard())
		}
		// Possibly send info about first 2 cards they can peek, etc.
	}

	g.scheduleNextTurnTimer()
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

		// If we're still on the same player, we skip or pass them
		g.handleTimeout(g.Players[g.CurrentPlayerIndex].ID)
	})
}

// handleTimeout applies a skip/pass if a player times out
func (g *CambiaGame) handleTimeout(playerID uuid.UUID) {
	log.Printf("Player %v timed out. Skipping turn.\n", playerID)
	g.advanceTurn()
}

// handleDisconnect logic from old code
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

// Advance turn to next player
func (g *CambiaGame) advanceTurn() {
	if g.GameOver {
		return
	}
	g.CurrentPlayerIndex = (g.CurrentPlayerIndex + 1) % len(g.Players)
	g.scheduleNextTurnTimer()
}

// drawCard from deck or from discard recycle if deck empty
func (g *CambiaGame) drawCard() *models.Card {
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
	}
	card := g.Deck[0]
	g.Deck = g.Deck[1:]
	return card
}

// HandlePlayerAction processes draw, discard, snap, special card usage, etc.
func (g *CambiaGame) HandlePlayerAction(playerID uuid.UUID, action models.GameAction) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.GameOver {
		return
	}
	// verify it's player's turn (unless action is 'snap')
	currentPID := g.Players[g.CurrentPlayerIndex].ID
	if action.ActionType != "snap" && playerID != currentPID {
		// Not your turn, ignore
		return
	}

	switch action.ActionType {
	case "draw":
		g.handleDraw(playerID)
	case "discard":
		g.handleDiscard(playerID, action.Payload)
	case "snap":
		g.handleSnap(playerID, action.Payload)
	case "cambia":
		g.handleCallCambia(playerID)
	default:
		log.Printf("Unknown action %s by player %v\n", action.ActionType, playerID)
	}
}

// handleDraw example
func (g *CambiaGame) handleDraw(playerID uuid.UUID) {
	// draw top card
	c := g.drawCard()
	if c == nil {
		return
	}
	// In Cambia, you must either discard it or swap, etc.
	// We'll store an ephemeral "drawnCard" in the player's state for next step
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			g.Players[i].DrawnCard = c
			break
		}
	}
}

// handleDiscard
func (g *CambiaGame) handleDiscard(playerID uuid.UUID, payload map[string]interface{}) {
	cardIDStr, _ := payload["card_id"].(string)
	if cardIDStr == "" {
		return
	}
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		return
	}
	// find the card in the player's hand or drawnCard, discard it
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			p := g.Players[i]
			if p.DrawnCard != nil && p.DrawnCard.ID == cardID {
				g.DiscardPile = append(g.DiscardPile, p.DrawnCard)
				g.applySpecialAbilityIfFreshlyDrawn(p.DrawnCard, playerID)
				p.DrawnCard = nil
				break
			} else {
				// search in hand
				for h := 0; h < len(p.Hand); h++ {
					if p.Hand[h].ID == cardID {
						g.DiscardPile = append(g.DiscardPile, p.Hand[h])
						// if it was replaced from hand, no special ability unless house rule says so
						if g.HouseRules.AllowDiscardAbilities {
							g.applySpecialAbilityIfFreshlyDrawn(p.Hand[h], playerID)
						}
						// remove from hand
						p.Hand = append(p.Hand[:h], p.Hand[h+1:]...)
						break
					}
				}
			}
			break
		}
	}
	g.advanceTurn()
}

// applySpecialAbilityIfFreshlyDrawn handles the immediate discard actions
func (g *CambiaGame) applySpecialAbilityIfFreshlyDrawn(c *models.Card, playerID uuid.UUID) {
	// e.g. King => look & swap, etc.
	switch c.Rank {
	case "K":
		// "Look & Swap"
		// We'll just log for now. Real flow would let player see any 2 cards then optionally swap
		log.Printf("Player %v discards K => can look & swap two cards", playerID)
	case "Q", "J":
		// "Blind Swap"
		log.Printf("Player %v discards Q/J => can blind swap two cards", playerID)
	case "9", "10":
		// "Look at another's"
		log.Printf("Player %v discards 9/10 => can look at one card of another player", playerID)
	case "7", "8":
		// "Look at own"
		log.Printf("Player %v discards 7/8 => can look at one card of their own", playerID)
	default:
		// no special ability
	}
}

// handleSnap
func (g *CambiaGame) handleSnap(playerID uuid.UUID, payload map[string]interface{}) {
	cardIDStr, _ := payload["card_id"].(string)
	if cardIDStr == "" {
		return
	}
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		return
	}
	// The user claims the top of the discard is the same rank.
	if len(g.DiscardPile) == 0 {
		// invalid
		g.penalizeSnapFail(playerID)
		return
	}
	lastDiscard := g.DiscardPile[len(g.DiscardPile)-1]
	var snapCard *models.Card
	// check the player's hand if that card matches the last discard rank
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			for h := 0; h < len(g.Players[i].Hand); h++ {
				if g.Players[i].Hand[h].ID == cardID {
					snapCard = g.Players[i].Hand[h]
					break
				}
			}
			break
		}
	}
	if snapCard == nil {
		// not found
		g.penalizeSnapFail(playerID)
		return
	}
	if snapCard.Rank == lastDiscard.Rank {
		// success
		log.Printf("Player %v snap success with rank %s", playerID, snapCard.Rank)
		// remove from player's hand
		g.removeCardFromPlayerHand(playerID, cardID)
		// discard it
		g.DiscardPile = append(g.DiscardPile, snapCard)
		// if it's from your own hand, you remain with fewer cards
		// if from an opponent's, you'd also do the step of moving one of your cards to them, etc.
	} else {
		// fail
		g.penalizeSnapFail(playerID)
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

// penalizeSnapFail deals penalty cards to the snapper
func (g *CambiaGame) penalizeSnapFail(playerID uuid.UUID) {
	penalty := g.HouseRules.PenaltyCardCount
	if penalty < 1 {
		penalty = 2
	}
	for i := 0; i < penalty; i++ {
		c := g.drawCard()
		if c == nil {
			// deck exhausted => game end
			break
		}
		// add to player's hand
		for j := range g.Players {
			if g.Players[j].ID == playerID {
				g.Players[j].Hand = append(g.Players[j].Hand, c)
				break
			}
		}
	}
}

// handleCallCambia => end game after other players get 1 more turn
func (g *CambiaGame) handleCallCambia(playerID uuid.UUID) {
	log.Printf("Player %v calls Cambia", playerID)
	// each other player gets 1 more turn
	g.advanceTurn()
}

// EndGame finalizes scoring and sets GameOver. We then persist to DB, handle rating updates, etc.
func (g *CambiaGame) EndGame() {
	if g.GameOver {
		return
	}
	g.GameOver = true
	log.Printf("Ending game %v, computing final scores...", g.ID)

	// compute final scores
	finalScores := g.computeScores()
	// find winner(s)
	winners := findWinners(finalScores)

	// Persist results
	go g.persistResults(finalScores, winners) // do async
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

// persistResults writes final game_results, updates rating, etc.
func (g *CambiaGame) persistResults(finalScores map[uuid.UUID]int, winners []uuid.UUID) {
	ctx := context.Background()

	// Insert game row in DB if needed, or mark existing game as completed
	// Insert game_results for each player
	// For rating, we do a Glicko2 update for 1v1, 4p, or 7p8p, using your multi-player approach
	err := database.RecordGameAndResults(ctx, g.ID, g.Players, finalScores, winners)
	if err != nil {
		log.Printf("Error persisting results: %v", err)
	}
}

func fetchParticipants(ctx context.Context, lobbyID uuid.UUID) ([]*models.Player, error) {
	q := `
		SELECT p.user_id, p.seat_position, u.username, u.is_ephemeral
		FROM lobby_participants p
		JOIN users u ON p.user_id = u.id
		WHERE p.lobby_id = $1
		ORDER BY p.seat_position
	`
	rows, err := database.DB.Query(ctx, q, lobbyID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var players []*models.Player
	for rows.Next() {
		var (
			userID    uuid.UUID
			seatPos   int
			username  string
			ephemeral bool
		)
		if err := rows.Scan(&userID, &seatPos, &username, &ephemeral); err != nil {
			return nil, err
		}
		players = append(players, &models.Player{
			ID:        userID,
			Hand:      []*models.Card{},
			Connected: true,
			User: &models.User{
				ID:          userID,
				Username:    username,
				IsEphemeral: ephemeral,
			},
		})
	}
	return players, nil
}
