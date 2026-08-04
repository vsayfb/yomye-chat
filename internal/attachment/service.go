package attachment

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vsayfb/gig-platform-chat-service/pkg/objectstorage/r2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

var (
	ErrNotFound         = errors.New("attachment not found")
	ErrForbidden        = errors.New("attachment access forbidden")
	ErrInvalidInput     = errors.New("invalid attachment input")
	ErrInvalidState     = errors.New("invalid attachment state")
	ErrExpired          = errors.New("attachment expired")
	ErrTooMany          = errors.New("too many attachments")
	ErrDuplicate        = errors.New("duplicate attachment")
	ErrMetadataMismatch = errors.New("attachment metadata mismatch")
)

const (
	MaxImageBytes      int64 = 2 * 1024 * 1024
	MaxVideoBytes      int64 = 75 * 1024 * 1024
	MaxVideoDurationMS int64 = 60_000
	MaxPerMessage            = 2
	PendingTTL               = time.Hour
	ReadyTTL                 = 3 * time.Hour
)

type ScopeAuthorizer func(
	ctx context.Context,
	scope Scope,
	scopeID bson.ObjectID,
	userID string,
) error

type ObjectStore interface {
	NewObjectKey(scope, contentType string) (string, error)
	PresignUpload(context.Context, r2.UploadRequest) (*r2.UploadAuthorization, error)
	PresignDownload(context.Context, string, string) (*r2.DownloadAuthorization, error)
	Verify(context.Context, r2.VerifyRequest) (*r2.Object, error)
	Delete(context.Context, string) error
}

type Service struct {
	repo      Repository
	store     ObjectStore
	client    *mongo.Client
	authorize ScopeAuthorizer
	now       func() time.Time
}

func NewService(
	repo Repository,
	store ObjectStore,
	client *mongo.Client,
	authorize ScopeAuthorizer,
) *Service {
	return &Service{
		repo: repo, store: store, client: client, authorize: authorize,
		now: func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) AuthorizeUpload(
	ctx context.Context,
	input UploadInput,
) (*UploadResult, error) {
	if err := s.authorize(ctx, input.Scope, input.ScopeID, input.UploaderID); err != nil {
		return nil, err
	}

	name, err := safeOriginalName(input.OriginalName)
	if err != nil {
		return nil, err
	}
	contentType, maxSize, err := validateMedia(
		input.ContentType,
		input.SizeBytes,
		input.DurationMillis,
	)
	if err != nil {
		return nil, err
	}

	scopePath := storageScopePath(input.Scope, input.ScopeID)
	key, err := s.store.NewObjectKey(scopePath, contentType)
	if err != nil {
		return nil, fmt.Errorf("generate attachment key: %w", err)
	}

	now := s.now()
	expiresAt := now.Add(PendingTTL)
	item := &Attachment{
		Scope: input.Scope, ScopeID: input.ScopeID, UploaderID: input.UploaderID,
		ObjectKey: key, OriginalName: name, ContentType: contentType,
		SizeBytes: input.SizeBytes, DurationMillis: input.DurationMillis,
		Status: StatusPendingUpload, ExpiresAt: &expiresAt,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repo.Create(ctx, item); err != nil {
		return nil, err
	}

	authorization, err := s.store.PresignUpload(ctx, r2.UploadRequest{
		Key: key, ContentType: contentType, SizeBytes: input.SizeBytes,
		MaxSizeBytes: maxSize,
	})
	if err != nil {
		return nil, err
	}

	return &UploadResult{
		AttachmentID: item.ID,
		URL:          authorization.URL, Method: authorization.Method,
		Headers: authorization.Headers, ExpiresAt: authorization.ExpiresAt,
	}, nil
}

func (s *Service) Confirm(
	ctx context.Context,
	id bson.ObjectID,
	uploaderID string,
) (*Attachment, error) {
	item, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item == nil || item.Status == StatusDeleted {
		return nil, ErrNotFound
	}
	if item.UploaderID != uploaderID {
		return nil, ErrForbidden
	}
	if err := s.authorize(ctx, item.Scope, item.ScopeID, uploaderID); err != nil {
		return nil, err
	}
	if item.Status == StatusReady || item.Status == StatusAttached {
		return item, nil
	}
	if item.Status != StatusPendingUpload {
		return nil, ErrInvalidState
	}
	now := s.now()
	if item.ExpiresAt == nil || !item.ExpiresAt.After(now) {
		return nil, ErrExpired
	}

	_, maxSize, err := validateMedia(
		item.ContentType,
		item.SizeBytes,
		item.DurationMillis,
	)
	if err != nil {
		return nil, err
	}
	object, err := s.store.Verify(ctx, r2.VerifyRequest{
		Key: item.ObjectKey, ExpectedType: item.ContentType,
		ExpectedSize: item.SizeBytes, MaxSizeBytes: maxSize,
	})
	if err != nil {
		if errors.Is(err, r2.ErrInvalidObject) ||
			errors.Is(err, r2.ErrUnsupportedMedia) {
			return nil, ErrMetadataMismatch
		}
		return nil, err
	}

	readyExpiry := now.Add(ReadyTTL)
	updated, err := s.repo.MarkReady(
		ctx, id, uploaderID, object.ETag, object.SizeBytes,
		object.ContentType, readyExpiry,
	)
	if err != nil {
		return nil, err
	}
	if !updated {
		current, findErr := s.repo.FindByID(ctx, id)
		if findErr != nil {
			return nil, findErr
		}
		if current != nil &&
			(current.Status == StatusReady || current.Status == StatusAttached) {
			return current, nil
		}
		return nil, ErrInvalidState
	}

	item.Status = StatusReady
	item.ETag = object.ETag
	item.SizeBytes = object.SizeBytes
	item.ContentType = object.ContentType
	item.ExpiresAt = &readyExpiry
	item.UpdatedAt = now
	return item, nil
}

func (s *Service) Download(
	ctx context.Context,
	id bson.ObjectID,
	userID string,
) (*DownloadResult, error) {
	item, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item == nil || item.Status == StatusDeleted || item.Status == StatusDeleting {
		return nil, ErrNotFound
	}
	if item.Status != StatusReady && item.Status != StatusAttached {
		return nil, ErrInvalidState
	}
	if err := s.authorize(ctx, item.Scope, item.ScopeID, userID); err != nil {
		return nil, err
	}

	result, err := s.store.PresignDownload(
		ctx, item.ObjectKey, item.OriginalName,
	)
	if err != nil {
		return nil, err
	}
	return &DownloadResult{URL: result.URL, ExpiresAt: result.ExpiresAt}, nil
}

// CommitMessage runs message creation and READY -> ATTACHED transitions in one
// MongoDB transaction. The callback may be retried by the driver and therefore
// must contain database operations only.
func (s *Service) CommitMessage(
	ctx context.Context,
	scope Scope,
	scopeID bson.ObjectID,
	uploaderID string,
	ids []bson.ObjectID,
	create func(context.Context, []Ref) (bson.ObjectID, error),
) error {
	if len(ids) == 0 {
		_, err := create(ctx, nil)
		return err
	}
	if len(ids) > MaxPerMessage {
		return ErrTooMany
	}
	if hasDuplicateIDs(ids) {
		return ErrDuplicate
	}

	session, err := s.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)

	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		now := s.now()
		items, err := s.repo.FindReady(
			txCtx, ids, scope, scopeID, uploaderID, now,
		)
		if err != nil {
			return nil, err
		}
		if len(items) != len(ids) {
			return nil, ErrInvalidState
		}

		byID := make(map[bson.ObjectID]Ref, len(items))
		for i := range items {
			byID[items[i].ID] = items[i].Ref()
		}
		refs := make([]Ref, 0, len(ids))
		for _, id := range ids {
			ref, ok := byID[id]
			if !ok {
				return nil, ErrInvalidState
			}
			refs = append(refs, ref)
		}

		messageID, err := create(txCtx, refs)
		if err != nil {
			return nil, err
		}
		count, err := s.repo.MarkAttached(
			txCtx, ids, scope, scopeID, uploaderID, messageID, now,
		)
		if err != nil {
			return nil, err
		}
		if count != int64(len(ids)) {
			return nil, ErrInvalidState
		}
		return messageID, nil
	})
	return err
}

