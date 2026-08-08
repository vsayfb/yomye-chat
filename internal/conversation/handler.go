package conversation

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/vsayfb/gig-platform-chat-service/internal/messagecursor"
	"github.com/vsayfb/gig-platform-chat-service/pkg/httputil"
	"github.com/vsayfb/gig-platform-chat-service/pkg/middleware"
)

type Handler struct {
	service Service
}

func NewHandler(service Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) List(w http.ResponseWriter, r *http.Request) {
	result, err := h.service.List(r.Context(), middleware.GetUserID(r.Context()))
	if err != nil {
		slog.ErrorContext(r.Context(), "failed to list direct conversations", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Internal Server Error")
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	result, err := h.service.Get(r.Context(), id, middleware.GetUserID(r.Context()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

func (h *Handler) ListMessages(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}

	limit := 50
	if value := r.URL.Query().Get("limit"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed <= 0 {
			httputil.WriteError(w, http.StatusBadRequest, "Invalid limit")
			return
		}
		if parsed > 100 {
			parsed = 100
		}
		limit = parsed
	}

	cursor, err := messagecursor.Parse(
		r.URL.Query().Get("before"),
		r.URL.Query().Get("before_id"),
	)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid message cursor")
		return
	}

	result, err := h.service.ListMessages(
		r.Context(),
		middleware.GetUserID(r.Context()),
		id,
		limit,
		cursor,
	)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

func parseID(w http.ResponseWriter, r *http.Request) (bson.ObjectID, bool) {
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "conversationID"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid conversation ID")
		return bson.NilObjectID, false
	}
	return id, true
}

func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, "Conversation not found")
	case errors.Is(err, ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, "Forbidden")
	default:
		slog.ErrorContext(r.Context(), "direct conversation request failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Internal Server Error")
	}
}
