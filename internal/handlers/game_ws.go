// internal/handlers/game_ws.go
package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/game" // Use game package types
	"github.com/jason-s-yu/cambia/internal/models"
	"github.com/sirupsen/logrus"
)

// GameMessage represents the structure for incoming WebSocket messages during the game phase.
// It includes fields necessary for various actions defined in the spec.
type GameMessage struct {
	Type string `json:"type"`

	// Used for single-card actions like discard, snap, replace
	Card map[string]interface{} `json:"card,omitempty"` // Use map for flexibility

	// Used for special actions involving two cards (swaps)
	Card1 map[string]interface{} `json:"card1,omitempty"` // Use map
	Card2 map[string]interface{} `json:"card2,omitempty"` // Use map

	// Used for special actions to specify the sub-action (e.g., "peek_self", "skip")
	Special string `json:"special,omitempty"`

	// Generic payload for extensibility, though spec uses top-level fields mostly
	Payload map[string]interface{} `json:"payload,omitempty"`
}

// GameWSHandler upgrades the connection and handles the game WebSocket lifecycle.
func GameWSHandler(logger *logrus.Logger, gs *GameServer) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// 1. Extract Game ID from URL path
		pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/game/ws/"), "/")
		if len(pathParts) < 1 || pathParts[0] == "" {
			http.Error(w, "Missing game_id in path (/game/ws/{game_id})", http.StatusBadRequest)
			return
		}
		gameIDStr := pathParts[0]
		gameID, err := uuid.Parse(gameIDStr)
		if err != nil {
			http.Error(w, "Invalid game_id format", http.StatusBadRequest)
			return
		}

		// 2. Find the game instance in the GameServer's store
		g, ok := gs.GameStore.GetGame(gameID)
		if !ok {
			http.Error(w, "Game not found", http.StatusNotFound)
			return
		}
		if g.GameOver {
			http.Error(w, "Game has already ended", http.StatusGone) // 410 Gone might be appropriate
			return
		}

		// 3. Upgrade WebSocket connection
		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:   []string{"game"}, // Enforce "game" subprotocol
			OriginPatterns: []string{"*"},    // Allow all origins (adjust for production)
		})
		if err != nil {
			logger.Warnf("WebSocket accept error for game %s: %v", gameID, err)
			// Error is implicitly sent by the websocket library
			return
		}
		defer c.Close(websocket.StatusInternalError, "Internal server error during handler exit.") // Ensure closure on error/exit

		if c.Subprotocol() != "game" {
			logger.Warnf("Client for game %s connected with invalid subprotocol: %s", gameID, c.Subprotocol())
			c.Close(websocket.StatusPolicyViolation, "Client must use the 'game' subprotocol.")
			return
		}
		logger.Infof("WebSocket connection established for game %s from %s", gameID, r.RemoteAddr)

		// 4. Authenticate user (reuse lobby logic or implement game-specific auth)
		userID, err := EnsureEphemeralUser(w, r) // Assuming this handles JWT/ephemeral logic
		if err != nil {
			logger.Warnf("User authentication failed for game %s: %v", gameID, err)
			c.Close(websocket.StatusPolicyViolation, "Authentication failed.")
			return
		}
		logger.Infof("User %s authenticated for game %s", userID, gameID)

		// Check if player is actually part of this game
		isPlayerInGame := false
		g.Mu.Lock()
		for _, p := range g.Players {
			if p.ID == userID {
				isPlayerInGame = true
				break
			}
		}
		g.Mu.Unlock()
		if !isPlayerInGame {
			logger.Warnf("User %s is not a player in game %s. Closing connection.", userID, gameID)
			c.Close(websocket.StatusPolicyViolation, "You are not a player in this game.")
			return
		}

		// 5. Register broadcast functions if not already set
		//    These need to be set up carefully to handle concurrency with the game lock.
		g.Mu.Lock() // Lock game state before modifying broadcast functions or player list
		if g.BroadcastFn == nil {
			g.BroadcastFn = func(ev game.GameEvent) {
				// This function will be called FROM within game logic (which holds the lock).
				// We need to send messages without holding the lock to avoid blocking game state.
				playersToSend := []*models.Player{}
				for _, p := range g.Players { // Iterate safely while holding lock
					if p.Connected && p.Conn != nil {
						playersToSend = append(playersToSend, p) // Add connected players
					}
				}

				// Prepare message bytes once.
				msgBytes, err := json.Marshal(ev)
				if err != nil {
					logger.Errorf("Failed to marshal broadcast event (%s) for game %s: %v", ev.Type, g.ID, err)
					return // Don't proceed if marshaling fails
				}

				// Send asynchronously outside the lock.
				go func(players []*models.Player, data []byte, gameID uuid.UUID) {
					for _, pl := range players {
						// Double-check connection status *before* writing, outside lock
						// It's possible player disconnected between lock release and write attempt.
						if pl.Conn != nil {
							ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second) // Timeout for write
							err := pl.Conn.Write(ctx, websocket.MessageText, data)
							cancel() // Ensure cancel is called regardless of error
							if err != nil {
								logger.Warnf("Failed to write broadcast message to player %s in game %s: %v", pl.ID, gameID, err)
								// Trigger disconnect handling asynchronously if write fails
								// Be cautious about triggering this too eagerly (e.g., temporary network issues)
								// gs.GameStore.HandleDisconnectAsync(gameID, pl.ID) // Needs implementation
							}
						}
					}
				}(playersToSend, msgBytes, g.ID) // Pass game ID for logging
			}
		}
		if g.BroadcastToPlayerFn == nil {
			g.BroadcastToPlayerFn = func(targetPlayerID uuid.UUID, ev game.GameEvent) {
				// Find the player while holding the lock
				var targetConn *websocket.Conn
				var targetPlayer *models.Player // Keep track of the player pointer
				for _, pl := range g.Players {
					if pl.ID == targetPlayerID {
						targetPlayer = pl
						if pl.Connected && pl.Conn != nil {
							targetConn = pl.Conn
						}
						break
					}
				}

				// If found and connected, send asynchronously outside the lock
				if targetConn != nil {
					msgBytes, err := json.Marshal(ev)
					if err != nil {
						logger.Errorf("Failed to marshal private event (%s) for player %s in game %s: %v", ev.Type, targetPlayerID, g.ID, err)
						return
					}
					// Capture necessary variables for the goroutine
					connToSend := targetConn
					gameID := g.ID
					go func(conn *websocket.Conn, data []byte, playerID uuid.UUID, gameID uuid.UUID) {
						ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
						err := conn.Write(ctx, websocket.MessageText, data)
						cancel()
						if err != nil {
							logger.Warnf("Failed to write private message to player %s in game %s: %v", playerID, gameID, err)
							// Consider triggering disconnect handling
							// gs.GameStore.HandleDisconnectAsync(gameID, playerID) // Needs implementation
						}
					}(connToSend, msgBytes, targetPlayerID, gameID) // Pass IDs for logging
				} else if targetPlayer != nil {
					// Log if player exists but isn't connected
					// logger.Warnf("Could not send private message to disconnected player %s in game %s", targetPlayerID, g.ID)
				} else {
					// Log if player doesn't exist in the game at all
					// logger.Warnf("Could not find player %s to send private message in game %s", targetPlayerID, g.ID)
				}
			}
		}
		g.Mu.Unlock() // Unlock after setting up broadcast functions

		// 6. Handle player connection/reconnection in the game logic
		// This needs the game lock internally. Pass the connection object.
		g.HandleReconnect(userID, c)

		// 7. Start reading messages from the client
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel() // Ensure context cancellation on exit

		// Run the read loop
		readGameMessages(ctx, c, g, userID, logger)

		// 8. Handle disconnection when readGameMessages returns
		logger.Infof("Player %s WebSocket read loop exited for game %s.", userID, gameID)
		// Disconnect handling is now triggered by read error or context cancellation.
		// Call HandleDisconnect directly here.
		g.HandleDisconnect(userID)
		// Close is deferred earlier
		logger.Infof("Player %s cleanup complete for game %s.", userID, gameID)
	}
}

