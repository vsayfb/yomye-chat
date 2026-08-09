package attachment

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vsayfb/gig-platform-chat-service/pkg/objectstorage/r2"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestAuthorizeUploadCreatesPendingRecordBeforePresigning(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo}
	service := newTestService(repo, store)
	scopeID := bson.NewObjectID()

	result, err := service.AuthorizeUpload(context.Background(), UploadInput{
		Scope: ScopeDirect, ScopeID: scopeID, UploaderID: "user-a",
		OriginalName: "../../photo.png", ContentType: "image/png",
		SizeBytes: 1024,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AttachmentID.IsZero() || store.presignedBeforeCreate {
		t.Fatalf("invalid upload result or ordering: %+v", result)
	}
	item := repo.items[result.AttachmentID]
	if item.Status != StatusPendingUpload ||
		item.OriginalName != "photo.png" ||
		!strings.HasPrefix(item.ObjectKey, "chat/conversations/"+scopeID.Hex()+"/") {
		t.Fatalf("unexpected pending attachment: %+v", item)
	}
}

func TestAuthorizeFirstContactUploadRejectsMissingTargetBeforeSigning(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo}
	service := newTestService(repo, store)
	service.authorizeFirst = func(
		context.Context, Scope, string, string, string,
	) error {
		return ErrTargetNotFound
	}

	_, err := service.AuthorizeFirstContactUpload(
		context.Background(),
		FirstContactUploadInput{
			Scope: ScopeDirect, UploaderID: "sender", TargetID: "missing",
			OriginalName: "photo.jpg", ContentType: "image/jpeg", SizeBytes: 1024,
		},
	)
	if !errors.Is(err, ErrTargetNotFound) {
		t.Fatalf("AuthorizeFirstContactUpload() error = %v, want target not found", err)
	}
	if store.uploadCalls != 0 || len(repo.items) != 0 {
		t.Fatalf("missing target created private upload state: calls=%d items=%d",
			store.uploadCalls, len(repo.items))
	}
}

func TestAuthorizeFirstContactUploadRejectsExistingScopeBeforeSigning(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo}
	service := newTestService(repo, store)
	service.authorizeFirst = func(
		context.Context, Scope, string, string, string,
	) error {
		return ErrScopeAlreadyExists
	}

	_, err := service.AuthorizeFirstContactUpload(
		context.Background(),
		FirstContactUploadInput{
			Scope: ScopeDirect, UploaderID: "sender", TargetID: "target",
			OriginalName: "photo.jpg", ContentType: "image/jpeg", SizeBytes: 1024,
		},
	)
	if !errors.Is(err, ErrScopeAlreadyExists) {
		t.Fatalf("AuthorizeFirstContactUpload() error = %v, want existing scope", err)
	}
	if store.uploadCalls != 0 || len(repo.items) != 0 {
		t.Fatalf("existing scope created private upload state: calls=%d items=%d",
			store.uploadCalls, len(repo.items))
	}
}

func TestAuthorizeFirstContactUploadCreatesProvisionalBinding(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo}
	service := newTestService(repo, store)
	result, err := service.AuthorizeFirstContactUpload(
		context.Background(),
		FirstContactUploadInput{
			Scope: ScopeApplication, UploaderID: "sender", TargetID: "target",
			GigID: "gig", OriginalName: "photo.jpg",
			ContentType: "image/jpeg", SizeBytes: 1024,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	item := repo.items[result.AttachmentID]
	if item == nil || !item.Provisional || !item.ScopeID.IsZero() ||
		item.TargetID != "target" || item.GigID != "gig" ||
		!strings.HasPrefix(item.ObjectKey, "chat/pending/application/") {
		t.Fatalf("unexpected provisional attachment: %+v", item)
	}
	if store.uploadCalls != 1 || store.presignedBeforeCreate {
		t.Fatalf("authorization ordering was unsafe: calls=%d before_create=%t",
			store.uploadCalls, store.presignedBeforeCreate)
	}
}

func TestAuthorizeUploadEnforcesMediaLimits(t *testing.T) {
	t.Parallel()

	tests := []UploadInput{
		{ContentType: "image/png", SizeBytes: MaxImageBytes + 1},
		{ContentType: "video/mp4", SizeBytes: MaxVideoBytes + 1, DurationMillis: 1000},
		{ContentType: "video/mp4", SizeBytes: 1024, DurationMillis: MaxVideoDurationMS + 1},
		{ContentType: "video/mp4", SizeBytes: 1024},
		{ContentType: "image/svg+xml", SizeBytes: 1024},
	}
	for _, input := range tests {
		repo := newFakeRepository()
		service := newTestService(repo, &fakeStore{repo: repo})
		input.Scope = ScopeDirect
		input.ScopeID = bson.NewObjectID()
		input.UploaderID = "user-a"
		input.OriginalName = "file"
		if _, err := service.AuthorizeUpload(context.Background(), input); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("AuthorizeUpload(%+v) error = %v, want ErrInvalidInput", input, err)
		}
	}
}

