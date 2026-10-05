package prview

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/reviewing"
)

func TestRenderWatchBlock(t *testing.T) {
	st := reviewing.PRState{
		Status: reviewing.StatusChangesRequested,
		Pinned: true,
		Note:   "one blocking\x1b[2J\x1b]52;c;ZXZpbA==\x07",
		LastRun: &reviewing.RunRecord{
			SessionID: "5c1e2a4f-9d1b-4a3e-8f2c-0b7d6e5a4c3b", Turns: 9, CostUSD: 2.5,
		},
		Ledger: []reviewing.Finding{
			{
				Path:     "bots/kev/sync.go",
				Line:     40,
				Blocking: false,
				Summary:  "nit: rename",
				Status:   reviewing.FindingOutstanding,
			},
			{
				Path:     "bots/kev/main.go",
				Line:     12,
				Blocking: true,
				Summary:  "nil map write panics",
				Status:   reviewing.FindingOutstanding,
			},
			{
				Path:     "bots/kev/old.go",
				Line:     3,
				Blocking: true,
				Summary:  "already fixed",
				Status:   reviewing.FindingAddressed,
			},
		},
		HeldDraft: "/state/drafts/x.json",
	}
	out := renderWatchBlock(st, 200, time.Now())

	assert.NotContains(t, out, "\x1b[2J", "terminal escapes from model output are stripped")
	assert.NotContains(t, out, "\x07")
	assert.Contains(
		t,
		out,
		"Watch: changes requested · added by hand · last run 9 turns · $2.50 · session 5c1e2a4f-9d1b-4a3e-8f2c-0b7d6e5a4c3b",
	)
	assert.Contains(t, out, "Note: one blocking")
	assert.NotContains(t, out, "already fixed", "only outstanding findings are listed")
	blocking := strings.Index(out, "nil map write panics")
	nit := strings.Index(out, "nit: rename")
	assert.True(t, blocking >= 0 && nit > blocking, "blocking findings come first")
	assert.Contains(t, out, "Held review: /state/drafts/x.json")
	assert.Contains(t, out, "press S to send it")
}

func TestRenderWatchBlockMinimal(t *testing.T) {
	out := renderWatchBlock(reviewing.PRState{Status: reviewing.StatusBaseline}, 80, time.Now())
	assert.Contains(t, out, "Watch: baseline (not reviewed)")
	assert.NotContains(t, out, "Outstanding")
	assert.NotContains(t, out, "Held review")
}

func TestRenderWatchBlockRetry(t *testing.T) {
	now := time.Date(2026, 10, 5, 16, 33, 0, 0, time.UTC)
	failed := reviewing.PRState{
		Status:  reviewing.StatusFailed,
		Note:    "preparing chainguard-dev/mono#66155: fatal: couldn't find remote ref refs/pull/66155/head",
		Backoff: reviewing.Backoff{Until: now.Add(13 * time.Minute), Step: 1},
	}
	lines := strings.Split(renderWatchBlock(failed, 200, now), "\n")
	require.GreaterOrEqual(t, len(lines), 3)
	assert.Equal(t, "Watch: failed", strings.TrimSpace(lines[0]))
	assert.Equal(
		t,
		"Retries in 13m · press N to retry now",
		strings.TrimSpace(lines[1]),
		"shown right under the status",
	)
	assert.True(
		t,
		strings.HasPrefix(strings.TrimSpace(lines[2]), "Note: preparing chainguard-dev/mono#66155"),
	)

	failed.Backoff.Until = now.Add(2 * time.Hour)
	assert.Contains(t, renderWatchBlock(failed, 200, now), "Retries in 2h")

	failed.Backoff.Until = now.Add(-time.Minute)
	assert.Contains(
		t,
		renderWatchBlock(failed, 200, now),
		"Retries on the next poll · press N to retry now",
	)

	attention := reviewing.PRState{Status: reviewing.StatusNeedsAttention}
	assert.NotContains(t, renderWatchBlock(attention, 200, now), "Retries")
}

func TestRenderDraft(t *testing.T) {
	d := reviewing.Draft{
		Event: "REQUEST_CHANGES",
		Comments: []reviewing.ReviewComment{
			{
				Path: "bots/kev/main.go",
				Line: 12,
				Body: "This writes to a nil map when the feed is empty.\x1b[2J",
			},
		},
		Replies: []reviewing.DraftReply{{Body: "The retry still drops the last page."}},
		Resolve: []string{"PRRT_1", "PRRT_2"},
		Unanchored: []reviewing.OutFinding{
			{Path: "bots/kev/README.md", Body: "No test covers the empty feed."},
		},
	}
	out := renderDraft(d, false, 200)
	for _, want := range []string{
		"Draft review (dry run): Request changes",
		"bots/kev/main.go:12",
		"  This writes to a nil map when the feed is empty.[2J",
		"Reply on an earlier thread:",
		"  The retry still drops the last page.",
		"Resolves 2 threads",
		"Not posted (no changed line to attach to):",
		"- bots/kev/README.md: No test covers the empty feed.",
	} {
		assert.Contains(t, out, want)
	}
	assert.NotContains(t, out, "\x1b[2J")

	assert.Contains(
		t,
		renderDraft(reviewing.Draft{Event: "APPROVE"}, false, 200),
		"Draft review (dry run): Approve, no comments",
	)
	assert.Contains(
		t,
		renderDraft(reviewing.Draft{Event: "COMMENT", Comments: d.Comments}, true, 200),
		"Held review: Comment",
	)
}
