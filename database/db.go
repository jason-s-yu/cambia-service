package database

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	_ "github.com/joho/godotenv/autoload"
)

var DB *pgxpool.Pool

func ConnectDB() {
	var err error
	connStr := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		os.Getenv("POSTGRES_USER"),
		os.Getenv("POSTGRES_PASSWORD"),
		os.Getenv("PG_HOST"),
		os.Getenv("PG_PORT"),
		os.Getenv("PG_DATABASE"),
	)

	redactedConnStr := fmt.Sprintf("postgres://%s:%s@%s:%s/%s",
		os.Getenv("POSTGRES_USER"),
		"******",
		os.Getenv("PG_HOST"),
		os.Getenv("PG_PORT"),
		os.Getenv("PG_DATABASE"),
	)

	config, err := pgxpool.ParseConfig(connStr)

	if err != nil {
		log.Printf("unable to create pgx pool config: %v\n", err)
	}

	DB, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		log.Printf("unable to create connection pool: %v\n", err)
	}

	log.Printf("attempt connection to database %v\n", redactedConnStr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	err = DB.Ping(ctx)
	if err != nil {
		log.Printf("unable to establish connection to database %v: %v\n", redactedConnStr, err)
	} else {
		log.Printf("connected to database %v\n", redactedConnStr)
	}
}
