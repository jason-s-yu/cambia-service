package api

import (
	"net/http"
)

// GameState is the struct which represents a live game instance/state with all players and cards
type GameState struct {
	// exported to json:
	Players []string        `json:"players"`
	Decks   map[string]Deck `json:"decks"`
}

func newGameState() GameState {
	gs := GameState{
		Players: [],
		Decks: { }
	}
	return gs
}

// JoinGame
func JoinGame(writer http.ResponseWriter, request *http.Request) {

}
