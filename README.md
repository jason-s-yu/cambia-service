# cambia-server

A socket-based API for Cambia with a self-learning bot, written in Golang

## Project Structure

* Server is written in `Golang`
  * Game state is stored in ephemeral storage
  * MongoDB with [`globalsign/mgo`](https://github.com/globalsign/mgo) for database logging
  * Websocket library is [`gorilla/websocket`](https://github.com/gorilla/websocket)
  * Router library is [`gorilla/mux`](https://github.com/gorilla/mux)
  * Dependency management with [`golang/dep`](https://github.com/golang/dep)
* UI written with JavaScript
* AI/bot written with C++

## Table of Contents

> **[`/api`](./api)** - API endpoints for the router

* [`client.go`](./api/client.go) - functions pertaining to socket connected clients
* [`connection.go`](./api/connection.go) - functions facilitating the websocket connection
* [`deck.go`](./api/deck.go) - deck functions
* [`game.go`](./api/game.go) - core game router

> **[`/cambia`](./cambia)** - Main Package

* [`server.go`](./cambia/server.go) - entry file

> **[`/constants`](./constants)** - Environment Files

* [`env.go`](./constants/env.go) - environment configuration

> **[`/database/`](./database)** - Database Files

* [`database.go`](./database/database.go) - database entry file