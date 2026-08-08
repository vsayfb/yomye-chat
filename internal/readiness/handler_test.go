package readiness

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

type fakeMongoPinger struct {
	err         error
	calls       int
	hadDeadline bool
}

func (f *fakeMongoPinger) Ping(ctx context.Context, rp *readpref.ReadPref) error {
	f.calls++
	_, f.hadDeadline = ctx.Deadline()
	if rp == nil {
		return errors.New("missing read preference")
	}
	return f.err
}

func TestHandlerReadyWhenMongoResponds(t *testing.T) {
	mongo := &fakeMongoPinger{}
	h := NewHandler(mongo)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	recorder := httptest.NewRecorder()

	h.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if mongo.calls != 1 {
		t.Fatalf("mongo ping calls = %d, want 1", mongo.calls)
	}
	if !mongo.hadDeadline {
		t.Fatal("mongo ping context must have a deadline")
	}
	if body := recorder.Body.String(); body != "{\"status\":200,\"data\":{\"status\":\"ready\"}}\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestHandlerNotReadyWhenMongoPingFails(t *testing.T) {
	mongo := &fakeMongoPinger{err: errors.New("mongo unavailable")}
	h := NewHandler(mongo)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	recorder := httptest.NewRecorder()

	h.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if mongo.calls != 1 {
		t.Fatalf("mongo ping calls = %d, want 1", mongo.calls)
	}
}

func TestHandlerNotReadyWhileDrainingWithoutPingingMongo(t *testing.T) {
	mongo := &fakeMongoPinger{}
	h := NewHandler(mongo)
	h.StartDraining()

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	recorder := httptest.NewRecorder()

	h.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if mongo.calls != 0 {
		t.Fatalf("mongo ping calls = %d, want 0", mongo.calls)
	}
}

func TestMongoPingTimeoutIsBounded(t *testing.T) {
	mongo := &deadlineInspectingPinger{}
	h := NewHandler(mongo)

	req := httptest.NewRequest(http.MethodGet, "/ready", nil)
	recorder := httptest.NewRecorder()

	before := time.Now()
	h.ServeHTTP(recorder, req)

	if mongo.deadline.IsZero() {
		t.Fatal("mongo ping context must have a deadline")
	}
	remaining := mongo.deadline.Sub(before)
	if remaining <= 0 || remaining > mongoPingTimeout+100*time.Millisecond {
		t.Fatalf("mongo ping deadline remaining = %s", remaining)
	}
}

type deadlineInspectingPinger struct {
	deadline time.Time
}

func (f *deadlineInspectingPinger) Ping(ctx context.Context, _ *readpref.ReadPref) error {
	f.deadline, _ = ctx.Deadline()
	return nil
}
