// internal/handlers/friend_test.go
package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/database"
	"github.com/jason-s-yu/cambia/internal/models"
)

// TestFriendFlow is a high-level integration test that ensures friend requests and acceptance works.
func TestFriendFlow(t *testing.T) {
	// init DB, etc. (assuming test DB)
	auth.Init()
	database.ConnectDB()

	// create two users
	u1 := createTestUser(t, "alice@example.com", "password", "alice")
	u2 := createTestUser(t, "bob@example.com", "password", "bob")

	// log them in
	aliceToken, _ := auth.CreateJWT(u1.ID.String())
	bobToken, _ := auth.CreateJWT(u2.ID.String())

	// alice sends friend request to bob
	reqBody := `{"friend_id":"` + u2.ID.String() + `"}`
	req := httptest.NewRequest("POST", "/friends/add", bytes.NewBuffer([]byte(reqBody)))
	req.Header.Set("Cookie", "auth_token="+aliceToken)
	w := httptest.NewRecorder()
	AddFriendHandler(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201 created, got %d, body=%s", w.Code, w.Body.String())
	}

	// bob accepts friend request from alice
	accBody := `{"friend_id":"` + u1.ID.String() + `"}`
	req2 := httptest.NewRequest("POST", "/friends/accept", bytes.NewBuffer([]byte(accBody)))
	req2.Header.Set("Cookie", "auth_token="+bobToken)
	w2 := httptest.NewRecorder()
	AcceptFriendHandler(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("expected 200 ok, got %d, body=%s", w2.Code, w2.Body.String())
	}

	// list bob's friends
	req3 := httptest.NewRequest("GET", "/friends/list", nil)
	req3.Header.Set("Cookie", "auth_token="+bobToken)
	w3 := httptest.NewRecorder()
	ListFriendsHandler(w3, req3)
	if w3.Code != http.StatusOK {
		t.Fatalf("expected 200 ok, got %d, body=%s", w3.Code, w3.Body.String())
	}
	var flist []models.Friend
	if err := json.Unmarshal(w3.Body.Bytes(), &flist); err != nil {
		t.Fatalf("failed to decode friend list: %v", err)
	}
	if len(flist) == 0 {
		t.Fatalf("expected at least 1 friend record, got 0")
	}
}

// helper to create a test user directly in DB
func createTestUser(t *testing.T, email, pass, uname string) models.User {
	u := models.User{
		Email:    email,
		Password: pass,
		Username: uname,
	}
	ctx := context.Background()
	if err := database.CreateUser(ctx, &u); err != nil {
		t.Fatalf("CreateUser failed: %v", err)
	}
	return u
}
