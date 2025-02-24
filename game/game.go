package game

import (
	"math/rand"
	"sync"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/models"
)

type Game struct {
	ID            uuid.UUID
	Players       []*models.Player
	Deck          map[uuid.UUID]*models.Card
	Stockpile     []*models.Card
	DiscardPile   []*models.Card
	CurrentPlayer int
	Started       bool
	Mutex         sync.Mutex
}

// NewGame creates a new game instance and initializes its game state.
func NewGame() *Game {
	id, err := uuid.NewV7()
	if err != nil {
		panic(err)
	}
	game := &Game{
		ID:            id,
		Players:       []*models.Player{},
		Deck:          make(map[uuid.UUID]*models.Card),
		Mutex:         sync.Mutex{},
		CurrentPlayer: -1,
		Started:       false,
	}
	game.initializeDeck()
	return game
}

func (g *Game) Start() {
	g.Started = true

	// deal cards to all players
	for range 4 {
		for _, player := range g.Players {
			card := g.DrawFromStockpile()
			player.Hand = append(player.Hand, card)
		}
	}

	for _, player := range g.Players {
		g.SendInitialHand(player)
	}

	// pick a random player to start
	g.CurrentPlayer = rand.Intn(len(g.Players))
}

// InitializeDeck creates and shuffles the game deck and sets it to the stockpile.
func (g *Game) initializeDeck() {
	suits := []string{"Hearts", "Diamonds", "Clubs", "Spades"}
	ranks := []string{"A", "2", "3", "4", "5", "6", "7", "8", "9", "10", "J", "Q", "K"}
	values := map[string]int{
		"A": 1, "2": 2, "3": 3, "4": 4,
		"5": 5, "6": 6, "7": 7, "8": 8,
		"9": 9, "10": 10, "J": 11, "Q": 12,
		"K": 13,
	}

	var deck []*models.Card
	for _, suit := range suits {
		for _, rank := range ranks {
			value := values[rank]

			// Red Kings have a value of -1
			if rank == "K" && (suit == "Hearts" || suit == "Diamonds") {
				value = -1
			}

			card := &models.Card{
				ID: func() uuid.UUID {
					id, err := uuid.NewV7() // cards are initialized with a random UUID for server-side obfuscation
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
	deck = append(deck, &models.Card{Suit: "Joker", Rank: "Joker", Value: 0})
	deck = append(deck, &models.Card{Suit: "Joker", Rank: "Joker", Value: 0})

	// Shuffle the deck
	// rand.NewSource(time.Now().UnixNano())
	rand.Shuffle(len(deck), func(i, j int) {
		deck[i], deck[j] = deck[j], deck[i]
	})

	g.Stockpile = copyDeck(deck)
	g.DiscardPile = []*models.Card{}
}

// DrawFromDeck draws a card from the stockpile
func (g *Game) DrawFromStockpile() *models.Card {
	if len(g.Stockpile) == 0 {
		// Reshuffle discard pile into deck
		g.Stockpile = g.DiscardPile
		g.DiscardPile = []*models.Card{}
		rand.Shuffle(len(g.Stockpile), func(i, j int) {
			g.Stockpile[i], g.Stockpile[j] = g.Stockpile[j], g.Stockpile[i]
		})
	}

	card := g.Stockpile[0]
	g.Stockpile = g.Stockpile[1:]
	return card
}

// DiscardCard adds the card to the discard pile
func (g *Game) DiscardCard(card *models.Card) {
	g.DiscardPile = append(g.DiscardPile, card)
}

func (g *Game) GetCardFromID(id uuid.UUID) *models.Card {
	return g.Deck[id]
}
