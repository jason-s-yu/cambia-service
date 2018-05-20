package api

import (
	"net/http"
)

// GameState is the struct which represents a live game instance/state with all players and cards
type GameState struct {
	// exported to json:
	Players []string `json:"players"`
	Decks   []Deck   `json:"decks"`
}
	
func newGameState() GameState {
	gs := GameState{}
	return gs
}

// JoinGame
func JoinGame(writer http.ResponseWriter, request *http.Request) {

}
