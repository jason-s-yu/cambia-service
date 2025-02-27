// cmd/server/main.go
package main

import (
	"log"
	"net/http"
	"os"

	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/handlers"
	"github.com/jason-s-yu/cambia/internal/lobby"
	"github.com/jason-s-yu/cambia/internal/middleware"
	_ "github.com/joho/godotenv/autoload"
	"github.com/sirupsen/logrus"
)

func main() {
	auth.Init()
	database.ConnectDB()

	logger := logrus.New()
	logger.SetLevel(logrus.DebugLevel)

	mux := http.NewServeMux()

	// user endpoints
	mux.HandleFunc("/user/create", handlers.CreateUserHandler)
	mux.HandleFunc("/user/login", handlers.LoginHandler)

	// friend endpoints
	mux.HandleFunc("/friends/add", handlers.AddFriendHandler)
	mux.HandleFunc("/friends/accept", handlers.AcceptFriendHandler)
	mux.HandleFunc("/friends/list", handlers.ListFriendsHandler)
	mux.HandleFunc("/friends/remove", handlers.RemoveFriendHandler)

	// game websocket
	gameSrv := handlers.NewGameServer()
	mux.Handle("/game/ws/", middleware.LogMiddleware(logger)(http.HandlerFunc(
		handlers.GameWSHandler(logger, gameSrv),
	)))

	// lobby endpoints
	mux.HandleFunc("/lobby/create", handlers.CreateLobbyHandler)
	mux.HandleFunc("/lobby/join", handlers.JoinLobbyHandler)
	mux.HandleFunc("/lobby/list", handlers.ListLobbiesHandler)
	mux.HandleFunc("/lobby/get", handlers.GetLobbyHandler) // NB: uses query param

	// lobby socket facilitation
	lm := lobby.NewLobbyManager()
	mux.Handle("/lobby/ws/", middleware.LogMiddleware(logger)(http.HandlerFunc(
		handlers.LobbyWSHandler(logger, lm),
	)))

	addr := ":8080"
	if port := os.Getenv("PORT"); port != "" {
		addr = ":" + port
	}
	logger.Infof("Running on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("server exited: %v", err)
	}
}
