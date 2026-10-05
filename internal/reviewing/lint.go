package reviewing

import (
	"regexp"
	"strings"
)

var (
	suggestionBlock = regexp.MustCompile("(?s)```suggestion.*?```")
	// A short or full commit SHA: 7-40 hex chars containing at least one
	// digit and one letter, so words like "defaced" and numbers like
	// "1234567" do not match.
	hexToken = regexp.MustCompile(`\b[0-9a-f]{7,40}\b`)
	hasDigit = regexp.MustCompile(`[0-9]`)
	hasAlpha = regexp.MustCompile(`[a-f]`)

	narration = []struct {
		re     *regexp.Regexp
		reason string
	}{
		{regexp.MustCompile(`(?i)\blgtm\b`), "says LGTM"},
		{regexp.MustCompile(`(?i)\blooks good\b`), "says looks good"},
		{regexp.MustCompile(`(?i)\bno issues( found)?\b`), "reports no issues"},
		{regexp.MustCompile(`(?i)\b(i|we)('ve| have)? (verified|checked|confirmed|reviewed|looked at|applied)\b`), "narrates what was checked"},
		{regexp.MustCompile(`(?i)\bverified (at|in|against) (the )?(code ?base|commit|head|repo)`), "narrates verification"},
		{regexp.MustCompile(`(?i)\bverified at\b`), "narrates verification"},
		{regexp.MustCompile(`(?i)\bconfirmed at\b`), "narrates verification"},
		{regexp.MustCompile(`(?i)\bfixed at\b`), "narrates verification"},
		{regexp.MustCompile(`(?i)\bseconding\b`), "seconds another reviewer"},
		{regexp.MustCompile(`(?i)\bconforms to\b`), "reports design conformance"},
		{regexp.MustCompile(`(?i)\bno design doc\b`), "reports design search"},
		{regexp.MustCompile(`(?i)\b(standards|skills) applied\b|\bapplied (the )?[a-z-]+-(standards|review)\b`), "lists skills applied"},
		{regexp.MustCompile(`(?i)\b\d+ (non-)?blocking\b`), "summarises finding counts"},
		{regexp.MustCompile(`(?i)\b(great|nice|excellent) (work|job|catch|change)\b|\bgood catch\b|\bwell done\b`), "praise"},
		{regexp.MustCompile(`(?i)\bdesign check\b`), "narrates design check"},
	}
)

// LintBody returns reasons a review body reads like bot narration rather
// than a reported problem. An empty result means it may be posted.
func LintBody(s string) []string {
	text := suggestionBlock.ReplaceAllString(s, "")
	var reasons []string
	for _, tok := range hexToken.FindAllString(strings.ToLower(text), -1) {
		if hasDigit.MatchString(tok) && hasAlpha.MatchString(tok) {
			reasons = append(reasons, "contains commit SHA "+tok)
			break
		}
	}
	for _, n := range narration {
		if n.re.MatchString(text) {
			reasons = append(reasons, n.reason)
		}
	}
	return reasons
}
