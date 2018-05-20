package main

import (
	"net/http"

	"github.com/gorilla/mux"

	"cambia-server/api"
	"github.com/gorilla/websocket"
	"log"
	"fmt"
	"encoding/json"
	"github.com/satori/go.uuid"
)

func main() {
	// Initialize the router
	router := mux.NewRouter()

	// Objectify the mgo dialing information. We will use this to connect to the database
	//mgoInfo := &mgo.DialInfo{
	//	Addrs:    []string{constants.DBHosts},
	//	Timeout:  60 * time.Second,
	//	Database: constants.DBName,
	//	Username: constants.DBUserName,
	//	Password: constants.DBPassword,
	//}

	// Initialize the mgo driver with the above information
	// session, err := mgo.DialWithInfo(mgoInfo)
	// session.SetMode(mgo.Monotonic, true)

	// Panic and log err if there is an error
	//if err != nil {
	//	panic(err)
	//}
	//
	//// Initialize the database
	//err = database.Init(session)
	//
	//// Close the mgo session once the program has concluded
	//defer session.Close()

	// Router endpoints and API
	serveEndpoints(router)
	fmt.Println("Serving on port 8080")
	if err := http.ListenAndServe(":8080", router); err != nil {
		log.Fatal(err)
	}

}

func serveEndpoints(router *mux.Router) {
	router.HandleFunc("/", defaultPage)
	router.HandleFunc("joingame", joinGame).Methods("POST")		// join a game -> redir to game
	router.HandleFunc("/game", handleConnections)							// this defaults to a GET method - we will change this in the function handleConnections

	go handleMessages()
}

func defaultPage(writer http.ResponseWriter, request *http.Request) {
	fmt.Fprintf(writer, "Welcome")
}

// joinGame is the end point for joining an existing game.
// This returns response 200 (StatusOK) if successful
func joinGame(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()

	var player api.Player
	if err := json.NewDecoder(request.Body).Decode(&player); err != nil {
		respondWithError(writer, http.StatusBadRequest, "Invalid username or player information")
		return
	}
	var err error
	player.UUID = uuid.Must(uuid.NewV4(), err)

	if err != nil {
		fmt.Printf("Something went wrong: %s", err)
	}

	// Database handling here

	respondWithJson(writer, http.StatusOK, player)
	// response 200 -> redirect player to /game
}

var upgrader = websocket.Upgrader{}				// This is the websocket upgrader
var clients = make(map[*websocket.Conn] bool)		// This is a collection of connected clients
var broadcastChannel = make(chan api.GameState)		// the broadcast channel containing the game state (diffs)

// handleConnections is the websocket bridge handler
// The websocket is initially opened here
func handleConnections(writer http.ResponseWriter, request *http.Request) {
	ws, err := upgrader.Upgrade(writer, request, nil)

	if err != nil {
		log.Fatal("Error in handle connections: ", err)
	}

	// Register a new client
	clients[ws] = true

	// The following loop listens to game changes from clients, processes the JSON and spits it back into the broadcast channel
	for {
		var state api.GameState

		// read JSON
		err := ws.ReadJSON(&state)
		if err != nil {
			log.Printf("err: %v", err)
			delete(clients, ws)
			break
		}

		// If no errors, we proceed to send the new state to the broadcast channel
		broadcastChannel <- state
	}
}

type Command int
const (
	DRAW Command = 0
	PLAY Command = 1
)

// handleMessages is a listener that continuously reads from the broadcast channel
// If a change is received, the function will take the diff and relay it to all the clients
func handleMessages() {
	for {
		// receive diff or command
		diff := <- broadcastChannel

		// send diff to every client
		for client := range clients {
			err := client.WriteJSON(&diff)

			// if an error is found, close the connection and remove it from the clients map
			if err != nil {
				log.Printf("err: %v", err)
				client.Close()
				delete(clients, client)
			}
		}
	}
}

func respondWithError(w http.ResponseWriter, code int, msg string) {
	respondWithJson(w, code, map[string]string{"error": msg})
}

func respondWithJson(w http.ResponseWriter, code int, payload interface{}) {
	response, _ := json.Marshal(payload)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	w.Write(response)
}