func TestConfirmVerifiesMetadataAndIsIdempotent(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo}
	service := newTestService(repo, store)
	now := service.now()
	id := bson.NewObjectID()
	scopeID := bson.NewObjectID()
	repo.items[id] = &Attachment{
		ID: id, Scope: ScopeApplication, ScopeID: scopeID, UploaderID: "user-a",
		ObjectKey:    "chat/application/" + scopeID.Hex() + "/object.jpg",
		OriginalName: "photo.jpg", ContentType: "image/jpeg", SizeBytes: 100,
		Status: StatusPendingUpload, ExpiresAt: timePointer(now.Add(time.Hour)),
	}
	store.object = &r2.Object{
		Key: repo.items[id].ObjectKey, ContentType: "image/jpeg",
		SizeBytes: 100, ETag: "etag",
	}

	first, err := service.Confirm(context.Background(), id, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Confirm(context.Background(), id, "user-a")
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != StatusReady || second.Status != StatusReady ||
		repo.markReadyCalls != 1 || store.verifyCalls != 1 {
		t.Fatalf("confirmation was not idempotent: first=%+v second=%+v", first, second)
	}
}

func TestDownloadReauthorizesCurrentParticipation(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo}
	service := newTestService(repo, store)
	service.authorize = func(context.Context, Scope, bson.ObjectID, string) error {
		return ErrForbidden
	}
	id := bson.NewObjectID()
	repo.items[id] = &Attachment{
		ID: id, Scope: ScopeDirect, ScopeID: bson.NewObjectID(),
		UploaderID: "user-a", ObjectKey: "chat/direct/id/object.jpg",
		Status: StatusAttached,
	}

	if _, err := service.Download(context.Background(), id, "user-b"); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Download() error = %v, want ErrForbidden", err)
	}
	if store.downloadCalls != 0 {
		t.Fatal("download URL was signed before authorization")
	}
}

type fakeRepository struct {
	items              map[bson.ObjectID]*Attachment
	markReadyCalls     int
	claimExpired       *Attachment
	markDeletedCalls   int
	retryDeletionCalls int
	retryDeletionNext  time.Time
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{items: make(map[bson.ObjectID]*Attachment)}
}

func (r *fakeRepository) EnsureIndexes(context.Context) error { return nil }
func (r *fakeRepository) Create(_ context.Context, item *Attachment) error {
	if item.ID.IsZero() {
		item.ID = bson.NewObjectID()
	}
	r.items[item.ID] = item
	return nil
}
func (r *fakeRepository) FindByID(_ context.Context, id bson.ObjectID) (*Attachment, error) {
	return r.items[id], nil
}
func (r *fakeRepository) MarkReady(
	_ context.Context, id bson.ObjectID, _ string, etag string,
	size int64, contentType string, expiresAt time.Time,
) (bool, error) {
	r.markReadyCalls++
	item := r.items[id]
	if item.Status != StatusPendingUpload {
		return false, nil
	}
	item.Status, item.ETag = StatusReady, etag
	item.SizeBytes, item.ContentType, item.ExpiresAt = size, contentType, &expiresAt
	return true, nil
}
func (r *fakeRepository) FindReady(
	context.Context, []bson.ObjectID, Scope, bson.ObjectID, string, time.Time,
) ([]Attachment, error) {
	return nil, nil
}
func (r *fakeRepository) FindReadyProvisional(
	context.Context, []bson.ObjectID, Scope, string, string, string, time.Time,
) ([]Attachment, error) {
	return nil, nil
}
func (r *fakeRepository) MarkAttached(
	context.Context, []bson.ObjectID, Scope, bson.ObjectID, string,
	bson.ObjectID, time.Time,
) (int64, error) {
	return 0, nil
}
func (r *fakeRepository) MarkProvisionalAttached(
	context.Context, []bson.ObjectID, Scope, string, string, string,
	bson.ObjectID, bson.ObjectID, time.Time,
) (int64, error) {
	return 0, nil
}
func (r *fakeRepository) ClaimExpired(context.Context, time.Time) (*Attachment, error) {
	return r.claimExpired, nil
}
func (r *fakeRepository) MarkDeleted(context.Context, bson.ObjectID, time.Time) error {
	r.markDeletedCalls++
	return nil
}
func (r *fakeRepository) RetryDeletion(_ context.Context, _ bson.ObjectID, next time.Time) error {
	r.retryDeletionCalls++
	r.retryDeletionNext = next
	return nil
}

