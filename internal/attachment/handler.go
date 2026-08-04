package attachment

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vsayfb/gig-platform-chat-service/pkg/httputil"
	"github.com/vsayfb/gig-platform-chat-service/pkg/middleware"
	"github.com/vsayfb/gig-platform-chat-service/pkg/objectstorage/r2"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type Handler struct {
	service *Service
}

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

type uploadRequest struct {
	OriginalFilename string `json:"original_filename"`
	ContentType      string `json:"content_type"`
	SizeBytes        int64  `json:"size_bytes"`
	DurationMillis   int64  `json:"duration_ms,omitempty"`
}

func (h *Handler) AuthorizeDirectUpload(w http.ResponseWriter, r *http.Request) {
	h.authorizeUpload(w, r, ScopeDirect, "conversationID")
}

func (h *Handler) AuthorizeApplicationUpload(w http.ResponseWriter, r *http.Request) {
	h.authorizeUpload(w, r, ScopeApplication, "threadID")
}

func (h *Handler) authorizeUpload(
	w http.ResponseWriter,
	r *http.Request,
	scope Scope,
	param string,
) {
	scopeID, err := bson.ObjectIDFromHex(chi.URLParam(r, param))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid conversation ID")
		return
	}
	var request uploadRequest
	if err := httputil.DecodeJSON(r, &request); err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid request body")
		return
	}

	result, err := h.service.AuthorizeUpload(r.Context(), UploadInput{
		Scope: scope, ScopeID: scopeID,
		UploaderID:   middleware.GetUserID(r.Context()),
		OriginalName: request.OriginalFilename,
		ContentType:  request.ContentType, SizeBytes: request.SizeBytes,
		DurationMillis: request.DurationMillis,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusCreated, result)
}

func (h *Handler) Confirm(w http.ResponseWriter, r *http.Request) {
	id, ok := attachmentID(w, r)
	if !ok {
		return
	}
	result, err := h.service.Confirm(
		r.Context(), id, middleware.GetUserID(r.Context()),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result.Ref())
}

func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	id, ok := attachmentID(w, r)
	if !ok {
		return
	}
	result, err := h.service.Download(
		r.Context(), id, middleware.GetUserID(r.Context()),
	)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	httputil.WriteJSON(w, http.StatusOK, result)
}

func attachmentID(w http.ResponseWriter, r *http.Request) (bson.ObjectID, bool) {
	id, err := bson.ObjectIDFromHex(chi.URLParam(r, "attachmentID"))
	if err != nil {
		httputil.WriteError(w, http.StatusBadRequest, "Invalid attachment ID")
		return bson.NilObjectID, false
	}
	return id, true
}

func (h *Handler) writeError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		httputil.WriteError(w, http.StatusNotFound, "Attachment not found")
	case errors.Is(err, ErrForbidden):
		httputil.WriteError(w, http.StatusForbidden, "Attachment access denied")
	case errors.Is(err, ErrInvalidInput), errors.Is(err, r2.ErrUnsupportedMedia):
		httputil.WriteError(w, http.StatusUnprocessableEntity, "Invalid attachment metadata")
	case errors.Is(err, ErrExpired):
		httputil.WriteError(w, http.StatusGone, "Attachment upload expired")
	case errors.Is(err, ErrInvalidState):
		httputil.WriteError(w, http.StatusConflict, "Attachment is not available")
	case errors.Is(err, ErrMetadataMismatch):
		httputil.WriteError(w, http.StatusUnprocessableEntity, "Uploaded object does not match")
	default:
		slog.ErrorContext(r.Context(), "attachment request failed", "error", err)
		httputil.WriteError(w, http.StatusInternalServerError, "Attachment request failed")
	}
}
