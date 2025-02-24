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

	"github.com/jason-s-yu/cambia/database"
	"github.com/jason-s-yu/cambia/handlers"
)

func main() {
	database.ConnectDB()

	mux := http.NewServeMux()
	mux.HandleFunc("/", handlers.PingHandler)
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
