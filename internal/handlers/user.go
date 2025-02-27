package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/models"
)

// If user arrives without a token, create ephemeral user
func EnsureEphemeralUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, error) {
	cookieHeader := r.Header.Get("Cookie")
	var token string
	if strings.Contains(cookieHeader, "auth_token=") { // ensure the user has a token
		token = extractTokenFromCookie(cookieHeader)
	} else {
		// create the temp user
		ephemeralUser := models.User{
			Email:       "",
			Password:    "",
			Username:    "Guest",
			IsEphemeral: true,
		}
		if err := database.CreateUser(context.Background(), &ephemeralUser); err != nil {
			return uuid.Nil, fmt.Errorf("failed to create ephemeral user: %w", err)
		}
		newToken, err := auth.CreateJWT(ephemeralUser.ID.String())
		if err != nil {
			return uuid.Nil, fmt.Errorf("failed to create ephemeral JWT: %w", err)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "auth_token",
			Value:    newToken,
			HttpOnly: true,
			Path:     "/",
		})
		return ephemeralUser.ID, nil
	}

	userID, err := auth.AuthenticateJWT(token)
	if err != nil {
		ephemeralUser := models.User{
			Email:       "",
			Password:    "",
			Username:    "Guest",
			IsEphemeral: true,
		}
		if createErr := database.CreateUser(context.Background(), &ephemeralUser); createErr != nil {
			return uuid.Nil, fmt.Errorf("failed to create ephemeral user: %w", createErr)
		}
		newToken, _ := auth.CreateJWT(ephemeralUser.ID.String())
		http.SetCookie(w, &http.Cookie{
			Name:     "auth_token",
			Value:    newToken,
			HttpOnly: true,
			Path:     "/",
		})
		return ephemeralUser.ID, nil
	}

	uuidVal, parseErr := uuid.Parse(userID)
	if parseErr != nil {
		return uuid.Nil, fmt.Errorf("invalid user ID in token: %w", parseErr)
	}
	return uuidVal, nil
}

// Extend ephemeral claim logic to handle optional username changes
type claimEphemeralRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Username string `json:"username"`
}

func ClaimEphemeralHandler(w http.ResponseWriter, r *http.Request) {
	token := extractTokenFromCookie(r.Header.Get("Cookie"))
	userIDStr, err := auth.AuthenticateJWT(token)
	if err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "invalid user id in token", http.StatusForbidden)
		return
	}

	u, err := database.GetUserByID(r.Context(), userID)
	if err != nil {
		http.Error(w, "user not found", http.StatusNotFound)
		return
	}
	if !u.IsEphemeral {
		http.Error(w, "user is not ephemeral", http.StatusBadRequest)
		return
	}

	var req claimEphemeralRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid claim payload", http.StatusBadRequest)
		return
	}

	u.Email = req.Email
	u.Password = req.Password
	if req.Username != "" {
		u.Username = req.Username
	}
	u.IsEphemeral = false

	err = database.UpdateUserCredentials(r.Context(), u)
	if err != nil {
		http.Error(w, "failed to finalize ephemeral user", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "ephemeral user claimed successfully")
}

// CreateUserHandler ensures that if the user is ephemeral, they can't recreate
func CreateUserHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	user := models.User{
		Email:       req.Email,
		Password:    req.Password,
		Username:    req.Username,
		IsEphemeral: false,
		IsAdmin:     false,
	}

	ctx := r.Context()
	err := database.CreateUser(ctx, &user)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) {
			if pgErr.Code == "23505" {
				http.Error(w, "email already exists", http.StatusConflict)
				return
			}
		}
		http.Error(w, "error creating user", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type loginResponse struct {
	Token string `json:"token"`
}

// LoginHandler handles user login requests. It expects a JSON payload with email and password,
// and returns a JSON response with an authentication token if the login is successful.
// If the login fails, it returns an appropriate HTTP error status and message.
//
// Request payload:
//
//	{
//	  "email": "someone@example.com",
//	  "password": "password"
//	}
//
// Response payload:
//
//	{
//	  "token": "{jwt}"
//	}
//
// The token is also sent via the Cookie header.
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request payload", http.StatusBadRequest)
		return
	}

	token, err := database.AuthenticateUser(context.Background(), req.Email, req.Password)
	if err != nil {
		log.Printf("failed to authenticate user: %v", err)
		http.Error(w, "authentication failed", http.StatusForbidden)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		HttpOnly: true,
		Path:     "/",
		MaxAge:   auth.TOKEN_EXPIRE_TIME_SEC,
	})

	resp := loginResponse{Token: token}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		http.Error(w, "failed to write response", http.StatusInternalServerError)
		return
	}
}
