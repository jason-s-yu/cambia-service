package models

import (
	"sync"

	"github.com/google/uuid"
)

type Game struct {
	ID            uuid.UUID
	Players       []*Player
	Deck          map[uuid.UUID]*Card
	Stockpile     []*Card
	DiscardPile   []*Card
	CurrentPlayer int
	Started       bool
	Mutex         sync.Mutex
}
