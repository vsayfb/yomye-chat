package messagecontent

import (
	"errors"
	"strings"
	"unicode/utf8"
)

var ErrInvalidMascotEmoji = errors.New("unsupported Yövmiye emoji")

const (
	notificationPlaceholder = "[Yövmiye emojisi]"
	reservedPrefix          = ":yovmiye_"
)

var mascotTokens = []string{
	":yovmiye_determined:",
	":yovmiye_excited:",
	":yovmiye_focused:",
	":yovmiye_happy:",
	":yovmiye_neutral:",
	":yovmiye_skeptical:",
	":yovmiye_surprised:",
	":yovmiye_thinking:",
}

var validMascotTokens = func() map[string]struct{} {
	result := make(map[string]struct{}, len(mascotTokens))
	for _, token := range mascotTokens {
		result[token] = struct{}{}
	}
	return result
}()

// Validate rejects any unsupported or malformed token in the reserved
// :yovmiye_*: namespace. It does not rewrite valid content.
func Validate(content string) error {
	lower := strings.ToLower(content)
	offset := 0

	for {
		start := strings.Index(lower[offset:], reservedPrefix)
		if start < 0 {
			return nil
		}
		start += offset

		relativeEnd := strings.IndexByte(content[start+len(reservedPrefix):], ':')
		if relativeEnd < 0 {
			return ErrInvalidMascotEmoji
		}
		end := start + len(reservedPrefix) + relativeEnd
		candidate := content[start : end+1]
		if _, ok := validMascotTokens[candidate]; !ok {
			return ErrInvalidMascotEmoji
		}
		offset = end + 1
	}
}

// LogicalLength counts each canonical mascot token as one character.
func LogicalLength(content string) int {
	length := 0
	offset := 0

	for offset < len(content) {
		if strings.HasPrefix(content[offset:], reservedPrefix) {
			end := strings.IndexByte(content[offset+len(reservedPrefix):], ':')
			if end >= 0 {
				tokenEnd := offset + len(reservedPrefix) + end
				if _, ok := validMascotTokens[content[offset:tokenEnd+1]]; ok {
					length++
					offset = tokenEnd + 1
					continue
				}
			}
		}

		_, size := utf8.DecodeRuneInString(content[offset:])
		if size == 0 {
			break
		}
		length++
		offset += size
	}
	return length
}

// NotificationText replaces mascot tokens because system notifications cannot
// render the app's custom emoji assets.
func NotificationText(content string) string {
	replacements := make([]string, 0, len(mascotTokens)*2)
	for _, token := range mascotTokens {
		replacements = append(replacements, token, notificationPlaceholder)
	}
	return strings.NewReplacer(replacements...).Replace(content)
}
