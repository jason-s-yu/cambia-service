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
}
