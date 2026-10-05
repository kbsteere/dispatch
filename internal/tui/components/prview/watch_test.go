package prview

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

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
	out := renderWatchBlock(st, 200)

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
	out := renderWatchBlock(reviewing.PRState{Status: reviewing.StatusBaseline}, 80)
	assert.Contains(t, out, "Watch: baseline (not reviewed)")
	assert.NotContains(t, out, "Outstanding")
	assert.NotContains(t, out, "Held review")
}
