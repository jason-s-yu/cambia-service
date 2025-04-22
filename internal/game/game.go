// internal/game/game.go
package game

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/cache" // Added for historian
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/models"

	"github.com/coder/websocket" // Import websocket
)

// OnGameEndFunc is a function signature that can handle a finished game, broadcasting results to the lobby, etc.
type OnGameEndFunc func(lobbyID uuid.UUID, winner uuid.UUID, scores map[uuid.UUID]int)

// GameEventType is an enum-like type for broadcasting game actions.
// Updated event types to match specification.
type GameEventType string

// --- Event Type Definitions ---
const (
	EventPlayerSnapSuccess      GameEventType = "player_snap_success"
	EventPlayerSnapFail         GameEventType = "player_snap_fail"
	EventPlayerSnapPenalty      GameEventType = "player_snap_penalty"            // Public notification of penalty draw
	EventPrivateSnapPenalty     GameEventType = "private_snap_penalty"           // Private notification of penalty card details
	EventGameReshuffleStockpile GameEventType = "game_reshuffle_stockpile"       // Reshuffle notification
	EventPlayerDrawStockpile    GameEventType = "player_draw_stockpile"          // Public draw notification
	EventPrivateDrawStockpile   GameEventType = "private_draw_stockpile"         // Private draw details
	EventPlayerDiscard          GameEventType = "player_discard"                 // Public discard notification (includes card details)
	EventPlayerReplace          GameEventType = "player_replace"                 // Public notification of replace action (only drawn card id/idx) - DEPRECATED? Should send Discard event instead.
	EventPlayerSpecialChoice    GameEventType = "player_special_choice"          // Notify player can use special ability
	EventPlayerSpecialAction    GameEventType = "player_special_action"          // Public notification of special action taken (obfuscated)
	EventPrivateSpecialSuccess  GameEventType = "private_special_action_success" // Private notification of successful special action (revealed details)
	EventPrivateSpecialFail     GameEventType = "private_special_action_fail"    // Private notification of failed special action attempt
	EventPlayerCambia           GameEventType = "player_cambia"                  // Public notification of Cambia call
	EventGamePlayerTurn         GameEventType = "game_player_turn"               // Public notification of whose turn it is
	EventPrivateSyncState       GameEventType = "private_sync_state"             // Private state sync on connect/reconnect
	EventPrivateInitialCards    GameEventType = "private_initial_cards"          // Private reveal of initial cards
	EventGameEnd                GameEventType = "game_end"                       // Public notification game has ended + results
)

// --- Event Payload Struct Definitions ---

// EventUser is used within GameEvent payloads for user identification.
type EventUser struct {
	ID uuid.UUID `json:"id"`
}

// EventCard is used within GameEvent payloads for card identification.
// Includes optional Rank, Suit, Value, Idx for different event types.
type EventCard struct {
	ID    uuid.UUID  `json:"id"`
	Rank  string     `json:"rank,omitempty"`
	Suit  string     `json:"suit,omitempty"`
	Value int        `json:"value,omitempty"`
	Idx   *int       `json:"idx,omitempty"`  // Use pointer to allow omitting zero index
	User  *EventUser `json:"user,omitempty"` // Added for special actions targeting specific users' cards
}

// REMOVED Placeholder: ObfGameState struct is defined in sync_state.go

