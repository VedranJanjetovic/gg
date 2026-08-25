package verification

import (
	"regexp"
	"strings"
)

var (
	durationPattern  = regexp.MustCompile(`\b[0-9]+(?:\.[0-9]+)?(?:ns|µs|us|ms|s|m)\b`)
	addressPattern   = regexp.MustCompile(`0x[0-9a-fA-F]+`)
	seedPattern      = regexp.MustCompile(`(?i)\b(seed|random(?:Seed)?)\s*[:=]\s*[0-9]+`)
	timestampPattern = regexp.MustCompile(`\b20[0-9]{2}-[0-9]{2}-[0-9]{2}(?:[T ][0-9:.+-Z]+)?\b`)
	tempPathPattern  = regexp.MustCompile(`(?i)(?:[A-Z]:[\\/](?:users|tmp|var)[\\/][^\s:]+|/(?:tmp|private/tmp|var/folders)/[^\s:]+)`)
	spacePattern     = regexp.MustCompile(`[ \t]+`)
)

// NormalizeReason removes only volatile details known to vary between runs.
// Semantic text and ordering remain visible for changed-reason comparison.
func NormalizeReason(reason string) string {
	reason = strings.ReplaceAll(reason, "\r\n", "\n")
	reason = strings.TrimSpace(reason)
	reason = timestampPattern.ReplaceAllString(reason, "<timestamp>")
	reason = durationPattern.ReplaceAllString(reason, "<duration>")
	reason = addressPattern.ReplaceAllString(reason, "<address>")
	reason = seedPattern.ReplaceAllString(reason, `${1}=<seed>`)
	reason = tempPathPattern.ReplaceAllString(reason, "<temp-path>")
	lines := strings.Split(reason, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(spacePattern.ReplaceAllString(line, " "))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
