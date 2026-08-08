package application

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/vsayfb/gig-platform-chat-service/internal/messagecursor"
	"github.com/vsayfb/gig-platform-chat-service/pkg/httputil"
	"github.com/vsayfb/gig-platform-chat-service/pkg/middleware"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type ApplicationHandler struct {
	applicationSvc Service
}

func NewApplicationHandler(
	applicationSvc Service,
) *ApplicationHandler {
	return &ApplicationHandler{
		applicationSvc: applicationSvc,
	}
}

// GET /applications/me
func (h *ApplicationHandler) ListApplications(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	userID := middleware.GetUserID(ctx)

	result, err := h.applicationSvc.ListApplications(ctx, userID)

	if err != nil {

		slog.ErrorContext(ctx, "error listing application", "err", err)

		httputil.WriteError(w, http.StatusInternalServerError, "Internal Server Error")

		return
	}

	httputil.WriteJSON(w, http.StatusOK, result)
}

// GET /applications/{threadID}
func (h *ApplicationHandler) GetApplication(
	w http.ResponseWriter,
	r *http.Request,
) {
	ctx := r.Context()

	userID := middleware.GetUserID(ctx)

	threadIDHex := chi.URLParam(r, "threadID")

	threadID, err := bson.ObjectIDFromHex(threadIDHex)

	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid thread ID")
		return
	}

	app, err := h.applicationSvc.GetApplication(ctx, threadID, userID)

	if err != nil {

		switch {

		case errors.Is(err, ErrApplicationNotFound):
			httputil.WriteError(w, http.StatusNotFound, "Application not found")
		case errors.Is(err, ErrForbidden):
			httputil.WriteError(w, http.StatusForbidden, "Forbidden")

		default:
			slog.ErrorContext(
				ctx,
				"failed to get application",
				"thread_id", threadIDHex,
				"error", err,
			)

			httputil.WriteError(
				w,
				http.StatusInternalServerError,
				"Internal Server Error",
			)
		}

		return
	}

	httputil.WriteJSON(w, http.StatusOK, app)
}

// GET /applications/{threadID}/messages?before=&before_id=&limit=50
func (h *ApplicationHandler) ListMessages(
	w http.ResponseWriter,
	r *http.Request,
) {
	ctx := r.Context()

	userID := middleware.GetUserID(ctx)

	threadIDHex := chi.URLParam(r, "threadID")

	threadID, err := bson.ObjectIDFromHex(threadIDHex)

	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid thread ID")
		return
	}

	q := r.URL.Query()

	limit := 50

	if v := q.Get("limit"); v != "" {

		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			httputil.WriteError(w, http.StatusBadRequest, "Invalid limit")
			return
		}

		if n > 100 {
			n = 100
		}

		limit = n
	}

	cursor, err := messagecursor.Parse(q.Get("before"), q.Get("before_id"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid message cursor")
		return
	}

	messages, err := h.applicationSvc.ListMessages(ctx, userID, threadID, limit, cursor)

	if err != nil {
		switch {
		case errors.Is(err, ErrApplicationNotFound):
			httputil.WriteError(w, http.StatusNotFound, "Application not found")
		case errors.Is(err, ErrForbidden):
			httputil.WriteError(w, http.StatusForbidden, "Forbidden")
		default:

			slog.ErrorContext(
				ctx,
				"failed to list messages",
				"thread_id", threadIDHex,
				"error", err,
			)

			httputil.WriteError(
				w,
				http.StatusInternalServerError,
				"Internal Server Error",
			)
		}

		return
	}

	httputil.WriteJSON(w, http.StatusOK, messages)
}

func (h *ApplicationHandler) ApproveApplication(
	w http.ResponseWriter,
	r *http.Request,
) {
	ctx := r.Context()

	userID := middleware.GetUserID(ctx)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	threadIDHex := chi.URLParam(r, "threadID")

	threadID, err := bson.ObjectIDFromHex(threadIDHex)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid thread ID")
		return
	}

	if err := h.applicationSvc.ApproveApplication(ctx, threadID, userID); err != nil {

		switch err {

		case ErrApplicationNotFound:
			httputil.WriteError(w, http.StatusNotFound, "Application not found")

		case ErrForbidden:
			httputil.WriteError(w, http.StatusForbidden, "Forbidden")

		case ErrInvalidApplicationStatus:
			httputil.WriteError(w, http.StatusConflict, "Invalid application status")

		default:
			slog.ErrorContext(ctx,
				"approve application failed",
				"thread_id", threadIDHex,
				"error", err,
			)

			httputil.WriteError(w,
				http.StatusInternalServerError,
				"Internal Server Error",
			)
		}

		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *ApplicationHandler) RejectApplication(
	w http.ResponseWriter,
	r *http.Request,
) {
	ctx := r.Context()

	userID := middleware.GetUserID(ctx)
	if userID == "" {
		httputil.WriteError(w, http.StatusUnauthorized, "Unauthorized")
		return
	}

	threadIDHex := chi.URLParam(r, "threadID")

	threadID, err := bson.ObjectIDFromHex(threadIDHex)
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid thread ID")
		return
	}

	if err := h.applicationSvc.RejectApplication(ctx, threadID, userID); err != nil {

		switch err {

		case ErrApplicationNotFound:
			httputil.WriteError(w, http.StatusNotFound, "Application not found")

		case ErrForbidden:
			httputil.WriteError(w, http.StatusForbidden, "Forbidden")

		case ErrInvalidApplicationStatus:
			httputil.WriteError(w, http.StatusConflict, "Invalid application status")

		default:
			slog.ErrorContext(ctx,
				"approve application failed",
				"thread_id", threadIDHex,
				"error", err,
			)

			httputil.WriteError(w,
				http.StatusInternalServerError,
				"Internal Server Error",
			)
		}

		return
	}

	w.WriteHeader(http.StatusNoContent)
}
