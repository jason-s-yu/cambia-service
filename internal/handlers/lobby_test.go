// internal/handlers/lobby_test.go
package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/game"
)

// TestLobbyCreate checks that /lobby/create builds an ephemeral lobby in memory.
func TestLobbyCreate(t *testing.T) {
	auth.Init() // ephemeral keys, no DB needed
	gs := NewGameServer()

	// ephemeral user ID
	uHost := uuid.New()

	token, _ := auth.CreateJWT(uHost.String())
	body := `{"type":"private","gameMode":"head_to_head"}`
	req := httptest.NewRequest("POST", "/lobby/create", bytes.NewBuffer([]byte(body)))
	req.Header.Set("Cookie", "auth_token="+token)
	w := httptest.NewRecorder()

	h := CreateLobbyHandler(gs)
	h.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var newLobby game.Lobby
	if err := json.Unmarshal(w.Body.Bytes(), &newLobby); err != nil {
		t.Fatalf("failed to decode lobby: %v", err)
	}
	if newLobby.ID == uuid.Nil {
		t.Fatalf("lobby has no ID")
	}
	if newLobby.HostUserID != uHost {
		t.Fatalf("lobby host mismatch, expected %v got %v", uHost, newLobby.HostUserID)
	}
}