type fakeStore struct {
	repo                  *fakeRepository
	object                *r2.Object
	presignedBeforeCreate bool
	verifyCalls           int
	downloadCalls         int
	deleteCalls           int
	deleteErr             error
	uploadCalls           int
}

func (s *fakeStore) NewObjectKey(scope, contentType string) (string, error) {
	extension := ".jpg"
	if contentType == "image/png" {
		extension = ".png"
	}
	return "chat/" + scope + "/generated" + extension, nil
}
func (s *fakeStore) PresignUpload(
	_ context.Context,
	request r2.UploadRequest,
) (*r2.UploadAuthorization, error) {
	s.uploadCalls++
	found := false
	for _, item := range s.repo.items {
		if item.ObjectKey == request.Key {
			found = true
		}
	}
	s.presignedBeforeCreate = !found
	return &r2.UploadAuthorization{
		URL: "https://upload", Method: "PUT", Headers: map[string]string{},
		ExpiresAt: time.Now().Add(time.Minute),
	}, nil
}
func (s *fakeStore) PresignDownload(
	context.Context,
	string,
	string,
) (*r2.DownloadAuthorization, error) {
	s.downloadCalls++
	return &r2.DownloadAuthorization{URL: "https://download"}, nil
}
func (s *fakeStore) Verify(context.Context, r2.VerifyRequest) (*r2.Object, error) {
	s.verifyCalls++
	return s.object, nil
}
func (s *fakeStore) Delete(context.Context, string) error {
	s.deleteCalls++
	return s.deleteErr
}

func newTestService(repo Repository, store ObjectStore) *Service {
	service := NewService(
		repo, store, nil,
		func(context.Context, Scope, bson.ObjectID, string) error { return nil },
		func(context.Context, Scope, string, string, string) error { return nil },
	)
	fixed := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixed }
	return service
}

func timePointer(value time.Time) *time.Time { return &value }

func TestCleanupOneDeletesClaimedObjectAndMarksRecordDeleted(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo}
	service := newTestService(repo, store)
	repo.claimExpired = &Attachment{ID: bson.NewObjectID(), ObjectKey: "chat/expired", DeletionAttempts: 1}

	worked, err := service.CleanupOne(context.Background())
	if err != nil {
		t.Fatalf("CleanupOne() error = %v", err)
	}
	if !worked || store.deleteCalls != 1 || repo.markDeletedCalls != 1 {
		t.Fatalf("cleanup result worked=%v deleteCalls=%d markDeletedCalls=%d", worked, store.deleteCalls, repo.markDeletedCalls)
	}
}

func TestCleanupOneSchedulesRetryWhenObjectDeletionFails(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo, deleteErr: errors.New("storage unavailable")}
	service := newTestService(repo, store)
	now := service.now()
	repo.claimExpired = &Attachment{
		ID: bson.NewObjectID(), ObjectKey: "chat/expired", DeletionAttempts: 3,
	}

	worked, err := service.CleanupOne(context.Background())
	if err == nil || !worked {
		t.Fatalf("CleanupOne() worked=%v err=%v, want worked with deletion error", worked, err)
	}
	if repo.retryDeletionCalls != 1 {
		t.Fatalf("retry calls = %d, want 1", repo.retryDeletionCalls)
	}
	if want := now.Add(3 * time.Minute); !repo.retryDeletionNext.Equal(want) {
		t.Fatalf("retry time = %v, want %v", repo.retryDeletionNext, want)
	}
	if repo.markDeletedCalls != 0 {
		t.Fatal("failed storage deletion marked attachment deleted")
	}
}

func TestCleanupOneDoesNothingWhenNothingIsClaimed(t *testing.T) {
	t.Parallel()

	repo := newFakeRepository()
	store := &fakeStore{repo: repo}
	service := newTestService(repo, store)

	worked, err := service.CleanupOne(context.Background())
	if err != nil || worked {
		t.Fatalf("CleanupOne() worked=%v err=%v, want no work", worked, err)
	}
	if store.deleteCalls != 0 {
		t.Fatal("storage delete called without a claimed attachment")
	}
}
