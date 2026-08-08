package httputil

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

const maxJSONBodyBytes int64 = 64 << 10 // 64 KiB

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

func DecodeJSON(w http.ResponseWriter, r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)

	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()

	if err := decoder.Decode(v); err != nil {
		return err
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err != nil {
			return err
		}
		return errors.New("request body must contain a single JSON value")
	}

	return nil
}
