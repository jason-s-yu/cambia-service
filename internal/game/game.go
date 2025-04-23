// internal/game/game.go
package game

import (
	"context"
	"log"
	"math/rand"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/cache"
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/models"

	"github.com/coder/websocket"
)

// OnGameEndFunc defines the signature for a callback function executed when a game ends.
// It receives the lobby ID, the primary winner's ID (can be Nil), and the final scores.
type OnGameEndFunc func(lobbyID uuid.UUID, winner uuid.UUID, scores map[uuid.UUID]int)

// GameEventType represents the type of a game-related event broadcast via WebSockets.
type GameEventType string

// Constants defining the various GameEvent types used for WebSocket communication.
const (
	EventPlayerSnapSuccess      GameEventType = "player_snap_success"
	EventPlayerSnapFail         GameEventType = "player_snap_fail"
	EventPlayerSnapPenalty      GameEventType = "player_snap_penalty"            // Public: Player drew penalty cards.
	EventPrivateSnapPenalty     GameEventType = "private_snap_penalty"           // Private: Details of penalty cards drawn.
	EventGameReshuffleStockpile GameEventType = "game_reshuffle_stockpile"       // Public: Discard pile was reshuffled into stockpile.
	EventPlayerDrawStockpile    GameEventType = "player_draw_stockpile"          // Public: Player drew a card (ID only).
	EventPrivateDrawStockpile   GameEventType = "private_draw_stockpile"         // Private: Details of the card drawn.
	EventPlayerDiscard          GameEventType = "player_discard"                 // Public: Player discarded a card (details revealed).
	EventPlayerReplace          GameEventType = "player_replace"                 // DEPRECATED? Sends EventPlayerDiscard instead.
	EventPlayerSpecialChoice    GameEventType = "player_special_choice"          // Public: Player can now use a special ability.
	EventPlayerSpecialAction    GameEventType = "player_special_action"          // Public: Player used a special ability (obfuscated details).
	EventPrivateSpecialSuccess  GameEventType = "private_special_action_success" // Private: Details of successful special action.
	EventPrivateSpecialFail     GameEventType = "private_special_action_fail"    // Private: Special action attempt failed.
	EventPlayerCambia           GameEventType = "player_cambia"                  // Public: Player called Cambia.
	EventGamePlayerTurn         GameEventType = "game_player_turn"               // Public: Notification of the current player's turn.
	EventPrivateSyncState       GameEventType = "private_sync_state"             // Private: Full game state sync for a player.
	EventPrivateInitialCards    GameEventType = "private_initial_cards"          // Private: Initial two cards revealed during pre-game.
	EventGameEnd                GameEventType = "game_end"                       // Public: Game has ended, includes results.
)

// EventUser identifies a user within a GameEvent payload.
type EventUser struct {
	ID uuid.UUID `json:"id"`
}

// EventCard identifies a card within a GameEvent payload, optionally including details.
type EventCard struct {
	ID    uuid.UUID  `json:"id"`
	Rank  string     `json:"rank,omitempty"`
	Suit  string     `json:"suit,omitempty"`
	Value int        `json:"value,omitempty"`
	Idx   *int       `json:"idx,omitempty"`  // Index in hand, if relevant.
	User  *EventUser `json:"user,omitempty"` // Owner of the card, if relevant (e.g., for swaps).
}

// GameEvent is the standard structure for broadcasting game state changes and actions.
type GameEvent struct {
	Type    GameEventType `json:"type"`
	User    *EventUser    `json:"user,omitempty"`    // The user initiating or targeted by the event.
	Card    *EventCard    `json:"card,omitempty"`    // Primary card involved.
	Card1   *EventCard    `json:"card1,omitempty"`   // First card in a two-card action (e.g., swap).
	Card2   *EventCard    `json:"card2,omitempty"`   // Second card in a two-card action.
	Special string        `json:"special,omitempty"` // Identifier for the specific special action (e.g., "peek_self").

	Payload map[string]interface{} `json:"payload,omitempty"` // Additional arbitrary data.

	State *ObfGameState `json:"state,omitempty"` // Full obfuscated state for sync events.
}

// SpecialActionState holds temporary information about a pending multi-step special action (e.g., King).
type SpecialActionState struct {
	Active        bool         // Is a special action currently pending?
	PlayerID      uuid.UUID    // Which player must act?
	CardRank      string       // Rank of the card that triggered the action ("K", "Q", etc.).
	FirstStepDone bool         // For King: Has the initial peek step completed?
	Card1         *models.Card // For King: First peeked card.
	Card1Owner    uuid.UUID    // For King: Owner of the first peeked card.
	Card2         *models.Card // For King: Second peeked card.
	Card2Owner    uuid.UUID    // For King: Owner of the second peeked card.
}

// CircuitRules defines parameters for tournament-style play across multiple rounds.
type CircuitRules struct {
	TargetScore            int  `json:"targetScore"`            // Score limit to trigger elimination or end.
	WinBonus               int  `json:"winBonus"`               // Bonus (usually negative) applied to winner's score.
	FalseCambiaPenalty     int  `json:"falseCambiaPenalty"`     // Penalty added if Cambia caller doesn't win.
	FreezeUserOnDisconnect bool `json:"freezeUserOnDisconnect"` // Prevent disconnected users from being auto-kicked.
}

// Circuit wraps the overall circuit settings.
type Circuit struct {
	Enabled bool         `json:"enabled"` // Is circuit mode active?
	Mode    string       `json:"mode"`    // Identifier for the circuit mode (e.g., "circuit_4p").
	Rules   CircuitRules `json:"rules"`   // Specific rules for this circuit.
}

// CambiaGame represents the state and logic for a single instance of the Cambia game.
type CambiaGame struct {
	ID      uuid.UUID // Unique identifier for this game instance.
	LobbyID uuid.UUID // ID of the lobby that created this game.

	HouseRules HouseRules // Configurable game rules.
	Circuit    Circuit    // Circuit mode settings.

	Players     []*models.Player // List of players in the game.
	Deck        []*models.Card   // The stockpile deck.
	DiscardPile []*models.Card   // The discard pile.

	// Turn Management
	CurrentPlayerIndex int           // Index of the current player in the Players slice.
	TurnID             int           // Increments each turn, useful for state synchronization and checks.
	TurnDuration       time.Duration // Configurable duration for each turn timer.
	turnTimer          *time.Timer   // Active timer for the current turn.
	actionIndex        int           // Sequential index for logging actions via historian.

	// Game Lifecycle State
	Started       bool // Has the game started (after pre-game)?
	GameOver      bool // Has the game finished?
	PreGameActive bool // Is the initial pre-game card reveal phase active?

	lastSeen map[uuid.UUID]time.Time // Tracks last activity time for players (potential future use).
	Mu       sync.Mutex              // Mutex protecting concurrent access to game state.

	// Communication Callbacks
	BroadcastFn         func(ev GameEvent)                     // Sends an event to all connected players.
	BroadcastToPlayerFn func(playerID uuid.UUID, ev GameEvent) // Sends an event to a single player.
	OnGameEnd           OnGameEndFunc                          // Callback executed when the game finishes.

	// Special Action State
	SpecialAction SpecialActionState // Holds state for pending multi-step special actions.

	// Cambia State
	CambiaCalled   bool      // Has Cambia been called in this game?
	CambiaCallerID uuid.UUID // ID of the player who called Cambia.

	// Snap State
	snapUsedForThisDiscard bool // Tracks if a snap has succeeded for the current discard (used for SnapRace rule).

	// Timers
	preGameTimer *time.Timer // Timer controlling the duration of the pre-game phase.
}

// NewCambiaGame creates a new game instance with a shuffled deck and default settings.
func NewCambiaGame() *CambiaGame {
	id, _ := uuid.NewRandom()
	g := &CambiaGame{
		ID:                     id,
		Deck:                   []*models.Card{},
		DiscardPile:            []*models.Card{},
		lastSeen:               make(map[uuid.UUID]time.Time),
		CurrentPlayerIndex:     0,
		TurnDuration:           15 * time.Second, // Default turn duration.
		snapUsedForThisDiscard: false,
		actionIndex:            0,
		TurnID:                 0,
		// Initialize HouseRules with standard defaults.
		HouseRules: HouseRules{
			AllowDrawFromDiscardPile: false,
			AllowReplaceAbilities:    false,
			SnapRace:                 false,
			ForfeitOnDisconnect:      true,
			PenaltyDrawCount:         2,
			AutoKickTurnCount:        3,
			TurnTimerSec:             15,
		},
		Circuit: Circuit{Enabled: false}, // Circuit mode disabled by default.
	}
	g.initializeDeck()
	return g
}

