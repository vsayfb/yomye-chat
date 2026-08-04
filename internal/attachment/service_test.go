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
	items          map[bson.ObjectID]*Attachment
	markReadyCalls int
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
func (r *fakeRepository) MarkAttached(
	context.Context, []bson.ObjectID, Scope, bson.ObjectID, string,
	bson.ObjectID, time.Time,
) (int64, error) {
	return 0, nil
}
func (r *fakeRepository) ClaimExpired(context.Context, time.Time) (*Attachment, error) {
	return nil, nil
}
func (r *fakeRepository) MarkDeleted(context.Context, bson.ObjectID, time.Time) error {
	return nil
}
func (r *fakeRepository) RetryDeletion(context.Context, bson.ObjectID, time.Time) error {
	return nil
}

type fakeStore struct {
	repo                  *fakeRepository
	object                *r2.Object
	presignedBeforeCreate bool
	verifyCalls           int
	downloadCalls         int
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
func (s *fakeStore) Delete(context.Context, string) error { return nil }

func newTestService(repo Repository, store ObjectStore) *Service {
	service := NewService(
		repo, store, nil,
		func(context.Context, Scope, bson.ObjectID, string) error { return nil },
	)
	fixed := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return fixed }
	return service
}

func timePointer(value time.Time) *time.Time { return &value }