// readGameMessages reads incoming messages from a single client WebSocket connection.
func readGameMessages(ctx context.Context, c *websocket.Conn, g *game.CambiaGame, userID uuid.UUID, logger *logrus.Logger) {
	for {
		msgType, data, err := c.Read(ctx)
		if err != nil {
			// Handle read errors (e.g., connection closed)
			status := websocket.CloseStatus(err)
			if status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
				logger.Infof("WebSocket closed normally for user %s in game %s.", userID, g.ID)
			} else if strings.Contains(err.Error(), "context canceled") {
				logger.Infof("WebSocket context canceled for user %s in game %s.", userID, g.ID)
			} else {
				logger.Warnf("Error reading from WebSocket for user %s in game %s: %v (Status: %d)", userID, g.ID, err, status)
			}
			return // Exit loop on read error/closure/cancelation
		}

		if msgType != websocket.MessageText {
			logger.Warnf("Received non-text message type %d from user %s in game %s. Ignoring.", msgType, userID, g.ID)
			continue
		}

		var msg GameMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			logger.Warnf("Invalid JSON received from user %s in game %s: %v. Data: %s", userID, g.ID, err, string(data))
			// Optionally send an error back to the client
			sendWsError(ctx, c, "Invalid JSON format.")
			continue
		}

		logger.Debugf("Received action '%s' from user %s in game %s.", msg.Type, userID, g.ID)

		// Acquire game lock before processing any action that modifies game state
		g.Mu.Lock()

		// --- Action Processing ---
		// Check if game is over before processing action
		if g.GameOver {
			logger.Warnf("Game %s is over. Ignoring action '%s' from user %s.", g.ID, msg.Type, userID)
			g.Mu.Unlock() // Release lock
			continue      // Ignore actions after game over
		}

		switch msg.Type {
		// Actions handled by HandlePlayerAction
		case "action_draw_stockpile", "action_draw_discardpile",
			"action_discard", "action_replace", "action_cambia", "action_snap":
			// Prepare the GameAction struct
			gameAction := models.GameAction{
				ActionType: msg.Type,
				Payload:    make(map[string]interface{}),
			}
			// Populate payload based on message structure
			// For discard/replace/snap, payload comes from msg.Card
			if msg.Card != nil {
				gameAction.Payload = msg.Card // Pass the whole card object as payload
			} else if msg.Payload != nil {
				// Fallback to generic payload if specific one isn't present
				gameAction.Payload = msg.Payload
			}
			// Call the game logic handler (which assumes lock is held)
			g.HandlePlayerAction(userID, gameAction)

		// Special actions handled by ProcessSpecialAction
		case "action_special":
			// Call the dedicated special action processor (which assumes lock is held)
			g.ProcessSpecialAction(userID, msg.Special, msg.Card1, msg.Card2)

		case "ping":
			// Handle ping immediately without holding the lock for the write
			g.Mu.Unlock() // Release lock before writing
			logger.Tracef("Received ping from user %s, sending pong.", userID)
			sendWsMessage(ctx, c, map[string]string{"type": "pong"})
			g.Mu.Lock() // Re-acquire lock after write (needed for loop consistency)

		default:
			logger.Warnf("Unknown action type '%s' from user %s in game %s.", msg.Type, userID, g.ID)
			// Send error back to client without releasing lock yet
			sendWsError(ctx, c, fmt.Sprintf("Unknown action type: %s", msg.Type))
		}

		// Release the lock after processing the action for this iteration
		g.Mu.Unlock()
		// --- End Action Processing ---

		// Check context cancellation after processing each message
		select {
		case <-ctx.Done():
			logger.Infof("Context canceled after processing message for user %s in game %s.", userID, g.ID)
			return // Exit loop if context is cancelled
		default:
			// Continue to the next iteration
		}
	}
}

