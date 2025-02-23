package game

import (
	"math/rand"
	"sync"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/models"
)

type Game struct {
	Players       map[string]*models.Player
	Stockpile     []models.Card
	DiscardPile   []models.Card
	CurrentPlayer string
	Started       bool
	Mutex         sync.Mutex
}

var Instance = Game{
	Players: make(map[string]*models.Player),
}

// InitializeDeck creates and shuffles the game deck and sets it to the stockpile.
func (g *Game) InitializeDeck() {
	suits := []string{"Hearts", "Diamonds", "Clubs", "Spades"}
	ranks := []string{"A", "2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K"}
	values := map[string]int{
		"A": 1, "2": 2, "3": 3, "4": 4,
		"5": 5, "6": 6, "7": 7, "8": 8,
		"9": 9, "10": 10, "J": 11, "Q": 12,
		"K": 13,
	}

	var deck []models.Card
	for _, suit := range suits {
		for _, rank := range ranks {
			value := values[rank]

			// Red Kings have a value of -1
			if rank == "K" && (suit == "Hearts" || suit == "Diamonds") {
				value = -1
			}

			card := models.Card{
				ID: func() uuid.UUID {
					id, err := uuid.NewV7()
					if err != nil {
						panic(err)
					}
					return id
				}(),
				Suit:  suit,
				Rank:  rank,
				Value: value,
			}
			deck = append(deck, card)
		}
	}

	// Add Jokers
	deck = append(deck, models.Card{Suit: "Joker", Rank: "Joker", Value: 0})
	deck = append(deck, models.Card{Suit: "Joker", Rank: "Joker", Value: 0})

	// Shuffle the deck
	// rand.NewSource(time.Now().UnixNano())
	rand.Shuffle(len(deck), func(i, j int) {
		deck[i], deck[j] = deck[j], deck[i]
	})

	g.Stockpile = deck
	g.DiscardPile = []models.Card{}
}

// DrawFromDeck draws a card from the stockpile
func (g *Game) DrawFromStockpile() models.Card {
	if len(g.Stockpile) == 0 {
		// Reshuffle discard pile into deck
		g.Stockpile = g.DiscardPile
		g.DiscardPile = []models.Card{}
		rand.Shuffle(len(g.Stockpile), func(i, j int) {
			g.Stockpile[i], g.Stockpile[j] = g.Stockpile[j], g.Stockpile[i]
		})
	}

	card := g.Stockpile[0]
	g.Stockpile = g.Stockpile[1:]
	return card
}
