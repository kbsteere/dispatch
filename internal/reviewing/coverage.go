package reviewing

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// maxGaps bounds how many gaps a review records.
const maxGaps = 10

// coverageInput is what a pass's completeness is judged on.
type coverageInput struct {
	Pass   Pass
	PROnly bool
	// Worktree is the PR checkout; Changed paths are relative to it.
	Worktree string
	// Diff is the path of the PR diff file written for the session.
	Diff    string
	Changed []string
	// Reads are the files the session opened with Read.
	Reads    []string
	Searches int
	Examined Examined
}

// reviewGaps lists what a full or incremental pass failed to read. A pass
// with gaps has not reviewed the code and cannot approve. Comments-only
// passes have no requirements. Paths from the reviewer are only compared,
// never opened.
func reviewGaps(in coverageInput) []string {
	if in.Pass == PassLedger {
		return nil
	}
	read := make(map[string]struct{}, len(in.Reads))
	for _, p := range in.Reads {
		read[filepath.Clean(p)] = struct{}{}
	}
	opened := func(p string) bool {
		if !filepath.IsAbs(p) {
			p = filepath.Join(in.Worktree, p)
		}
		_, ok := read[filepath.Clean(p)]
		return ok
	}

	var gaps []string
	if !opened(in.Diff) {
		gaps = append(gaps, "never read the PR diff")
	}
	ex := in.Examined
	for _, p := range slices.Concat(ex.Docs, ex.Code, ex.Usages, ex.Tests) {
		if !opened(p) {
			gaps = append(gaps, fmt.Sprintf("listed %s as examined but never opened it", p))
		}
	}
	if in.PROnly {
		return capGaps(gaps)
	}

	codeChanged := false
	for _, f := range in.Changed {
		if skippable(f) {
			continue
		}
		if _, err := os.Stat(filepath.Join(in.Worktree, f)); err != nil {
			continue // deleted by the PR
		}
		if !opened(f) {
			gaps = append(gaps, "never read changed file "+f)
		}
		if isCode(f) && !isTest(f) {
			codeChanged = true
		}
	}
	if !slices.ContainsFunc(in.Reads, isDoc) {
		gaps = append(gaps, "read no documentation or design docs")
	}
	if codeChanged {
		if !slices.ContainsFunc(in.Reads, isTest) {
			gaps = append(gaps, "read no existing tests for the changed code")
		}
		if in.Searches == 0 {
			gaps = append(gaps, "never searched for usages of the changed code")
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
	"go.sum", "package-lock.json", "yarn.lock", "pnpm-lock.yaml", "Cargo.lock",
	"poetry.lock", "Gemfile.lock", "composer.lock", "uv.lock",
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
