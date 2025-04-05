// internal/handlers/lobby_ws.go

package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/internal/auth"
	"github.com/jason-s-yu/cambia/internal/game"
	"github.com/sirupsen/logrus"
)

// We'll keep a reference to the GameServer so we can create the game instance upon start game command
var GameServerForLobbyWS *GameServer

// LobbyWSHandler returns an http.HandlerFunc that upgrades to a WebSocket
// for the given lobby, subprotocol "lobby". It uses a LobbyStore to track real-time state.
// LobbyWSHandler handles WebSocket connections for a game.
// It performs the following steps:
// 1. Parses {lobby_id} from the request path.
// 2. Checks if the subprotocol is "lobby".
// 3. Authenticates the user using the auth_token from the cookie.
// 4. Verifies if the user is a participant in the specified game.
// 5. Accepts the WebSocket connection, tracks it in the LobbyStore, and starts the read loop.
//
// Parameters:
// - logger: A logrus.Logger instance for logging.
// - ls: A LobbyStore instance to manage lobby states.
//
// Returns:
// - An http.HandlerFunc that handles the WebSocket connection.
func LobbyWSHandler(logger *logrus.Logger, gs *GameServer) http.HandlerFunc {
	GameServerForLobbyWS = gs
	return func(w http.ResponseWriter, r *http.Request) {
		pathParts := strings.Split(strings.TrimPrefix(r.URL.Path, "/lobby/ws/"), "/")
		if len(pathParts) < 1 {
			http.Error(w, "missing lobby_id", http.StatusBadRequest)
			return
		}
		lobbyIDStr := pathParts[0]
		lobbyUUID, err := uuid.Parse(lobbyIDStr)
		if err != nil {
			http.Error(w, "invalid lobby_id", http.StatusBadRequest)
			return
		}

		c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			Subprotocols:   []string{"lobby"},
			OriginPatterns: []string{"*"},
		})

		if err != nil {
			logger.Warnf("websocket accept error: %v", err)
			return
		}
		if c.Subprotocol() != "lobby" {
			c.Close(BadSubprotocolError, "client must speak the lobby subprotocol")
			return
		}

		token := extractCookieToken(r.Header.Get("Cookie"), "auth_token")
		userIDStr, err := auth.AuthenticateJWT(token)
		if err != nil {
			logger.Warnf("invalid token: %v", err)
			c.Close(InvalidAuthTokenError, "invalid auth_token")
			return
		}
		userUUID, err := uuid.Parse(userIDStr)
		if err != nil {
			logger.Warnf("invalid userID parse: %v", err)
			c.Close(InvalidUserIDError, "invalid user ID")
			return
		}

		if lobby, exists := gs.LobbyStore.GetLobby(lobbyUUID); exists {
			ctx, cancel := context.WithCancel(r.Context())
			conn := &game.LobbyConnection{
				UserID:  userUUID,
				Cancel:  cancel,
				OutChan: make(chan map[string]interface{}, 10),
				IsHost:  lobby.HostUserID == userUUID,
			}

			err := lobby.AddConnection(userUUID, conn)

			if err != nil {
				logger.Warnf("failed to add connection to lobby: %v", err)
				c.Close(websocket.StatusPolicyViolation, fmt.Sprintf("failed to add connection to lobby: %v", err.Error()))
				return
			}

			logger.Infof("User %v connected to lobby %v", userUUID, lobbyUUID)

			go writePump(ctx, c, conn, logger)

			lobby.BroadcastJoin(userUUID)
			readPump(ctx, c, lobby, conn, logger, lobbyUUID)
		} else {
			c.Close(InvalidLobbyIDError, "lobby does not exist")
			return
		}
	}
}

// readPump reads messages from the websocket until disconnect. We handle JSON commands here.
func readPump(ctx context.Context, c *websocket.Conn, lobby *game.Lobby, conn *game.LobbyConnection, logger *logrus.Logger, lobbyID uuid.UUID) {
	defer func() {
		lobby.RemoveUser(conn.UserID)
		conn.Cancel()
		c.Close(websocket.StatusNormalClosure, "closing")
	}()

	for {
		typ, msg, err := c.Read(ctx)
		if err != nil {
			logger.Infof("user %v read err: %v", conn.UserID, err)
			return
		}
		if typ != websocket.MessageText {
			continue
		}

		var packet map[string]interface{}
		if err := json.Unmarshal(msg, &packet); err != nil {
			logger.Warnf("invalid json from user %v: %v", conn.UserID, err)
			continue
		}

		handleLobbyMessage(packet, lobby, conn, logger)
	}
}

// handleLobbyMessage interprets the "type" field received by client and updates the lobby or broadcasts accordingly.
func handleLobbyMessage(packet map[string]interface{}, lobby *game.Lobby, senderConn *game.LobbyConnection, logger *logrus.Logger) {
	action, _ := packet["type"].(string)
	switch action {
	case "ready":
		lobby.MarkUserReady(senderConn.UserID)

		if lobby.AreAllReady() {
			// TODO: create and attach the game instance now

			// check for auto start
			lobby.StartCountdown(10, func(lobby *game.Lobby) {
				g := GameServerForLobbyWS.NewCambiaGameFromLobby(context.Background(), lobby)

				lobby.BroadcastAll(map[string]interface{}{
					"game_id": g.ID.String(),
				})
			})
		}
	case "unready":
		lobby.MarkUserUnready(senderConn.UserID)
	case "invite":
		userToAdd, err := uuid.Parse(packet["userID"].(string))

		if err != nil {
			logger.Warnf("invalid user ID to invite: %v", packet["userID"])
			return
		}

		lobby.InviteUser(userToAdd)

		// TODO: issue notification to the target user eventually
	case "leave_lobby":
		lobby.RemoveUser(senderConn.UserID)
		lobby.BroadcastLeave(senderConn.UserID)
		senderConn.Cancel()
	case "chat":
		msg, _ := packet["msg"].(string)
		lobby.BroadcastChat(senderConn.UserID, msg)
	case "update_rules":
		// host can update auto_start, etc.
		if !senderConn.IsHost {
			senderConn.Write(map[string]interface{}{
				"type":    "error",
				"message": "Only the host can update rules",
			})
			return
		}

		if rules, ok := packet["rules"].(map[string]interface{}); ok {
			lobby.HouseRules.Update(rules)
		}

		// TODO: broadcast new rules to lobby
	case "start_game":
		// this message is sent to forcibly start the game, regardless of the timer status
		// this must be sent to start the game if autoStart == false
		// check if we're in a game already
		if lobby.InGame {
			senderConn.WriteError("game already in progress")
			return
		}
		if !lobby.AreAllReady() {
			senderConn.WriteError("not all users are ready")
			return
		}
		lobby.CancelCountdown()

		// create game now
		g := GameServerForLobbyWS.NewCambiaGameFromLobby(context.Background(), lobby)
		lobby.BroadcastAll(map[string]interface{}{
			"type":    "game_start",
			"game_id": g.ID.String(),
		})
	default:
		logger.Warnf("unknown action %s from user %v", action, senderConn.UserID)
	}
}

// writePump writes messages from conn.OutChan to the websocket until context is canceled.
func writePump(ctx context.Context, c *websocket.Conn, conn *game.LobbyConnection, logger *logrus.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case msg := <-conn.OutChan:
			data, err := json.Marshal(msg)
			if err != nil {
				logger.Warnf("failed to marshal out msg: %v", err)
				continue
			}
			err = c.Write(ctx, websocket.MessageText, data)
			if err != nil {
				logger.Warnf("failed to write to ws: %v", err)
				return
			}
		}
	}
}
