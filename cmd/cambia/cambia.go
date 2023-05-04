package main

import (
	"os"
	"strings"

	_ "github.com/joho/godotenv/autoload"

	"log"
	"net/http"

	"github.com/fatih/color"
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

	if os.Getenv("CAMBIA_ENV") != "production" {
		log.Println(color.GreenString("INFO"), "Environment variables loaded:\n\t", strings.Join(os.Environ(), "\n\t"))
	}

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

	var addr string
	if os.Getenv("CAMBIA_ENV") == "production" {
		// bind to all hosts in production mode
		addr = ":8080"
	} else {
		// otherwise bind to localhost
		addr = "localhost:8080"
	}

	log.Println(color.GreenString("INFO"), "Cambia server listening on", color.CyanString(addr))
	log.Fatal(http.ListenAndServe(addr, r))
}