// BeginPreGame starts the initial phase where players see their first two cards.
// Deals cards and schedules the transition to the main game start.
func (g *CambiaGame) BeginPreGame() {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	if g.Started || g.GameOver || g.PreGameActive {
		log.Printf("Game %s: BeginPreGame called in invalid state (Started:%v, Over:%v, PreGame:%v).", g.ID, g.Started, g.GameOver, g.PreGameActive)
		return
	}
	g.PreGameActive = true
	g.logAction(uuid.Nil, "game_pregame_start", nil)

	// Apply turn duration from house rules.
	if g.HouseRules.TurnTimerSec > 0 {
		g.TurnDuration = time.Duration(g.HouseRules.TurnTimerSec) * time.Second
	} else {
		g.TurnDuration = 0 // Disable timer if set to 0.
	}

	// Deal 4 cards to each player.
	for _, p := range g.Players {
		p.Hand = make([]*models.Card, 0, 4)
		for i := 0; i < 4; i++ {
			card := g.internalDrawStockpile() // Draw internally without broadcasts.
			if card == nil {
				log.Printf("Warning: Game %s ran out of cards during initial deal for player %s.", g.ID, p.ID)
				break // Stop dealing to this player if deck empty.
			}
			p.Hand = append(p.Hand, card)
		}
	}

	// Persist initial state for potential replay/audit.
	g.persistInitialGameState()

	// Privately reveal the two closest cards (indices 0, 1) to each player.
	for _, p := range g.Players {
		if len(p.Hand) >= 2 {
			c0, idx0 := p.Hand[0], 0
			c1, idx1 := p.Hand[1], 1
			g.firePrivateInitialCards(p.ID,
				buildEventCard(c0, &idx0, p.ID, true),
				buildEventCard(c1, &idx1, p.ID, true),
			)
		} else if len(p.Hand) == 1 { // Handle case where player only got 1 card.
			c0, idx0 := p.Hand[0], 0
			g.firePrivateInitialCards(p.ID, buildEventCard(c0, &idx0, p.ID, true), nil)
		} else {
			log.Printf("Warning: Player %s has 0 cards during pregame reveal in game %s.", p.ID, g.ID)
			g.firePrivateInitialCards(p.ID, nil, nil) // Send event indicating no cards shown.
		}
	}

	// Schedule the transition to the main game phase.
	preGameDuration := 10 * time.Second // Standard pre-game duration.
	g.preGameTimer = time.AfterFunc(preGameDuration, func() {
		g.StartGame() // Call StartGame after the timer.
	})
	log.Printf("Game %s: Pre-game phase started. Will transition in %s.", g.ID, preGameDuration)
}

// StartGame transitions the game from the pre-game phase to active play.
// It marks the game as started and initiates the first turn.
func (g *CambiaGame) StartGame() {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	// Ensure StartGame is called in the correct state.
	if g.GameOver || g.Started || !g.PreGameActive {
		log.Printf("Game %s: StartGame called in invalid state (GameOver:%v, Started:%v, PreGameActive:%v). Ignoring.", g.ID, g.GameOver, g.Started, g.PreGameActive)
		return
	}

	// Stop the pre-game timer if it's still running.
	if g.preGameTimer != nil {
		g.preGameTimer.Stop()
		g.preGameTimer = nil
	}

	g.PreGameActive = false
	g.Started = true
	log.Printf("Game %s: Started.", g.ID)
	g.logAction(uuid.Nil, "game_start", nil)

	// Start the turn cycle.
	g.scheduleNextTurnTimer()
	g.broadcastPlayerTurn()
}

// Start is deprecated. Use BeginPreGame instead to initiate the game flow.
// Deprecated: Use BeginPreGame() which handles the pre-game reveal and timer.
func (g *CambiaGame) Start() {
	g.BeginPreGame()
}

// firePrivateInitialCards sends the initial card reveal event to a specific player.
// Assumes lock is held by caller.
func (g *CambiaGame) firePrivateInitialCards(playerID uuid.UUID, card1, card2 *EventCard) {
	if g.BroadcastToPlayerFn == nil {
		log.Println("Warning: BroadcastToPlayerFn is nil, cannot send private initial cards.")
		return
	}
	ev := GameEvent{
		Type:  EventPrivateInitialCards,
		Card1: card1, // EventCard struct already contains details.
		Card2: card2,
	}
	g.BroadcastToPlayerFn(playerID, ev)
}

// persistInitialGameState saves the initial deck order and player hands to the database.
// Assumes lock is held by caller.
func (g *CambiaGame) persistInitialGameState() {
	// Structure for saving initial state.
	type initialState struct {
		Deck    []*models.Card            `json:"deck"`
		Players map[string][]*models.Card `json:"players"` // Use string UUID as JSON key.
	}

	// Create snapshot safely.
	snap := initialState{
		Deck:    make([]*models.Card, len(g.Deck)),
		Players: make(map[string][]*models.Card),
	}
	copy(snap.Deck, g.Deck)

	for _, p := range g.Players {
		handCopy := make([]*models.Card, len(p.Hand))
		copy(handCopy, p.Hand)
		snap.Players[p.ID.String()] = handCopy
	}

	// Persist asynchronously to avoid blocking game thread.
	go database.UpsertInitialGameState(g.ID, snap)
	g.logAction(uuid.Nil, "game_initial_state_saved", map[string]interface{}{"deckSize": len(snap.Deck)})
}

// AddPlayer adds a player to the game if not started, or marks them as reconnected.
// Assumes lock is held by caller.
func (g *CambiaGame) AddPlayer(p *models.Player) {
	found := false
	for i, pl := range g.Players {
		if pl.ID == p.ID {
			// Player reconnecting.
			g.Players[i].Conn = p.Conn
			g.Players[i].Connected = true
			g.Players[i].User = p.User // Update user info.
			g.lastSeen[p.ID] = time.Now()
			log.Printf("Game %s: Player %s (%s) reconnected.", g.ID, p.ID, p.User.Username)
			found = true
			// Send sync state on reconnect (handled by HandleReconnect).
			break
		}
	}
	if !found {
		// New player joining (only possible before game starts).
		if !g.Started && !g.PreGameActive {
			g.Players = append(g.Players, p)
			g.lastSeen[p.ID] = time.Now()
			log.Printf("Game %s: Player %s (%s) added.", g.ID, p.ID, p.User.Username)
		} else {
			log.Printf("Game %s: Player %s (%s) cannot be added because game has already started.", g.ID, p.ID, p.User.Username)
			// Optionally close connection or send error.
			if p.Conn != nil {
				p.Conn.Close(websocket.StatusPolicyViolation, "Game already in progress.")
			}
			return
		}
	}
	g.logAction(p.ID, "player_add", map[string]interface{}{"reconnect": found, "username": p.User.Username})
}

// initializeDeck creates and shuffles a standard 52-card deck plus two jokers.
// Assigns Cambia-specific values (e.g., Red Kings = -1, Jokers = 0).
// Assumes lock is held by caller.
func (g *CambiaGame) initializeDeck() {
	suits := []string{"H", "D", "C", "S"} // Hearts, Diamonds, Clubs, Spades.
	ranks := []string{"A", "2", "3", "4", "5", "6", "7", "8", "9", "T", "J", "Q", "K"}
	values := map[string]int{
		"A": 1, "2": 2, "3": 3, "4": 4, "5": 5, "6": 6, "7": 7, "8": 8,
		"9": 9, "T": 10, "J": 11, "Q": 12, "K": 13, // Default King value.
	}

	var deck []*models.Card
	// Add standard cards.
	for _, suit := range suits {
		for _, rank := range ranks {
			val := values[rank]
			// Red Kings (Hearts, Diamonds) have value -1.
			if rank == "K" && (suit == "H" || suit == "D") {
				val = -1
			}
			cid, _ := uuid.NewRandom()
			deck = append(deck, &models.Card{ID: cid, Suit: suit, Rank: rank, Value: val})
		}
	}
	// Add Jokers (value 0).
	jokerSuits := []string{"R", "B"} // Red Joker, Black Joker.
	for _, suit := range jokerSuits {
		cid, _ := uuid.NewRandom()
		// Use rank "O" for Joker to avoid confusion with Jack ("J").
		deck = append(deck, &models.Card{ID: cid, Suit: suit, Rank: "O", Value: 0})
	}

	// Shuffle the deck using a time-seeded random source.
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	r.Shuffle(len(deck), func(i, j int) { deck[i], deck[j] = deck[j], deck[i] })

	g.Deck = deck
	log.Printf("Game %s: Initialized and shuffled deck with %d cards.", g.ID, len(g.Deck))
}