// GameEvent holds data about an event that can be broadcast to the clients in a consistent format.
// Refined structure to better match specification.
type GameEvent struct {
	Type    GameEventType `json:"type"`
	User    *EventUser    `json:"user,omitempty"`    // Use pointer for omitempty
	Card    *EventCard    `json:"card,omitempty"`    // Use pointer for omitempty
	Card1   *EventCard    `json:"card1,omitempty"`   // Use pointer for omitempty
	Card2   *EventCard    `json:"card2,omitempty"`   // Use pointer for omitempty
	Special string        `json:"special,omitempty"` // For special action events

	// Use 'Payload' for miscellaneous fields instead of 'Other' for clarity
	Payload map[string]interface{} `json:"payload,omitempty"`

	// Added for state sync
	State *ObfGameState `json:"state,omitempty"` // Use pointer
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

// Circuit settings relevant to game logic (penalties, bonuses).
type CircuitRules struct {
	TargetScore            int  `json:"target_score"`
	WinBonus               int  `json:"winBonus"`
	FalseCambiaPenalty     int  `json:"falseCambiaPenalty"`
	FreezeUserOnDisconnect bool `json:"freezeUserOnDisconnect"`
}
type Circuit struct {
	Enabled bool         `json:"enabled"`
	Mode    string       `json:"mode"`
	Rules   CircuitRules `json:"rules"`
}

// CambiaGame holds the entire state for a single game instance in memory.
type CambiaGame struct {
	ID      uuid.UUID
	LobbyID uuid.UUID // references the lobby that spawned this game

	HouseRules HouseRules // Use defined HouseRules struct
	Circuit    Circuit    // Added Circuit settings

	Players     []*models.Player
	Deck        []*models.Card
	DiscardPile []*models.Card

	// Turn logic
	CurrentPlayerIndex int
	TurnID             int // Increments each turn
	TurnDuration       time.Duration
	turnTimer          *time.Timer
	actionIndex        int // Increments for each game action for historian

	Started       bool
	GameOver      bool
	PreGameActive bool // Added flag for pre-game phase
	lastSeen      map[uuid.UUID]time.Time
	Mu            sync.Mutex

	// BroadcastFn is used to send events to all players. If nil, no broadcast is done.
	BroadcastFn func(ev GameEvent)

	// BroadcastToPlayerFn sends an event to a single specific player.
	BroadcastToPlayerFn func(playerID uuid.UUID, ev GameEvent)

	// OnGameEnd is invoked at game end to broadcast results, etc.
	OnGameEnd OnGameEndFunc

	// SpecialAction is used for multi-step card logic (K, Q, J, etc.)
	SpecialAction SpecialActionState

	// Cambia-called tracking
	CambiaCalled       bool
	CambiaCallerID     uuid.UUID
	CambiaFinalCounter int // Tracks turns after Cambia called - DEPRECATED? Logic moved to advanceTurn

	// Snap usage
	snapUsedForThisDiscard bool // Used for SnapRace rule

	// Timer that schedules the real start after pre-game
	preGameTimer *time.Timer
}

// NewCambiaGame builds an empty instance with a newly shuffled deck.
func NewCambiaGame() *CambiaGame {
	id, _ := uuid.NewRandom()
	g := &CambiaGame{
		ID:                     id,
		Deck:                   []*models.Card{},
		DiscardPile:            []*models.Card{},
		lastSeen:               make(map[uuid.UUID]time.Time),
		CurrentPlayerIndex:     0,
		TurnDuration:           15 * time.Second, // Default, can be overridden by HouseRules
		snapUsedForThisDiscard: false,
		actionIndex:            0, // Start action counter
		TurnID:                 0, // Start turn counter
		// Initialize HouseRules with defaults
		HouseRules: HouseRules{
			AllowDrawFromDiscardPile: false,
			AllowReplaceAbilities:    false,
			SnapRace:                 false,
			ForfeitOnDisconnect:      true,
			PenaltyDrawCount:         2, // Default penalty is 2
			AutoKickTurnCount:        3, // Default kick count
			TurnTimerSec:             15,
		},
		// Initialize Circuit with defaults if needed
		Circuit: Circuit{Enabled: false},
	}
	g.initializeDeck()
	return g
}

// BeginPreGame deals each player 4 cards, broadcasts each player's two closest cards (idx 0,1).
// Then starts a 10s timer. Once done, StartGame() is invoked.
func (g *CambiaGame) BeginPreGame() {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	if g.Started || g.GameOver || g.PreGameActive {
		return
	}
	g.PreGameActive = true
	g.logAction(uuid.Nil, "game_pregame_start", nil) // Log pregame start

	// Default turn duration from house rules if set
	if g.HouseRules.TurnTimerSec > 0 {
		g.TurnDuration = time.Duration(g.HouseRules.TurnTimerSec) * time.Second
	}

	// Deal 4 cards each
	for _, p := range g.Players {
		p.Hand = make([]*models.Card, 0, 4)
		for i := 0; i < 4; i++ {
			// Use internal draw without broadcasting events yet
			card := g.internalDrawStockpile()
			if card == nil {
				log.Printf("Warning: Ran out of cards during initial deal for game %s", g.ID)
				// Decide game rules: end game? continue? For now, just log.
				break // Stop dealing to this player
			}
			p.Hand = append(p.Hand, card)
		}
	}

	// Persist initial state after dealing
	g.persistInitialGameState()

	// For each player: privately reveal their idx=0 and idx=1 cards
	for _, p := range g.Players {
		if g.BroadcastToPlayerFn == nil { // Check if broadcast function is set
			continue
		}
		if len(p.Hand) >= 2 { // Ensure player has at least 2 cards
			c0 := p.Hand[0]
			idx0 := 0
			c1 := p.Hand[1]
			idx1 := 1
			g.firePrivateInitialCards(p.ID,
				buildEventCard(c0, &idx0, p.ID, true), // Pass p.ID as owner, reveal private info
				buildEventCard(c1, &idx1, p.ID, true), // Pass p.ID as owner, reveal private info
			)
		} else {
			log.Printf("Warning: Player %s has less than 2 cards during pregame reveal.", p.ID)
			// If they have 1 card, reveal that one?
			if len(p.Hand) == 1 {
				c0 := p.Hand[0]
				idx0 := 0
				g.firePrivateInitialCards(p.ID,
					buildEventCard(c0, &idx0, p.ID, true),
					nil, // No second card
				)
			}
		}
	}

	// Start a 10-second timer that transitions from pre-game => in-progress
	g.preGameTimer = time.AfterFunc(10*time.Second, func() {
		g.StartGame() // This will acquire the lock again
	})
}

// StartGame finalizes the pre-game stage and begins the normal turn cycle.
func (g *CambiaGame) StartGame() {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	// Check states again after acquiring lock
	if g.GameOver || g.Started || !g.PreGameActive {
		log.Printf("StartGame called in invalid state (GameOver:%v, Started:%v, PreGameActive:%v) for game %s", g.GameOver, g.Started, g.PreGameActive, g.ID)
		return
	}
	// Stop pre-game timer if it hasn't fired yet
	if g.preGameTimer != nil {
		g.preGameTimer.Stop()
		g.preGameTimer = nil
	}

	g.PreGameActive = false
	g.Started = true
	log.Printf("Game %v started.", g.ID)
	g.logAction(uuid.Nil, "game_start", nil) // Log game start

	g.scheduleNextTurnTimer()
	g.broadcastPlayerTurn()
}

// Start is left for backward compatibility, call BeginPreGame() => StartGame().
// Deprecated: Use BeginPreGame() instead.
func (g *CambiaGame) Start() {
	g.BeginPreGame() // Automatically transitions after 10s or when StartGame is called
}

// firePrivateInitialCards sends the 2 revealed cards for a player's pre-game reveal (idx 0,1).
func (g *CambiaGame) firePrivateInitialCards(playerID uuid.UUID, card1, card2 *EventCard) {
	if g.BroadcastToPlayerFn == nil {
		log.Println("Warning: BroadcastToPlayerFn is nil, cannot send private initial cards.")
		return
	}
	ev := GameEvent{
		Type:  EventPrivateInitialCards,
		Card1: card1,
		Card2: card2,
	}
	g.BroadcastToPlayerFn(playerID, ev)
}

// persistInitialGameState saves the entire deck order and each player's initial 4 cards into games.initial_game_state.
// This is used so a replay can reconstruct the original deck and hands. This does not do obfuscation.
func (g *CambiaGame) persistInitialGameState() {
	type initialState struct {
		Deck    []*models.Card            `json:"deck"`
		Players map[string][]*models.Card `json:"players"` // Use string key for JSON
	}

	// Create snapshot safely within lock
	snap := initialState{
		Deck:    make([]*models.Card, len(g.Deck)),
		Players: make(map[string][]*models.Card),
	}
	copy(snap.Deck, g.Deck) // Copy deck state

	for _, p := range g.Players {
		handCopy := make([]*models.Card, len(p.Hand))
		copy(handCopy, p.Hand)
		snap.Players[p.ID.String()] = handCopy // Use string UUID as key
	}

	// Persist asynchronously
	go database.UpsertInitialGameState(g.ID, snap)
	g.logAction(uuid.Nil, "game_initial_state_saved", map[string]interface{}{"deckSize": len(snap.Deck)})
}

// AddPlayer adds a player to the game or updates their connection status if they already exist.
func (g *CambiaGame) AddPlayer(p *models.Player) {
	g.Mu.Lock()
	defer g.Mu.Unlock()
	found := false
	for i, pl := range g.Players {
		if pl.ID == p.ID {
			// Player reconnecting
			g.Players[i].Conn = p.Conn
			g.Players[i].Connected = true
			g.lastSeen[p.ID] = time.Now()
			log.Printf("Player %s reconnected to game %s", p.ID, g.ID)
			found = true
			break
		}
	}
	if !found {
		// New player joining (only possible before game starts usually)
		if !g.Started && !g.PreGameActive {
			g.Players = append(g.Players, p)
			g.lastSeen[p.ID] = time.Now()
			log.Printf("Player %s added to game %s", p.ID, g.ID)
		} else {
			log.Printf("Player %s cannot be added to game %s because it has already started.", p.ID, g.ID)
			// Optionally send an error back to the player?
			return // Prevent adding player mid-game unless rules allow
		}
	}
	// Log add/reconnect attempt regardless
	g.logAction(p.ID, "player_add", map[string]interface{}{"reconnect": found})
}

// initializeDeck sets up a standard Cambia deck, including jokers, red kings = -1, etc.
func (g *CambiaGame) initializeDeck() {
	suits := []string{"H", "D", "C", "S"} // Use single letter suits
	ranks := []string{"A", "2", "3", "4", "5", "6", "7", "8", "9", "T", "J", "Q", "K"}
	values := map[string]int{
		"A": 1, "2": 2, "3": 3, "4": 4, "5": 5, "6": 6, "7": 7, "8": 8,
		"9": 9, "T": 10, "J": 11, "Q": 12, "K": 13, // Default King value
	}

	var deck []*models.Card
	// Add standard cards
	for _, suit := range suits {
		for _, rank := range ranks {
			val := values[rank]
			// Red Kings (Hearts, Diamonds) have value -1
			if rank == "K" && (suit == "H" || suit == "D") {
				val = -1
			}
			cid, _ := uuid.NewRandom()
			card := &models.Card{ID: cid, Suit: suit, Rank: rank, Value: val}
			deck = append(deck, card)
		}
	}
	// Add Jokers (value 0)
	jokerSuits := []string{"R", "B"} // Red Joker, Black Joker
	for _, suit := range jokerSuits {
		cid, _ := uuid.NewRandom()
		// Use rank "O" for Joker (avoids conflict with Jack "J")
		deck = append(deck, &models.Card{ID: cid, Suit: suit, Rank: "O", Value: 0})
	}

	// Shuffle the deck
	r := rand.New(rand.NewSource(time.Now().UnixNano())) // Use time-seeded random source
	r.Shuffle(len(deck), func(i, j int) {
		deck[i], deck[j] = deck[j], deck[i]
	})
	g.Deck = deck
	log.Printf("Initialized and shuffled deck for game %s with %d cards.", g.ID, len(g.Deck))
}

// internalDrawStockpile draws the top card from the stockpile, handling reshuffle internally.
// This version does NOT broadcast events, intended for setup or internal logic.
// Assumes lock is held.
func (g *CambiaGame) internalDrawStockpile() *models.Card {
	if len(g.Deck) == 0 { // Check if deck is empty *before* checking discard pile size
		if len(g.DiscardPile) == 0 {
			log.Printf("Game %s: Stockpile and discard pile are empty. Cannot draw.", g.ID)
			// Consider ending the game if no cards can be drawn
			g.EndGame() // Uncomment if desired
			return nil
		}
		// Reshuffle discard pile into stockpile
		log.Printf("Game %s: Stockpile empty. Reshuffling %d card(s) from discard pile.", g.ID, len(g.DiscardPile))

		// Temporarily store the top card if needed (e.g., if top is not reshuffled)
		// topDiscard := g.DiscardPile[len(g.DiscardPile)-1] // Not standard rule
		// g.DiscardPile = g.DiscardPile[:len(g.DiscardPile)-1] // Exclude top

		g.Deck = append(g.Deck, g.DiscardPile...) // Add discard pile cards to deck
		g.DiscardPile = []*models.Card{}          // Clear discard pile

		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		r.Shuffle(len(g.Deck), func(i, j int) { // Shuffle the combined deck
			g.Deck[i], g.Deck[j] = g.Deck[j], g.Deck[i]
		})
		log.Printf("Game %s: Reshuffled discard pile into stockpile. New size: %d", g.ID, len(g.Deck))

		// Broadcast reshuffle event
		g.fireEvent(GameEvent{
			Type: EventGameReshuffleStockpile,
			Payload: map[string]interface{}{
				"stockpileSize": len(g.Deck),
			},
		})
		g.logAction(uuid.Nil, string(EventGameReshuffleStockpile), map[string]interface{}{"newSize": len(g.Deck)})
	}

	// After potential reshuffle, check again if deck is empty
	if len(g.Deck) == 0 {
		log.Printf("Game %s: Stockpile is still empty after attempting reshuffle. Cannot draw.", g.ID)
		g.EndGame() // Game likely cannot continue
		return nil
	}

	// Draw the top card
	card := g.Deck[0]
	g.Deck = g.Deck[1:] // Remove card from deck
	return card
}

// drawTopStockpile draws the top card, handles reshuffle, AND broadcasts events.
// Assumes lock is held.
func (g *CambiaGame) drawTopStockpile(playerID uuid.UUID) *models.Card {
	card := g.internalDrawStockpile() // Use internal logic first
	if card == nil {
		// internalDrawStockpile already logs/ends game if needed
		return nil
	}

	// Broadcast public draw event (obfuscated card ID)
	g.fireEvent(GameEvent{
		Type: EventPlayerDrawStockpile,
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: card.ID}, // Only reveal ID publicly
		Payload: map[string]interface{}{
			"stockpileSize": len(g.Deck),
		},
	})

	// Broadcast private draw event (full card details)
	g.fireEventToPlayer(playerID, GameEvent{
		Type: EventPrivateDrawStockpile,
		Card: &EventCard{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
	})

	g.logAction(playerID, string(EventPlayerDrawStockpile), map[string]interface{}{"cardId": card.ID, "newSize": len(g.Deck)})
	return card
}

// drawTopDiscard draws from the top of the discard if allowed and non-empty. Broadcasts events.
// Assumes lock is held.
func (g *CambiaGame) drawTopDiscard(playerID uuid.UUID) *models.Card {
	if !g.HouseRules.AllowDrawFromDiscardPile {
		log.Printf("Game %s: Player %s attempted to draw from discard pile, but house rule disallowed.", g.ID, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Drawing from discard pile is not allowed."}})
		return nil
	}
	if len(g.DiscardPile) == 0 {
		log.Printf("Game %s: Player %s attempted to draw from empty discard pile.", g.ID, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Discard pile is empty."}})
		return nil
	}

	idx := len(g.DiscardPile) - 1
	card := g.DiscardPile[idx]
	g.DiscardPile = g.DiscardPile[:idx] // Remove card from discard

	// Broadcast public draw event (reveals card details as it came from discard)
	g.fireEvent(GameEvent{
		Type: EventPlayerDrawStockpile, // Use same public event as drawing from stockpile
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value}, // Reveal details
		Payload: map[string]interface{}{
			"source":      "discardpile", // Indicate source
			"discardSize": len(g.DiscardPile),
		},
	})

	// Broadcast private draw event (also full card details)
	g.fireEventToPlayer(playerID, GameEvent{
		Type: EventPrivateDrawStockpile, // Use same private event
		Card: &EventCard{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
		Payload: map[string]interface{}{
			"source": "discardpile",
		},
	})
	g.logAction(playerID, "action_draw_discardpile", map[string]interface{}{"cardId": card.ID, "newSize": len(g.DiscardPile)})
	return card
}

// scheduleNextTurnTimer restarts a turn timer for the current player if turnDuration > 0.
// Assumes lock is held.
func (g *CambiaGame) scheduleNextTurnTimer() {
	if g.TurnDuration <= 0 { // Skip if timer is disabled
		return
	}
	if g.turnTimer != nil {
		g.turnTimer.Stop() // Stop any existing timer
	}
	// Ensure there are players and index is valid before scheduling
	if len(g.Players) == 0 || g.CurrentPlayerIndex < 0 || g.CurrentPlayerIndex >= len(g.Players) {
		log.Printf("Game %s: Invalid player state (Players: %d, Index: %d), cannot schedule turn timer.", g.ID, len(g.Players), g.CurrentPlayerIndex)
		// Optionally end game if state is unrecoverable
		if !g.GameOver {
			g.EndGame()
		}
		return
	}
	// Get current player safely
	currentPlayer := g.Players[g.CurrentPlayerIndex]
	if !currentPlayer.Connected {
		log.Printf("Game %s: Current player %s is disconnected. Skipping turn timer schedule.", g.ID, currentPlayer.ID)
		g.advanceTurn() // Immediately advance if current player is disconnected
		return
	}

	curPID := currentPlayer.ID

	g.turnTimer = time.AfterFunc(g.TurnDuration, func() {
		// Run timeout logic in a separate goroutine to avoid deadlocking the timer callback
		// if handleTimeout needs to acquire the same lock.
		go func(playerID uuid.UUID, gameID uuid.UUID, turnID int) {
			// Re-fetch game or pass necessary state if using a central store
			// For in-memory, we need to re-acquire the lock
			g.Mu.Lock()
			defer g.Mu.Unlock()

			// Verify it's still this player's turn, game hasn't ended, and turn ID matches
			// This prevents acting on a stale timer callback
			if !g.GameOver && g.Started && len(g.Players) > g.CurrentPlayerIndex && g.Players[g.CurrentPlayerIndex].ID == playerID && g.TurnID == turnID {
				log.Printf("Game %s, Turn %d: Timer fired for player %s.", g.ID, g.TurnID, playerID)
				g.handleTimeout(playerID)
			} else {
				log.Printf("Game %s, Turn %d: Stale timer fired for player %s (Current: %d, Game Over: %v, Started: %v). Ignoring.", g.ID, turnID, playerID, g.TurnID, g.GameOver, g.Started)
			}
		}(curPID, g.ID, g.TurnID) // Pass game ID and current TurnID for validation
	})
	// log.Printf("Game %s: Scheduled turn timer for player %s (%s duration).", g.ID, curPID, g.TurnDuration)
}

// handleTimeout forcibly handles player timeout.
// If player has drawn a card, discard it.
// If player is in a special action, skip it.
// Otherwise, draw a card and immediately discard it.
// Assumes lock is held.
func (g *CambiaGame) handleTimeout(playerID uuid.UUID) {
	log.Printf("Game %s: Player %s timed out.", g.ID, playerID)
	g.logAction(playerID, "player_timeout", nil)

	player := g.getPlayerByID(playerID)
	if player == nil {
		log.Printf("Game %s: Timed out player %s not found. Advancing turn.", g.ID, playerID)
		g.advanceTurn()
		return
	}

	// If a special action is active for this player, skip it.
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerID {
		specialRank := g.SpecialAction.CardRank
		log.Printf("Game %s: Timeout skipping special action for rank %s for player %s", g.ID, specialRank, playerID)

		// Process the skip action directly
		// Need to temporarily release lock if ProcessSpecialAction acquires it internally,
		// but ProcessSpecialAction assumes lock is already held.
		g.processSkipSpecialAction(playerID) // Call internal skip handler
		// Turn is advanced within processSkipSpecialAction
		return // Exit after handling special action skip
	}

	// Check if the player has a drawn card waiting for action
	cardToDiscard := player.DrawnCard
	if cardToDiscard != nil {
		player.DrawnCard = nil // Clear the drawn card
		log.Printf("Game %s: Player %s timed out with drawn card %s. Discarding.", g.ID, playerID, cardToDiscard.ID)
	} else {
		// If no card was drawn yet, draw one from the stockpile and prepare to discard it.
		log.Printf("Game %s: Player %s timed out without drawing. Drawing and discarding.", g.ID, playerID)
		cardToDiscard = g.drawTopStockpile(playerID) // Draws and broadcasts public/private draw events
		if cardToDiscard == nil {
			log.Printf("Game %s: Player %s timed out, but no cards left to draw. Advancing turn.", g.ID, playerID)
			g.advanceTurn() // No card to discard, just advance
			return
		}
	}

	// Discard the card (either the one they held or the one just drawn)
	g.DiscardPile = append(g.DiscardPile, cardToDiscard)
	g.logAction(playerID, "player_timeout_discard", map[string]interface{}{"cardId": cardToDiscard.ID})
	// Broadcast the discard event
	g.fireEvent(GameEvent{
		Type: EventPlayerDiscard,
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: cardToDiscard.ID, Rank: cardToDiscard.Rank, Suit: cardToDiscard.Suit, Value: cardToDiscard.Value},
	})
	g.snapUsedForThisDiscard = false // Reset snap state
	// Do NOT trigger special abilities on timeout discard

	// Advance the turn after handling the timeout discard
	g.advanceTurn()
}

// broadcastPlayerTurn notifies all players whose turn it is now.
// Assumes lock is held.
func (g *CambiaGame) broadcastPlayerTurn() {
	if g.GameOver || !g.Started || len(g.Players) == 0 || g.CurrentPlayerIndex < 0 || g.CurrentPlayerIndex >= len(g.Players) {
		log.Printf("Cannot broadcast player turn in current state (GameOver:%v, Started:%v, Players:%d, Index:%d) for game %s", g.GameOver, g.Started, len(g.Players), g.CurrentPlayerIndex, g.ID)
		return
	}
	currentPID := g.Players[g.CurrentPlayerIndex].ID
	// Turn ID is incremented *before* scheduling the timer for that turn
	log.Printf("Game %s: Turn %d starting for player %s.", g.ID, g.TurnID, currentPID)
	g.fireEvent(GameEvent{
		Type: EventGamePlayerTurn,
		User: &EventUser{ID: currentPID},
		Payload: map[string]interface{}{
			"turn": g.TurnID,
		},
	})
	g.logAction(currentPID, string(EventGamePlayerTurn), map[string]interface{}{"turn": g.TurnID})
}

// fireEvent broadcasts an event to all connected players.
// Assumes lock is held.
func (g *CambiaGame) fireEvent(ev GameEvent) {
	if g.BroadcastFn != nil {
		g.BroadcastFn(ev)
	} else {
		log.Printf("Warning: BroadcastFn is nil for game %s, cannot broadcast event type %s.", g.ID, ev.Type)
	}
}

// fireEventToPlayer sends an event only to a specific player.
// Assumes lock is held.
func (g *CambiaGame) fireEventToPlayer(playerID uuid.UUID, ev GameEvent) {
	if g.BroadcastToPlayerFn != nil {
		// Check if the target player is actually connected
		targetPlayer := g.getPlayerByID(playerID)
		if targetPlayer != nil && targetPlayer.Connected {
			g.BroadcastToPlayerFn(playerID, ev)
		} else {
			// log.Printf("Warning: Target player %s not found or not connected for private event type %s in game %s.", playerID, ev.Type, g.ID)
		}
	} else {
		log.Printf("Warning: BroadcastToPlayerFn is nil for game %s, cannot send private event type %s to player %s.", g.ID, ev.Type, playerID)
	}
}

// advanceTurn moves to the next valid player, handles Cambia final round logic, and game end.
// Assumes lock is held.
func (g *CambiaGame) advanceTurn() {
	if g.GameOver {
		return
	}
	if len(g.Players) == 0 {
		log.Printf("Game %s: Cannot advance turn, no players in game.", g.ID)
		if !g.GameOver {
			g.EndGame() // End game if no players left
		}
		return
	}

	// Calculate the index of the player whose turn just ended
	prevPlayerIndex := g.CurrentPlayerIndex // Index before advancing

	// If Cambia is called, check if the game should end
	if g.CambiaCalled {
		callerIdx := -1
		for i, p := range g.Players {
			if p.ID == g.CambiaCallerID {
				callerIdx = i
				break
			}
		}

		// If the player whose turn just ended (prevPlayerIndex) is the one *before* the caller,
		// the final round is over. The modulo arithmetic handles wrap-around.
		if callerIdx != -1 && prevPlayerIndex == (callerIdx-1+len(g.Players))%len(g.Players) {
			log.Printf("Game %s: Final turn after Cambia call completed by player %s. Ending game.", g.ID, g.Players[prevPlayerIndex].ID)
			if !g.GameOver {
				g.EndGame() // End the game immediately
			}
			return // Stop processing turn advancement
		}
	}

	// Increment Turn ID *before* scheduling the next turn
	g.TurnID++

	// Move to the next player index
	nextIndex := (prevPlayerIndex + 1) % len(g.Players)

	// Skip disconnected players if needed (or handle differently based on rules)
	// REMOVED unused startIndex
	skippedCount := 0 // Track skips to prevent infinite loop
	for !g.Players[nextIndex].Connected {
		log.Printf("Game %s: Skipping disconnected player %s at index %d.", g.ID, g.Players[nextIndex].ID, nextIndex)
		nextIndex = (nextIndex + 1) % len(g.Players)
		skippedCount++
		if skippedCount >= len(g.Players) { // Use skippedCount instead of comparing index
			log.Printf("Game %s: No connected players left to advance turn to after checking all. Ending game.", g.ID)
			if !g.GameOver {
				g.EndGame()
			}
			return
		}
	}

	g.CurrentPlayerIndex = nextIndex

	// Reset player's drawn card state for the new turn
	if len(g.Players) > g.CurrentPlayerIndex && g.Players[g.CurrentPlayerIndex] != nil {
		g.Players[g.CurrentPlayerIndex].DrawnCard = nil
	} else {
		log.Printf("Error: CurrentPlayerIndex %d out of bounds or player is nil during turn advance.", g.CurrentPlayerIndex)
		// Potentially end game or handle error state
		if !g.GameOver {
			g.EndGame()
		}
		return
	}

	// Stop previous timer and schedule next
	if g.turnTimer != nil {
		g.turnTimer.Stop()
	}
	g.scheduleNextTurnTimer() // This now uses the incremented TurnID

	// Broadcast whose turn it is now
	g.broadcastPlayerTurn()
}

// HandleDisconnect processes a player’s disconnection.
func (g *CambiaGame) HandleDisconnect(playerID uuid.UUID) {
	g.Mu.Lock()
	defer g.Mu.Unlock()
	log.Printf("Game %s: Handling disconnect for player %s.", g.ID, playerID)
	g.logAction(playerID, "player_disconnect", nil)
	found := false
	playerIndex := -1
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			if !g.Players[i].Connected {
				log.Printf("Game %s: Player %s already marked as disconnected.", g.ID, playerID)
				return // Avoid processing disconnect twice
			}
			g.Players[i].Connected = false
			g.Players[i].Conn = nil // Clear connection object
			found = true
			playerIndex = i
			break
		}
	}
	if !found {
		log.Printf("Game %s: Disconnected player %s not found in game.", g.ID, playerID)
		return
	}

	// Check if game should end or turn should advance
	shouldAdvance := false
	if g.Started && !g.GameOver {
		// Option 1: Forfeit immediately if rule enabled and check if game should end
		if g.HouseRules.ForfeitOnDisconnect {
			log.Printf("Game %s: Player %s disconnected, marked as forfeited due to house rules.", g.ID, playerID)
			// Check if only one player remains (or zero)
			if g.countConnectedPlayers() <= 1 {
				log.Printf("Game %s: Only one or zero players left connected after forfeit. Ending game.", g.ID)
				if !g.GameOver {
					g.EndGame() // End game immediately
				}
				return // Don't advance turn if game ended
			}
		}
		// Check if the disconnected player was the current player
		if playerIndex == g.CurrentPlayerIndex {
			log.Printf("Game %s: Current player %s disconnected. Advancing turn.", g.ID, playerID)
			shouldAdvance = true // Advance turn if the current player disconnected
		}
	}

	// Broadcast the disconnected state *before* advancing turn
	g.broadcastSyncStateToAll()

	// Advance turn if needed (e.g., current player left)
	if shouldAdvance {
		// Call advanceTurn internally without unlocking/relocking
		g.advanceTurn()
	}
}

