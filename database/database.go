package database

import (
	"github.com/globalsign/mgo"
	"go-restful/constants"
)

// Globally exported DATABASE instance
var DB *mgo.Database

func Init(session *mgo.Session) error {
	var err error

	// Initialize a test database if we are on environment
	// We will drop the old test database and re-populate it
	if constants.Env == "dev" || constants.Env == "development" {
		if constants.DBName == "" {
			err = session.DB("test").DropDatabase()
			if err != nil {
				panic(err)
			}
			DB = session.DB("test")
		} else {
			DB = session.DB(constants.DBName)
		}
	} else {
		DB = session.DB(constants.DBName)
	}

	return err
}
