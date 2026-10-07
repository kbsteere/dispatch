// Package reviewing runs autonomous pull request reviews for configured
// watches: it polls GitHub, runs bounded claude sessions against local
// checkouts, and posts findings through guarded GitHub writes.
package reviewing

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var (
	nameRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	shaRe    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	shortRef = regexp.MustCompile(`^([^/\s#]+)/([^/\s#]+)#(-?\d+)$`)
	urlRef   = regexp.MustCompile(`^https://github\.com/([^/\s]+)/([^/\s]+)/pull/(-?\d+)(/.*)?$`)
)

// PRKey identifies a pull request.
type PRKey struct {
	Owner  string
	Repo   string
	Number int
}

func (k PRKey) String() string        { return fmt.Sprintf("%s/%s#%d", k.Owner, k.Repo, k.Number) }
func (k PRKey) NameWithOwner() string { return k.Owner + "/" + k.Repo }

func (k PRKey) FileName() string { return fmt.Sprintf("%s__%s__%d", k.Owner, k.Repo, k.Number) }

func (k PRKey) URL() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", k.Owner, k.Repo, k.Number)
}

// ValidName reports whether s is safe to use as a GitHub owner or repo name
// in command arguments and file paths.
func ValidName(s string) bool {
	return nameRe.MatchString(s) && s != "." && s != ".."
}

// ValidSHA reports whether s is a full lowercase commit SHA.
func ValidSHA(s string) bool { return shaRe.MatchString(s) }

// ParsePRRef parses "owner/repo#N" or a github.com pull request URL.
func ParsePRRef(s string) (PRKey, error) {
	s = strings.TrimSpace(s)
	m := shortRef.FindStringSubmatch(s)
	if m == nil {
		m = urlRef.FindStringSubmatch(s)
	}
	if m == nil {
		return PRKey{}, fmt.Errorf(
			"not a PR reference: %q (want owner/repo#N or a github.com PR URL)",
			s,
		)
	}
	n, err := strconv.Atoi(m[3])
	if err != nil || n <= 0 {
		return PRKey{}, fmt.Errorf("invalid PR number in %q", s)
	}
	if !ValidName(m[1]) || !ValidName(m[2]) {
		return PRKey{}, fmt.Errorf("invalid owner or repo in %q", s)
	}
	return PRKey{Owner: m[1], Repo: m[2], Number: n}, nil
}

// ParsePRNumber reads a bare PR number, such as 66370 or #66370.
func ParsePRNumber(s string) (int, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// SanitizeTerminal removes control characters other than newline and tab so
// untrusted text cannot emit terminal escape sequences.
func SanitizeTerminal(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if r == '\n' || r == '\t' {
			b.WriteRune(r)
			continue
		}
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