// internalDrawStockpile draws a card from the stockpile, handling reshuffles.
// Returns the drawn card or nil if no cards are available.
// Does NOT broadcast events.
// Assumes lock is held by caller.
func (g *CambiaGame) internalDrawStockpile() *models.Card {
	if len(g.Deck) == 0 {
		if len(g.DiscardPile) <= 1 { // Need at least 2 cards in discard to reshuffle (leave top).
			log.Printf("Game %s: Stockpile empty and discard pile has %d card(s). Cannot draw.", g.ID, len(g.DiscardPile))
			if !g.GameOver { // End game only if not already over.
				g.EndGame()
			}
			return nil
		}
		// Reshuffle discard pile (excluding top card) into stockpile.
		log.Printf("Game %s: Stockpile empty. Reshuffling %d card(s) from discard pile.", g.ID, len(g.DiscardPile)-1)

		topDiscard := g.DiscardPile[len(g.DiscardPile)-1]
		cardsToShuffle := g.DiscardPile[:len(g.DiscardPile)-1]

		g.Deck = append(g.Deck, cardsToShuffle...)
		g.DiscardPile = []*models.Card{topDiscard} // Keep only the top card.

		// Shuffle the newly added cards.
		r := rand.New(rand.NewSource(time.Now().UnixNano()))
		r.Shuffle(len(g.Deck), func(i, j int) { g.Deck[i], g.Deck[j] = g.Deck[j], g.Deck[i] })
		log.Printf("Game %s: Reshuffled discard pile into stockpile. New size: %d", g.ID, len(g.Deck))

		// Broadcast reshuffle event.
		g.fireEvent(GameEvent{
			Type:    EventGameReshuffleStockpile,
			Payload: map[string]interface{}{"stockpileSize": len(g.Deck)},
		})
		g.logAction(uuid.Nil, string(EventGameReshuffleStockpile), map[string]interface{}{"newSize": len(g.Deck)})
	}

	// After potential reshuffle, check again if deck is empty.
	if len(g.Deck) == 0 {
		log.Printf("Game %s: Stockpile is still empty after attempting reshuffle. Cannot draw.", g.ID)
		if !g.GameOver {
			g.EndGame()
		}
		return nil
	}

	// Draw the top card.
	card := g.Deck[0]
	g.Deck = g.Deck[1:] // Remove card from deck.
	return card
}

// drawTopStockpile draws from stockpile, broadcasts events, and returns the card.
// Assumes lock is held by caller.
func (g *CambiaGame) drawTopStockpile(playerID uuid.UUID) *models.Card {
	card := g.internalDrawStockpile()
	if card == nil {
		return nil // internalDrawStockpile handles logging/game end.
	}

	// Public draw event (obfuscated ID).
	g.fireEvent(GameEvent{
		Type: EventPlayerDrawStockpile,
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: card.ID},
		Payload: map[string]interface{}{
			"stockpileSize": len(g.Deck),
			"source":        "stockpile", // Indicate source.
		},
	})

	// Private draw event (full card details).
	g.fireEventToPlayer(playerID, GameEvent{
		Type: EventPrivateDrawStockpile,
		Card: &EventCard{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
		Payload: map[string]interface{}{
			"source": "stockpile",
		},
	})

	g.logAction(playerID, string(EventPlayerDrawStockpile), map[string]interface{}{"cardId": card.ID, "newSize": len(g.Deck)})
	return card
}