// handleReconnect marks a player as connected again.
func (g *CambiaGame) HandleReconnect(playerID uuid.UUID, conn *websocket.Conn) {
	g.Mu.Lock()
	defer g.Mu.Unlock()
	log.Printf("Game %s: Handling reconnect for player %s.", g.ID, playerID)
	g.logAction(playerID, "player_reconnect", nil)
	found := false
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			if g.Players[i].Connected {
				log.Printf("Game %s: Player %s attempting to reconnect but already marked as connected.", g.ID, playerID)
				// Update connection anyway, maybe the old one died silently
			}
			g.Players[i].Connected = true
			g.Players[i].Conn = conn // Update connection object
			g.lastSeen[playerID] = time.Now()
			found = true
			// Send current game state upon reconnect
			g.sendSyncState(playerID)
			break
		}
	}
	if !found {
		log.Printf("Game %s: Reconnecting player %s not found in game.", g.ID, playerID)
		// Optionally add them back if game rules allow late joins/rejoins after full removal
		// If adding back, need to deal cards, etc. Complex logic.
		// For now, just log. Close the connection?
		if conn != nil {
			conn.Close(websocket.StatusPolicyViolation, "Game not found or you were removed.")
		}
	} else {
		// Broadcast sync state to everyone else too so they see the player reconnect
		g.broadcastSyncStateToAll()

		// If it's this player's turn and they were disconnected, restart timer?
		if g.Started && !g.GameOver && g.CurrentPlayerIndex >= 0 && g.CurrentPlayerIndex < len(g.Players) && g.Players[g.CurrentPlayerIndex].ID == playerID {
			log.Printf("Game %s: Reconnected player %s's turn. Restarting turn timer.", g.ID, playerID)
			g.scheduleNextTurnTimer() // Reschedule timer for the reconnected player
		}
	}
}

