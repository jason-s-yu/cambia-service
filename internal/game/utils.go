package game

import (
	"encoding/json"
	"log"
)

// convertEventToBytes marshals a GameEvent into JSON bytes. If an error occurs during
// the marshal, it logs the error and returns a minimal "{}" to avoid runtime failures.
func convertEventToBytes(ev GameEvent) []byte {
	data, err := json.Marshal(ev)
	if err != nil {
		log.Printf("WARNING: failed to marshal GameEvent: %v", err)
		return []byte("{}")
	}
	return data
}