// drawTopDiscard draws from discard pile if allowed, broadcasts events, and returns the card.
// Assumes lock is held by caller.
func (g *CambiaGame) drawTopDiscard(playerID uuid.UUID) *models.Card {
	if !g.HouseRules.AllowDrawFromDiscardPile {
		log.Printf("Game %s: Player %s tried to draw from discard, but rule disallows.", g.ID, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Drawing from discard pile is not allowed."}})
		return nil
	}
	if len(g.DiscardPile) == 0 {
		log.Printf("Game %s: Player %s tried to draw from empty discard pile.", g.ID, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Discard pile is empty."}})
		return nil
	}

	idx := len(g.DiscardPile) - 1
	card := g.DiscardPile[idx]
	g.DiscardPile = g.DiscardPile[:idx] // Remove card from discard.

	// Public draw event (reveals details as it came from discard).
	g.fireEvent(GameEvent{
		Type: EventPlayerDrawStockpile, // Reuse public event type.
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
		Payload: map[string]interface{}{
			"source":      "discardpile",
			"discardSize": len(g.DiscardPile),
		},
	})

	// Private draw event (also full details).
	g.fireEventToPlayer(playerID, GameEvent{
		Type: EventPrivateDrawStockpile, // Reuse private event type.
		Card: &EventCard{ID: card.ID, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
		Payload: map[string]interface{}{
			"source": "discardpile",
		},
	})
	g.logAction(playerID, "action_draw_discardpile", map[string]interface{}{"cardId": card.ID, "newSize": len(g.DiscardPile)})
	return card
}

// scheduleNextTurnTimer sets or resets the timer for the current player's turn.
// Handles disabled timers, disconnected players, and stale timer callbacks.
// Assumes lock is held by caller.
func (g *CambiaGame) scheduleNextTurnTimer() {
	// Stop any existing timer.
	if g.turnTimer != nil {
		g.turnTimer.Stop()
		g.turnTimer = nil
	}
	// Skip if timer is disabled by rules or game is over/not started.
	if g.TurnDuration <= 0 || g.GameOver || !g.Started {
		return
	}
	// Validate player state.
	if len(g.Players) == 0 || g.CurrentPlayerIndex < 0 || g.CurrentPlayerIndex >= len(g.Players) {
		log.Printf("Game %s: Invalid player state (Players: %d, Index: %d). Cannot schedule turn timer.", g.ID, len(g.Players), g.CurrentPlayerIndex)
		if !g.GameOver {
			g.EndGame() // End game if state is unrecoverable.
		}
		return
	}

	currentPlayer := g.Players[g.CurrentPlayerIndex]
	// Skip timer for disconnected players; advance turn immediately instead.
	if !currentPlayer.Connected {
		log.Printf("Game %s: Current player %s is disconnected. Advancing turn instead of starting timer.", g.ID, currentPlayer.ID)
		g.advanceTurn() // Automatically skip disconnected player's turn.
		return
	}

	curPID := currentPlayer.ID
	currentTurnID := g.TurnID // Capture current turn ID for validation in callback.

	// Schedule the timer callback.
	g.turnTimer = time.AfterFunc(g.TurnDuration, func() {
		// Execute timeout logic in a separate goroutine to avoid deadlocks.
		go func(playerID uuid.UUID, gameID uuid.UUID, expectedTurnID int) {
			g.Mu.Lock() // Re-acquire lock within the goroutine.
			defer g.Mu.Unlock()

			// Validate if the timer is still relevant.
			isValidTimer := !g.GameOver && g.Started &&
				len(g.Players) > g.CurrentPlayerIndex &&
				g.Players[g.CurrentPlayerIndex].ID == playerID && // Still this player's turn?
				g.TurnID == expectedTurnID // Still the same turn number?

			if isValidTimer {
				log.Printf("Game %s, Turn %d: Timer fired for player %s.", g.ID, g.TurnID, playerID)
				g.handleTimeout(playerID) // Handle the timeout logic.
			} else {
				log.Printf("Game %s: Stale timer fired for player %s (Turn: %d, Expected: %d, CurrentPlayer: %s). Ignoring.", g.ID, playerID, g.TurnID, expectedTurnID, g.Players[g.CurrentPlayerIndex].ID)
			}
		}(curPID, g.ID, currentTurnID) // Pass necessary identifiers for validation.
	})
	// log.Printf("Game %s: Scheduled %s turn timer for player %s (Turn %d).", g.ID, g.TurnDuration, curPID, currentTurnID)
}

// handleTimeout processes the timeout logic for a player.
// Discards drawn card, skips special actions, or draws/discards if necessary.
// Assumes lock is held by caller.
func (g *CambiaGame) handleTimeout(playerID uuid.UUID) {
	log.Printf("Game %s: Player %s timed out on turn %d.", g.ID, playerID, g.TurnID)
	g.logAction(playerID, "player_timeout", map[string]interface{}{"turn": g.TurnID})

	player := g.getPlayerByID(playerID)
	if player == nil {
		log.Printf("Game %s: Timed out player %s not found. Advancing turn.", g.ID, playerID)
		g.advanceTurn()
		return
	}

	// If a special action is pending, skip it.
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerID {
		specialRank := g.SpecialAction.CardRank
		log.Printf("Game %s: Timeout skipping special action for rank %s for player %s", g.ID, specialRank, playerID)
		g.processSkipSpecialAction(playerID) // This advances the turn internally.
		return
	}

	// If player had already drawn a card, discard it.
	cardToDiscard := player.DrawnCard
	if cardToDiscard != nil {
		player.DrawnCard = nil // Clear the held card.
		log.Printf("Game %s: Player %s timed out with drawn card %s. Discarding.", g.ID, playerID, cardToDiscard.ID)
	} else {
		// Player timed out without drawing; draw and discard immediately.
		log.Printf("Game %s: Player %s timed out without drawing. Drawing and discarding.", g.ID, playerID)
		cardToDiscard = g.drawTopStockpile(playerID)
		if cardToDiscard == nil {
			log.Printf("Game %s: Player %s timed out, but no cards left to draw/discard. Advancing turn.", g.ID, playerID)
			g.advanceTurn() // Game might end here via draw logic.
			return
		}
	}

	// Discard the relevant card.
	g.DiscardPile = append(g.DiscardPile, cardToDiscard)
	g.logAction(playerID, "player_timeout_discard", map[string]interface{}{"cardId": cardToDiscard.ID})
	g.fireEvent(GameEvent{
		Type: EventPlayerDiscard,
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: cardToDiscard.ID, Rank: cardToDiscard.Rank, Suit: cardToDiscard.Suit, Value: cardToDiscard.Value},
	})
	g.snapUsedForThisDiscard = false // Reset snap state.
	// Do NOT trigger special abilities on timeout discard.

	g.advanceTurn() // Advance turn after handling the timeout.
}

// broadcastPlayerTurn notifies all players of the current player's turn.
// Assumes lock is held by caller.
func (g *CambiaGame) broadcastPlayerTurn() {
	// Validate state before broadcasting.
	if g.GameOver || !g.Started || len(g.Players) == 0 || g.CurrentPlayerIndex < 0 || g.CurrentPlayerIndex >= len(g.Players) {
		log.Printf("Game %s: Cannot broadcast player turn in current state (GameOver:%v, Started:%v, Players:%d, Index:%d).", g.ID, g.GameOver, g.Started, len(g.Players), g.CurrentPlayerIndex)
		return
	}
	currentPID := g.Players[g.CurrentPlayerIndex].ID
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

// fireEvent broadcasts an event to all connected players via the BroadcastFn callback.
// Assumes lock is held by caller.
func (g *CambiaGame) fireEvent(ev GameEvent) {
	if g.BroadcastFn != nil {
		g.BroadcastFn(ev) // Execute the callback.
	} else {
		log.Printf("Warning: Game %s: BroadcastFn is nil, cannot broadcast event type %s.", g.ID, ev.Type)
	}
}

// fireEventToPlayer sends an event to a specific player via the BroadcastToPlayerFn callback.
// Checks if the player is connected before sending.
// Assumes lock is held by caller.
func (g *CambiaGame) fireEventToPlayer(playerID uuid.UUID, ev GameEvent) {
	if g.BroadcastToPlayerFn != nil {
		targetPlayer := g.getPlayerByID(playerID)
		if targetPlayer != nil && targetPlayer.Connected {
			g.BroadcastToPlayerFn(playerID, ev) // Execute the callback.
		} else {
			// Log quietly if player not found or disconnected.
			// log.Printf("Debug: Game %s: Target player %s not found or not connected for private event type %s.", g.ID, playerID, ev.Type)
		}
	} else {
		log.Printf("Warning: Game %s: BroadcastToPlayerFn is nil, cannot send private event type %s to player %s.", g.ID, ev.Type, playerID)
	}
}

// advanceTurn moves game control to the next valid (connected) player.
// Handles turn wrapping, skipping disconnected players, and Cambia end-game condition.
// Assumes lock is held by caller.
func (g *CambiaGame) advanceTurn() {
	if g.GameOver {
		return
	}
	if len(g.Players) == 0 {
		log.Printf("Game %s: Cannot advance turn, no players remaining.", g.ID)
		if !g.GameOver {
			g.EndGame()
		}
		return
	}

	prevPlayerIndex := g.CurrentPlayerIndex

	// Check Cambia end condition: If the turn just completed by the player *before* the caller.
	if g.CambiaCalled {
		callerIdx := -1
		for i, p := range g.Players {
			if p.ID == g.CambiaCallerID {
				callerIdx = i
				break
			}
		}
		// Use modulo arithmetic for wrap-around check.
		if callerIdx != -1 && prevPlayerIndex == (callerIdx-1+len(g.Players))%len(g.Players) {
			log.Printf("Game %s: Final turn after Cambia call completed by player %s. Ending game.", g.ID, g.Players[prevPlayerIndex].ID)
			if !g.GameOver {
				g.EndGame()
			}
			return // Stop advancement, game is ending.
		}
	}

	// Increment Turn ID for the *next* turn.
	g.TurnID++

	// Find the next connected player.
	nextIndex := (prevPlayerIndex + 1) % len(g.Players)
	skippedCount := 0
	for !g.Players[nextIndex].Connected {
		log.Printf("Game %s: Skipping disconnected player %s at index %d.", g.ID, g.Players[nextIndex].ID, nextIndex)
		nextIndex = (nextIndex + 1) % len(g.Players)
		skippedCount++
		if skippedCount >= len(g.Players) {
			log.Printf("Game %s: No connected players left. Ending game.", g.ID)
			if !g.GameOver {
				g.EndGame()
			}
			return
		}
	}

	g.CurrentPlayerIndex = nextIndex

	// Reset the new current player's state.
	if g.Players[g.CurrentPlayerIndex] != nil {
		g.Players[g.CurrentPlayerIndex].DrawnCard = nil // Clear any previously drawn card.
	} else {
		log.Printf("Error: Game %s: Current player at index %d is nil during turn advance.", g.ID, g.CurrentPlayerIndex)
		if !g.GameOver {
			g.EndGame()
		}
		return
	}

	// Schedule the timer for the new turn.
	g.scheduleNextTurnTimer() // This uses the incremented TurnID.

	// Broadcast the new turn event.
	g.broadcastPlayerTurn()
}

// HandleDisconnect marks a player as disconnected and handles game state consequences.
// Assumes lock is held by caller.
func (g *CambiaGame) HandleDisconnect(playerID uuid.UUID) {
	log.Printf("Game %s: Handling disconnect for player %s.", g.ID, playerID)
	g.logAction(playerID, "player_disconnect", nil)

	playerIndex := -1
	found := false
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			if !g.Players[i].Connected {
				log.Printf("Game %s: Player %s already marked as disconnected.", g.ID, playerID)
				return // Already handled.
			}
			g.Players[i].Connected = false
			g.Players[i].Conn = nil // Clear WebSocket connection reference.
			found = true
			playerIndex = i
			break
		}
	}
	if !found {
		log.Printf("Game %s: Disconnected player %s not found.", g.ID, playerID)
		return
	}

	shouldAdvanceTurn := false
	shouldEndGame := false

	if g.Started && !g.GameOver {
		// Check if game ends due to forfeit rule.
		if g.HouseRules.ForfeitOnDisconnect {
			log.Printf("Game %s: Player %s disconnected, forfeiting due to house rules.", g.ID, playerID)
			if g.countConnectedPlayers() <= 1 {
				log.Printf("Game %s: Only %d player(s) left connected after forfeit. Ending game.", g.ID, g.countConnectedPlayers())
				shouldEndGame = true
			}
		} else {
			// If no forfeit, check if the current player disconnected.
			if playerIndex == g.CurrentPlayerIndex {
				log.Printf("Game %s: Current player %s disconnected. Advancing turn.", g.ID, playerID)
				shouldAdvanceTurn = true
			}
		}
	}

	// Broadcast updated state to remaining players *before* ending or advancing.
	g.broadcastSyncStateToAll()

	if shouldEndGame {
		if !g.GameOver {
			g.EndGame() // End the game immediately.
		}
	} else if shouldAdvanceTurn {
		g.advanceTurn() // Advance turn if current player left.
	}
}

// HandleReconnect marks a player as connected and sends them the current game state.
// Assumes lock is held by caller.
func (g *CambiaGame) HandleReconnect(playerID uuid.UUID, conn *websocket.Conn) {
	log.Printf("Game %s: Handling reconnect for player %s.", g.ID, playerID)

	found := false
	for i := range g.Players {
		if g.Players[i].ID == playerID {
			if g.Players[i].Connected {
				log.Printf("Game %s: Player %s reconnected but was already marked connected.", g.ID, playerID)
				// Update connection object anyway.
			}
			g.Players[i].Connected = true
			g.Players[i].Conn = conn
			g.Players[i].User = g.Players[i].User // Assume User struct is still valid.
			g.lastSeen[playerID] = time.Now()
			found = true

			g.logAction(playerID, "player_reconnect", map[string]interface{}{"username": g.Players[i].User.Username})

			// Send sync state immediately to the reconnected player.
			g.sendSyncState(playerID)

			// Broadcast updated state to others.
			g.broadcastSyncStateToAll()

			// If it was this player's turn, reschedule timer.
			if g.Started && !g.GameOver && g.CurrentPlayerIndex == i {
				log.Printf("Game %s: Player %s reconnected on their turn. Rescheduling timer.", g.ID, playerID)
				g.scheduleNextTurnTimer()
			}
			break
		}
	}

	if !found {
		log.Printf("Game %s: Reconnecting player %s not found in game.", g.ID, playerID)
		g.logAction(playerID, "player_reconnect_fail", map[string]interface{}{"reason": "player not found"})
		if conn != nil {
			// Close connection if player isn't actually part of this game.
			conn.Close(websocket.StatusPolicyViolation, "Game not found or you were removed.")
		}
	}
}

// sendSyncState sends the current obfuscated game state to a single player.
// Assumes lock is held by caller.
func (g *CambiaGame) sendSyncState(playerID uuid.UUID) {
	if g.BroadcastToPlayerFn == nil {
		log.Println("Warning: BroadcastToPlayerFn is nil, cannot send sync state.")
		return
	}
	// Generate state specifically for this player.
	state := g.GetCurrentObfuscatedGameState(playerID)
	ev := GameEvent{
		Type:  EventPrivateSyncState,
		State: &state, // Embed the state object.
	}
	g.fireEventToPlayer(playerID, ev) // Uses internal check for connection status.
	// log.Printf("Game %s: Sent sync state to player %s.", g.ID, playerID) // Reduce noise.
}

// broadcastSyncStateToAll sends the obfuscated game state to all currently connected players.
// Assumes lock is held by caller.
func (g *CambiaGame) broadcastSyncStateToAll() {
	if g.BroadcastToPlayerFn == nil {
		log.Println("Warning: BroadcastToPlayerFn is nil, cannot broadcast sync state to all.")
		return
	}
	connectedCount := 0
	for _, p := range g.Players {
		if p.Connected {
			g.sendSyncState(p.ID) // Generate and send state for each connected player.
			connectedCount++
		}
	}
	// log.Printf("Game %s: Broadcasted sync state to %d connected players.", g.ID, connectedCount) // Reduce noise.
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

// drawCardFromLocation wraps draw logic for stockpile or discard pile.
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
		return nil // Invalid location.
	}
	return card // Returns card or nil if draw failed.
}

