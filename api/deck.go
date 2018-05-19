package api

import "net/http"

type Deck struct {
}

// GetDecks returns a nested json structure of all the user decks in the game
func GetDecks(writer http.ResponseWriter, request *http.Request) {

}

// GetCards returns the current hand of a specified user
// /api/getCards/{:id}
func GetCards(writer http.ResponseWriter, request *http.Request) {

}

// DrawCard will draw cards for a specified user
func DrawCard(writer http.ResponseWriter, request *http.Request) {

}
