// internal/game/sync_state.go
package game

import (
	"github.com/google/uuid"
)

// ObfCard holds minimal info for obfuscating face-down cards and optionally revealing
// rank/suit for cards that belong to the requesting player or are otherwise visible.
type ObfCard struct {
	ID    uuid.UUID `json:"id"`
	Known bool      `json:"known"`
	Rank  string    `json:"rank,omitempty"`
	Suit  string    `json:"suit,omitempty"`
	Value int       `json:"value,omitempty"`
	Idx   int       `json:"idx,omitempty"`
}

// ObfPlayerState represents the minimal state for one player from the perspective of a requesting user.
type ObfPlayerState struct {
	PlayerID        uuid.UUID `json:"player_id"`
	HandSize        int       `json:"hand_size"`
	HasCalledCambia bool      `json:"hasCalledCambia"`
	Connected       bool      `json:"connected"`
	IsCurrentTurn   bool      `json:"isCurrentTurn"`
	RevealedHand    []ObfCard `json:"revealedHand,omitempty"` // only for self if needed
	DrawnCard       *ObfCard  `json:"drawnCard,omitempty"`    // if this is the current user and they've drawn
}

// ObfGameState is returned by GetCurrentObfuscatedGameState.
type ObfGameState struct {
	GameID          uuid.UUID        `json:"game_id"`
	PreGameActive   bool             `json:"preGameActive"`
	Started         bool             `json:"started"`
	GameOver        bool             `json:"gameOver"`
	CurrentPlayerID uuid.UUID        `json:"currentPlayerId"`
	StockpileSize   int              `json:"stockpileSize"`
	DiscardSize     int              `json:"discardSize"`
	DiscardTop      *ObfCard         `json:"discardTop,omitempty"`
	Players         []ObfPlayerState `json:"players"`
	CambiaCalled    bool             `json:"cambiaCalled"`
	CambiaCallerID  uuid.UUID        `json:"cambiaCallerId,omitempty"`
}

// GetCurrentObfuscatedGameState generates a snapshot of the game for the requesting user.
func (g *CambiaGame) GetCurrentObfuscatedGameState(forUser uuid.UUID) ObfGameState {
	g.Mu.Lock()
	defer g.Mu.Unlock()

	obf := ObfGameState{
		GameID:          g.ID,
		PreGameActive:   g.PreGameActive,
		Started:         g.Started,
		GameOver:        g.GameOver,
		CambiaCalled:    g.CambiaCalled,
		CambiaCallerID:  g.CambiaCallerID,
		CurrentPlayerID: g.Players[g.CurrentPlayerIndex].ID,
		StockpileSize:   len(g.Deck),
		DiscardSize:     len(g.DiscardPile),
	}

	// If we have at least 1 discard, we might show the top card depending on house rule.
	var discardTop *ObfCard
	if len(g.DiscardPile) > 0 {
		top := g.DiscardPile[len(g.DiscardPile)-1]
		// TODO: House rule: if discard is face-up, reveal it. Otherwise only show the ID.
		discardTop = &ObfCard{
			ID:    top.ID,
			Known: true,
			Rank:  top.Rank,
			Suit:  top.Suit,
			Value: top.Value,
		}
	}
	obf.DiscardTop = discardTop

	for i, pl := range g.Players {
		ps := ObfPlayerState{
			PlayerID:        pl.ID,
			HandSize:        len(pl.Hand),
			HasCalledCambia: pl.HasCalledCambia,
			Connected:       pl.Connected,
			IsCurrentTurn:   (i == g.CurrentPlayerIndex),
		}
		// If I'm looking at my own state, reveal known cards
		if pl.ID == forUser {
			ps.RevealedHand = make([]ObfCard, len(pl.Hand))
			for j, c := range pl.Hand {
				ps.RevealedHand[j] = ObfCard{
					ID:    c.ID,
					Known: true,
					Rank:  c.Rank,
					Suit:  c.Suit,
					Value: c.Value,
					Idx:   j,
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
		obf.Players = append(obf.Players, ps)
	}

	return obf
}