// sendSyncState sends the obfuscated game state to a specific player.
// Assumes lock is held by caller.
func (g *CambiaGame) sendSyncState(playerID uuid.UUID) {
	if g.BroadcastToPlayerFn == nil {
		log.Println("Warning: BroadcastToPlayerFn is nil, cannot send sync state.")
		return
	}
	// Generate state specifically for this player
	state := g.GetCurrentObfuscatedGameState(playerID) // Call the method
	ev := GameEvent{
		Type:  EventPrivateSyncState,
		State: &state, // Embed the state object directly
	}
	g.fireEventToPlayer(playerID, ev)
	// log.Printf("Game %s: Sent sync state to player %s.", g.ID, playerID) // Reduce log noise
}

// broadcastSyncStateToAll sends the sync state to all currently connected players.
// Assumes lock is held by caller.
func (g *CambiaGame) broadcastSyncStateToAll() {
	if g.BroadcastToPlayerFn == nil {
		log.Println("Warning: BroadcastToPlayerFn is nil, cannot broadcast sync state to all.")
		return
	}
	playerIDs := []uuid.UUID{}
	for _, p := range g.Players {
		if p.Connected {
			playerIDs = append(playerIDs, p.ID)
		}
	}

	// Send state outside the player loop if generation is safe
	for _, playerID := range playerIDs {
		// Need to generate state specifically for each player
		g.sendSyncState(playerID)
	}
	log.Printf("Game %s: Broadcasted sync state to %d connected players.", g.ID, len(playerIDs))
}

// countConnectedPlayers returns the number of players currently marked as connected.
// Assumes lock is held by caller.
func (g *CambiaGame) countConnectedPlayers() int {
	count := 0
	for _, p := range g.Players {
		if p.Connected {
			count++
		}
	}
	return count
}

