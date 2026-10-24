// Package httperr writes the error body shared by every route of the API.
package httperr

import (
	"encoding/json"
	"net/http"
)

type body struct {
	Error detail `json:"error"`
}

type detail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Write answers with {"error": {"code": ..., "message": ...}}. The code is stable and meant for programs, the message
// for people.
func Write(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body{Error: detail{Code: code, Message: message}})
}