// sendWsMessage is a helper to send a structured message to a WebSocket client.
func sendWsMessage(ctx context.Context, c *websocket.Conn, message interface{}) {
	if c == nil {
		log.Println("Error: Attempted to send WebSocket message on nil connection.")
		return
	}
	msgBytes, err := json.Marshal(message)
	if err != nil {
		log.Printf("Error marshaling WebSocket message: %v", err) // Use log package for simplicity here
		return
	}

	// Use a separate context for the write operation to avoid blocking the main read loop context
	writeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second) // Increased timeout
	defer cancel()

	err = c.Write(writeCtx, websocket.MessageText, msgBytes)
	if err != nil {
		// Log errors, especially useful for diagnosing closed connections
		status := websocket.CloseStatus(err)
		if status != websocket.StatusNormalClosure && status != websocket.StatusGoingAway && !strings.Contains(err.Error(), "context deadline exceeded") {
			log.Printf("Error writing WebSocket message: %v (Status: %d)", err, status)
		} else if strings.Contains(err.Error(), "context deadline exceeded") {
			log.Printf("Timeout writing WebSocket message: %v", err)
		}
		// Don't necessarily close connection here, read loop handles closure detection
	}
}

// sendWsError is a helper to send an error message back to the client.
func sendWsError(ctx context.Context, c *websocket.Conn, errorMsg string) {
	sendWsMessage(ctx, c, map[string]interface{}{
		"type":    "error", // Consistent error type
		"message": errorMsg,
	})
}