// drawCardFromLocation handles drawing from stockpile or discard, wrapper for events.
// Assumes lock is held by caller.
func (g *CambiaGame) drawCardFromLocation(playerID uuid.UUID, location string) *models.Card {
	var card *models.Card
	if location == "stockpile" {
		card = g.drawTopStockpile(playerID)
	} else if location == "discardpile" {
		card = g.drawTopDiscard(playerID)
	} else {
		log.Printf("Game %s: Invalid draw location '%s' requested by player %s.", g.ID, location, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Invalid draw location specified."}})
		return nil
	}
	return card
}

// HandlePlayerAction interprets draw, discard, snap, cambia, replace, etc.
// This is the main router for player actions during their turn (or snap out of turn).
// Assumes lock is held by the caller (e.g., the WS handler).
func (g *CambiaGame) HandlePlayerAction(playerID uuid.UUID, action models.GameAction) {
	// NOTE: Lock is assumed to be HELD by the caller.

	if g.GameOver {
		log.Printf("Game %s: Action %s received from player %s after game over. Ignoring.", g.ID, action.ActionType, playerID)
		return
	}
	if !g.Started && !g.PreGameActive {
		log.Printf("Game %s: Action %s received from player %s before game start. Ignoring.", g.ID, action.ActionType, playerID)
		return
	}
	if g.PreGameActive { // No actions allowed during pregame reveal
		log.Printf("Game %s: Action %s received from player %s during pregame. Ignoring.", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Cannot perform actions during pre-game reveal."}})
		return
	}

	// Check if player exists and is connected
	player := g.getPlayerByID(playerID)
	if player == nil || !player.Connected {
		log.Printf("Game %s: Action %s received from non-existent or disconnected player %s. Ignoring.", g.ID, action.ActionType, playerID)
		return
	}

	// Check if it's the player's turn, except for snap action
	isCurrentPlayer := len(g.Players) > g.CurrentPlayerIndex && g.Players[g.CurrentPlayerIndex].ID == playerID
	if action.ActionType != "action_snap" && !isCurrentPlayer {
		log.Printf("Game %s: Action %s received from player %s out of turn. Ignoring.", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "It's not your turn."}})
		return
	}
	// Check if player is trying to act while a special action is pending for them
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerID && action.ActionType != "action_special" {
		log.Printf("Game %s: Player %s attempted action %s while special action for rank %s is pending. Ignoring.", g.ID, playerID, action.ActionType, g.SpecialAction.CardRank)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "You must resolve the special card action first (use action_special with 'skip' or required payload)."}})
		return
	}
	// Check if player already drew a card and is trying to draw again
	if player.DrawnCard != nil && (action.ActionType == "action_draw_stockpile" || action.ActionType == "action_draw_discardpile") {
		log.Printf("Game %s: Player %s tried to draw again after already drawing. Ignoring.", g.ID, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "You have already drawn a card this turn."}})
		return
	}
	// Check if player hasn't drawn and is trying to discard/replace
	if player.DrawnCard == nil && (action.ActionType == "action_discard" || action.ActionType == "action_replace") {
		log.Printf("Game %s: Player %s tried to %s without drawing first. Ignoring.", g.ID, playerID, action.ActionType)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "You must draw a card first."}})
		return
	}

	// Update last seen time for the active player
	g.lastSeen[playerID] = time.Now()

	// Route the action
	switch action.ActionType {
	case "action_snap":
		g.handleSnap(playerID, action.Payload) // Snap can happen anytime
	case "action_draw_stockpile":
		g.handleDrawFrom(playerID, "stockpile")
	case "action_draw_discardpile": // Corrected type name
		g.handleDrawFrom(playerID, "discardpile")
	case "action_discard":
		g.handleDiscard(playerID, action.Payload)
	case "action_replace":
		g.handleReplace(playerID, action.Payload)
	case "action_cambia":
		g.handleCallCambia(playerID)
	// action_special is handled by ProcessSpecialAction, called from WS handler
	default:
		log.Printf("Game %s: Unknown action type '%s' received from player %s.", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Unknown action type."}})
	}
}

// handleDrawFrom handles drawing from either stockpile or discard pile.
// Assumes lock is held by caller.
func (g *CambiaGame) handleDrawFrom(playerID uuid.UUID, location string) {
	player := g.getPlayerByID(playerID)
	if player == nil {
		log.Printf("Game %s: Player %s not found for draw action.", g.ID, playerID)
		return // Should not happen if checks passed
	}
	// Redundant check, already done in HandlePlayerAction
	// if player.DrawnCard != nil { ... }

	card := g.drawCardFromLocation(playerID, location) // This handles broadcasts internally
	if card != nil {
		player.DrawnCard = card // Store the drawn card temporarily
		g.ResetTurnTimer()      // Reset timer after successful draw
	} else {
		// Draw failed (e.g., empty piles), turn should likely advance or game end
		log.Printf("Game %s: Draw from %s failed for player %s. Advancing turn.", g.ID, location, playerID)
		// internalDrawStockpile might end the game if both piles are empty
		if !g.GameOver {
			g.advanceTurn()
		}
	}
	// No turn advance here if draw succeeded; player must now discard or replace.
}

// handleDiscard handles discarding the player's DrawnCard.
// Assumes lock is held by caller.
func (g *CambiaGame) handleDiscard(playerID uuid.UUID, payload map[string]interface{}) {
	player := g.getPlayerByID(playerID)
	if player == nil {
		log.Printf("Game %s: Player %s not found for discard action.", g.ID, playerID)
		return
	}
	// Redundant check, already done in HandlePlayerAction
	// if player.DrawnCard == nil { ... }

	// Check if DrawnCard is nil before accessing its ID (important!)
	if player.DrawnCard == nil {
		log.Printf("Game %s: Player %s discard attempt failed, no card was drawn.", g.ID, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "No card drawn to discard."}})
		return
	}

	// Verify the card ID in the payload matches the drawn card
	cardIDStr, _ := payload["id"].(string) // Use Card.ID from payload
	cardID, err := uuid.Parse(cardIDStr)

	if err != nil || player.DrawnCard.ID != cardID {
		log.Printf("Game %s: Player %s discard payload card ID '%s' does not match drawn card ID '%s'. Ignoring.", g.ID, playerID, cardIDStr, player.DrawnCard.ID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Card ID mismatch for discard."}})
		return
	}

	discardedCard := player.DrawnCard
	player.DrawnCard = nil // Clear the drawn card from player state

	g.DiscardPile = append(g.DiscardPile, discardedCard)
	g.logAction(playerID, string(EventPlayerDiscard), map[string]interface{}{"cardId": discardedCard.ID, "source": "drawn"})

	// Broadcast discard event with full card details
	g.fireEvent(GameEvent{
		Type: EventPlayerDiscard,
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: discardedCard.ID, Rank: discardedCard.Rank, Suit: discardedCard.Suit, Value: discardedCard.Value},
		// No Idx needed here as per spec for discarding a drawn card
	})

	g.snapUsedForThisDiscard = false // Reset snap state for the new discard

	// Check for special ability only on freshly drawn cards (standard rule)
	// AllowReplaceAbilities rule applies only when replacing, not direct discard.
	triggered := g.applySpecialAbilityIfFreshlyDrawn(discardedCard, playerID)
	// If triggered, applySpecialAbility handles timer reset.
	// If not triggered, applySpecialAbility advances the turn.
	if !triggered {
		// Turn already advanced in applySpecialAbilityIfFreshlyDrawn if no ability
	} else {
		// Timer reset in applySpecialAbilityIfFreshlyDrawn if ability triggered
	}
}

// handleReplace handles swapping the player's DrawnCard with a card at a specific index in their hand.
// Assumes lock is held by caller.
func (g *CambiaGame) handleReplace(playerID uuid.UUID, payload map[string]interface{}) {
	player := g.getPlayerByID(playerID)
	if player == nil {
		log.Printf("Game %s: Player %s not found for replace action.", g.ID, playerID)
		return
	}
	if player.DrawnCard == nil {
		log.Printf("Game %s: Player %s replace attempt failed, no card was drawn.", g.ID, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "No card drawn to replace with."}})
		return
	}

	// Card ID in payload should be the ID of the card *being replaced* in the hand.
	cardIDToReplaceStr, _ := payload["id"].(string) // Use Card.ID from payload
	idxToReplaceFloat, idxOK := payload["idx"].(float64)
	idxToReplace := int(idxToReplaceFloat)

	if !idxOK || idxToReplace < 0 || idxToReplace >= len(player.Hand) {
		log.Printf("Game %s: Player %s provided invalid index %d for replace action. Hand size: %d. Ignoring.", g.ID, playerID, idxToReplace, len(player.Hand))
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Invalid index for replacement."}})
		return
	}

	cardToReplace := player.Hand[idxToReplace]
	// Optional: Verify the ID from payload matches the card at the index
	cardIDToReplace, err := uuid.Parse(cardIDToReplaceStr)
	if err != nil || cardToReplace.ID != cardIDToReplace {
		log.Printf("Game %s: Player %s replace payload card ID '%s' does not match card ID '%s' at index %d. Ignoring.", g.ID, playerID, cardIDToReplaceStr, cardToReplace.ID, idxToReplace)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Card ID mismatch for replacement target."}})
		return
	}

	drawnCard := player.DrawnCard
	player.DrawnCard = nil // Clear drawn card state

	// Perform the swap in the player's hand
	player.Hand[idxToReplace] = drawnCard

	// Add the replaced card to the discard pile
	g.DiscardPile = append(g.DiscardPile, cardToReplace)
	g.logAction(playerID, string(EventPlayerDiscard), map[string]interface{}{ // Log as a discard event
		"cardId":  cardToReplace.ID,
		"index":   idxToReplace,
		"source":  "replace", // Indicate source was replacement
		"drawnId": drawnCard.ID,
	})

	// Broadcast discard event for the card leaving the hand
	eventIdx := idxToReplace // Capture index for pointer
	g.fireEvent(GameEvent{
		Type: EventPlayerDiscard,
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: cardToReplace.ID, Rank: cardToReplace.Rank, Suit: cardToReplace.Suit, Value: cardToReplace.Value, Idx: &eventIdx}, // Include index here
	})

	g.snapUsedForThisDiscard = false // Reset snap state

	// Check for special ability on the *replaced* card if the house rule allows it.
	abilityTriggered := false
	if g.HouseRules.AllowReplaceAbilities {
		abilityTriggered = g.applySpecialAbilityIfFreshlyDrawn(cardToReplace, playerID)
	}

	// If no special ability was triggered by the replaced card, advance the turn.
	if !abilityTriggered {
		g.advanceTurn()
	} else {
		// Timer reset handled within applySpecialAbility
	}
}

