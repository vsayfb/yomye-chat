package httputil

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeJSONAcceptsSingleKnownObject(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"chat"}`))
	response := httptest.NewRecorder()
	var body struct {
		Name string `json:"name"`
	}

	if err := DecodeJSON(response, request, &body); err != nil {
		t.Fatalf("DecodeJSON() error = %v", err)
	}
	if body.Name != "chat" {
		t.Fatalf("DecodeJSON() name = %q, want chat", body.Name)
	}
}

func TestDecodeJSONRejectsUnknownFields(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"chat","extra":true}`))
	response := httptest.NewRecorder()
	var body struct {
		Name string `json:"name"`
	}

	if err := DecodeJSON(response, request, &body); err == nil {
		t.Fatal("DecodeJSON() accepted an unknown field")
	}
}

func TestDecodeJSONRejectsTrailingJSON(t *testing.T) {
	t.Parallel()

	request := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"chat"} {"name":"second"}`))
	response := httptest.NewRecorder()
	var body struct {
		Name string `json:"name"`
	}

	if err := DecodeJSON(response, request, &body); err == nil {
		t.Fatal("DecodeJSON() accepted multiple JSON values")
	}
}

func TestDecodeJSONRejectsOversizedBody(t *testing.T) {
	t.Parallel()

	bodyText := `{"name":"` + strings.Repeat("a", int(maxJSONBodyBytes)) + `"}`
	request := httptest.NewRequest("POST", "/", strings.NewReader(bodyText))
	response := httptest.NewRecorder()
	var body struct {
		Name string `json:"name"`
	}

	if err := DecodeJSON(response, request, &body); err == nil {
		t.Fatal("DecodeJSON() accepted an oversized body")
	}
}

func TestWriteJSONUsesEnvelopeAndContentType(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	WriteJSON(rr, http.StatusCreated, map[string]string{"id": "abc"})

	if rr.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d", rr.Code, http.StatusCreated)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
	var body struct {
		Status int               `json:"status"`
		Data   map[string]string `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body.Status != http.StatusCreated || body.Data["id"] != "abc" {
		t.Fatalf("body = %#v", body)
	}
}

func TestWriteErrorUsesHTTPStatusTextAndDescription(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	WriteError(rr, http.StatusForbidden, "not allowed")

	var body ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if rr.Code != http.StatusForbidden || body.Status != http.StatusForbidden {
		t.Fatalf("status response=%d body=%d", rr.Code, body.Status)
	}
	if body.Error != http.StatusText(http.StatusForbidden) || body.Description != "not allowed" {
		t.Fatalf("body = %#v", body)
	}
}
