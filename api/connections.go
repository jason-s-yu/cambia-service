package api

import "github.com/gorilla/websocket"

// Hub maintains the set of active clients and messages to the clients
type Hub struct {
	// Clients connected
	clients map[*Client]bool

	// register requests
	register chan *Client

	// unregister requests
	unregister chan *Client
}

type Client struct {
	hub *Hub

	connection *websocket.Conn
}

// initHub creates a new instance of a hub
func initHub() *Hub {
	return &Hub{
		register:   make(chan *Client),
		unregister: make(chan *Client),
		clients:    make(map[*Client]bool),
	}
}

func run(hub *Hub) {
	for {
		select {
		case client := <- hub.register:
			hub.clients[client] = true
		case client := <- hub.unregister:
			if _, ok := hubclients[client]; ok {
				delete(hub.clients, client)
				close(client.send)
			}
		case message := <- hub.broadcast:
			for client := range hub.clients {
				select {
				case client.send <- message:
				default:
					close(client.send)
					delete(hub.clients, client)
				}
			}
		}
	}
}
