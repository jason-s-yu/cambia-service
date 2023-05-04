package main

import (
	"os"
	"strings"

	_ "github.com/joho/godotenv/autoload"

	"log"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

// https://stackoverflow.com/questions/34312615/log-when-server-is-started

func main() {
	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Use(middleware.Heartbeat("/ping"))

	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: func() []string {
			// allow only origins specified in dotenv file if we are in production mode
			if os.Getenv("CAMBIA_ENV") == "production" {
				return strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",")
			} else {
				return []string{"https://*", "http://*"}
			}
		}(),
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type", "X-CSRF-Token"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300, // Maximum value not ignored by any of major browsers
	}))

	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	})

	log.Println("Listening")

	if os.Getenv("CAMBIA_ENV") == "production" {
		// bind to all hosts in production mode
		log.Fatal(http.ListenAndServe(":8080", r))
	} else {
		// otherwise bind to localhost
		log.Fatal(http.ListenAndServe("localhost:8080", r))
	}
}
