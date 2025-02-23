package main

import (
	"log"
	"net/http"

	"github.com/jason-s-yu/cambia/handlers"
)

func main() {
	s := handlers.NewGameServer(log.Printf)

	http.HandleFunc("/", handlers.PingHandler)
	http.HandleFunc("/subscribe", s.WsHandler)

	log.Println("Server started on :8080")
	err := http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal("ListenAndServe: ", err)
	}
}
