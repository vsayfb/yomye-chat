package attachment

import (
	"errors"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestValidateMediaAcceptsSupportedBoundaryValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		size        int64
		duration    int64
		wantType    string
		wantMax     int64
	}{
		{name: "jpeg with parameters", contentType: " Image/JPEG; charset=binary ", size: MaxImageBytes, wantType: "image/jpeg", wantMax: MaxImageBytes},
		{name: "legacy gif image", contentType: "image/gif", size: MaxImageBytes, wantType: "image/gif", wantMax: MaxImageBytes},
		{name: "webp image", contentType: "image/webp", size: MaxImageBytes, wantType: "image/webp", wantMax: MaxImageBytes},
		{name: "video max duration", contentType: "video/mp4", size: MaxVideoBytes, duration: MaxVideoDurationMS, wantType: "video/mp4", wantMax: MaxVideoBytes},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotType, gotMax, err := validateMedia(tt.contentType, tt.size, tt.duration)
			if err != nil {
				t.Fatalf("validateMedia() error = %v", err)
			}
			if gotType != tt.wantType || gotMax != tt.wantMax {
				t.Fatalf("validateMedia() = (%q,%d), want (%q,%d)", gotType, gotMax, tt.wantType, tt.wantMax)
			}
		})
	}
}

func TestValidateMediaRejectsInvalidBoundaryValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		contentType string
		size        int64
		duration    int64
	}{
		{name: "zero image", contentType: "image/png", size: 0},
		{name: "image duration", contentType: "image/png", size: 1, duration: 1},
		{name: "zero video", contentType: "video/mp4", size: 0, duration: 1},
		{name: "zero video duration", contentType: "video/mp4", size: 1, duration: 0},
		{name: "unsupported type", contentType: "application/pdf", size: 1},
		{name: "malformed type", contentType: "image/png; broken", size: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := validateMedia(tt.contentType, tt.size, tt.duration); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("validateMedia() error = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestSafeOriginalNameStripsPathsAndRejectsUnsafeNames(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"../../photo.png":        "photo.png",
		`..\\windows\\video.mp4`: "video.mp4",
		"  report.webp  ":        "report.webp",
	} {
		got, err := safeOriginalName(input)
		if err != nil {
			t.Fatalf("safeOriginalName(%q) error = %v", input, err)
		}
		if got != want {
			t.Fatalf("safeOriginalName(%q) = %q, want %q", input, got, want)
		}
	}

	for _, input := range []string{"", ".", "bad\x00name.png", "bad\nname.png", strings.Repeat("a", 256)} {
		if _, err := safeOriginalName(input); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("safeOriginalName(%q) error = %v, want ErrInvalidInput", input, err)
		}
	}
}

func TestHasDuplicateIDsRejectsDuplicatesAndZeroIDs(t *testing.T) {
	t.Parallel()

	first := bson.NewObjectID()
	second := bson.NewObjectID()
	if hasDuplicateIDs([]bson.ObjectID{first, second}) {
		t.Fatal("unique IDs were reported as duplicates")
	}
	if !hasDuplicateIDs([]bson.ObjectID{first, first}) {
		t.Fatal("duplicate IDs were accepted")
	}
	if !hasDuplicateIDs([]bson.ObjectID{{}, second}) {
		t.Fatal("zero attachment ID was accepted")
	}
}

func TestStorageScopePathIsNamespaceSpecific(t *testing.T) {
	t.Parallel()

	id := bson.NewObjectID()
	if got := storageScopePath(ScopeDirect, id); got != "conversations/"+id.Hex() {
		t.Fatalf("direct scope path = %q", got)
	}
	if got := storageScopePath(ScopeApplication, id); got != "applications/"+id.Hex() {
		t.Fatalf("application scope path = %q", got)
	}
	if got := storageScopePath(Scope("unknown"), id); got != "" {
		t.Fatalf("unknown scope path = %q, want empty", got)
	}
}
