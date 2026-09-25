package attachment

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"mime"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/vsayfb/gig-platform-chat-service/pkg/objectstorage/r2"
	chatTracing "github.com/vsayfb/gig-platform-chat-service/pkg/tracing"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.opentelemetry.io/otel/attribute"
)

var (
	ErrNotFound               = errors.New("attachment not found")
	ErrForbidden              = errors.New("attachment access forbidden")
	ErrInvalidInput           = errors.New("invalid attachment input")
	ErrInvalidState           = errors.New("invalid attachment state")
	ErrExpired                = errors.New("attachment expired")
	ErrTooMany                = errors.New("too many attachments")
	ErrDuplicate              = errors.New("duplicate attachment")
	ErrMetadataMismatch       = errors.New("attachment metadata mismatch")
	ErrTargetNotFound         = errors.New("target user not found")
	ErrGigNotFound            = errors.New("gig not found")
	ErrDirectMessagesDisabled = errors.New("target user does not allow direct messages")
	ErrScopeAlreadyExists     = errors.New("conversation already exists")
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

type FirstContactAuthorizer func(
	ctx context.Context,
	scope Scope,
	uploaderID string,
	targetID string,
	gigID string,
) error

type ObjectStore interface {
	NewObjectKey(scope, contentType string) (string, error)
	ProcessedObjectKey(attachmentID, contentType string) (string, error)
	PresignUpload(context.Context, r2.UploadRequest) (*r2.UploadAuthorization, error)
	PresignDownload(context.Context, string, string) (*r2.DownloadAuthorization, error)
	PresignView(context.Context, string) (*r2.DownloadAuthorization, error)
	Verify(context.Context, r2.VerifyRequest) (*r2.Object, error)
	Delete(context.Context, string) error
}

type Service struct {
	repo           Repository
	store          ObjectStore
	client         *mongo.Client
	authorize      ScopeAuthorizer
	authorizeFirst FirstContactAuthorizer
	processor      Processor
	now            func() time.Time
}

func NewService(
	repo Repository,
	store ObjectStore,
	client *mongo.Client,
	authorize ScopeAuthorizer,
	authorizeFirst FirstContactAuthorizer,
	processors ...Processor,
) *Service {
	s := &Service{
		repo: repo, store: store, client: client,
		authorize: authorize, authorizeFirst: authorizeFirst,
		now: func() time.Time { return time.Now().UTC() },
	}
	if len(processors) > 0 {
		s.processor = processors[0]
	}
	return s
}

func (s *Service) AuthorizeFirstContactUpload(
	ctx context.Context,
	input FirstContactUploadInput,
) (result *UploadResult, err error) {
	ctx, span := chatTracing.StartDomain(ctx, "chat.attachment.authorize_first_contact_upload",
		attribute.String("chat.domain", string(input.Scope)),
		attribute.String("attachment.media_category", mediaCategory(input.ContentType)),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

	if s.authorizeFirst == nil || input.UploaderID == "" || input.TargetID == "" ||
		input.UploaderID == input.TargetID {
		return nil, ErrInvalidInput
	}
	if err := s.authorizeFirst(
		ctx, input.Scope, input.UploaderID, input.TargetID, input.GigID,
	); err != nil {
		return nil, err
	}

	name, err := safeOriginalName(input.OriginalName)
	if err != nil {
		return nil, err
	}
	contentType, maxSize, err := validateMedia(
		input.ContentType, input.SizeBytes, input.DurationMillis,
	)
	if err != nil {
		return nil, err
	}

	now := s.now()
	expiresAt := now.Add(PendingTTL)
	item := &Attachment{
		ID: bson.NewObjectID(), Scope: input.Scope,
		UploaderID: input.UploaderID, Provisional: true,
		TargetID: input.TargetID, GigID: input.GigID,
		OriginalName: name, ContentType: contentType,
		SizeBytes: input.SizeBytes, DurationMillis: input.DurationMillis,
		Status: StatusPendingUpload, ExpiresAt: &expiresAt,
		CreatedAt: now, UpdatedAt: now,
	}
	key, err := s.store.NewObjectKey(
		fmt.Sprintf("pending/%s/%s", input.Scope, item.ID.Hex()),
		contentType,
	)
	if err != nil {
		return nil, fmt.Errorf("generate provisional attachment key: %w", err)
	}
	item.ObjectKey = key

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

func (s *Service) AuthorizeUpload(
	ctx context.Context,
	input UploadInput,
) (result *UploadResult, err error) {
	ctx, span := chatTracing.StartDomain(ctx, "chat.attachment.authorize_upload",
		attribute.String("chat.domain", string(input.Scope)),
		attribute.String("attachment.media_category", mediaCategory(input.ContentType)),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

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
) (result *Attachment, err error) {
	ctx, span := chatTracing.StartDomain(ctx, "chat.attachment.confirm")
	defer func() { chatTracing.EndDomain(span, err) }()

	item, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item == nil || item.Status == StatusDeleted {
		return nil, ErrNotFound
	}
	span.SetAttributes(
		attribute.String("chat.domain", string(item.Scope)),
		attribute.String("attachment.media_category", mediaCategory(item.ContentType)),
	)
	if item.UploaderID != uploaderID {
		return nil, ErrForbidden
	}
	if item.Provisional {
		if s.authorizeFirst == nil {
			return nil, ErrInvalidState
		}
		if err := s.authorizeFirst(
			ctx, item.Scope, uploaderID, item.TargetID, item.GigID,
		); err != nil {
			return nil, err
		}
	} else {
		if err := s.authorize(ctx, item.Scope, item.ScopeID, uploaderID); err != nil {
			return nil, err
		}
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
	_, err = s.store.Verify(ctx, r2.VerifyRequest{
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
	if s.processor == nil {
		return nil, ErrProcessorUnavailable
	}
	cleanType := "image/png"
	cleanExtension := ".png"
	if strings.HasPrefix(item.ContentType, "video/") {
		cleanType = "video/mp4"
		cleanExtension = ".mp4"
	}
	cleanKey, err := s.store.ProcessedObjectKey(id.Hex(), cleanType)
	if err != nil {
		return nil, err
	}
	cleanAuthorization, err := s.store.PresignUpload(ctx, r2.UploadRequest{
		Key: cleanKey, ContentType: cleanType, SizeBytes: maxSize, MaxSizeBytes: maxSize,
	})
	if err != nil {
		return nil, err
	}
	sourceAuthorization, err := s.store.PresignDownload(ctx, item.ObjectKey, item.OriginalName)
	if err != nil {
		return nil, err
	}
	processed, err := s.processor.Process(ctx, sourceAuthorization.URL, cleanAuthorization,
		item.ContentType, maxSize, item.DurationMillis)
	if err != nil {
		if !errors.Is(err, ErrProcessorUnavailable) {
			return nil, err
		}
		// The validator may have completed its write-once PUT before its response
		// was lost. Only the validator ever receives a signed PUT for this key.
		prior, verifyErr := s.store.Verify(ctx, r2.VerifyRequest{
			Key: cleanKey, ExpectedType: cleanType, MaxSizeBytes: maxSize,
		})
		if verifyErr != nil {
			return nil, err
		}
		processed = &ProcessedMedia{MIME: cleanType, SizeBytes: prior.SizeBytes}
		if cleanType == "video/mp4" {
			processed.DurationMillis = item.DurationMillis
		}
	}
	if processed.MIME != cleanType || processed.SizeBytes <= 0 || processed.SizeBytes > maxSize ||
		(strings.HasPrefix(cleanType, "video/") && (processed.DurationMillis <= 0 || processed.DurationMillis > MaxVideoDurationMS)) ||
		(cleanType == "image/png" && processed.DurationMillis != 0) {
		return nil, ErrMetadataMismatch
	}
	cleanObject, err := s.store.Verify(ctx, r2.VerifyRequest{
		Key: cleanKey, ExpectedType: cleanType, ExpectedSize: processed.SizeBytes, MaxSizeBytes: maxSize,
	})
	if err != nil {
		if errors.Is(err, r2.ErrInvalidObject) {
			return nil, ErrMetadataMismatch
		}
		return nil, err
	}
	name := strings.TrimSuffix(item.OriginalName, path.Ext(item.OriginalName)) + cleanExtension
	originalKey := item.ObjectKey

	readyExpiry := now.Add(ReadyTTL)
	updated, err := s.repo.MarkReady(
		ctx, id, uploaderID, cleanObject.ETag, cleanObject.SizeBytes,
		cleanObject.ContentType, cleanKey, name, processed.DurationMillis, readyExpiry,
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
	item.ETag = cleanObject.ETag
	item.ObjectKey = cleanKey
	item.OriginalName = name
	item.SizeBytes = cleanObject.SizeBytes
	item.ContentType = cleanObject.ContentType
	item.DurationMillis = processed.DurationMillis
	item.ExpiresAt = &readyExpiry
	item.UpdatedAt = now
	if err := s.store.Delete(ctx, originalKey); err != nil {
		slog.WarnContext(ctx, "could not delete quarantined attachment object", "err", err)
	}
	return item, nil
}

func (s *Service) Download(
	ctx context.Context,
	id bson.ObjectID,
	userID string,
) (result *DownloadResult, err error) {
	ctx, span := chatTracing.StartDomain(ctx, "chat.attachment.authorize_download")
	defer func() { chatTracing.EndDomain(span, err) }()

	item, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item == nil || item.Status == StatusDeleted || item.Status == StatusDeleting {
		return nil, ErrNotFound
	}
	span.SetAttributes(
		attribute.String("chat.domain", string(item.Scope)),
		attribute.String("attachment.media_category", mediaCategory(item.ContentType)),
	)
	if item.Status != StatusReady && item.Status != StatusAttached {
		return nil, ErrInvalidState
	}
	if item.Provisional {
		return nil, ErrInvalidState
	}
	if err := s.authorize(ctx, item.Scope, item.ScopeID, userID); err != nil {
		return nil, err
	}

	authorization, err := s.store.PresignDownload(
		ctx, item.ObjectKey, item.OriginalName,
	)
	if err != nil {
		return nil, err
	}
	return &DownloadResult{URL: authorization.URL, ExpiresAt: authorization.ExpiresAt}, nil
}

func (s *Service) View(ctx context.Context, id bson.ObjectID, userID string) (*DownloadResult, error) {
	item, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item == nil || (item.Status != StatusReady && item.Status != StatusAttached) || item.Provisional {
		return nil, ErrNotFound
	}
	if err := s.authorize(ctx, item.Scope, item.ScopeID, userID); err != nil {
		return nil, err
	}
	if !strings.HasPrefix(item.ObjectKey, "chat/processed/") ||
		(item.ContentType != "image/png" && item.ContentType != "video/mp4") {
		return nil, ErrInvalidState
	}
	url, err := s.store.PresignView(ctx, item.ObjectKey)
	if err != nil {
		return nil, err
	}
	return &DownloadResult{URL: url.URL, ExpiresAt: url.ExpiresAt}, nil
}

// CommitMessage runs message creation and READY -> ATTACHED transitions in one
// MongoDB transaction. The callback returns whether it created a new durable
// message. An idempotent replay returns created=false so already-attached media
// is not transitioned a second time. The callback may be retried by the driver
// and therefore must contain database operations only.
func (s *Service) CommitMessage(
	ctx context.Context,
	scope Scope,
	scopeID bson.ObjectID,
	uploaderID string,
	targetID string,
	gigID string,
	ids []bson.ObjectID,
	create func(context.Context, []Ref) (bson.ObjectID, bson.ObjectID, bool, error),
) (err error) {
	ctx, span := chatTracing.StartDomain(ctx, "chat.attachment.commit",
		attribute.String("chat.domain", string(scope)),
		attribute.Int("message.attachment_count", len(ids)),
		attribute.Bool("db.transaction", len(ids) > 0),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

	if len(ids) == 0 {
		_, _, created, err := create(ctx, nil)
		span.SetAttributes(attribute.Bool("message.created", created))
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

	transactionAttempts := 0
	messageCreated := false
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		transactionAttempts++
		now := s.now()
		var items []Attachment
		var findErr error
		if scopeID.IsZero() {
			items, findErr = s.repo.FindReadyProvisional(
				txCtx, ids, scope, uploaderID, targetID, gigID, now,
			)
		} else {
			items, findErr = s.repo.FindReady(
				txCtx, ids, scope, scopeID, uploaderID, now,
			)
		}
		if findErr != nil {
			return nil, findErr
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

		messageID, actualScopeID, created, err := create(txCtx, refs)
		if err != nil {
			return nil, err
		}
		messageCreated = created
		if !created {
			return messageID, nil
		}
		if actualScopeID.IsZero() {
			return nil, ErrInvalidState
		}
		if !scopeID.IsZero() && actualScopeID != scopeID {
			return nil, ErrInvalidState
		}
		var count int64
		if scopeID.IsZero() {
			count, err = s.repo.MarkProvisionalAttached(
				txCtx, ids, scope, uploaderID, targetID, gigID,
				actualScopeID, messageID, now,
			)
		} else {
			count, err = s.repo.MarkAttached(
				txCtx, ids, scope, scopeID, uploaderID, messageID, now,
			)
		}
		if err != nil {
			return nil, err
		}
		if count != int64(len(ids)) {
			return nil, ErrInvalidState
		}
		return messageID, nil
	})
	span.SetAttributes(
		attribute.Int("db.transaction_attempts", transactionAttempts),
		attribute.Bool("message.created", messageCreated),
	)
	return err
}

func (s *Service) CleanupOne(ctx context.Context) (processed bool, err error) {
	now := s.now()
	item, err := s.repo.ClaimExpired(ctx, now)
	if err != nil || item == nil {
		return false, err
	}
	ctx, span := chatTracing.StartDomain(ctx, "chat.attachment.cleanup",
		attribute.String("chat.domain", string(item.Scope)),
		attribute.String("attachment.status", string(item.Status)),
		attribute.Int("attachment.deletion_attempt", item.DeletionAttempts),
	)
	defer func() { chatTracing.EndDomain(span, err) }()

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

func mediaCategory(contentType string) string {
	parsed, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil {
		return "unknown"
	}
	parsed = strings.ToLower(parsed)
	if strings.HasPrefix(parsed, "image/") {
		return "image"
	}
	if strings.HasPrefix(parsed, "video/") {
		return "video"
	}
	return "other"
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
