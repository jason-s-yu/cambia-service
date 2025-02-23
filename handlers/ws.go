package handlers

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"github.com/jason-s-yu/cambia/game"
	"github.com/jason-s-yu/cambia/models"
	"golang.org/x/time/rate"
)

type GameServer struct {
	GameInstance *game.Game
	Logf         func(f string, v ...interface{})
}

func NewGameServer(logf func(f string, v ...interface{})) *GameServer {
	return &GameServer{
		GameInstance: game.NewGame(),
		Logf:         logf,
	}
}

func (s *GameServer) NewGameHandler(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		Subprotocols: []string{"echo"},
	})
	if err != nil {
		s.Logf("%v", err)
		return
	}

	// Create a new player instance and store the connection
	playerID := r.URL.Query().Get("player_id")
	if playerID == "" {
		s.Logf("missing player_id")
		c.Close(websocket.StatusPolicyViolation, "missing player_id")
		return
	}

	player := &models.Player{
		ID: func() uuid.UUID {
			id, err := uuid.Parse(playerID)
			if err != nil {
				s.Logf("invalid player_id: %v", err)
				c.Close(websocket.StatusPolicyViolation, "invalid player_id")
				return uuid.Nil
			}
			return id
		}(),
		Conn:      c,
		Connected: true,
	}

	// Store the player in the game instance
	s.GameInstance.Mutex.Lock()
	s.GameInstance.Players = append(s.GameInstance.Players, player)
	s.GameInstance.Mutex.Unlock()
	defer c.CloseNow()

	if c.Subprotocol() != "echo" {
		c.Close(websocket.StatusPolicyViolation, "client must speak the echo subprotocol")
		return
	}

	l := rate.NewLimiter(rate.Every(time.Millisecond*100), 10)
	for {
		err = echo(c, l)
		if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
			return
		}
		if err != nil {
			s.Logf("failed to echo with %v: %v", r.RemoteAddr, err)
			return
		}
	}
}

func echo(c *websocket.Conn, l *rate.Limiter) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()

	err := l.Wait(ctx)
	if err != nil {
		return err
	}

	typ, r, err := c.Reader(ctx)
	if err != nil {
		return err
	}

	w, err := c.Writer(ctx, typ)
	if err != nil {
		return err
	}

	_, err = io.Copy(w, r)
	if err != nil {
		return fmt.Errorf("failed to io.Copy: %w", err)
	}

	err = w.Close()
	return err
}
