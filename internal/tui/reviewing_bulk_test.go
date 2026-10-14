package tui

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kbsteere/dispatch/v4/internal/reviewing"
)

func TestBaselinePrompt(t *testing.T) {
	assert.Equal(
		t,
		"Queue full reviews of 37 baseline PRs (about $15–52; $2.55 of $100 spent today)?",
		baselinePrompt(37, reviewing.WatchStatus{SpentToday: 2.55, DailyBudget: 100}),
	)
}

func TestRetryPrompt(t *testing.T) {
	assert.Equal(
		t,
		"Retry 6 failed reviews (about $2–8; $14.20 of $100 spent today)?",
		retryPrompt(6, reviewing.WatchStatus{SpentToday: 14.2, DailyBudget: 100}),
	)
}

func TestPostPrompt(t *testing.T) {
	drafts := map[reviewing.PRKey]reviewing.Draft{
		{Owner: "chainguard-dev", Repo: "stereo", Number: 366936}: {Event: "APPROVE"},
		{Owner: "chainguard-dev", Repo: "stereo", Number: 368115}: {Event: "APPROVE"},
		{Owner: "chainguard-dev", Repo: "mono", Number: 66370}:    {Event: "REQUEST_CHANGES"},
		{Owner: "chainguard-dev", Repo: "mono", Number: 63980}:    {Event: "COMMENT"},
	}
	assert.Equal(
		t,
		"Post 4 drafts (2 approvals, 1 change requests, 1 comments)? Approvals are checked again first.",
		postPrompt(drafts),
	)
}

func TestPostSummary(t *testing.T) {
	k := func(repo string, n int) reviewing.PRKey {
		return reviewing.PRKey{Owner: "chainguard-dev", Repo: repo, Number: n}
	}
	got := postSummary([]reviewing.PostResult{
		{Key: k("stereo", 366936), Action: reviewing.ActionApprove},
		{Key: k("mono", 66370), Action: reviewing.ActionRequestChanges},
		{
			Key:         k("mono", 63980),
			Action:      reviewing.ActionComment,
			NotApproved: "required CI is not green",
		},
		{Key: k("mono", 66601), Err: errors.New("changed since the review was written\x1b[2J")},
	})
	assert.Equal(
		t,
		"Posted 3 (1 approved, 1 changes requested, 1 comments); skipped 1: mono#66601: changed since the review was written[2J",
		got,
	)

	var many []reviewing.PostResult
	for i := range 5 {
		many = append(many, reviewing.PostResult{Key: k("mono", i+1), Err: errors.New("moved")})
	}
	assert.Equal(
		t,
		"Posted 0 (0 approved, 0 changes requested, 0 comments); skipped 5: mono#1: moved; mono#2: moved; mono#3: moved; and 2 more",
		postSummary(many),
	)
}
