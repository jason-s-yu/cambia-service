package models

import "github.com/coder/websocket"

type Player struct {
	ID              string          `json:"id"`
	Hand            []Card          `json:"hand"`
	Revealed        []bool          `json:"revealed"`
	IsDealer        bool            `json:"isDealer"`
	Connected       bool            `json:"connected"`
	Conn            *websocket.Conn `json:"-"`
	Ready           bool            `json:"ready"`
	HasCalledCambia bool            `json:"hasCalledCambia"`
}
