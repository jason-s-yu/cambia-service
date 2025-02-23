package game

import (
	"context"
	"encoding/json"

	"github.com/coder/websocket"
	"github.com/jason-s-yu/cambia/models"
)

// SendInitialHand sends the initial hand to the player
func (g *Game) SendInitialHand(player *models.Player) {
	// Create a masked hand to send to the player
	maskedHand := []models.Card{}
	for i, card := range player.Hand {
		if player.Revealed[i] {
			maskedHand = append(maskedHand, card)
		} else {
			maskedHand = append(maskedHand, models.Card{
				Suit:  "",
				Rank:  "Face-down",
				Value: 0,
			})
		}
	}

	// Marshal the JSON payload
	payload := map[string]interface{}{
		"type": "initial_hand",
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
func (g *Game) NotifyPlayer(playerID string, message map[string]interface{}) {
	player, exists := g.Players[playerID]
	if !exists {
		return
	}

	data, err := json.Marshal(message)
	if err != nil {
		return
	}

	ctx := context.Background()
	player.Conn.Write(ctx, websocket.MessageText, data)
}