// HandlePlayerAction routes incoming player actions (draw, discard, replace, snap, cambia).
// Validates turn, state, and payload before executing the corresponding handler.
// Assumes lock is held by the caller.
func (g *CambiaGame) HandlePlayerAction(playerID uuid.UUID, action models.GameAction) {
	// --- Basic State Checks ---
	if g.GameOver {
		log.Printf("Game %s: Action %s from %s ignored (game over).", g.ID, action.ActionType, playerID)
		return
	}
	if !g.Started && !g.PreGameActive {
		log.Printf("Game %s: Action %s from %s ignored (game not started).", g.ID, action.ActionType, playerID)
		return
	}
	if g.PreGameActive {
		log.Printf("Game %s: Action %s from %s ignored (pre-game active).", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Cannot perform actions during pre-game reveal."}})
		return
	}

	// --- Player Validation ---
	player := g.getPlayerByID(playerID)
	if player == nil || !player.Connected {
		log.Printf("Game %s: Action %s from non-existent/disconnected player %s ignored.", g.ID, action.ActionType, playerID)
		return
	}

	// --- Turn and State Validation ---
	isCurrentPlayer := len(g.Players) > g.CurrentPlayerIndex && g.Players[g.CurrentPlayerIndex].ID == playerID
	// Allow snap anytime.
	if action.ActionType != "action_snap" && !isCurrentPlayer {
		log.Printf("Game %s: Action %s from %s ignored (not their turn).", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "It's not your turn."}})
		return
	}
	// Check if blocked by pending special action.
	if g.SpecialAction.Active && g.SpecialAction.PlayerID == playerID && action.ActionType != "action_special" {
		log.Printf("Game %s: Action %s from %s ignored (special action pending).", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "You must resolve the special card action first (use action_special with 'skip' or required payload)."}})
		return
	}
	// Prevent drawing twice.
	isDrawAction := action.ActionType == "action_draw_stockpile" || action.ActionType == "action_draw_discardpile"
	if player.DrawnCard != nil && isDrawAction {
		log.Printf("Game %s: Action %s from %s ignored (already drawn).", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "You have already drawn a card this turn."}})
		return
	}
	// Prevent discard/replace without drawing first.
	isDiscardReplace := action.ActionType == "action_discard" || action.ActionType == "action_replace"
	if player.DrawnCard == nil && isDiscardReplace {
		log.Printf("Game %s: Action %s from %s ignored (must draw first).", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "You must draw a card first."}})
		return
	}

	// Update last seen time.
	g.lastSeen[playerID] = time.Now()

	// --- Route Action ---
	switch action.ActionType {
	case "action_snap":
		g.handleSnap(playerID, action.Payload)
	case "action_draw_stockpile":
		g.handleDrawFrom(playerID, "stockpile")
	case "action_draw_discardpile":
		g.handleDrawFrom(playerID, "discardpile")
	case "action_discard":
		g.handleDiscard(playerID, action.Payload)
	case "action_replace":
		g.handleReplace(playerID, action.Payload)
	case "action_cambia":
		g.handleCallCambia(playerID)
	// Note: "action_special" is handled directly by ProcessSpecialAction.
	default:
		log.Printf("Game %s: Unknown action type '%s' received from player %s.", g.ID, action.ActionType, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Unknown action type."}})
	}
}

// handleDrawFrom processes drawing from the specified location (stockpile or discard).
// Assumes lock is held by caller and basic validation passed.
func (g *CambiaGame) handleDrawFrom(playerID uuid.UUID, location string) {
	player := g.getPlayerByID(playerID) // Assumed to exist.
	card := g.drawCardFromLocation(playerID, location)

	if card != nil {
		player.DrawnCard = card // Store the drawn card.
		g.ResetTurnTimer()      // Reset timer after successful draw.
	} else {
		// Draw failed (e.g., empty piles). Game may have ended in drawCardFromLocation.
		log.Printf("Game %s: Draw from %s failed for player %s.", g.ID, location, playerID)
		// No explicit turn advance needed here; draw logic handles game end or returns nil.
	}
	// Turn does not advance here; player must now discard or replace.
}

// handleDiscard processes discarding the currently held DrawnCard.
// Assumes lock is held by caller and basic validation passed.
func (g *CambiaGame) handleDiscard(playerID uuid.UUID, payload map[string]interface{}) {
	player := g.getPlayerByID(playerID) // Assumed to exist.
	drawnCard := player.DrawnCard       // Assumed to be non-nil.

	// Validate card ID from payload matches the held card.
	cardIDStr, _ := payload["id"].(string)
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil || drawnCard.ID != cardID {
		log.Printf("Game %s: Player %s discard payload card ID '%s' does not match drawn card ID '%s'. Ignoring.", g.ID, playerID, cardIDStr, drawnCard.ID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Card ID mismatch for discard."}})
		return
	}

	player.DrawnCard = nil // Clear the held card.

	g.DiscardPile = append(g.DiscardPile, drawnCard)
	g.logAction(playerID, string(EventPlayerDiscard), map[string]interface{}{"cardId": drawnCard.ID, "source": "drawn"})

	// Broadcast discard event (revealing card details).
	g.fireEvent(GameEvent{
		Type: EventPlayerDiscard,
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: drawnCard.ID, Rank: drawnCard.Rank, Suit: drawnCard.Suit, Value: drawnCard.Value},
	})

	g.snapUsedForThisDiscard = false // Reset snap state.

	// Check for special ability on the discarded card.
	// This function handles advancing the turn if no ability is triggered,
	// or resetting the timer if an ability requires further player input.
	g.applySpecialAbilityIfFreshlyDrawn(drawnCard, playerID)
}

