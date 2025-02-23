package game

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/models"
)

// copyDeck creates a deep copy of the deck slice, but retains the original pointers
// Cards and their pointers are not deep copied.
func copyDeck(deck []*models.Card) []*models.Card {
	newDeck := make([]*models.Card, len(deck))
	copy(newDeck, deck)
	return newDeck
}

// SendInitialHand sends the initial hand to the player
func (g *Game) SendInitialHand(player *models.Player) {
	// Create a masked hand to send to the player
	// i.e. only the first two hands are revealed in full
	maskedHand := []models.Card{}
	for i, card := range player.Hand {
		if i == 1 || i == 2 {
			maskedHand = append(maskedHand, *card)
		} else {
			maskedHand = append(maskedHand, models.Card{
				ID:    card.ID,
				Suit:  "",
				Rank:  "Face-down",
				Value: 0,
			})
		}
	}

	payload := map[string]interface{}{
		"type": "deal",
		"hand": maskedHand,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}

	// Write message using coder/websocket
	ctx := context.Background()
	player.Conn.Write(ctx, websocket.MessageText, data)
}

// NotifyAllPlayers sends a message to all connected players
func (g *Game) NotifyAllPlayers(message map[string]interface{}) {
	data, err := json.Marshal(message)
	if err != nil {
		return
	}

	// Broadcast to each connected player
	for _, player := range g.Players {
		ctx := context.Background()
		player.Conn.Write(ctx, websocket.MessageText, data)
	}
}

// NotifyPlayer sends a message to a specific player
func (g *Game) NotifyPlayerByID(playerID uuid.UUID, message map[string]interface{}) error {
	var player *models.Player
	for _, p := range g.Players {
		if p.ID == playerID {
			player = p
		}
	}
	if player == nil {
		return fmt.Errorf("player not found")
	}

	data, err := json.Marshal(message)
	if err != nil {
		return err
	}

	ctx := context.Background()
	err = player.Conn.Write(ctx, websocket.MessageText, data)
	if err != nil {
		return err
	}

	return nil
}
