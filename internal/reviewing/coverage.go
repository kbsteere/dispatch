package reviewing

import (
	"cmp"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// maxGaps bounds how many gaps a review records.
const maxGaps = 10

// maxPerFileChecks is the most changed code files a pass must each read. A
// larger PR is judged on its diff and the other checks alone.
const maxPerFileChecks = 40

// coverageInput is what a pass's completeness is judged on.
type coverageInput struct {
	Pass   Pass
	PROnly bool
	// Worktree is the PR checkout; Changed paths are relative to it.
	Worktree string
	// Diff is the path of the PR diff file written for the session.
	Diff string
	// SinceLast is the diff since the last review, on an incremental pass;
	// reading it counts as reading the diff.
	SinceLast string
	Changed   []string
	// Reads are the files the session opened with Read.
	Reads []string
	// Ranges are the lines each Read covered, by path.
	Ranges   map[string][]LineRange
	Searches int
	// SecurityRefs are the security guide reference files the session was
	// given; when code changed, at least one must be read.
	SecurityRefs []string
}

// reviewGaps lists what a full or incremental pass failed to read: the
// diff; every changed code file, opened or with its whole change read in
// the diff, up to maxPerFileChecks files; documentation on a full pass;
// and, when code changed, existing tests, a search for usages, and the
// security guide. An incremental pass is judged on what changed since the
// last review. A pass with gaps has not reviewed the code and cannot
// approve. Comments-only passes have no requirements. Paths from the
// reviewer are only compared, never opened.
func reviewGaps(in coverageInput) []string {
	if in.Pass == PassLedger {
		return nil
	}
	read := newReadSet(in.Reads)
	opened := func(p string) bool { return read.opened(in.Worktree, p) }

	var gaps []string
	if !opened(in.Diff) && (in.SinceLast == "" || !opened(in.SinceLast)) {
		gaps = append(gaps, "never read the PR diff")
	}
	if in.PROnly {
		return capGaps(gaps)
	}

	codeChanged := false
	var code []string
	for _, f := range in.Changed {
		if skippable(f) || !isCode(f) {
			// Config, data, and docs changes are fully visible in the diff.
			continue
		}
		if _, err := os.Stat(filepath.Join(in.Worktree, f)); err != nil {
			continue // deleted by the PR
		}
		code = append(code, f)
		if !isTest(f) {
			codeChanged = true
		}
	}
	if len(code) <= maxPerFileChecks {
		// Code needs its surrounding file read, unless the reviewer read
		// the file's whole change in the diff.
		inDiff := readInDiff(in.Ranges, in.Diff, in.SinceLast)
		for _, f := range code {
			if _, ok := inDiff[f]; !ok && !opened(f) {
				gaps = append(gaps, "never read changed file "+f)
			}
		}
	}
	// A follow-up review builds on the full review that read the docs.
	if in.Pass == PassFull && !slices.ContainsFunc(in.Reads, isDoc) {
		gaps = append(gaps, "read no documentation or design docs")
	}
	if codeChanged {
		if !slices.ContainsFunc(in.Reads, isTest) {
			gaps = append(gaps, "read no existing tests for the changed code")
		}
		if in.Searches == 0 {
			gaps = append(gaps, "never searched for usages of the changed code")
		}
		if len(in.SecurityRefs) > 0 && !slices.ContainsFunc(in.SecurityRefs, opened) {
			gaps = append(gaps, "read none of the security review guide's references")
		}
	}
	return capGaps(gaps)
}

func capGaps(gaps []string) []string {
	if len(gaps) > maxGaps {
		return append(gaps[:maxGaps], fmt.Sprintf("and %d more", len(gaps)-maxGaps))
	}
	return gaps
}

var lockfiles = []string{
	"go.sum",
	".terraform.lock.hcl",
	"package-lock.json",
	"yarn.lock",
	"pnpm-lock.yaml",
	"Cargo.lock",
	"poetry.lock",
	"Gemfile.lock",
	"composer.lock",
	"uv.lock",
}

// skippable reports whether a changed file need not be read: lockfiles,
// generated code, and archives.
func skippable(p string) bool {
	base := path.Base(p)
	switch {
	case slices.Contains(lockfiles, base), strings.HasSuffix(base, ".lock"):
		return true
	case strings.HasSuffix(base, ".pb.go"), strings.HasSuffix(base, "_generated.go"),
		strings.HasPrefix(base, "zz_generated"), strings.HasSuffix(base, ".gen.go"):
		return true
	}
	switch path.Ext(base) {
	case ".tar", ".gz", ".tgz", ".zip", ".jar", ".so", ".bin", ".exe":
		return true
	}
	return false
}

var codeExts = []string{
	".go", ".py", ".rs", ".ts", ".tsx", ".js", ".jsx", ".java", ".kt", ".rb",
	".c", ".cc", ".cpp", ".h", ".hpp", ".sh", ".swift", ".cs", ".scala",
}

func isCode(p string) bool { return slices.Contains(codeExts, path.Ext(p)) }

// isTest reports whether p is a test file or test fixture.
func isTest(p string) bool {
	p = filepath.ToSlash(p)
	base := path.Base(p)
	switch {
	case strings.HasSuffix(base, "_test.go"), strings.HasSuffix(base, "_test.py"),
		strings.HasPrefix(base, "test_") && strings.HasSuffix(base, ".py"),
		strings.Contains(base, ".test."), strings.Contains(base, ".spec."):
		return true
	}
	for _, dir := range []string{"/testdata/", "/tests/", "/test/", "/e2e/", "/__tests__/"} {
		if strings.Contains(p, dir) {
			return true
		}
	}
	return false
}

var docExts = []string{
	".md", ".markdown", ".rst", ".adoc", ".puml", ".plantuml", ".drawio", ".mmd",
	".svg", ".png", ".jpg", ".jpeg",
}

// isDoc reports whether p is documentation or a diagram.
func isDoc(p string) bool {
	return slices.Contains(docExts, strings.ToLower(path.Ext(p)))
}

// verifiedExamined keeps only the examined paths the session really opened.
// The reviewer's list is shown to the user, so it must not claim reads that
// did not happen; requirements are checked against reads, not this list.
func verifiedExamined(ex Examined, worktree string, reads []string) Examined {
	read := newReadSet(reads)
	keep := func(paths []string) []string {
		return slices.DeleteFunc(slices.Clone(paths), func(p string) bool {
			return !read.opened(worktree, p)
		})
	}
	return Examined{
		Docs:   keep(ex.Docs),
		Code:   keep(ex.Code),
		Usages: keep(ex.Usages),
		Tests:  keep(ex.Tests),
	}
}

// readSet is the files a session opened.
type readSet map[string]struct{}

func newReadSet(reads []string) readSet {
	r := make(readSet, len(reads))
	for _, p := range reads {
		r[filepath.Clean(p)] = struct{}{}
	}
	return r
}

// opened reports whether p, absolute or relative to worktree, was read.
func (r readSet) opened(worktree, p string) bool {
	if !filepath.IsAbs(p) {
		p = filepath.Join(worktree, p)
	}
	_, ok := r[filepath.Clean(p)]
	return ok
}

// readInDiff returns the files whose whole section of one of the diffs was
// covered by the lines the reviewer read.
func readInDiff(ranges map[string][]LineRange, diffs ...string) map[string]struct{} {
	out := map[string]struct{}{}
	for _, d := range diffs {
		read := ranges[filepath.Clean(d)]
		if d == "" || len(read) == 0 {
			continue
		}
		b, err := os.ReadFile(d)
		if err != nil {
			continue
		}
		for f, section := range diffSections(string(b)) {
			if covers(read, section) {
				out[f] = struct{}{}
			}
		}
	}
	return out
}

// diffSections maps each file in a git diff to the lines of its section,
// from its "diff --git" header to the line before the next one.
func diffSections(diff string) map[string]LineRange {
	out := map[string]LineRange{}
	var cur string
	lines := strings.Split(strings.TrimSuffix(diff, "\n"), "\n")
	for i, l := range lines {
		header, ok := strings.CutPrefix(l, "diff --git ")
		if !ok {
			continue
		}
		if cur != "" {
			out[cur] = LineRange{From: out[cur].From, To: i}
		}
		cur = ""
		if j := strings.LastIndex(header, " b/"); j >= 0 {
			cur = header[j+len(" b/"):]
			out[cur] = LineRange{From: i + 1}
		}
	}
	if cur != "" {
		out[cur] = LineRange{From: out[cur].From, To: len(lines)}
	}
	return out
}

// covers reports whether the union of read includes every line of want.
func covers(read []LineRange, want LineRange) bool {
	sorted := slices.Clone(read)
	slices.SortFunc(sorted, func(a, b LineRange) int { return a.From - b.From })
	next := want.From
	for _, r := range sorted {
		if r.From > next {
			break
		}
		next = max(next, r.To+1)
	}
	return next > want.To
}

// diffLines are the lines of each file GitHub accepts an inline review
// comment on: those inside the diff's hunks on the new side.
type diffLines map[string]map[int]struct{}

// commentableLines reads a git diff into the lines GitHub accepts inline
// comments on.
func commentableLines(diff string) diffLines {
	out := diffLines{}
	var lines map[int]struct{}
	next := 0
	for _, l := range strings.Split(diff, "\n") {
		if header, ok := strings.CutPrefix(l, "diff --git "); ok {
			lines = nil
			next = 0
			if j := strings.LastIndex(header, " b/"); j >= 0 {
				lines = map[int]struct{}{}
				out[header[j+len(" b/"):]] = lines
			}
			continue
		}
		if lines == nil {
			continue
		}
		if hunk, ok := strings.CutPrefix(l, "@@ "); ok {
			next = hunkStart(hunk)
			continue
		}
		if next == 0 {
			continue // file headers before the first hunk
		}
		switch {
		case strings.HasPrefix(l, "+"), strings.HasPrefix(l, " "):
			lines[next] = struct{}{}
			next++
		case strings.HasPrefix(l, "-"), strings.HasPrefix(l, `\`):
		default:
			next = 0
		}
	}
	return out
}

// hunkStart is the new-side start line of a hunk header such as
// "-12,7 +14,9 @@ func f()", or 0 if it cannot be read.
func hunkStart(hunk string) int {
	_, after, ok := strings.Cut(hunk, "+")
	if !ok {
		return 0
	}
	num, _, _ := strings.Cut(after, " ")
	num, _, _ = strings.Cut(num, ",")
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0
	}
	return n
}

// accepts reports whether GitHub takes an inline comment on path from
// startLine (0 for one line) to line: every line between must be inside the
// diff's hunks. Without a diff to judge by, every line is accepted.
func (d diffLines) accepts(path string, startLine, line int) bool {
	if d == nil {
		return true
	}
	if startLine > line {
		return false
	}
	lines := d[path]
	for n := cmp.Or(startLine, line); n <= line; n++ {
		if _, ok := lines[n]; !ok {
			return false
		}
	}
	return line > 0
}
