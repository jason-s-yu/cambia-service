package models

import (
	"github.com/coder/websocket"
	"github.com/google/uuid"
)

type Player struct {
	ID              uuid.UUID       `json:"id"`
	Hand            []*Card         `json:"hand"`
	Connected       bool            `json:"connected"`
	Conn            *websocket.Conn `json:"-"`
	HasCalledCambia bool            `json:"hasCalledCambia"`
	User            *User           `json:"-"`
}

func NewPlayer(user *User) (*Player, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}

	return &Player{
		ID:              id,
		Hand:            []*Card{},
		Connected:       true,
		HasCalledCambia: false,
		User:            user,
	}, nil
}
