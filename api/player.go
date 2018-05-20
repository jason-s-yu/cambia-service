package api

import "github.com/satori/go.uuid"

type Player struct {
	UUID		uuid.UUID	`json:uuid`
	Username	string		`json:username`
	FirstName	string		`json:firstName`
	LastName	string		`json:lastName`
	Email		string		`json:email`
}