// handleSnap processes an out-of-turn snap/burn attempt.
// Assumes lock is held by caller.
func (g *CambiaGame) handleSnap(playerID uuid.UUID, payload map[string]interface{}) {
	cardIDStr, _ := payload["id"].(string) // Use Card.ID from payload
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		log.Printf("Game %s: Invalid card ID '%s' in snap payload from player %s. Ignoring.", g.ID, cardIDStr, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Invalid card ID format for snap."}})
		return
	}

	g.logAction(playerID, "action_snap_attempt", map[string]interface{}{"cardId": cardID})

	if len(g.DiscardPile) == 0 {
		log.Printf("Game %s: Player %s attempted to snap card %s, but discard pile is empty. Penalizing.", g.ID, playerID, cardID)
		g.penalizeSnapFail(playerID, nil) // Pass nil as card wasn't found/validated yet
		return
	}

	// If SnapRace is true and a snap already succeeded for this discard, fail subsequent attempts.
	if g.HouseRules.SnapRace && g.snapUsedForThisDiscard {
		log.Printf("Game %s: Player %s snap attempt for card %s failed due to SnapRace rule.", g.ID, playerID, cardID)
		g.penalizeSnapFail(playerID, nil) // Card wasn't validated, pass nil
		return
	}

	lastDiscardedCard := g.DiscardPile[len(g.DiscardPile)-1]
	player := g.getPlayerByID(playerID)
	if player == nil {
		log.Printf("Game %s: Player %s not found for snap action.", g.ID, playerID)
		return // Should not happen if player is connected
	}

	// Find the card in the player's hand
	var snappedCard *models.Card
	var snappedCardIdx int = -1
	for h, c := range player.Hand {
		if c.ID == cardID {
			snappedCard = c
			snappedCardIdx = h
			break
		}
	}

	if snappedCard == nil {
		log.Printf("Game %s: Player %s attempted to snap card %s which was not found in their hand. Penalizing.", g.ID, playerID, cardID)
		g.penalizeSnapFail(playerID, nil) // Card not found, pass nil
		return
	}

	// Check if ranks match
	if snappedCard.Rank == lastDiscardedCard.Rank {
		// Successful Snap!
		log.Printf("Game %s: Player %s successfully snapped card %s (Rank: %s).", g.ID, playerID, snappedCard.ID, snappedCard.Rank)
		g.logAction(playerID, string(EventPlayerSnapSuccess), map[string]interface{}{"cardId": snappedCard.ID, "rank": snappedCard.Rank})

		// Mark snap used if race rule applies
		if g.HouseRules.SnapRace {
			g.snapUsedForThisDiscard = true
		}

		// Remove card from player's hand
		player.Hand = append(player.Hand[:snappedCardIdx], player.Hand[snappedCardIdx+1:]...)

		// Add snapped card to discard pile
		g.DiscardPile = append(g.DiscardPile, snappedCard)

		// Broadcast success event
		eventIdx := snappedCardIdx // Use the original index in hand for the event
		g.fireEvent(GameEvent{
			Type: EventPlayerSnapSuccess,
			User: &EventUser{ID: playerID},
			Card: &EventCard{
				ID:    snappedCard.ID,
				Rank:  snappedCard.Rank,
				Suit:  snappedCard.Suit,
				Value: snappedCard.Value,
				Idx:   &eventIdx, // Include index from where it was snapped
			},
		})
		// Consider if turn should change on successful snap (rules don't specify, usually doesn't)
	} else {
		// Failed Snap (ranks don't match)
		log.Printf("Game %s: Player %s failed snap. Card %s (Rank: %s) does not match discard top %s (Rank: %s). Penalizing.", g.ID, playerID, snappedCard.ID, snappedCard.Rank, lastDiscardedCard.ID, lastDiscardedCard.Rank)
		g.penalizeSnapFail(playerID, snappedCard) // Pass the card they attempted to snap
	}
}

// penalizeSnapFail handles the penalty for an incorrect snap attempt.
// Assumes lock is held by caller.
func (g *CambiaGame) penalizeSnapFail(playerID uuid.UUID, attemptedCard *models.Card) {
	var attemptedCardID uuid.UUID
	if attemptedCard != nil {
		attemptedCardID = attemptedCard.ID
	}
	g.logAction(playerID, string(EventPlayerSnapFail), map[string]interface{}{"attemptedCardId": attemptedCardID})
	player := g.getPlayerByID(playerID)
	if player == nil {
		return // Should not happen
	}

	// Broadcast public failure event
	failEvent := GameEvent{
		Type: EventPlayerSnapFail,
		User: &EventUser{ID: playerID},
	}
	// Include card details if a card was actually attempted (not just empty discard or race fail)
	if attemptedCard != nil {
		// Find the index of the failed card for the event payload
		failIdx := -1
		for i, c := range player.Hand {
			if c.ID == attemptedCard.ID {
				failIdx = i
				break
			}
		}
		failEvent.Card = &EventCard{
			ID:    attemptedCard.ID,
			Rank:  attemptedCard.Rank, // Reveal rank/suit/value on failure as per spec
			Suit:  attemptedCard.Suit,
			Value: attemptedCard.Value,
		}
		if failIdx != -1 {
			eventIdx := failIdx // Capture for pointer
			failEvent.Card.Idx = &eventIdx
		}
	}
	g.fireEvent(failEvent)

	// Apply penalty draws
	penaltyCount := g.HouseRules.PenaltyDrawCount
	if penaltyCount <= 0 {
		log.Printf("Game %s: Penalty draw count is %d. No penalty cards drawn for failed snap by %s.", g.ID, penaltyCount, playerID)
		return // No penalty if count is zero or less
	}
	log.Printf("Game %s: Applying %d penalty card(s) to player %s for failed snap.", g.ID, penaltyCount, playerID)

	// REMOVED unused initialHandSize
	newCardIDs := []uuid.UUID{} // Track IDs for logging

	for i := 0; i < penaltyCount; i++ {
		card := g.internalDrawStockpile() // Draw internally without broadcasting draw events
		if card == nil {
			log.Printf("Game %s: No more cards in stockpile to draw for penalty %d/%d for player %s.", g.ID, i+1, penaltyCount, playerID)
			break // Stop if deck runs out
		}

		// Add card to player's hand
		player.Hand = append(player.Hand, card)
		newCardIDs = append(newCardIDs, card.ID)
		newCardIndex := len(player.Hand) - 1 // The index of the newly added card

		// Broadcast public penalty draw notification (obfuscated card ID)
		g.fireEvent(GameEvent{
			Type: EventPlayerSnapPenalty,
			User: &EventUser{ID: playerID}, // Use standard "User" field
			Card: &EventCard{ID: card.ID},  // Public only gets ID
			Payload: map[string]interface{}{
				"count": i + 1,
				"total": penaltyCount,
			},
		})

		// Broadcast private penalty card details
		privateIdx := newCardIndex // Use the actual index in hand for private message
		g.fireEventToPlayer(playerID, GameEvent{
			Type: EventPrivateSnapPenalty,
			Card: &EventCard{
				ID:    card.ID,
				Idx:   &privateIdx,
				Rank:  card.Rank, // Reveal full details privately
				Suit:  card.Suit,
				Value: card.Value,
			},
			Payload: map[string]interface{}{
				"count": i + 1,
				"total": penaltyCount,
			},
		})
	}
	g.logAction(playerID, "player_snap_penalty_applied", map[string]interface{}{"count": len(newCardIDs), "newCards": newCardIDs})
}