// handleReplace processes swapping the DrawnCard with a card in the player's hand.
// Assumes lock is held by caller and basic validation passed.
func (g *CambiaGame) handleReplace(playerID uuid.UUID, payload map[string]interface{}) {
	player := g.getPlayerByID(playerID) // Assumed to exist.
	drawnCard := player.DrawnCard       // Assumed to be non-nil.

	// Extract target card ID and index from payload.
	cardIDToReplaceStr, _ := payload["id"].(string)
	idxToReplaceFloat, idxOK := payload["idx"].(float64)
	idxToReplace := int(idxToReplaceFloat)

	// Validate index.
	if !idxOK || idxToReplace < 0 || idxToReplace >= len(player.Hand) {
		log.Printf("Game %s: Player %s provided invalid index %d for replace action. Hand size: %d. Ignoring.", g.ID, playerID, idxToReplace, len(player.Hand))
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Invalid index for replacement."}})
		return
	}

	cardToReplace := player.Hand[idxToReplace]
	// Validate target card ID matches the card at the index.
	cardIDToReplace, err := uuid.Parse(cardIDToReplaceStr)
	if err != nil || cardToReplace.ID != cardIDToReplace {
		log.Printf("Game %s: Player %s replace payload card ID '%s' does not match card ID '%s' at index %d. Ignoring.", g.ID, playerID, cardIDToReplaceStr, cardToReplace.ID, idxToReplace)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Card ID mismatch for replacement target."}})
		return
	}

	player.DrawnCard = nil // Clear held card state.

	// Perform swap and discard.
	player.Hand[idxToReplace] = drawnCard
	g.DiscardPile = append(g.DiscardPile, cardToReplace)
	g.logAction(playerID, string(EventPlayerDiscard), map[string]interface{}{
		"cardId":  cardToReplace.ID,
		"index":   idxToReplace,
		"source":  "replace",
		"drawnId": drawnCard.ID,
	})

	// Broadcast discard event for the card leaving the hand.
	eventIdx := idxToReplace
	g.fireEvent(GameEvent{
		Type: EventPlayerDiscard,
		User: &EventUser{ID: playerID},
		Card: &EventCard{ID: cardToReplace.ID, Rank: cardToReplace.Rank, Suit: cardToReplace.Suit, Value: cardToReplace.Value, Idx: &eventIdx},
	})

	g.snapUsedForThisDiscard = false // Reset snap state.

	// Check special ability based on house rule.
	abilityTriggered := false
	if g.HouseRules.AllowReplaceAbilities {
		abilityTriggered = g.applySpecialAbilityIfFreshlyDrawn(cardToReplace, playerID)
	}

	// Advance turn if no ability was triggered or requires further action.
	if !abilityTriggered {
		g.advanceTurn()
	}
	// Timer reset/handling occurs within applySpecialAbilityIfFreshlyDrawn if triggered.
}

