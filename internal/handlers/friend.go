// internal/handlers/friend.go
package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/database"
)

// AddFriendHandler handles a user sending a friend request to another user.
//
// Request payload: { "friend_id": "some-uuid-string" }
// We store a row in the friends table with status='pending'.
func AddFriendHandler(w http.ResponseWriter, r *http.Request) {
	cookieHeader := r.Header.Get("Cookie")
	if !strings.Contains(cookieHeader, "auth_token=") {
		http.Error(w, "missing auth_token", http.StatusUnauthorized)
		return
	}
	jwtToken := extractCookieToken(cookieHeader, "auth_token")

	userIDStr, err := auth.AuthenticateJWT(jwtToken)
	if err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "invalid user id in token", http.StatusBadRequest)
		return
	}

	var req struct {
		FriendID string `json:"friend_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	friendUUID, err := uuid.Parse(req.FriendID)
	if err != nil {
		http.Error(w, "invalid friend_id", http.StatusBadRequest)
		return
	}

	if userUUID == friendUUID {
		http.Error(w, "cannot friend yourself", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	err = database.InsertFriendRequest(ctx, userUUID, friendUUID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to insert friend request: %v", err), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusCreated)
	w.Write([]byte("friend request sent"))
}

// AcceptFriendHandler handles a user accepting a friend request that was sent to them.
//
// Request payload: { "friend_id": "some-uuid-string" }
// This means the user with friend_id had previously called AddFriendHandler, and now
// we set status='accepted' for (friend_id -> user).
func AcceptFriendHandler(w http.ResponseWriter, r *http.Request) {
	cookieHeader := r.Header.Get("Cookie")
	if !strings.Contains(cookieHeader, "auth_token=") {
		http.Error(w, "missing auth_token", http.StatusUnauthorized)
		return
	}
	jwtToken := extractCookieToken(cookieHeader, "auth_token")

	userIDStr, err := auth.AuthenticateJWT(jwtToken)
	if err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "invalid user id in token", http.StatusBadRequest)
		return
	}

	var req struct {
		FriendID string `json:"friend_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	friendUUID, err := uuid.Parse(req.FriendID)
	if err != nil {
		http.Error(w, "invalid friend_id", http.StatusBadRequest)
		return
	}

	// The pending request was from friendUUID -> userUUID
	err = database.AcceptFriend(r.Context(), friendUUID, userUUID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to accept friend: %v", err), http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("friend request accepted"))
}

// ListFriendsHandler returns a JSON array of all friend relationships (pending or accepted)
// associated with the authenticated user.
func ListFriendsHandler(w http.ResponseWriter, r *http.Request) {
	cookieHeader := r.Header.Get("Cookie")
	if !strings.Contains(cookieHeader, "auth_token=") {
		http.Error(w, "missing auth_token", http.StatusUnauthorized)
		return
	}
	jwtToken := extractCookieToken(cookieHeader, "auth_token")

	userIDStr, err := auth.AuthenticateJWT(jwtToken)
	if err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "invalid user id in token", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	friends, err := database.ListFriends(ctx, userUUID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to list friends: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(friends)
}

// RemoveFriendHandler handles removing (unfriending) a user.
//
// Request payload: { "friend_id": "some-uuid-string" }
func RemoveFriendHandler(w http.ResponseWriter, r *http.Request) {
	cookieHeader := r.Header.Get("Cookie")
	if !strings.Contains(cookieHeader, "auth_token=") {
		http.Error(w, "missing auth_token", http.StatusUnauthorized)
		return
	}
	jwtToken := extractCookieToken(cookieHeader, "auth_token")

	userIDStr, err := auth.AuthenticateJWT(jwtToken)
	if err != nil {
		http.Error(w, "invalid token", http.StatusForbidden)
		return
	}
	userUUID, err := uuid.Parse(userIDStr)
	if err != nil {
		http.Error(w, "invalid user id in token", http.StatusBadRequest)
		return
	}

	var req struct {
		FriendID string `json:"friend_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}
	friendUUID, err := uuid.Parse(req.FriendID)
	if err != nil {
		http.Error(w, "invalid friend_id", http.StatusBadRequest)
		return
	}

	err = database.RemoveFriend(r.Context(), userUUID, friendUUID)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to remove friend: %v", err), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("friend removed"))
}