// handleCallCambia handles a player calling Cambia.
// Assumes lock is held by caller.
func (g *CambiaGame) handleCallCambia(playerID uuid.UUID) {
	if g.CambiaCalled {
		log.Printf("Game %s: Player %s attempted to call Cambia, but it was already called by %s. Ignoring.", g.ID, playerID, g.CambiaCallerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Cambia has already been called."}})
		return
	}

	// Validate house rules for calling Cambia (e.g., minimum rounds)
	// Example: Require at least one full round per player before Cambia can be called
	minTurnsBeforeCambia := len(g.Players) // Simplistic check: turn ID >= num players
	if g.TurnID < minTurnsBeforeCambia {
		log.Printf("Game %s: Player %s attempted to call Cambia on turn %d, before minimum turns (%d) requirement met. Ignoring.", g.ID, playerID, g.TurnID, minTurnsBeforeCambia)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Cannot call Cambia yet."}})
		return
	}

	log.Printf("Game %s: Player %s calls Cambia!", g.ID, playerID)
	g.logAction(playerID, string(EventPlayerCambia), nil)

	g.CambiaCalled = true
	g.CambiaCallerID = playerID

	// Mark the player model
	player := g.getPlayerByID(playerID)
	if player != nil {
		player.HasCalledCambia = true
	} else {
		log.Printf("Error: Could not find player %s to mark HasCalledCambia", playerID)
	}

	// Broadcast the event
	g.fireEvent(GameEvent{
		Type: EventPlayerCambia,
		User: &EventUser{ID: playerID},
	})

	// The player's turn ends immediately after calling Cambia.
	// No draw/discard/replace needed.
	if player != nil {
		player.DrawnCard = nil // Ensure no lingering drawn card state
	}
	g.advanceTurn()
}

// applySpecialAbilityIfFreshlyDrawn checks if the card has a special ability and triggers the flow.
// Returns true if a special ability was triggered, false otherwise.
// Assumes lock is held by caller.
func (g *CambiaGame) applySpecialAbilityIfFreshlyDrawn(c *models.Card, playerID uuid.UUID) bool {
	specialType := rankToSpecial(c.Rank)
	if specialType != "" {
		log.Printf("Game %s: Player %s discarded %s (%s), triggering special action choice: %s", g.ID, playerID, c.ID, c.Rank, specialType)

		// Activate special action state
		g.SpecialAction = SpecialActionState{
			Active:        true,
			PlayerID:      playerID,
			CardRank:      c.Rank,
			FirstStepDone: false, // Reset for King
		}

		// Reset turn timer to give player time to decide
		g.ResetTurnTimer() // Corrected case

		// Broadcast the choice event
		g.fireEvent(GameEvent{
			Type:    EventPlayerSpecialChoice,
			User:    &EventUser{ID: playerID},
			Card:    &EventCard{ID: c.ID, Rank: c.Rank}, // Show rank that triggered
			Special: specialType,
		})
		g.logAction(playerID, string(EventPlayerSpecialChoice), map[string]interface{}{"cardId": c.ID, "rank": c.Rank, "special": specialType})
		return true // Special ability triggered, turn does NOT advance yet
	}
	// No special ability, advance turn
	g.advanceTurn()
	return false
}

// rankToSpecial maps card ranks to the "special" string identifier used in actions.
func rankToSpecial(rank string) string {
	switch rank {
	case "7", "8":
		return "peek_self"
	case "9", "T": // 9 and 10 (Ten)
		return "peek_other"
	case "J", "Q":
		return "swap_blind"
	case "K":
		return "swap_peek" // Refers to the initial step for King
	default:
		return "" // Not a special ability card
	}
}

// EndGame finalizes scoring, sets GameOver, logs result, and calls OnGameEnd callback.
// Assumes lock is held by caller.
func (g *CambiaGame) EndGame() {
	if g.GameOver {
		log.Printf("Game %s: EndGame called, but game is already over.", g.ID)
		return
	}
	g.GameOver = true
	g.Started = false // Mark as not started anymore
	log.Printf("Game %s: Ending game. Computing final scores...", g.ID)

	// Stop timers
	if g.turnTimer != nil {
		g.turnTimer.Stop()
		g.turnTimer = nil // Clear timer
	}
	if g.preGameTimer != nil {
		g.preGameTimer.Stop()
		g.preGameTimer = nil // Clear timer
	}

	finalScores := g.computeScores()
	winners, penaltyApplies := g.findWinnersWithCambiaLogic(finalScores)

	// --- Apply Circuit Bonuses/Penalties ---
	adjustedScores := make(map[uuid.UUID]int)
	for id, score := range finalScores {
		adjustedScores[id] = score // Copy initial scores
	}

	// Apply penalty to caller if they didn't win
	if penaltyApplies && g.CambiaCallerID != uuid.Nil {
		if _, ok := adjustedScores[g.CambiaCallerID]; ok {
			penaltyValue := 1 // Default penalty
			if g.Circuit.Enabled && g.Circuit.Rules.FalseCambiaPenalty > 0 {
				penaltyValue = g.Circuit.Rules.FalseCambiaPenalty
			}
			adjustedScores[g.CambiaCallerID] += penaltyValue
			log.Printf("Game %s: Applying +%d penalty to Cambia caller %s for not winning.", g.ID, penaltyValue, g.CambiaCallerID)
		} else {
			log.Printf("Warning: Cambia caller %s not found in final scores. Cannot apply penalty.", g.CambiaCallerID)
		}
	}

	// Apply win bonus if circuit enabled and there are winners
	if g.Circuit.Enabled && g.Circuit.Rules.WinBonus != 0 && len(winners) > 0 {
		winBonus := g.Circuit.Rules.WinBonus // Usually negative, e.g., -1
		for _, winnerID := range winners {
			if _, ok := adjustedScores[winnerID]; ok {
				adjustedScores[winnerID] += winBonus
				log.Printf("Game %s: Applying %d win bonus to winner %s.", g.ID, winBonus, winnerID)
			}
		}
	}
	// --- End Circuit Bonuses/Penalties ---

	// Log final state and results using adjusted scores
	g.logAction(uuid.Nil, string(EventGameEnd), map[string]interface{}{
		"scores":         adjustedScores, // Log adjusted scores
		"winners":        winners,
		"caller":         g.CambiaCallerID,
		"penaltyApplied": penaltyApplies,
		"winBonus":       g.Circuit.Rules.WinBonus, // Log potential bonus value
	})
	g.persistFinalGameState(adjustedScores, winners) // Persist final hands etc., using adjusted scores

	// Determine the primary winner (first in list, or Nil if no winner)
	var firstWinner uuid.UUID
	if len(winners) > 0 {
		firstWinner = winners[0]
	}

	// Broadcast game end event with adjusted results
	resultsPayload := map[string]interface{}{
		"scores":          map[string]int{},
		"winner":          firstWinner.String(), // Can be Nil UUID string "0000..."
		"caller":          g.CambiaCallerID.String(),
		"penaltyApplied":  penaltyApplies,
		"winBonusApplied": g.Circuit.Enabled && g.Circuit.Rules.WinBonus != 0 && len(winners) > 0, // Indicate if bonus was applied
		// Optionally include all winners if needed by client
		// "winners": winnerIDsToStrings(winners),
	}
	for pid, score := range adjustedScores { // Use adjusted scores for broadcast
		resultsPayload["scores"].(map[string]int)[pid.String()] = score
	}
	g.fireEvent(GameEvent{
		Type:    EventGameEnd,
		Payload: resultsPayload,
	})

	// Call the OnGameEnd callback if provided (e.g., to update lobby state)
	if g.OnGameEnd != nil {
		// Pass the adjusted scores
		g.OnGameEnd(g.LobbyID, firstWinner, adjustedScores)
	}

	// Optional: Persist game results and ratings to DB
	// g.persistResults(adjustedScores, winners) // Uncomment if DB persistence is desired here

	log.Printf("Game %s: Ended. Winner(s): %v. Final Scores (after bonuses/penalties): %v", g.ID, winners, adjustedScores)
}

// computeScores calculates the sum of card values in each player's hand.
// Assumes lock is held by caller.
func (g *CambiaGame) computeScores() map[uuid.UUID]int {
	scores := make(map[uuid.UUID]int)
	for _, p := range g.Players {
		// Calculate score only if connected or if disconnect doesn't mean forfeit
		if p.Connected || !g.HouseRules.ForfeitOnDisconnect {
			sum := 0
			for _, c := range p.Hand {
				sum += c.Value
			}
			scores[p.ID] = sum
		} else {
			// Optionally assign a very high score or handle forfeit differently
			log.Printf("Game %s: Player %s score not computed due to disconnect/forfeit.", g.ID, p.ID)
			// scores[p.ID] = 999 // Example: Assign high score
		}
	}
	return scores
}

// findWinnersWithCambiaLogic determines winners based on lowest score, applying Cambia rules.
// Returns: list of winner UUIDs, boolean indicating if caller penalty applies.
// Assumes lock is held by caller.
func (g *CambiaGame) findWinnersWithCambiaLogic(scores map[uuid.UUID]int) ([]uuid.UUID, bool) {
	if len(scores) == 0 {
		return []uuid.UUID{}, false // No players with scores, no winners
	}

	// Find the lowest score among players considered
	lowestScore := -1
	first := true
	consideredPlayerIDs := []uuid.UUID{} // Track IDs considered

	for playerID := range scores { // Iterate over players who have a score
		consideredPlayerIDs = append(consideredPlayerIDs, playerID)
		score := scores[playerID]
		if first || score < lowestScore {
			lowestScore = score
			first = false
		}
	}

	// If no players were considered (shouldn't happen if scores map is not empty)
	if len(consideredPlayerIDs) == 0 {
		return []uuid.UUID{}, false
	}

	// Find all players with the lowest score
	potentialWinners := []uuid.UUID{}
	for _, playerID := range consideredPlayerIDs {
		if scores[playerID] == lowestScore {
			potentialWinners = append(potentialWinners, playerID)
		}
	}

	// Apply Cambia caller logic
	callerIsPotentialWinner := false
	if g.CambiaCalled && g.CambiaCallerID != uuid.Nil {
		for _, winnerID := range potentialWinners {
			if winnerID == g.CambiaCallerID {
				callerIsPotentialWinner = true
				break
			}
		}
	}

	if g.CambiaCalled && g.CambiaCallerID != uuid.Nil {
		if callerIsPotentialWinner {
			// Caller wins (even if tied)
			log.Printf("Game %s: Cambia caller %s won or tied for lowest score (%d).", g.ID, g.CambiaCallerID, lowestScore)
			return []uuid.UUID{g.CambiaCallerID}, false // No penalty
		} else {
			// Caller did not win (or tie for lowest).
			// Penalty applies to the caller.
			log.Printf("Game %s: Cambia caller %s did not win (Lowest score: %d). Penalty applies.", g.ID, g.CambiaCallerID, lowestScore)
			nonCallerWinners := []uuid.UUID{}
			for _, winnerID := range potentialWinners {
				// The list already only contains potential winners (lowest score)
				nonCallerWinners = append(nonCallerWinners, winnerID)
			}

			// Spec: "If the caller does not win the round, and there exists a tie among remaining players, there is no victory granted."
			if len(nonCallerWinners) == 1 {
				// Single non-caller winner
				log.Printf("Game %s: Single winner (%s) found.", g.ID, nonCallerWinners[0])
				return nonCallerWinners, true // Single winner, caller penalty applies
			} else {
				// Tie among non-callers (or no non-caller potential winners), no victory granted
				log.Printf("Game %s: Tie among %d non-caller winners. No victory granted.", g.ID, len(nonCallerWinners))
				return []uuid.UUID{}, true // No winner, caller penalty applies
			}
		}
	} else {
		// Cambia not called, normal win condition (lowest score wins, ties possible)
		log.Printf("Game %s: Cambia not called. Lowest score: %d. Winners: %v", g.ID, lowestScore, potentialWinners)
		return potentialWinners, false // No caller penalty
	}
}

// persistFinalGameState saves the final hands and winners to the database.
// Assumes lock is held by caller.
func (g *CambiaGame) persistFinalGameState(finalScores map[uuid.UUID]int, winners []uuid.UUID) {
	type finalHand struct {
		Rank  string `json:"rank"`
		Suit  string `json:"suit"`
		Value int    `json:"value"`
	}
	type finalPlayerState struct {
		Hand  []finalHand `json:"hand"`
		Score int         `json:"score"`
	}
	snapshot := map[string]interface{}{
		"players": map[string]finalPlayerState{},
		"winners": winners, // Store winner IDs
	}

	playerStates := snapshot["players"].(map[string]finalPlayerState)
	for _, p := range g.Players { // Iterate over original player list
		score, scoreOk := finalScores[p.ID]
		if !scoreOk {
			// Handle players who might not have a score (e.g., disconnected/forfeited)
			// Depending on rules, they might still have a hand state to record
			// score = 999 // Assign high score if not found? Or omit? Omit for now.
		}
		state := finalPlayerState{
			Hand:  make([]finalHand, len(p.Hand)),
			Score: score, // Store the calculated score
		}
		for i, c := range p.Hand {
			state.Hand[i] = finalHand{Rank: c.Rank, Suit: c.Suit, Value: c.Value}
		}
		playerStates[p.ID.String()] = state
	}

	go database.StoreFinalGameStateInDB(context.Background(), g.ID, snapshot)
}

// removeCardFromPlayerHand removes a card from a player's hand by ID.
// Assumes lock is held by caller.
func (g *CambiaGame) removeCardFromPlayerHand(playerID, cardID uuid.UUID) (bool, int) { // Return bool success, int index
	player := g.getPlayerByID(playerID)
	if player == nil {
		return false, -1
	}
	newHand := []*models.Card{}
	found := false
	removedIndex := -1
	for i, c := range player.Hand {
		if c.ID != cardID {
			newHand = append(newHand, c)
		} else {
			found = true
			removedIndex = i
		}
	}
	if found {
		player.Hand = newHand
	}
	return found, removedIndex
}

// getPlayerByID is a helper to find a player struct by their ID.
// Assumes lock is held by caller.
func (g *CambiaGame) getPlayerByID(playerID uuid.UUID) *models.Player {
	for _, p := range g.Players {
		if p.ID == playerID {
			return p
		}
	}
	return nil
}

// logAction sends the action details to the historian service via Redis.
// Assumes lock is held by caller.
func (g *CambiaGame) logAction(actorID uuid.UUID, actionType string, payload map[string]interface{}) {
	g.actionIndex++ // Increment action index for ordering
	// Ensure payload is not nil for marshaling
	if payload == nil {
		payload = make(map[string]interface{})
	}
	record := cache.GameActionRecord{
		GameID:        g.ID,
		ActionIndex:   g.actionIndex,
		ActorUserID:   actorID,
		ActionType:    actionType,
		ActionPayload: payload,
		Timestamp:     time.Now().UnixMilli(),
	}
	// Asynchronously publish to Redis
	go func(rec cache.GameActionRecord) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second) // Short timeout for Redis push
		defer cancel()
		if cache.Rdb == nil {
			// This can happen if Redis isn't connected yet at startup
			// log.Printf("Warning: Redis client (Rdb) is nil. Cannot log action %d for game %s.", rec.ActionIndex, g.ID)
			return
		}
		if err := cache.PublishGameAction(ctx, rec); err != nil {
			log.Printf("Error publishing game action %d to Redis for game %s: %v", rec.ActionIndex, g.ID, err)
		}
	}(record)
}

