// internal/handlers/user.go
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

// EnsureEphemeralUser checks for an existing `auth_token` cookie.
// If valid, it authenticates the user and returns their UUID.
// If invalid or missing, it creates a new ephemeral guest user, sets a new `auth_token` cookie,
// and returns the new guest user's UUID.
func EnsureEphemeralUser(w http.ResponseWriter, r *http.Request) (uuid.UUID, error) {
	cookieHeader := r.Header.Get("Cookie")
	var token string

	// Helper function to create and set cookie for a new ephemeral user.
	createAndSetEphemeralUser := func() (uuid.UUID, error) {
		ephemeralUser := models.User{
			// ID will be generated by database.CreateUser.
			Email:       "", // Ephemeral users don't have email/password initially.
			Password:    "",
			Username:    "Guest", // Default guest username.
			IsEphemeral: true,
		}
		if err := database.CreateUser(context.Background(), &ephemeralUser); err != nil {
			return uuid.Nil, fmt.Errorf("failed to create ephemeral user: %w", err)
		}
		newToken, err := auth.CreateJWT(ephemeralUser.ID.String())
		if err != nil {
			// Attempt to clean up the created user if JWT creation fails? Complex.
			return uuid.Nil, fmt.Errorf("failed to create JWT for ephemeral user: %w", err)
		}
		http.SetCookie(w, &http.Cookie{
			Name:     "auth_token",
			Value:    newToken,
			HttpOnly: true,
			Path:     "/",
			MaxAge:   auth.TOKEN_EXPIRE_TIME_SEC, // Use configured expiry.
		})
		log.Printf("Created ephemeral user %s and set auth cookie.", ephemeralUser.ID)
		return ephemeralUser.ID, nil
	}

	// Check if the auth token exists in the cookie header.
	if strings.Contains(cookieHeader, "auth_token=") {
		token = extractCookieToken(cookieHeader, "auth_token")
	} else {
		// No token found, create a new ephemeral user.
		return createAndSetEphemeralUser()
	}

	// Token exists, try to authenticate.
	userIDStr, err := auth.AuthenticateJWT(token)
	if err != nil {
		// Token is invalid or expired, create a new ephemeral user.
		log.Printf("Invalid JWT token provided: %v. Creating new ephemeral user.", err)
		return createAndSetEphemeralUser()
	}

	// Token is valid, parse the user ID.
	uuidVal, parseErr := uuid.Parse(userIDStr)
	if parseErr != nil {
		// User ID in token is not a valid UUID, this indicates a problem.
		// Treat as invalid token scenario.
		log.Printf("Invalid user ID format in token: %v. Creating new ephemeral user.", parseErr)
		return createAndSetEphemeralUser()
	}

	// Successfully authenticated existing user (could be ephemeral or persistent).
	return uuidVal, nil
}

// ClaimEphemeralHandler handles requests to convert an ephemeral user account
// into a persistent one by adding email and password.
// Note: This handler is defined but not currently routed in main.go.
// If needed, a route like POST /user/claim should be added.
type claimEphemeralRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Username string `json:"username"` // Optional: Allow updating username during claim.
}

func ClaimEphemeralHandler(w http.ResponseWriter, r *http.Request) {
	token := extractCookieToken(r.Header.Get("Cookie"), "auth_token")
	userIDStr, err := auth.AuthenticateJWT(token)
	if err != nil {
		http.Error(w, "Invalid or missing authentication token", http.StatusForbidden)
		return
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "Invalid user ID format in token", http.StatusForbidden)
		return
	}

	// Fetch the user associated with the token.
	u, err := database.GetUserByID(r.Context(), userID)
	if err != nil {
		// If user not found, token might be stale or DB issue.
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}
	// Check if the user is actually ephemeral.
	if !u.IsEphemeral {
		http.Error(w, "Account has already been claimed", http.StatusBadRequest)
		return
	}

	// Decode the request payload.
	var req claimEphemeralRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}
	// Basic validation for required fields.
	if req.Email == "" || req.Password == "" {
		http.Error(w, "Email and password are required to claim an account", http.StatusBadRequest)
		return
	}

	// Update user details.
	u.Email = req.Email
	u.Password = req.Password // Will be re-hashed by UpdateUserCredentials.
	if req.Username != "" {
		u.Username = req.Username // Update username if provided.
	}
	u.IsEphemeral = false // Mark as persistent.

	// Persist changes to the database.
	err = database.UpdateUserCredentials(r.Context(), u)
	if err != nil {
		// Handle potential constraint violations (e.g., email conflict).
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // Unique violation.
			http.Error(w, "Email address is already in use", http.StatusConflict)
			return
		}
		log.Printf("Failed to finalize ephemeral user %s: %v", userID, err)
		http.Error(w, "Failed to claim account", http.StatusInternalServerError)
		return
	}

	// Optionally issue a new token if claims need updating, though not strictly necessary here.

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "Account claimed successfully.") // Simple confirmation message.
}

