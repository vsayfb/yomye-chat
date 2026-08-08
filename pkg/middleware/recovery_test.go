package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRecoveryReturnsInternalServerError(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}

	if got := recorder.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type = %q, want application/json", got)
	}

	if got := recorder.Body.String(); got != "{\"error\":\"internal server error\"}\n" {
		t.Fatalf("body = %q", got)
	}
}

func TestRecoveryDoesNotOverwriteStartedResponse(t *testing.T) {
	handler := Recovery(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte("started"))
		panic("boom")
	}))

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/panic", nil)

	handler.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusAccepted)
	}

	if got := recorder.Body.String(); got != "started" {
		t.Fatalf("body = %q, want started", got)
	}
}
