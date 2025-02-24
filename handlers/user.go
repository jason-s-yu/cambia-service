package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jason-s-yu/cambia/auth"
	"github.com/jason-s-yu/cambia/database"
	"github.com/jason-s-yu/cambia/models"
)

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

type createUserRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Username string `json:"username"`
}

// CreateUserHandler handles user creation requests. It expects a JSON payload with email, password,
// and username, and returns a JSON response with the created user details if the creation is successful.
// If a UUID (id) is specified in the request payload, it is discarded and a new UUID is generated for the user.
// If the user already exists, it returns an appropriate HTTP error status and message.
//
// Request payload:
//
//	{
//	  "email": "someone@example.com",
//	  "password": "password",
//	  "username": "username"
//	}
//
// Response payload:
//
//	{
//	  "id": "{uuid}",
//	  "email": "someone@example.com",
//	  "password": "password",
//	  "username": "username"
//	}
func CreateUserHandler(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request payload", http.StatusBadRequest)
		return
	}

	user := models.User{
		Email:    req.Email,
		Password: req.Password,
		Username: req.Username,
	}

	var errStr string
	var pgErr *pgconn.PgError
	if errors.As(database.CreateUser(context.Background(), &user), &pgErr) {
		if pgErr.Code == "23505" { // unique violation
			errStr = fmt.Sprintf("user with %v already exists", strings.Split(pgErr.ConstraintName, "_")[1])
		}
	}

	if errStr != "" {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": errStr})
	} else {
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(user)
	}
}
