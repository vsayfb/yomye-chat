package messagecursor

import (
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

var ErrInvalid = errors.New("invalid message cursor")

type Cursor struct {
	SentAt time.Time
	ID     bson.ObjectID
}

// Parse accepts the public message-history cursor pair. Both values must be
// omitted for the first page or provided together for subsequent pages.
func Parse(before, beforeID string) (*Cursor, error) {
	if before == "" && beforeID == "" {
		return nil, nil
	}
	if before == "" || beforeID == "" {
		return nil, ErrInvalid
	}

	sentAt, err := time.Parse(time.RFC3339Nano, before)
	if err != nil {
		return nil, ErrInvalid
	}

	id, err := bson.ObjectIDFromHex(beforeID)
	if err != nil {
		return nil, ErrInvalid
	}

	return &Cursor{SentAt: sentAt.UTC(), ID: id}, nil
}

// BeforeFilter returns the stable descending-page boundary for messages sorted
// by (sent_at DESC, _id DESC).
func BeforeFilter(cursor *Cursor) bson.M {
	if cursor == nil {
		return nil
	}

	return bson.M{
		"$or": bson.A{
			bson.M{"sent_at": bson.M{"$lt": cursor.SentAt}},
			bson.M{
				"sent_at": cursor.SentAt,
				"_id":     bson.M{"$lt": cursor.ID},
			},
		},
	}
}
