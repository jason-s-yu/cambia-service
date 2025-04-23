// internal/game/sync_state.go
package game

import (
	"github.com/google/uuid"
)

// ObfCard represents a card's state for client synchronization, potentially hiding details.
type ObfCard struct {
	ID    uuid.UUID `json:"id"`
	Known bool      `json:"known"` // True if Rank/Suit/Value should be revealed to the requesting client.
	Rank  string    `json:"rank,omitempty"`
	Suit  string    `json:"suit,omitempty"`
	Value int       `json:"value,omitempty"`
	Idx   *int      `json:"idx,omitempty"` // Pointer to allow omitting zero index (relevant for hand cards).
}

// ObfPlayerState represents the state of a single player, obfuscated for a specific observer.
type ObfPlayerState struct {
	PlayerID        uuid.UUID `json:"playerId"` // Using camelCase for consistency with frontend expectations.
	Username        string    `json:"username"` // Added username.
	HandSize        int       `json:"handSize"`
	HasCalledCambia bool      `json:"hasCalledCambia"`
	Connected       bool      `json:"connected"`
	IsCurrentTurn   bool      `json:"isCurrentTurn"`
	// RevealedHand is populated only for the player requesting the state ('self').
	RevealedHand []ObfCard `json:"revealedHand,omitempty"`
	// DrawnCard is populated only for the player requesting the state ('self').
	DrawnCard *ObfCard `json:"drawnCard,omitempty"`
}

// ObfGameState represents the overall game state, obfuscated for a specific observer.
type ObfGameState struct {
	GameID          uuid.UUID        `json:"gameId"` // Using camelCase.
	PreGameActive   bool             `json:"preGameActive"`
	Started         bool             `json:"started"`
	GameOver        bool             `json:"gameOver"`
	CurrentPlayerID uuid.UUID        `json:"currentPlayerId"` // Using camelCase.
	TurnID          int              `json:"turnId"`          // Using camelCase.
	StockpileSize   int              `json:"stockpileSize"`
	DiscardSize     int              `json:"discardSize"`
	DiscardTop      *ObfCard         `json:"discardTop,omitempty"` // Top card of discard pile (always known).
	Players         []ObfPlayerState `json:"players"`
	CambiaCalled    bool             `json:"cambiaCalled"`
	CambiaCallerID  uuid.UUID        `json:"cambiaCallerId,omitempty"` // Using camelCase.
	HouseRules      HouseRules       `json:"houseRules"`               // Include house rules for client display/logic.
}

// GetCurrentObfuscatedGameState generates a snapshot of the game state,
// tailored to the perspective of the requesting user (`forUser`).
// This function assumes the game lock is HELD by the caller.
func (g *CambiaGame) GetCurrentObfuscatedGameState(forUser uuid.UUID) ObfGameState {
	// Caller must hold the lock.

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
		HouseRules:     g.HouseRules,
	}

	// Determine current player ID safely.
	if len(g.Players) > 0 && g.CurrentPlayerIndex >= 0 && g.CurrentPlayerIndex < len(g.Players) {
		obf.CurrentPlayerID = g.Players[g.CurrentPlayerIndex].ID
	} else {
		obf.CurrentPlayerID = uuid.Nil // Indicate no current player if state is inconsistent.
	}

	// Set discard top card (always public knowledge).
	if len(g.DiscardPile) > 0 {
		top := g.DiscardPile[len(g.DiscardPile)-1]
		obf.DiscardTop = &ObfCard{
			ID:    top.ID,
			Known: true,
			Rank:  top.Rank,
			Suit:  top.Suit,
			Value: top.Value,
		}
	}

	// Prepare player states.
	obf.Players = make([]ObfPlayerState, len(g.Players))
	for i, pl := range g.Players {
		isSelf := pl.ID == forUser
		ps := ObfPlayerState{
			PlayerID:        pl.ID,
			Username:        pl.User.Username, // Include username.
			HandSize:        len(pl.Hand),
			HasCalledCambia: pl.HasCalledCambia,
			Connected:       pl.Connected,
			IsCurrentTurn:   (pl.ID == obf.CurrentPlayerID && g.Started && !g.GameOver),
		}

		// Reveal details only for 'self'.
		if isSelf {
			ps.RevealedHand = make([]ObfCard, len(pl.Hand))
			for j, c := range pl.Hand {
				idx := j // Capture index for the pointer.
				ps.RevealedHand[j] = ObfCard{
					ID:    c.ID,
					Known: true,
					Rank:  c.Rank,
					Suit:  c.Suit,
					Value: c.Value,
					Idx:   &idx,
				}
			}
			if pl.DrawnCard != nil {
				ps.DrawnCard = &ObfCard{
					ID:    pl.DrawnCard.ID,
					Known: true,
					Rank:  pl.DrawnCard.Rank,
					Suit:  pl.DrawnCard.Suit,
					Value: pl.DrawnCard.Value,
				}
			}
		}
		// No need for an 'else' block; non-self players only get the basic info.
		obf.Players[i] = ps
	}

	return obf
}
