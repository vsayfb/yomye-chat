package smoke

import "regexp"

var (
	bearerPattern         = regexp.MustCompile(`(?i)(authorization\s*[:=]\s*bearer\s+|bearer\s+)[^\s,;"']+`)
	ticketPattern         = regexp.MustCompile(`(?i)([?&]ticket=)[^&\s]+`)
	contentPattern        = regexp.MustCompile(`(?i)("content"\s*:\s*")[^"]*(")`)
	credentialJSONPattern = regexp.MustCompile(`(?i)("(?:access_token|ticket)"\s*:\s*")[^"]*(")`)
)

func Redact(value string) string {
	value = bearerPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = ticketPattern.ReplaceAllString(value, `${1}[REDACTED]`)
	value = credentialJSONPattern.ReplaceAllString(value, `${1}[REDACTED]${2}`)
	return contentPattern.ReplaceAllString(value, `${1}[REDACTED]${2}`)
}