// CreateUserHandler handles new user registration requests.
// It expects email, password, and username in the JSON payload.
func CreateUserHandler(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Username string `json:"username"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}
	// Basic validation.
	if req.Email == "" || req.Password == "" || req.Username == "" {
		http.Error(w, "Email, password, and username are required", http.StatusBadRequest)
		return
	}

	user := models.User{
		Email:       req.Email,
		Password:    req.Password, // Will be hashed by database.CreateUser.
		Username:    req.Username,
		IsEphemeral: false, // New users created via this endpoint are persistent.
		IsAdmin:     false, // Default to non-admin.
	}

	ctx := r.Context()
	err := database.CreateUser(ctx, &user) // Creates user and hashes password.
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" { // Unique constraint violation.
			// Check if the violation is on the email field.
			if strings.Contains(pgErr.ConstraintName, "email") {
				http.Error(w, "Email address already exists", http.StatusConflict)
			} else {
				// Handle other unique constraints if any.
				http.Error(w, "Username or other field already exists", http.StatusConflict)
			}
			return
		}
		// Log the specific error for debugging.
		log.Printf("Error creating user %s: %v", req.Email, err)
		http.Error(w, "Error creating user account", http.StatusInternalServerError)
		return
	}

	// Return the created user object (excluding password).
	user.Password = "" // Clear password before encoding response.
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(user)
}

// loginRequest defines the expected JSON structure for login attempts.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// loginResponse defines the JSON structure returned upon successful login.
type loginResponse struct {
	Token string `json:"token"`
}

// LoginHandler handles user login requests.
// It authenticates the user based on email and password, generates a JWT,
// sets it as an HttpOnly cookie, and returns the token in the response body.
func LoginHandler(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		return
	}
	if req.Email == "" || req.Password == "" {
		http.Error(w, "Email and password are required", http.StatusBadRequest)
		return
	}

	// AuthenticateUser verifies credentials and returns a JWT if valid.
	token, err := database.AuthenticateUser(context.Background(), req.Email, req.Password)
	if err != nil {
		// Log the failure reason but return a generic forbidden status.
		log.Printf("Authentication failed for user %s: %v", req.Email, err)
		http.Error(w, "Invalid email or password", http.StatusForbidden)
		return
	}

	// Set the JWT as an HttpOnly cookie.
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		HttpOnly: true,                       // Important for security.
		Path:     "/",                        // Cookie applies to all paths.
		MaxAge:   auth.TOKEN_EXPIRE_TIME_SEC, // Use configured expiry.
		// Secure: true, // Uncomment in production when using HTTPS.
		// SameSite: http.SameSiteLaxMode, // Or SameSiteStrictMode depending on needs.
	})

	// Return the token in the response body as well.
	resp := loginResponse{Token: token}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		// Log internal error if response writing fails.
		log.Printf("Failed to write login response for user %s: %v", req.Email, err)
		http.Error(w, "Failed to process login response", http.StatusInternalServerError)
		return
	}
}

// MeHandler retrieves and returns basic information about the currently authenticated user.
// It relies on the `auth_token` cookie being present and valid.
func MeHandler(w http.ResponseWriter, r *http.Request) {
	token := extractCookieToken(r.Header.Get("Cookie"), "auth_token")
	userIDStr, err := auth.AuthenticateJWT(token) // Verifies token validity.
	if err != nil {
		http.Error(w, "Invalid or missing authentication token", http.StatusForbidden)
		return
	}
	userID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "Invalid user ID format in token", http.StatusForbidden) // Should not happen with valid JWT.
		return
	}

	// Fetch user details from the database.
	user, err := database.GetUserByID(r.Context(), userID)
	if err != nil {
		// If user not found, the token might be for a deleted user.
		log.Printf("User %s from valid token not found in DB: %v", userID, err)
		http.Error(w, "User not found", http.StatusNotFound)
		return
	}

	// Prepare a sanitized response object excluding sensitive fields like password hash.
	sanitizedUser := struct {
		ID          uuid.UUID `json:"id"`
		Username    string    `json:"username"`
		IsEphemeral bool      `json:"is_ephemeral"`
		IsAdmin     bool      `json:"is_admin"`
		// Add other non-sensitive fields like Elo ratings if needed by the client.
		// Elo1v1      int       `json:"elo_1v1"`
	}{
		ID:          user.ID,
		Username:    user.Username,
		IsEphemeral: user.IsEphemeral,
		IsAdmin:     user.IsAdmin,
		// Elo1v1:      user.Elo1v1,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(sanitizedUser); err != nil {
		log.Printf("Failed to write /user/me response for user %s: %v", userID, err)
		http.Error(w, "Failed to process user information", http.StatusInternalServerError)
		return
	}
}
