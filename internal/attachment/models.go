package attachment

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type Scope string

const (
	ScopeDirect      Scope = "direct"
	ScopeApplication Scope = "application"
)

type Status string

const (
	StatusPendingUpload Status = "PENDING_UPLOAD"
	StatusReady         Status = "READY"
	StatusAttached      Status = "ATTACHED"
	StatusDeleting      Status = "DELETING"
	StatusDeleted       Status = "DELETED"
)

type Attachment struct {
	ID bson.ObjectID `bson:"_id,omitempty" json:"id"`

	Scope      Scope         `bson:"scope" json:"scope"`
	ScopeID    bson.ObjectID `bson:"scope_id,omitempty" json:"scope_id,omitempty"`
	UploaderID string        `bson:"uploader_id" json:"uploader_id"`
	// Provisional attachments belong to a first-contact message before its
	// conversation/application exists. These fields are removed atomically when
	// the first message creates the durable scope.
	Provisional bool   `bson:"provisional,omitempty" json:"-"`
	TargetID    string `bson:"target_id,omitempty" json:"-"`
	GigID       string `bson:"gig_id,omitempty" json:"-"`

	ObjectKey      string `bson:"object_key" json:"-"`
	OriginalName   string `bson:"original_filename" json:"original_filename"`
	ContentType    string `bson:"content_type" json:"content_type"`
	SizeBytes      int64  `bson:"size_bytes" json:"size_bytes"`
	DurationMillis int64  `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`
	ETag           string `bson:"etag,omitempty" json:"etag,omitempty"`

	Status    Status         `bson:"status" json:"status"`
	MessageID *bson.ObjectID `bson:"message_id,omitempty" json:"message_id,omitempty"`

	ExpiresAt        *time.Time `bson:"expires_at,omitempty" json:"expires_at,omitempty"`
	CreatedAt        time.Time  `bson:"created_at" json:"created_at"`
	UpdatedAt        time.Time  `bson:"updated_at" json:"updated_at"`
	DeletionAttempts int        `bson:"deletion_attempts,omitempty" json:"-"`
	NextDeletionAt   *time.Time `bson:"next_deletion_at,omitempty" json:"-"`
	DeletedAt        *time.Time `bson:"deleted_at,omitempty" json:"-"`
}

// Ref is embedded in messages as an immutable display snapshot. Download
// authorization still resolves the attachment record by ID.
type Ref struct {
	ID             bson.ObjectID `bson:"id" json:"id"`
	OriginalName   string        `bson:"original_filename" json:"original_filename"`
	ContentType    string        `bson:"content_type" json:"content_type"`
	SizeBytes      int64         `bson:"size_bytes" json:"size_bytes"`
	DurationMillis int64         `bson:"duration_ms,omitempty" json:"duration_ms,omitempty"`
}

func (a *Attachment) Ref() Ref {
	return Ref{
		ID:             a.ID,
		OriginalName:   a.OriginalName,
		ContentType:    a.ContentType,
		SizeBytes:      a.SizeBytes,
		DurationMillis: a.DurationMillis,
	}
}

type UploadInput struct {
	Scope          Scope
	ScopeID        bson.ObjectID
	UploaderID     string
	OriginalName   string
	ContentType    string
	SizeBytes      int64
	DurationMillis int64
}

type FirstContactUploadInput struct {
	Scope          Scope
	UploaderID     string
	TargetID       string
	GigID          string
	OriginalName   string
	ContentType    string
	SizeBytes      int64
	DurationMillis int64
}

type UploadResult struct {
	AttachmentID bson.ObjectID     `json:"attachment_id"`
	Key          string            `json:"-"`
	URL          string            `json:"url"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers"`
	ExpiresAt    time.Time         `json:"expires_at"`
}

type DownloadResult struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}
