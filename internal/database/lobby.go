package database

import (
	"context"
	"math/rand"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jason-s-yu/cambia/internal/models"
)

// InsertLobby creates a new lobby row in the DB.
func InsertLobby(ctx context.Context, lobby *models.Lobby) error {
	q := `
	INSERT INTO lobbies (
		id, host_user_id, type, circuit_mode,
		ranked, ranking_mode,
		house_rule_freeze_disconnect,
		house_rule_forfeit_disconnect,
		house_rule_missed_round_threshold,
		penalty_card_count,
		allow_replaced_discard_abilities,
		disconnection_threshold,
		turn_timeout_sec,
		auto_start,
		allow_draw_from_discard_pile
	)
	VALUES ($1, $2, $3, $4,
	        $5, $6,
	        $7, $8, $9,
	        $10, $11,
	        $12, $13, $14,
	        $15)
	`
	return pgx.BeginTxFunc(ctx, DB, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q,
			lobby.ID,
			lobby.HostUserID,
			lobby.Type,
			lobby.CircuitMode,
			lobby.Ranked,
			lobby.RankingMode,
			lobby.HouseRules.FreezeOnDisconnect,
			lobby.HouseRules.ForfeitOnDisconnect,
			lobby.HouseRules.MissedRoundThreshold,
			lobby.HouseRules.PenaltyCardCount,
			lobby.HouseRules.AllowDiscardAbilities,
			lobby.HouseRules.DisconnectionRoundLimit,
			lobby.HouseRules.TurnTimeoutSec,
			lobby.HouseRules.AutoStart,
			lobby.HouseRules.AllowDrawFromDiscardPile,
		)
		return err
	})
}

// GetLobby fetches a lobby by ID
func GetLobby(ctx context.Context, lobbyID uuid.UUID) (*models.Lobby, error) {
	var l models.Lobby
	q := `
	SELECT 
		id, host_user_id, type, circuit_mode,
		ranked, ranking_mode,
		house_rule_freeze_disconnect,
		house_rule_forfeit_disconnect,
		house_rule_missed_round_threshold,
		penalty_card_count,
		allow_replaced_discard_abilities,
		disconnection_threshold,
		turn_timeout_sec,
		auto_start,
		allow_draw_from_discard_pile
	FROM lobbies
	WHERE id = $1
	`
	err := DB.QueryRow(ctx, q, lobbyID).Scan(
		&l.ID,
		&l.HostUserID,
		&l.Type,
		&l.CircuitMode,
		&l.Ranked,
		&l.RankingMode,
		&l.HouseRules.FreezeOnDisconnect,
		&l.HouseRules.ForfeitOnDisconnect,
		&l.HouseRules.MissedRoundThreshold,
		&l.HouseRules.PenaltyCardCount,
		&l.HouseRules.AllowDiscardAbilities,
		&l.HouseRules.DisconnectionRoundLimit,
		&l.HouseRules.TurnTimeoutSec,
		&l.HouseRules.AutoStart,
		&l.HouseRules.AllowDrawFromDiscardPile,
	)
	if err != nil {
		return nil, err
	}
	return &l, nil
}

// GetAllLobbies returns a slice of all lobbies in the DB.
func GetAllLobbies(ctx context.Context) ([]models.Lobby, error) {
	q := `
		SELECT 
			id, host_user_id, type, circuit_mode,
			ranked, ranking_mode,
			house_rule_freeze_disconnect,
			house_rule_forfeit_disconnect,
			house_rule_missed_round_threshold,
			penalty_card_count,
			allow_replaced_discard_abilities,
			disconnection_threshold,
			turn_timeout_sec,
			auto_start,
			allow_draw_from_discard_pile
		FROM lobbies
	`
	rows, err := DB.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lobbies []models.Lobby
	for rows.Next() {
		var l models.Lobby
		err := rows.Scan(
			&l.ID,
			&l.HostUserID,
			&l.Type,
			&l.CircuitMode,
			&l.Ranked,
			&l.RankingMode,
			&l.HouseRules.FreezeOnDisconnect,
			&l.HouseRules.ForfeitOnDisconnect,
			&l.HouseRules.MissedRoundThreshold,
			&l.HouseRules.PenaltyCardCount,
			&l.HouseRules.AllowDiscardAbilities,
			&l.HouseRules.DisconnectionRoundLimit,
			&l.HouseRules.TurnTimeoutSec,
			&l.HouseRules.AutoStart,
			&l.HouseRules.AllowDrawFromDiscardPile,
		)
		if err != nil {
			return nil, err
		}
		lobbies = append(lobbies, l)
	}
	return lobbies, nil
}

// InsertParticipant inserts a user into lobby_participants with a seat position.
// If seat_position is 0, we treat that as "not assigned" and assign randomly.
func InsertParticipant(ctx context.Context, lobbyID, userID uuid.UUID, seatPos int) error {
	// If seatPos == 0, we randomize. In a real system, you'd want to see how many seats are open
	if seatPos == 0 {
		seatPos = rand.Intn(9999) + 1 // naive seat assignment
	}

	q := `
	INSERT INTO lobby_participants (lobby_id, user_id, is_ready, seat_position)
	VALUES ($1, $2, false, $3)
	`
	return pgx.BeginTxFunc(ctx, DB, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, lobbyID, userID, seatPos)
		return err
	})
}

// IsUserInLobby checks if the user is already in the lobby
func IsUserInLobby(ctx context.Context, lobbyID, userID uuid.UUID) (bool, error) {
	q := `
	SELECT 1 
	  FROM lobby_participants
	  WHERE lobby_id = $1 AND user_id = $2
	  LIMIT 1
	`
	var tmp int
	err := DB.QueryRow(ctx, q, lobbyID, userID).Scan(&tmp)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// RemoveUserFromLobby removes a user from the lobby_participants table.
func RemoveUserFromLobby(ctx context.Context, userID uuid.UUID, lobbyID uuid.UUID) error {
	q := `DELETE FROM lobby_participants WHERE lobby_id=$1 AND user_id=$2`
	return pgx.BeginTxFunc(ctx, DB, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, q, lobbyID, userID)
		return err
	})
}

// DeleteLobby removes a lobby row from the DB by ID. We also remove participants.
func DeleteLobby(ctx context.Context, lobbyID uuid.UUID) error {
	q := `DELETE FROM lobbies WHERE id=$1`
	return pgx.BeginTxFunc(ctx, DB, pgx.TxOptions{}, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `DELETE FROM lobby_participants WHERE lobby_id=$1`, lobbyID)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, q, lobbyID)
		return err
	})
}
