// filepath: /home/jasonyu/dev/cambia/service/database/user.go
package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jason-s-yu/cambia/auth"
	"github.com/jason-s-yu/cambia/models"
)

func CreateUser(ctx context.Context, user *models.User) error {
	if user.ID != uuid.Nil {
		return fmt.Errorf("user ID must be nil")
	}

	// init ID, hash pw
	id, err := uuid.NewV7()
	if err != nil {
		return fmt.Errorf("failed to generate UUID: %w", err)
	}
	user.ID = id
	user.Password, err = auth.CreateHash(user.Password, auth.Params)

	if err != nil {
		return fmt.Errorf("failed to hash password: %w", err)
	}

	// begin tx
	query := `INSERT INTO users (id, email, password, username) VALUES ($1, $2, $3, $4)`
	err = pgx.BeginTxFunc(context.Background(), DB, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(context.Background(), query, user.ID, user.Email, user.Password, user.Username)
		return err
	})

	if err != nil {
		return fmt.Errorf("failed to create user: %w", err)
	}
	return nil
}

// GetUserByEmail retrieves a user from the database by email
func GetUserByEmail(ctx context.Context, email string) (*models.User, error) {
	var user models.User
	query := `SELECT id, email, password, username FROM public.users WHERE email = $1`
	err := DB.QueryRow(ctx, query, email).Scan(&user.ID, &user.Email, &user.Password, &user.Username)
	if err != nil {
		return &user, fmt.Errorf("failed to get user by email: %w", err)
	}
	return &user, nil
}

// GetUserByID retrieves a user from the database by ID
func GetUserByID(ctx context.Context, id uuid.UUID) (*models.User, error) {
	var user models.User
	query := `SELECT id, email, password, username FROM public.users WHERE id = $1`
	err := DB.QueryRow(ctx, query, id).Scan(&user.ID, &user.Email, &user.Password, &user.Username)
	if err != nil {
		return &user, fmt.Errorf("failed to get user by ID: %w", err)
	}
	return &user, nil
}

func AuthenticateUser(ctx context.Context, email, password string) (string, error) {
	user, err := GetUserByEmail(ctx, email)
	if err != nil {
		return "", err
	}

	match, err := auth.ComparePasswordAndHash(password, user.Password)
	if err != nil {
		return "", err
	}

	if !match {
		return "", fmt.Errorf("forbidden: invalid password")
	}

	jwt, err := auth.CreateJWT(user.ID.String())
	if err != nil {
		return "", fmt.Errorf("failed to generate JWT: %w", err)
	}

	return jwt, nil
}
