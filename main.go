package main

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	_ "github.com/joho/godotenv/autoload"

	"github.com/jason-s-yu/cambia/auth"
	"github.com/jason-s-yu/cambia/database"
	"github.com/jason-s-yu/cambia/handlers"
)

var flags Flags

type Flags struct {
	verbose bool
}

func main() {
	// Parse command line flags
	for _, arg := range os.Args[1:] {
		if arg == "-v" {
			flags.verbose = true
		}
	}

	if flags.verbose {
		log.Println("Verbose mode enabled")
	}

	// init db connection
	database.ConnectDB()
	defer database.DB.Close()

	// init auth keys
	auth.Init()

	// init routers
	mux := http.NewServeMux()
	mux.HandleFunc("/", handlers.PingHandler)

	mux.HandleFunc("/user/create", handlers.CreateUserHandler)
	mux.HandleFunc("/user/login", handlers.LoginHandler)

	mux.Handle("/game", handlers.NewGameServer())
	mux.Handle("/game/", handlers.NewGameServer())

	server := &http.Server{
		Handler:      mux,
		ReadTimeout:  time.Second * 10,
		WriteTimeout: time.Second * 10,
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%s", os.Getenv("CAMBIA_SERVICE_PORT")))
	if err != nil {
		log.Fatalf("failed to listen: %v", err)
	}

	log.Printf("listening on %s", l.Addr())

	errc := make(chan error, 1)
	go func() {
		errc <- server.Serve(l)
	}()

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt)
	select {
	case err := <-errc:
		log.Printf("failed to serve: %v", err)
	case sig := <-sigs:
		log.Printf("terminating: %v", sig)
	}
}
