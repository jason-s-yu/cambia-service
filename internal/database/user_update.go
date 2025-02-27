package database

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/models"
)

// UpdateUserCredentials updates a user's email/password and ephemeral flag in DB
func UpdateUserCredentials(ctx context.Context, u *models.User) error {
	hashed, err := auth.CreateHash(u.Password, auth.Params)
	if err != nil {
		return err
	}

	q := `UPDATE users SET email = $1, password = $2, is_ephemeral = $3 WHERE id = $4`
	err = pgx.BeginTxFunc(ctx, DB, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, q, u.Email, hashed, u.IsEphemeral, u.ID)
		return e
	})
	if err != nil {
		return fmt.Errorf("failed to update user credentials: %w", err)
	}
	return nil
}
