package main

import (
	"github.com/gorilla/mux"
	"github.com/globalsign/mgo"

	"time"
	"cambia-server/constants"
	"cambia-server/database"
	"cambia-server/api"
)

func main() {
	// Initialize the router
	router := mux.NewRouter()

	// Objectify the mgo dialing information. We will use this to connect to the database
	mgoInfo := &mgo.DialInfo {
		Addrs:		[] string {constants.DBHosts},
		Timeout:	60 * time.Second,
		Database: 	constants.DBName,
		Username:	constants.DBUserName,
		Password:	constants.DBPassword,
	}

	// Initialize the mgo driver with the above information
	session, err := mgo.DialWithInfo(mgoInfo)
	session.SetMode(mgo.Monotonic, true)

	// Panic and log err if there is an error
	if err != nil {
		panic(err)
	}

	// Initialize the database
	err = database.Init(session)

	// Close the mgo session once the program has concluded
	defer session.Close()

	// Router endpoints and API
	serveEndpoints(router)

	// Account API
}

func serveEndpoints(router *mux.Router) {
	router.HandleFunc("", api.GetDecks).Methods("POST")
}

func accountAPI() {

}
