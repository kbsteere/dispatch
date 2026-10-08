package reviewing

import (
	"cmp"
	"os"
	"path/filepath"
	"strings"
	"unicode"
)

// A suggestion that shares less than misplacedBelow of its words with the
// lines it replaces, while another run of the file shares at least
// misplacedMatch, was anchored on the wrong lines.
const (
	misplacedBelow = 0.5
	misplacedMatch = 0.75
)

// suggestionText returns the replacement lines of the first ```suggestion
// block in body, and whether body has one.
func suggestionText(body string) ([]string, bool) {
	var out []string
	in := false
	for l := range strings.SplitSeq(body, "\n") {
		trimmed := strings.TrimSpace(l)
		switch {
		case !in && strings.HasPrefix(trimmed, "```suggestion"):
			in = true
		case in && strings.HasPrefix(trimmed, "```"):
			return out, true
		case in:
			out = append(out, l)
		}
	}
	return nil, false
}

// suggestionMisplaced reports whether a suggestion anchored on lines
// start..line of file (start 0 for one line) was meant for other lines: it
// shares little with the lines it would replace, while a run of the same
// length elsewhere in the file closely matches it. Committing such a
// suggestion would overwrite the wrong lines. A suggestion that deletes the
// lines, or anchored outside the file, is not judged.
func suggestionMisplaced(suggestion, file []string, start, line int) bool {
	start = cmp.Or(start, line)
	if start < 1 || line < start || line > len(file) {
		return false
	}
	want := words(strings.Join(suggestion, "\n"))
	if len(want) == 0 {
		return false
	}
	n := line - start + 1
	target := overlap(words(strings.Join(file[start-1:line], "\n")), want)
	if target >= misplacedBelow {
		return false
	}
	for i := 0; i+n <= len(file); i++ {
		if i == start-1 {
			continue
		}
		if s := overlap(words(strings.Join(file[i:i+n], "\n")), want); s >= misplacedMatch {
			return true
		}
	}
	return false
}

// words is the set of lowercased words and numbers in s.
func words(s string) map[string]struct{} {
	out := map[string]struct{}{}
	for w := range strings.FieldsFuncSeq(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		out[w] = struct{}{}
	}
	return out
}

// overlap is the share of words a and b have in common, out of the larger
// set.
func overlap(a, b map[string]struct{}) float64 {
	n := max(len(a), len(b))
	if n == 0 {
		return 1
	}
	common := 0
	for w := range a {
		if _, ok := b[w]; ok {
			common++
		}
	}
	return float64(common) / float64(n)
}

// misplacedSuggestion reports whether f carries a suggestion meant for other
// lines of its file in worktree. f.Path must already be one of the PR's
// files; a file that cannot be read is not judged.
func misplacedSuggestion(f OutFinding, worktree string) bool {
	suggestion, ok := suggestionText(f.Body)
	if !ok || worktree == "" || !filepath.IsLocal(f.Path) {
		return false
	}
	b, err := os.ReadFile(filepath.Join(worktree, f.Path))
	if err != nil {
		return false
	}
	return suggestionMisplaced(suggestion, strings.Split(string(b), "\n"), f.StartLine, f.Line)
}
