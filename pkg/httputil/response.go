package httputil

import (
	"encoding/json"
	"net/http"
)

type SuccessResponse struct {
	Status int `json:"status"`
	Data   any `json:"data"`
}

type ErrorResponse struct {
	Status      int    `json:"status"`
	Error       string `json:"error"`
	Description string `json:"description"`
}

func WriteJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")

	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(SuccessResponse{
		Status: status,
		Data:   data,
	})
}

func WriteError(w http.ResponseWriter, status int, description string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	_ = json.NewEncoder(w).Encode(ErrorResponse{
		Status:      status,
		Error:       http.StatusText(status),
		Description: description,
	})
}

func DecodeJSON(r *http.Request, v any) error {
	return json.NewDecoder(r.Body).Decode(v)
}
