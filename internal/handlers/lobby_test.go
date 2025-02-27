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

func TestLobbyCreateAndJoin(t *testing.T) {
	auth.Init()
	database.ConnectDB()

	// use temp user
	u := models.User{
		Email:    "someone@example.com",
		Password: "password",
		Username: "someone",
	}
	if err := database.CreateUser(context.Background(), &u); err != nil {
		t.Fatalf("create user: %v", err)
	}

	token, _ := auth.CreateJWT(u.ID.String())

	// create lobby request
	body := `{
		"type":"private",
		"circuit_mode":false,
		"ranked":false,
		"ranking_mode":"1v1",
		"disconnection_threshold":2
	}`
	req := httptest.NewRequest("POST", "/lobby/create", bytes.NewBuffer([]byte(body)))
	req.Header.Set("Cookie", "auth_token="+token)
	w := httptest.NewRecorder()

	CreateLobbyHandler(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("CreateLobbyHandler failed: %v", w.Body.String())
	}
	var lob models.Lobby
	if err := json.Unmarshal(w.Body.Bytes(), &lob); err != nil {
		t.Fatalf("unmarshal lobby: %v", err)
	}

	// join lobby with same user
	req2Body := `{"lobby_id":"` + lob.ID.String() + `"}`
	req2 := httptest.NewRequest("POST", "/lobby/join", bytes.NewBuffer([]byte(req2Body)))
	req2.Header.Set("Cookie", "auth_token="+token)
	w2 := httptest.NewRecorder()

	JoinLobbyHandler(w2, req2)
	if w2.Code != http.StatusOK {
		t.Fatalf("JoinLobbyHandler failed: code=%d, body=%s", w2.Code, w2.Body.String())
	}
}
