package reviewing

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// visionDoc has the layout of mono#65196: a new design doc whose one odd
// heading, at line 8, ends with a period. In the PR diff the heading sits at
// line 14, which here is a paragraph.
const visionDoc = `---
title: "Release Tooling: Vision"
status: draft
---

# Release Tooling: Vision

## Where we are, honestly.

Most releases still go out by hand, and each one takes a person most of a day.

We never revisit a release once it ships. Nothing looks at it again after tagging.

Upstream may ship a fix, and the release stays where we left it.
`

func TestSuggestionText(t *testing.T) {
	got, ok := suggestionText(
		"nit: drop the period.\n\n```suggestion\n## Where we are, honestly\n```\n",
	)
	require.True(t, ok)
	assert.Equal(t, []string{"## Where we are, honestly"}, got)

	got, ok = suggestionText("Delete these lines.\n```suggestion\n```")
	require.True(t, ok, "an empty suggestion deletes the lines")
	assert.Empty(t, got)

	_, ok = suggestionText("Use `errors.Is` here.\n```go\nerrors.Is(err, fs.ErrNotExist)\n```")
	assert.False(t, ok, "a plain code block is not a suggestion")

	_, ok = suggestionText("```suggestion\nnever closed")
	assert.False(t, ok, "an unclosed block is not a suggestion GitHub would offer")
}

func TestSuggestionMisplaced(t *testing.T) {
	doc := strings.Split(visionDoc, "\n")
	heading := []string{"## Where we are, honestly"}
	assert.True(t, suggestionMisplaced(heading, doc, 0, 14),
		"mono#65196: committing it would replace the paragraph with a second heading")
	assert.False(t, suggestionMisplaced(heading, doc, 0, 8), "on the heading it fixes")

	code := strings.Split(`func (b *beat) sample() {
	cur := b.read()
	if cur > b.last {
		b.log(cur)
	}
}`, "\n")
	assert.False(t, suggestionMisplaced([]string{"\tif cur > b.peak {"}, code, 0, 3),
		"a one-word change keeps most of the line")
	assert.False(t, suggestionMisplaced([]string{"\tcur := b.sampleOnce()"}, code, 0, 2),
		"a rewrite that matches nothing else in the file is the reviewer's call")
	assert.False(t, suggestionMisplaced([]string{"\tif cur > b.peak {"}, code, 0, 4),
		"a neighbouring line sharing half its words is given the benefit of the doubt")
	assert.True(t, suggestionMisplaced([]string{"\tif cur > b.peak {"}, code, 0, 5),
		"on the closing brace two lines below the condition it rewrites")
	assert.False(t, suggestionMisplaced(
		[]string{"\tcur := b.read()", "\tif cur > b.peak {"}, code, 2, 3),
		"a range on the lines it rewrites")
	assert.True(t, suggestionMisplaced(
		[]string{"\tcur := b.read()", "\tif cur > b.peak {"}, code, 4, 5),
		"a range two lines below the ones it rewrites")
	assert.False(t, suggestionMisplaced(nil, code, 0, 3), "deleting the line")
	assert.False(t, suggestionMisplaced(heading, doc, 0, 400), "outside the file")
	assert.False(t, suggestionMisplaced(heading, doc, 9, 8), "a reversed range")
}

func TestMisplacedSuggestion(t *testing.T) {
	wt := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(wt, "docs"), 0o755))
	require.NoError(
		t,
		os.WriteFile(filepath.Join(wt, "docs", "vision.md"), []byte(visionDoc), 0o644),
	)
	body := "nit: drop the period.\n\n```suggestion\n## Where we are, honestly\n```"

	assert.True(
		t,
		misplacedSuggestion(OutFinding{Path: "docs/vision.md", Line: 14, Body: body}, wt),
	)
	assert.False(
		t,
		misplacedSuggestion(OutFinding{Path: "docs/vision.md", Line: 8, Body: body}, wt),
	)
	assert.False(t, misplacedSuggestion(
		OutFinding{Path: "docs/vision.md", Line: 14, Body: "nit: drop the period."}, wt),
		"no suggestion to check")
	assert.False(t, misplacedSuggestion(
		OutFinding{Path: "../" + filepath.Base(wt) + "/docs/vision.md", Line: 14, Body: body}, wt),
		"a path leaving the worktree is never opened")
	assert.False(t, misplacedSuggestion(OutFinding{Path: "docs/gone.md", Line: 14, Body: body}, wt),
		"a file that cannot be read")
}

func TestDocPreference(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    OutFinding
		want bool
	}{
		{"heading style on a doc", OutFinding{Path: "docs/vision.md", Category: CategoryDesign}, true},
		{"a doc rule", OutFinding{Path: "docs/vision.md", Category: "rule"}, false},
		{"a wrong statement in a doc", OutFinding{Path: "README.md", Category: "bug"}, false},
		{"blocking on a doc", OutFinding{Path: "docs/design.md", Category: CategoryDesign, Blocking: true}, false},
		{"design on code", OutFinding{Path: "kev.go", Category: CategoryDesign}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, docPreference(tc.f))
		})
	}
}