// ResetTurnTimer is exported for external usage (e.g., special actions).
// Assumes lock is held by caller.
func (g *CambiaGame) ResetTurnTimer() {
	g.scheduleNextTurnTimer() // Use the internal scheduler
}

// FireEventPrivateSpecialActionFail sends a private fail event. Lock should be held by caller.
func (g *CambiaGame) FireEventPrivateSpecialActionFail(userID uuid.UUID, reason string, special string, card1, card2 *EventCard) {
	ev := GameEvent{
		Type:    EventPrivateSpecialFail,
		Special: special,
		Payload: map[string]interface{}{"message": reason},
		Card1:   card1, // Include card info in failure message as per spec
		Card2:   card2,
	}
	g.fireEventToPlayer(userID, ev)
	g.logAction(userID, string(EventPrivateSpecialFail), map[string]interface{}{"reason": reason, "special": special})
}

// FailSpecialAction handles failing a special action. Lock should be held by caller.
func (g *CambiaGame) FailSpecialAction(userID uuid.UUID, reason string) {
	if !g.SpecialAction.Active || g.SpecialAction.PlayerID != userID {
		log.Printf("Warning: FailSpecialAction called for player %s but no action active or mismatch.", userID)
		// Send fail event even if state seems inconsistent, as player might expect a response
		g.FireEventPrivateSpecialActionFail(userID, reason, g.SpecialAction.CardRank, nil, nil)
		return
	}
	specialType := rankToSpecial(g.SpecialAction.CardRank) // Get type before clearing
	log.Printf("Game %s: Failing special action %s for player %s. Reason: %s", g.ID, specialType, userID, reason)

	// Fire the fail event *before* clearing state
	// Pass nil for cards as this is a generic failure not tied to specific selections usually
	g.FireEventPrivateSpecialActionFail(userID, reason, specialType, nil, nil)

	g.SpecialAction = SpecialActionState{} // Clear state
	g.advanceTurn()                        // Advance turn after failure
}

// FireEventPrivateSuccess sends a private success event revealing card details. Lock should be held by caller.
func (g *CambiaGame) FireEventPrivateSuccess(userID uuid.UUID, special string, c1Ev, c2Ev *EventCard) {
	ev := GameEvent{
		Type:    EventPrivateSpecialSuccess, // Correct event type
		Special: special,
		Card1:   c1Ev,
		Card2:   c2Ev,
	}
	g.fireEventToPlayer(userID, ev)
	// Logging handled within specific do* functions
}

// FireEventPlayerSpecialAction broadcasts public info about a special action. Lock should be held by caller.
// Requires card details (ID, maybe index/user) for the event payload.
func (g *CambiaGame) FireEventPlayerSpecialAction(userID uuid.UUID, special string, c1Ev, c2Ev *EventCard) {
	ev := GameEvent{
		Type:    EventPlayerSpecialAction,
		User:    &EventUser{ID: userID},
		Special: special,
		Card1:   c1Ev, // Pass the prepared EventCard structs
		Card2:   c2Ev,
	}
	g.fireEvent(ev)
	// Logging handled within specific do* functions
}
