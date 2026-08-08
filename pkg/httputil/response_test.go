package httputil

import (
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