// handleSnap processes an out-of-turn snap attempt by a player.
// Assumes lock is held by caller.
func (g *CambiaGame) handleSnap(playerID uuid.UUID, payload map[string]interface{}) {
	// Validate payload.
	cardIDStr, _ := payload["id"].(string)
	cardID, err := uuid.Parse(cardIDStr)
	if err != nil {
		log.Printf("Game %s: Invalid card ID '%s' in snap payload from player %s. Ignoring.", g.ID, cardIDStr, playerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Invalid card ID format for snap."}})
		return
	}
	g.logAction(playerID, "action_snap_attempt", map[string]interface{}{"cardId": cardID})

	// Check discard pile state.
	if len(g.DiscardPile) == 0 {
		log.Printf("Game %s: Player %s snap failed (discard empty). Penalizing.", g.ID, playerID)
		g.penalizeSnapFail(playerID, nil) // Pass nil card ID.
		return
	}

	// Check SnapRace rule.
	if g.HouseRules.SnapRace && g.snapUsedForThisDiscard {
		log.Printf("Game %s: Player %s snap failed (SnapRace used). Penalizing.", g.ID, playerID)
		g.penalizeSnapFail(playerID, nil) // Pass nil card ID.
		return
	}

	lastDiscardedCard := g.DiscardPile[len(g.DiscardPile)-1]
	player := g.getPlayerByID(playerID) // Assumed to exist.

	// Find card in player's hand.
	snappedCard, snappedCardIdx := g.findCardByID(playerID, cardID)
	if snappedCard == nil {
		log.Printf("Game %s: Player %s snap failed (card %s not in hand). Penalizing.", g.ID, playerID, cardID)
		g.penalizeSnapFail(playerID, nil) // Pass nil card ID.
		return
	}

	// Check for rank match.
	if snappedCard.Rank == lastDiscardedCard.Rank {
		// --- Successful Snap ---
		log.Printf("Game %s: Player %s successfully snapped card %s (Rank: %s).", g.ID, playerID, snappedCard.ID, snappedCard.Rank)
		g.logAction(playerID, string(EventPlayerSnapSuccess), map[string]interface{}{"cardId": snappedCard.ID, "rank": snappedCard.Rank})

		if g.HouseRules.SnapRace {
			g.snapUsedForThisDiscard = true // Mark snap used for this discard.
		}

		// Update game state.
		player.Hand = append(player.Hand[:snappedCardIdx], player.Hand[snappedCardIdx+1:]...) // Remove card.
		g.DiscardPile = append(g.DiscardPile, snappedCard)                                    // Add to discard.

		// Broadcast success event.
		eventIdx := snappedCardIdx
		g.fireEvent(GameEvent{
			Type: EventPlayerSnapSuccess,
			User: &EventUser{ID: playerID},
			Card: &EventCard{ID: snappedCard.ID, Rank: snappedCard.Rank, Suit: snappedCard.Suit, Value: snappedCard.Value, Idx: &eventIdx},
		})
		// Turn does not typically advance on successful snap.
	} else {
		// --- Failed Snap (Rank Mismatch) ---
		log.Printf("Game %s: Player %s failed snap (rank mismatch: %s vs %s). Penalizing.", g.ID, playerID, snappedCard.Rank, lastDiscardedCard.Rank)
		g.penalizeSnapFail(playerID, snappedCard) // Pass the specific card attempted.
	}
}

// penalizeSnapFail applies penalties for incorrect snap attempts.
// Assumes lock is held by caller.
func (g *CambiaGame) penalizeSnapFail(playerID uuid.UUID, attemptedCard *models.Card) {
	attemptedCardID := uuid.Nil
	if attemptedCard != nil {
		attemptedCardID = attemptedCard.ID
	}
	g.logAction(playerID, string(EventPlayerSnapFail), map[string]interface{}{"attemptedCardId": attemptedCardID})

	player := g.getPlayerByID(playerID) // Assumed to exist.

	// Broadcast public failure event.
	failEvent := GameEvent{
		Type: EventPlayerSnapFail,
		User: &EventUser{ID: playerID},
	}
	// Include attempted card details if a specific card was involved.
	if attemptedCard != nil {
		failIdx := -1 // Find index for event payload.
		for i, c := range player.Hand {
			if c.ID == attemptedCard.ID {
				failIdx = i
				break
			}
		}
		failEvent.Card = &EventCard{
			ID:    attemptedCard.ID,
			Rank:  attemptedCard.Rank, // Reveal details on failure.
			Suit:  attemptedCard.Suit,
			Value: attemptedCard.Value,
		}
		if failIdx != -1 {
			eventIdx := failIdx
			failEvent.Card.Idx = &eventIdx
		}
	}
	g.fireEvent(failEvent)

	// Apply penalty draws based on house rules.
	penaltyCount := g.HouseRules.PenaltyDrawCount
	if penaltyCount <= 0 {
		log.Printf("Game %s: No penalty cards drawn for failed snap by %s (Count: %d).", g.ID, playerID, penaltyCount)
		return
	}
	log.Printf("Game %s: Applying %d penalty card(s) to player %s for failed snap.", g.ID, penaltyCount, playerID)

	newCardIDs := []uuid.UUID{} // Track IDs for logging.
	for i := 0; i < penaltyCount; i++ {
		card := g.internalDrawStockpile() // Draw internally.
		if card == nil {
			log.Printf("Game %s: Stockpile empty during penalty draw %d/%d for player %s.", g.ID, i+1, penaltyCount, playerID)
			break // Stop if deck runs out.
		}

		// Add card to hand and track ID.
		player.Hand = append(player.Hand, card)
		newCardIDs = append(newCardIDs, card.ID)
		newCardIndex := len(player.Hand) - 1

		// Broadcast public penalty notification (obfuscated ID).
		g.fireEvent(GameEvent{
			Type: EventPlayerSnapPenalty,
			User: &EventUser{ID: playerID},
			Card: &EventCard{ID: card.ID},
			Payload: map[string]interface{}{
				"count": i + 1,
				"total": penaltyCount,
			},
		})

		// Broadcast private penalty card details.
		privateIdx := newCardIndex
		g.fireEventToPlayer(playerID, GameEvent{
			Type: EventPrivateSnapPenalty,
			Card: &EventCard{ID: card.ID, Idx: &privateIdx, Rank: card.Rank, Suit: card.Suit, Value: card.Value},
			Payload: map[string]interface{}{
				"count": i + 1,
				"total": penaltyCount,
			},
		})
	}
	g.logAction(playerID, "player_snap_penalty_applied", map[string]interface{}{"count": len(newCardIDs), "newCards": newCardIDs})
}

// handleCallCambia processes a player calling Cambia, initiating the final round.
// Assumes lock is held by caller.
func (g *CambiaGame) handleCallCambia(playerID uuid.UUID) {
	// Validate state: Cambia not already called.
	if g.CambiaCalled {
		log.Printf("Game %s: Player %s tried to call Cambia, but already called by %s. Ignoring.", g.ID, playerID, g.CambiaCallerID)
		g.fireEventToPlayer(playerID, GameEvent{Type: EventPrivateSpecialFail, Payload: map[string]interface{}{"message": "Cambia has already been called."}})
		return
	}

	// Optional: Validate house rules for calling Cambia (e.g., minimum rounds).
	// Example: Require TurnID >= number of players.
	// if g.TurnID < len(g.Players) { ... return error ... }

	log.Printf("Game %s: Player %s calls Cambia!", g.ID, playerID)
	g.logAction(playerID, string(EventPlayerCambia), nil)

	// Update game state.
	g.CambiaCalled = true
	g.CambiaCallerID = playerID

	// Update player state.
	player := g.getPlayerByID(playerID) // Assumed to exist.
	if player != nil {
		player.HasCalledCambia = true
		player.DrawnCard = nil // Clear any held card.
	} else {
		log.Printf("Error: Game %s: Could not find player %s to mark HasCalledCambia.", g.ID, playerID)
	}

	// Broadcast the event.
	g.fireEvent(GameEvent{
		Type: EventPlayerCambia,
		User: &EventUser{ID: playerID},
	})

	// Turn ends immediately after calling Cambia.
	g.advanceTurn()
}

// applySpecialAbilityIfFreshlyDrawn checks if a discarded card triggers a special action,
// updates game state accordingly, and either advances the turn or resets the timer.
// Returns true if an ability was triggered, false otherwise.
// Assumes lock is held by caller.
func (g *CambiaGame) applySpecialAbilityIfFreshlyDrawn(c *models.Card, playerID uuid.UUID) bool {
	specialType := rankToSpecial(c.Rank)
	if specialType != "" {
		// --- Special Ability Triggered ---
		log.Printf("Game %s: Player %s discarded %s (%s), triggering special action choice: %s", g.ID, playerID, c.ID, c.Rank, specialType)

		// Activate pending special action state.
		g.SpecialAction = SpecialActionState{
			Active:        true,
			PlayerID:      playerID,
			CardRank:      c.Rank,
			FirstStepDone: false, // Reset King state.
		}

		g.ResetTurnTimer() // Give player time to decide.

		// Broadcast choice event.
		g.fireEvent(GameEvent{
			Type:    EventPlayerSpecialChoice,
			User:    &EventUser{ID: playerID},
			Card:    &EventCard{ID: c.ID, Rank: c.Rank},
			Special: specialType,
		})
		g.logAction(playerID, string(EventPlayerSpecialChoice), map[string]interface{}{"cardId": c.ID, "rank": c.Rank, "special": specialType})

		return true // Ability triggered, turn does NOT advance yet.
	} else {
		// --- No Special Ability ---
		g.advanceTurn() // Advance turn immediately.
		return false
	}
}

// rankToSpecial maps card ranks to their corresponding special action identifier string.
// Returns an empty string if the rank has no special ability.
func rankToSpecial(rank string) string {
	switch rank {
	case "7", "8":
		return "peek_self"
	case "9", "T": // T represents Ten.
		return "peek_other"
	case "J", "Q": // Jack, Queen.
		return "swap_blind"
	case "K": // King.
		return "swap_peek" // Initial step for King.
	default:
		return ""
	}
}

// EndGame finalizes the game, computes scores, determines winners, applies bonuses/penalties,
// broadcasts results, and triggers the OnGameEnd callback.
// Assumes lock is held by caller.
func (g *CambiaGame) EndGame() {
	if g.GameOver {
		log.Printf("Game %s: EndGame called, but game is already over.", g.ID)
		return
	}
	g.GameOver = true
	g.Started = false // Mark as inactive.
	log.Printf("Game %s: Ending game. Computing final scores...", g.ID)

	// Stop timers.
	if g.turnTimer != nil {
		g.turnTimer.Stop()
		g.turnTimer = nil
	}
	if g.preGameTimer != nil {
		g.preGameTimer.Stop()
		g.preGameTimer = nil
	}

	// --- Scoring and Winner Determination ---
	finalScores := g.computeScores()
	winners, penaltyApplies := g.findWinnersWithCambiaLogic(finalScores)
	adjustedScores := make(map[uuid.UUID]int) // Scores after bonus/penalty.
	for id, score := range finalScores {
		adjustedScores[id] = score
	}

	// Apply Cambia caller penalty if needed.
	if penaltyApplies && g.CambiaCallerID != uuid.Nil {
		if _, ok := adjustedScores[g.CambiaCallerID]; ok {
			penaltyValue := 1 // Default penalty.
			if g.Circuit.Enabled {
				penaltyValue = g.Circuit.Rules.FalseCambiaPenalty
			}
			adjustedScores[g.CambiaCallerID] += penaltyValue
			log.Printf("Game %s: Applying +%d penalty to Cambia caller %s for not winning.", g.ID, penaltyValue, g.CambiaCallerID)
		} else {
			log.Printf("Warning: Game %s: Cambia caller %s not found in final scores for penalty.", g.ID, g.CambiaCallerID)
		}
	}

	// Apply circuit win bonus if needed.
	winBonusApplied := false
	if g.Circuit.Enabled && g.Circuit.Rules.WinBonus != 0 && len(winners) > 0 {
		winBonus := g.Circuit.Rules.WinBonus
		for _, winnerID := range winners {
			if _, ok := adjustedScores[winnerID]; ok {
				adjustedScores[winnerID] += winBonus
				log.Printf("Game %s: Applying %d win bonus to winner %s.", g.ID, winBonus, winnerID)
				winBonusApplied = true
			}
		}
	}
	// --- End Scoring ---

	g.logAction(uuid.Nil, string(EventGameEnd), map[string]interface{}{
		"scores":         adjustedScores,
		"winners":        winners,
		"caller":         g.CambiaCallerID,
		"penaltyApplied": penaltyApplies,
		"winBonus":       g.Circuit.Rules.WinBonus, // Log potential bonus value.
	})
	g.persistFinalGameState(adjustedScores, winners) // Persist final hands.

	// Determine primary winner for event payload.
	var firstWinner uuid.UUID
	if len(winners) > 0 {
		firstWinner = winners[0]
	}

	// Broadcast game end event.
	resultsPayload := map[string]interface{}{
		"scores":          map[string]int{},
		"winner":          firstWinner.String(), // "0000..." if no winner.
		"caller":          g.CambiaCallerID.String(),
		"penaltyApplied":  penaltyApplies,
		"winBonusApplied": winBonusApplied,
		// "winners": winnerIDsToStrings(winners), // Optional: Send all winners.
	}
	for pid, score := range adjustedScores { // Use adjusted scores.
		resultsPayload["scores"].(map[string]int)[pid.String()] = score
	}
	g.fireEvent(GameEvent{
		Type:    EventGameEnd,
		Payload: resultsPayload,
	})

	// Trigger external callback (e.g., update lobby).
	if g.OnGameEnd != nil {
		g.OnGameEnd(g.LobbyID, firstWinner, adjustedScores)
	}

	// Optional: Persist detailed results/ratings.
	// g.persistResults(adjustedScores, winners)

	log.Printf("Game %s: Ended. Winner(s): %v. Final Scores (Adj): %v", g.ID, winners, adjustedScores)
}

// computeScores calculates the sum of card values for each valid player.
// Assumes lock is held by caller.
func (g *CambiaGame) computeScores() map[uuid.UUID]int {
	scores := make(map[uuid.UUID]int)
	for _, p := range g.Players {
		// Score only connected players or if disconnect doesn't forfeit.
		if p.Connected || !g.HouseRules.ForfeitOnDisconnect {
			sum := 0
			for _, c := range p.Hand {
				sum += c.Value
			}
			scores[p.ID] = sum
		} else {
			log.Printf("Game %s: Player %s score omitted (disconnected/forfeited).", g.ID, p.ID)
		}
	}
	return scores
}

// findWinnersWithCambiaLogic determines winners based on lowest score, applying Cambia rules.
// Returns list of winner UUIDs and bool indicating if caller penalty applies.
// Assumes lock is held by caller.
func (g *CambiaGame) findWinnersWithCambiaLogic(scores map[uuid.UUID]int) ([]uuid.UUID, bool) {
	if len(scores) == 0 {
		return []uuid.UUID{}, false // No scores, no winners.
	}

	// Find lowest score among scored players.
	lowestScore := -1
	first := true
	for _, score := range scores {
		if first || score < lowestScore {
			lowestScore = score
			first = false
		}
	}

	// Find all players with the lowest score.
	potentialWinners := []uuid.UUID{}
	for playerID, score := range scores {
		if score == lowestScore {
			potentialWinners = append(potentialWinners, playerID)
		}
	}

	// Apply Cambia logic if called.
	if g.CambiaCalled && g.CambiaCallerID != uuid.Nil {
		callerIsPotentialWinner := false
		for _, winnerID := range potentialWinners {
			if winnerID == g.CambiaCallerID {
				callerIsPotentialWinner = true
				break
			}
		}

		if callerIsPotentialWinner {
			// Caller wins or ties for lowest. They are the sole winner. Penalty does not apply.
			log.Printf("Game %s: Cambia caller %s won or tied for lowest score (%d).", g.ID, g.CambiaCallerID, lowestScore)
			return []uuid.UUID{g.CambiaCallerID}, false
		} else {
			// Caller did not win. Penalty applies. Check for ties among others.
			log.Printf("Game %s: Cambia caller %s did not win (Lowest score: %d). Penalty applies.", g.ID, g.CambiaCallerID, lowestScore)
			// The winners are the potential winners (who already have the lowest score).
			// Spec: "If the caller does not win the round, and there exists a tie among remaining players, there is no victory granted."
			if len(potentialWinners) == 1 {
				// Single non-caller winner.
				log.Printf("Game %s: Single winner (%s) found after caller failed.", g.ID, potentialWinners[0])
				return potentialWinners, true // Penalty applies to caller.
			} else {
				// Tie among non-callers, or no non-callers had lowest score. No victory granted.
				log.Printf("Game %s: Tie among %d non-caller winners. No victory granted.", g.ID, len(potentialWinners))
				return []uuid.UUID{}, true // No winner, penalty applies to caller.
			}
		}
	} else {
		// Cambia not called: Normal win (lowest score wins, ties allowed).
		log.Printf("Game %s: Cambia not called. Lowest score: %d. Winners: %v", g.ID, lowestScore, potentialWinners)
		return potentialWinners, false // No caller penalty.
	}
}

// persistFinalGameState saves final hands and winners to the database.
// Assumes lock is held by caller.
func (g *CambiaGame) persistFinalGameState(finalScores map[uuid.UUID]int, winners []uuid.UUID) {
	type finalHandCard struct {
		Rank string `json:"rank"`
		Suit string `json:"suit"`
		Val  int    `json:"value"` // Use 'Val' to avoid conflict if struct embedding happens later
	}
	type finalPlayerState struct {
		Hand  []finalHandCard `json:"hand"`
		Score int             `json:"score"`
	}
	// Snapshot structure matches expected JSONB format.
	snapshot := map[string]interface{}{
		"players": map[string]finalPlayerState{},
		"winners": winners, // Store list of winner UUIDs.
	}

	playerStates := snapshot["players"].(map[string]finalPlayerState)
	for _, p := range g.Players { // Iterate over original player list to capture all hands.
		score, scoreOk := finalScores[p.ID]
		if !scoreOk {
			// Handle players without a score (e.g., forfeited).
			// Record their hand but maybe use a special score indicator?
			score = -999 // Example: Indicate forfeit score.
		}
		state := finalPlayerState{
			Hand:  make([]finalHandCard, len(p.Hand)),
			Score: score,
		}
		for i, c := range p.Hand {
			state.Hand[i] = finalHandCard{Rank: c.Rank, Suit: c.Suit, Val: c.Value}
		}
		playerStates[p.ID.String()] = state
	}

	// Persist asynchronously.
	go database.StoreFinalGameStateInDB(context.Background(), g.ID, snapshot)
}

// removeCardFromPlayerHand removes a specific card instance from a player's hand.
// Returns true if found and removed, false otherwise, and the index where it was found.
// Assumes lock is held by caller.
func (g *CambiaGame) removeCardFromPlayerHand(playerID, cardID uuid.UUID) (bool, int) {
	player := g.getPlayerByID(playerID)
	if player == nil {
		return false, -1
	}
	removedIndex := -1
	for i, c := range player.Hand {
		if c.ID == cardID {
			removedIndex = i
			break
		}
	}
	if removedIndex != -1 {
		player.Hand = append(player.Hand[:removedIndex], player.Hand[removedIndex+1:]...)
		return true, removedIndex
	}
	return false, -1
}

// getPlayerByID finds a player struct by ID within the game's Players slice.
// Returns the player pointer or nil if not found.
// Assumes lock is held by caller.
func (g *CambiaGame) getPlayerByID(playerID uuid.UUID) *models.Player {
	for _, p := range g.Players {
		if p.ID == playerID {
			return p
		}
	}
	return nil
}

// logAction sends game action details to the historian service via Redis queue.
// Increments the internal action index for ordering.
// Assumes lock is held by caller.
func (g *CambiaGame) logAction(actorID uuid.UUID, actionType string, payload map[string]interface{}) {
	g.actionIndex++
	if payload == nil {
		payload = make(map[string]interface{}) // Ensure payload is not nil.
	}
	record := cache.GameActionRecord{
		GameID:        g.ID,
		ActionIndex:   g.actionIndex,
		ActorUserID:   actorID, // Can be Nil for game events.
		ActionType:    actionType,
		ActionPayload: payload,
		Timestamp:     time.Now().UnixMilli(),
	}

	// Asynchronously publish to Redis.
	go func(rec cache.GameActionRecord) {
		// Short timeout for the Redis operation.
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Check if Redis client is initialized.
		if cache.Rdb == nil {
			// log.Printf("Debug: Redis client (Rdb) is nil. Cannot log action %d for game %s.", rec.ActionIndex, g.ID) // Reduce noise
			return
		}
		if err := cache.PublishGameAction(ctx, rec); err != nil {
			log.Printf("Error: Game %s: Failed publishing action %d ('%s') to Redis: %v", g.ID, rec.ActionIndex, rec.ActionType, err)
		}
	}(record)
}

// ResetTurnTimer restarts the turn timer for the current player.
// Exported for use by special action logic.
// Assumes lock is held by caller.
func (g *CambiaGame) ResetTurnTimer() {
	g.scheduleNextTurnTimer() // Use the internal scheduler.
}

// FireEventPrivateSpecialActionFail helper to send a private failure event for special actions.
// Assumes lock is held by caller.
func (g *CambiaGame) FireEventPrivateSpecialActionFail(userID uuid.UUID, reason string, special string, card1, card2 *EventCard) {
	ev := GameEvent{
		Type:    EventPrivateSpecialFail,
		Special: special,
		Payload: map[string]interface{}{"message": reason},
		Card1:   card1, // Include card info if relevant to the failure.
		Card2:   card2,
	}
	g.fireEventToPlayer(userID, ev)
	g.logAction(userID, string(EventPrivateSpecialFail), map[string]interface{}{"reason": reason, "special": special})
}

// FailSpecialAction clears the pending special action state and advances the turn, sending a failure event.
// Assumes lock is held by caller.
func (g *CambiaGame) FailSpecialAction(userID uuid.UUID, reason string) {
	if !g.SpecialAction.Active || g.SpecialAction.PlayerID != userID {
		log.Printf("Warning: Game %s: FailSpecialAction called for %s but state mismatch (Active:%v, Player:%s). Sending fail event anyway.", g.ID, userID, g.SpecialAction.Active, g.SpecialAction.PlayerID)
		// Send fail event even if state is inconsistent.
		g.FireEventPrivateSpecialActionFail(userID, reason, g.SpecialAction.CardRank, nil, nil)
		// Don't advance turn if state was already inconsistent.
		return
	}
	specialType := rankToSpecial(g.SpecialAction.CardRank) // Get type before clearing.
	log.Printf("Game %s: Failing special action %s for player %s. Reason: %s", g.ID, specialType, userID, reason)

	// Fire the fail event before clearing state.
	g.FireEventPrivateSpecialActionFail(userID, reason, specialType, nil, nil)

	g.SpecialAction = SpecialActionState{} // Clear state.
	g.advanceTurn()                        // Advance turn after failure.
}

// FireEventPrivateSuccess helper to send a private success event for special actions.
// Assumes lock is held by caller.
func (g *CambiaGame) FireEventPrivateSuccess(userID uuid.UUID, special string, c1Ev, c2Ev *EventCard) {
	ev := GameEvent{
		Type:    EventPrivateSpecialSuccess,
		Special: special,
		Card1:   c1Ev, // Include revealed card details.
		Card2:   c2Ev,
	}
	g.fireEventToPlayer(userID, ev)
	// Logging is typically handled within the specific do* action function.
}

// FireEventPlayerSpecialAction helper to broadcast public info about a special action.
// Assumes lock is held by caller.
func (g *CambiaGame) FireEventPlayerSpecialAction(userID uuid.UUID, special string, c1Ev, c2Ev *EventCard) {
	ev := GameEvent{
		Type:    EventPlayerSpecialAction,
		User:    &EventUser{ID: userID},
		Special: special,
		Card1:   c1Ev, // Include obfuscated card details (ID, index, owner).
		Card2:   c2Ev,
	}
	g.fireEvent(ev)
	// Logging is typically handled within the specific do* action function.
}
