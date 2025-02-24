package handlers

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/auth"
	"github.com/jason-s-yu/cambia/database"
	"github.com/jason-s-yu/cambia/game"
	"github.com/jason-s-yu/cambia/models"
)

type GameServer struct {
	GameStore *game.GameStore
	Logf      func(f string, v ...interface{})
}

func NewGameServer() *GameServer {
	gs := &GameServer{
		GameStore: game.NewGameStore(),
		Logf:      log.Printf,
	}

	return gs
}

func (s GameServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/game/create" {
		s.CreateGameHandler(w, r)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"game"}})
	if err != nil {
		s.Logf("%v", err)
		return
	}

	if c.Subprotocol() != "game" {
		c.Close(websocket.StatusPolicyViolation, "client must speak the game subprotocol")
		return
	}

	// ensure a game ID is supplied in the path
	queryGameID := r.URL.Path[len("/game/"):]
	if queryGameID == "" {
		s.Logf("missing game_id")
		c.Close(websocket.StatusPolicyViolation, "missing game_id")
		return
	}

	// convert ID string to UUID
	gameID, err := uuid.Parse(queryGameID)
	if err != nil {
		s.Logf("invalid game_id: %v", err)
		c.Close(websocket.StatusPolicyViolation, "invalid uuid game_id")
		return
	}

	// check if the game exists
	game, ok := s.GameStore.GetGame(gameID)
	if !ok {
		s.Logf("game_id not found: %v", gameID)
		c.Close(websocket.StatusPolicyViolation, "game_id not found")
		return
	}

	player := &models.Player{
		ID:        gameID,
		Conn:      c,
		Connected: true,
	}

	// Store the player in the game instance
	game.Mutex.Lock()
	game.Players = append(game.Players, player)
	game.Mutex.Unlock()
}

func (s *GameServer) CreateGameHandler(w http.ResponseWriter, r *http.Request) {
	// extract cookie from session
	cookieHeader := r.Header.Get("Cookie")
	if cookieHeader == "" {
		http.Error(w, "missing cookie header", http.StatusBadRequest)
		return
	}

	// ensure proper token prefix is present
	if !strings.Contains(cookieHeader, "auth_token=") {
		http.Error(w, "missing auth_token in cookie", http.StatusBadRequest)
		return
	}

	// authenticate the user first
	token := strings.Split(cookieHeader, "auth_token=")[1]
	userID, err := auth.AuthenticateJWT(token)
	if err != nil {
		http.Error(w, "authentication failed", http.StatusForbidden)
	}

	uuid, err := uuid.Parse(userID)
	if err != nil {
		http.Error(w, "invalid user_id to uuid format", http.StatusBadRequest)
		return
	}

	user, err := database.GetUserByID(context.Background(), uuid)
	player, err := models.NewPlayer(user)

	g := game.NewGameWithPlayers([]*models.Player{player})

	w.Header().Set("Content-Type", "application/json")

	s.GameStore.AddGame(g)

	if err := json.NewEncoder(w).Encode(g); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
}
