package game

import (
	"github.com/jason-s-yu/cambia/models"
)

// DealCards deals 4 cards to each player
func (g *Game) DealCards() {
	for _, player := range g.Players {
		player.Hand = []models.Card{}
		player.Revealed = []bool{false, false, false, false}
		for i := 0; i < 4; i++ {
			card := g.DrawFromStockpile()
			player.Hand = append(player.Hand, card)
		}

		// Reveal the two cards nearest to the player (first two cards)
		player.Revealed[0] = true
		player.Revealed[1] = true

		// Send initial hand to the player using the utility function
		g.SendInitialHand(player)
	}
}
