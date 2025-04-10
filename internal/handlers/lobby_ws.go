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

var GameServerForLobbyWS *GameServer

// LobbyWSHandler sets up the ephemeral in-memory WS flow.
// Steps:
//  1. parse lobby_id
//  2. check subprotocol == "lobby"
//  3. authenticate user
//  4. find ephemeral lobby in memory
//  5. if private, ensure the user was invited
//  6. add ephemeral connection
//  7. handle messages: ready, unready, invite, leave_lobby, chat, update_rules, start_game
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

		// authenticate user
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

		lobby, exists := gs.LobbyStore.GetLobby(lobbyUUID)
		if !exists {
			c.Close(InvalidLobbyIDError, "lobby does not exist")
			return
		}

		// ephemeral check if private
		if lobby.Type == "private" {
			if _, ok := lobby.Users[userUUID]; !ok {
				c.Close(websocket.StatusPolicyViolation, "user not invited to private lobby")
				return
			}
		}
		lobby.Users[userUUID] = true

		// create ephemeral connection
		ctx, cancel := context.WithCancel(r.Context())
		conn := &game.LobbyConnection{
			UserID:  userUUID,
			Cancel:  cancel,
			OutChan: make(chan map[string]interface{}, 10),
			IsHost:  (lobby.HostUserID == userUUID),
		}
		if err := lobby.AddConnection(userUUID, conn); err != nil {
			logger.Warnf("failed AddConnection: %v", err)
			c.Close(websocket.StatusPolicyViolation, fmt.Sprintf("AddConnection error: %v", err))
			return
		}

		logger.Infof("User %v connected to lobby %v", userUUID, lobbyUUID)
		go writePump(ctx, c, conn, logger)

		lobby.BroadcastJoin(userUUID)
		readPump(ctx, c, lobby, conn, logger, lobbyUUID)
	}
}

// readPump handles incoming messages from the lobby websocket
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

// handleLobbyMessage interprets the "type" field for ephemeral lobby logic.
func handleLobbyMessage(packet map[string]interface{}, lobby *game.Lobby, senderConn *game.LobbyConnection, logger *logrus.Logger) {
	action, _ := packet["type"].(string)
	switch action {
	case "ready":
		lobby.MarkUserReady(senderConn.UserID)
		// autoStart => countdown if all ready
		if lobby.AreAllReady() && lobby.LobbySettings.AutoStart {
			lobby.StartCountdown(10, func(l *game.Lobby) {
				g := GameServerForLobbyWS.NewCambiaGameFromLobby(context.Background(), l)
				l.InGame = true
				l.BroadcastAll(map[string]interface{}{
					"type":    "game_start",
					"game_id": g.ID.String(),
				})
			})
		}

	case "unready":
		lobby.MarkUserUnready(senderConn.UserID)

	case "invite":
		// ephemeral invite
		userIDStr, _ := packet["userID"].(string)
		userToAdd, err := uuid.Parse(userIDStr)
		if err != nil {
			logger.Warnf("invalid user ID to invite: %v", packet["userID"])
			return
		}
		lobby.InviteUser(userToAdd)
		// optionally broadcast that user was invited:
		lobby.BroadcastAll(map[string]interface{}{
			"type":      "lobby_invite",
			"invitedID": userToAdd.String(),
		})

	case "leave_lobby":
		lobby.RemoveUser(senderConn.UserID)
		lobby.BroadcastLeave(senderConn.UserID)
		senderConn.Cancel()

	case "chat":
		msg, _ := packet["msg"].(string)
		lobby.BroadcastChat(senderConn.UserID, msg)

	case "update_rules":
		if !senderConn.IsHost {
			senderConn.Write(map[string]interface{}{
				"type":    "error",
				"message": "Only the host can update rules",
			})
			return
		}
		if rules, ok := packet["rules"].(map[string]interface{}); ok {
			lobby.HouseRules.Update(rules)
			lobby.BroadcastAll(map[string]interface{}{
				"type":  "lobby_rules_updated",
				"rules": lobby.HouseRules,
			})
		}

	case "start_game":
		// forcibly start if not autoStart
		if lobby.InGame {
			senderConn.WriteError("game already in progress")
			return
		}
		if !lobby.AreAllReady() {
			senderConn.WriteError("not all users are ready")
			return
		}
		lobby.CancelCountdown()
		g := GameServerForLobbyWS.NewCambiaGameFromLobby(context.Background(), lobby)
		lobby.InGame = true
		lobby.BroadcastAll(map[string]interface{}{
			"type":    "game_start",
			"game_id": g.ID.String(),
		})

	default:
		logger.Warnf("unknown action %s from user %v", action, senderConn.UserID)
	}
}

// writePump writes messages from conn.OutChan to the websocket
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
