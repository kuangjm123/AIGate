// Package respond writes JSON API bodies without copying request secrets.
package respond

import (
	"encoding/json"
	"net/http"
)

// JSON sets the content type and writes v. Encoding errors are not logged,
// because the value may sit next to a secret the caller already removed.
func JSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// Error writes {"error": code}. code is a fixed token, not request data.
func Error(w http.ResponseWriter, status int, code string) {
	JSON(w, status, map[string]string{"error": code})
}

// NoContent writes an empty 204.
func NoContent(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNoContent)
}
