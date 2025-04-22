// internal/game/sync_state.go
package game

import (
	"github.com/google/uuid"
)

// ObfCard holds minimal info for obfuscating face-down cards and optionally revealing
// rank/suit/value for cards that belong to the requesting player or are otherwise visible.
type ObfCard struct {
	ID    uuid.UUID `json:"id"`
	Known bool      `json:"known"` // Indicates if rank/suit/value should be shown
	Rank  string    `json:"rank,omitempty"`
	Suit  string    `json:"suit,omitempty"`
	Value int       `json:"value,omitempty"`
	Idx   *int      `json:"idx,omitempty"` // Pointer to allow omitting zero index
}

// ObfPlayerState represents the minimal state for one player from the perspective of a requesting user.
type ObfPlayerState struct {
	PlayerID        uuid.UUID `json:"playerId"` // Changed casing for consistency
	HandSize        int       `json:"handSize"`
	HasCalledCambia bool      `json:"hasCalledCambia"` // Include Cambia status
	Connected       bool      `json:"connected"`
	IsCurrentTurn   bool      `json:"isCurrentTurn"`
	// Reveal hand only for the requesting player ('self')
	RevealedHand []ObfCard `json:"revealedHand,omitempty"`
	// Reveal drawn card only for the requesting player ('self')
	DrawnCard *ObfCard `json:"drawnCard,omitempty"`
}

// ObfGameState is returned by GetCurrentObfuscatedGameState.
type ObfGameState struct {
	GameID          uuid.UUID        `json:"gameId"` // Changed casing
	PreGameActive   bool             `json:"preGameActive"`
	Started         bool             `json:"started"`
	GameOver        bool             `json:"gameOver"`
	CurrentPlayerID uuid.UUID        `json:"currentPlayerId"` // Changed casing
	TurnID          int              `json:"turnId"`          // Added Turn ID
	StockpileSize   int              `json:"stockpileSize"`
	DiscardSize     int              `json:"discardSize"`
	DiscardTop      *ObfCard         `json:"discardTop,omitempty"` // Top card of discard pile
	Players         []ObfPlayerState `json:"players"`
	CambiaCalled    bool             `json:"cambiaCalled"`             // Overall Cambia status
	CambiaCallerID  uuid.UUID        `json:"cambiaCallerId,omitempty"` // Who called Cambia
	HouseRules      HouseRules       `json:"houseRules"`               // Include house rules
	// Include SpecialAction state if needed by client? Usually private.
	// SpecialAction *SpecialActionState `json:"specialAction,omitempty"`
}

// GetCurrentObfuscatedGameState generates a snapshot of the game state,
// tailored to the perspective of the requesting user (`forUser`).
// Assumes the game lock is held by the caller.
func (g *CambiaGame) GetCurrentObfuscatedGameState(forUser uuid.UUID) ObfGameState {
	// No locking here - caller must hold the lock (e.g., HandleReconnect or a dedicated sync endpoint)

	obf := ObfGameState{
		GameID:         g.ID,
		PreGameActive:  g.PreGameActive,
		Started:        g.Started,
		GameOver:       g.GameOver,
		TurnID:         g.TurnID,
		StockpileSize:  len(g.Deck),
		DiscardSize:    len(g.DiscardPile),
		CambiaCalled:   g.CambiaCalled,
		CambiaCallerID: g.CambiaCallerID,
		HouseRules:     g.HouseRules, // Include house rules in state sync
	}

	// Determine current player ID safely
	if len(g.Players) > 0 && g.CurrentPlayerIndex >= 0 && g.CurrentPlayerIndex < len(g.Players) {
		obf.CurrentPlayerID = g.Players[g.CurrentPlayerIndex].ID
	} else {
		obf.CurrentPlayerID = uuid.Nil // Indicate no current player if state is inconsistent
	}

	// Set discard top card (always known according to most rule sets)
	if len(g.DiscardPile) > 0 {
		top := g.DiscardPile[len(g.DiscardPile)-1]
		obf.DiscardTop = &ObfCard{
			ID:    top.ID,
			Known: true, // Discard pile top is public knowledge
			Rank:  top.Rank,
			Suit:  top.Suit,
			Value: top.Value,
			// Idx not relevant for discard pile top
		}
	}

	// Prepare player states
	obf.Players = make([]ObfPlayerState, len(g.Players))
	for i, pl := range g.Players {
		isSelf := pl.ID == forUser
		ps := ObfPlayerState{
			PlayerID:        pl.ID,
			HandSize:        len(pl.Hand),
			HasCalledCambia: pl.HasCalledCambia, // Include player's Cambia status
			Connected:       pl.Connected,
			IsCurrentTurn:   (pl.ID == obf.CurrentPlayerID && g.Started && !g.GameOver), // Ensure game started and not over
		}

		// Reveal hand details only for 'self'
		if isSelf {
			ps.RevealedHand = make([]ObfCard, len(pl.Hand))
			for j, c := range pl.Hand {
				idx := j // Capture index for the pointer
				ps.RevealedHand[j] = ObfCard{
					ID:    c.ID,
					Known: true, // Player knows their own cards
					Rank:  c.Rank,
					Suit:  c.Suit,
					Value: c.Value,
					Idx:   &idx,
				}
			}
			// Reveal drawn card details only for 'self'
			if pl.DrawnCard != nil {
				ps.DrawnCard = &ObfCard{
					ID:    pl.DrawnCard.ID,
					Known: true, // Player knows their drawn card
					Rank:  pl.DrawnCard.Rank,
					Suit:  pl.DrawnCard.Suit,
					Value: pl.DrawnCard.Value,
					// Idx not relevant for drawn card
				}
			}
		} else {
			// For other players, just show ID and index for potential targeting
			// HandSize is already included. No need to send obfuscated cards for others.
		}
		obf.Players[i] = ps
	}

	return obf
}
