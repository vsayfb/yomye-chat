package messagecursor

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestParseRequiresCompleteCursorPair(t *testing.T) {
	t.Parallel()

	id := bson.NewObjectID().Hex()
	for _, test := range []struct {
		name     string
		before   string
		beforeID string
	}{
		{name: "timestamp only", before: "2026-08-08T10:00:00Z"},
		{name: "id only", beforeID: id},
		{name: "bad timestamp", before: "bad", beforeID: id},
		{name: "bad id", before: "2026-08-08T10:00:00Z", beforeID: "bad"},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := Parse(test.before, test.beforeID); err == nil {
				t.Fatal("Parse() expected error")
			}
		})
	}
}

func TestParseAndBeforeFilterUseStableTieBreaker(t *testing.T) {
	t.Parallel()

	id := bson.NewObjectID()
	cursor, err := Parse("2026-08-08T10:00:00.123Z", id.Hex())
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	wantTime := time.Date(2026, 8, 8, 10, 0, 0, 123_000_000, time.UTC)
	if !cursor.SentAt.Equal(wantTime) || cursor.ID != id {
		t.Fatalf("Parse() = %+v, want sent_at=%s id=%s", cursor, wantTime, id.Hex())
	}

	filter := BeforeFilter(cursor)
	branches, ok := filter["$or"].(bson.A)
	if !ok || len(branches) != 2 {
		t.Fatalf("BeforeFilter() = %#v", filter)
	}

	tie, ok := branches[1].(bson.M)
	if !ok {
		t.Fatalf("tie branch = %#v", branches[1])
	}
	if got, ok := tie["_id"].(bson.M); !ok || got["$lt"] != id {
		t.Fatalf("tie branch must use _id as secondary boundary: %#v", tie)
	}
}
