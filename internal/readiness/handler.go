package readiness

import (
	"context"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/pkg/httputil"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

const mongoPingTimeout = 2 * time.Second

type mongoPinger interface {
	Ping(context.Context, *readpref.ReadPref) error
}

type Handler struct {
	mongo    mongoPinger
	draining atomic.Bool
}

func NewHandler(mongo mongoPinger) *Handler {
	return &Handler{mongo: mongo}
}

func (h *Handler) StartDraining() {
	h.draining.Store(true)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.draining.Load() {
		httputil.WriteError(w, http.StatusServiceUnavailable, "chat service is draining")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), mongoPingTimeout)
	defer cancel()

	if err := h.mongo.Ping(ctx, readpref.Primary()); err != nil {
		httputil.WriteError(w, http.StatusServiceUnavailable, "chat service is not ready")
		return
	}

	httputil.WriteJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
