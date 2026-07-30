package messagecontent

import (
	"errors"
	"testing"
)

func TestValidateAcceptsCanonicalMascotTokens(t *testing.T) {
	t.Parallel()

	for _, token := range mascotTokens {
		token := token
		t.Run(token, func(t *testing.T) {
			t.Parallel()
			if err := Validate("hello " + token + " world"); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestValidateAcceptsEmojiOnlyAndPlainText(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		":yovmiye_happy:",
		"ordinary text 😀",
		":yovmiye_happy::yovmiye_thinking:",
	} {
		if err := Validate(content); err != nil {
			t.Fatalf("Validate(%q) error = %v", content, err)
		}
	}
}

func TestValidateRejectsUnsupportedMalformedAndNonCanonicalTokens(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		":yovmiye_angry:",
		":yovmiye_happy",
		":YOVMIYE_HAPPY:",
		":Yovmiye_happy:",
		":yovmiye_happy.png:",
	} {
		if err := Validate(content); !errors.Is(err, ErrInvalidMascotEmoji) {
			t.Fatalf("Validate(%q) error = %v, want ErrInvalidMascotEmoji", content, err)
		}
	}
}

func TestLogicalLengthCountsMascotTokenAsOne(t *testing.T) {
	t.Parallel()

	content := "A:yovmiye_happy:😀"
	if got, want := LogicalLength(content), 3; got != want {
		t.Fatalf("LogicalLength() = %d, want %d", got, want)
	}
}

func TestNotificationTextReplacesMascotTokens(t *testing.T) {
	t.Parallel()

	got := NotificationText("Hi :yovmiye_happy: :yovmiye_thinking:")
	want := "Hi [Yövmiye emojisi] [Yövmiye emojisi]"
	if got != want {
		t.Fatalf("NotificationText() = %q, want %q", got, want)
	}
}