func (s *Service) CleanupOne(ctx context.Context) (bool, error) {
	now := s.now()
	item, err := s.repo.ClaimExpired(ctx, now)
	if err != nil || item == nil {
		return false, err
	}
	if err := s.store.Delete(ctx, item.ObjectKey); err != nil {
		delay := time.Duration(item.DeletionAttempts) * time.Minute
		if delay > time.Hour {
			delay = time.Hour
		}
		_ = s.repo.RetryDeletion(ctx, item.ID, now.Add(delay))
		return true, err
	}
	return true, s.repo.MarkDeleted(ctx, item.ID, now)
}

func validateMedia(
	contentType string,
	size int64,
	durationMillis int64,
) (string, int64, error) {
	parsed, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return "", 0, ErrInvalidInput
	}
	parsed = strings.ToLower(parsed)

	switch parsed {
	case "image/jpeg", "image/png", "image/webp", "image/gif":
		if size <= 0 || size > MaxImageBytes || durationMillis != 0 {
			return "", 0, ErrInvalidInput
		}
		return parsed, MaxImageBytes, nil
	case "video/mp4", "video/quicktime", "video/webm":
		if size <= 0 || size > MaxVideoBytes ||
			durationMillis <= 0 || durationMillis > MaxVideoDurationMS {
			return "", 0, ErrInvalidInput
		}
		return parsed, MaxVideoBytes, nil
	default:
		return "", 0, ErrInvalidInput
	}
}

func safeOriginalName(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	value = path.Base(value)
	if value == "" || value == "." || value == "/" ||
		utf8.RuneCountInString(value) > 255 {
		return "", ErrInvalidInput
	}
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return "", ErrInvalidInput
		}
	}
	return value, nil
}

func hasDuplicateIDs(ids []bson.ObjectID) bool {
	seen := make(map[bson.ObjectID]struct{}, len(ids))
	for _, id := range ids {
		if id.IsZero() {
			return true
		}
		if _, ok := seen[id]; ok {
			return true
		}
		seen[id] = struct{}{}
	}
	return false
}

func storageScopePath(scope Scope, scopeID bson.ObjectID) string {
	switch scope {
	case ScopeDirect:
		return fmt.Sprintf("conversations/%s", scopeID.Hex())
	case ScopeApplication:
		return fmt.Sprintf("applications/%s", scopeID.Hex())
	default:
		return ""
	}
}
