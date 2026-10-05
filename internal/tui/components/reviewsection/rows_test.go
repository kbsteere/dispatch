package reviewsection

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/reviewing"
)

var now = time.Date(2026, 10, 5, 15, 0, 0, 0, time.UTC)

func ts(d time.Duration) string { return now.Add(-d).Format(time.RFC3339) }

func st(repo string, n int, status reviewing.Status, updated time.Duration) reviewing.PRState {
	return reviewing.PRState{
		Key:       reviewing.PRKey{Owner: "chainguard-dev", Repo: repo, Number: n},
		Status:    status,
		UpdatedAt: ts(updated),
		Title:     "title",
		Author:    "dnegreira",
		CI:        "SUCCESS",
	}
}

func TestSortStates(t *testing.T) {
	in := []reviewing.PRState{
		st("mono", 1, reviewing.StatusBaseline, time.Minute),
		st("mono", 2, reviewing.StatusApproved, time.Hour),
		st("mono", 3, reviewing.StatusNeedsAttention, 3*time.Hour),
		st("mono", 4, reviewing.StatusChangesRequested, 2*time.Hour),
		st("mono", 5, reviewing.StatusChangesRequested, 10*time.Minute),
		st("mono", 6, reviewing.StatusReviewing, 5*time.Hour),
		st("mono", 7, reviewing.StatusWaitingOnCI, time.Minute),
		st("mono", 8, reviewing.StatusFailed, time.Minute),
	}
	got := SortStates(in)
	var order []int
	for _, s := range got {
		order = append(order, s.Key.Number)
	}
	assert.Equal(
		t,
		[]int{3, 5, 4, 6, 7, 2, 8, 1},
		order,
		"needs attention, then changes requested (newest first), reviewing, waiting, approved, failed, baseline",
	)
	assert.Equal(t, 1, in[0].Key.Number, "input is not reordered")
}

func TestSortStatesHidesInactive(t *testing.T) {
	merged := st("mono", 66155, reviewing.StatusFailed, time.Minute)
	merged.Inactive = true
	got := SortStates(
		[]reviewing.PRState{merged, st("stereo", 367322, reviewing.StatusFailed, time.Minute)},
	)
	require.Len(t, got, 1)
	assert.Equal(t, 367322, got[0].Key.Number)
}

func TestSortStatesSkipsInvalidKeys(t *testing.T) {
	bad := st("..", 1, reviewing.StatusApproved, time.Minute)
	got := SortStates(
		[]reviewing.PRState{bad, st("mono", 2, reviewing.StatusApproved, time.Minute)},
	)
	require.Len(t, got, 1)
	assert.Equal(t, 2, got[0].Key.Number)
}

func TestCells(t *testing.T) {
	s := st("mono", 62436, reviewing.StatusChangesRequested, 14*time.Minute)
	s.Title = "versiondata: add epochs\x1b[2J"
	s.Author = "tcnghia"
	s.Ledger = []reviewing.Finding{
		{Blocking: true, Status: reviewing.FindingOutstanding},
		{Blocking: true, Status: reviewing.FindingOutstanding},
		{Blocking: false, Status: reviewing.FindingOutstanding},
		{Blocking: true, Status: reviewing.FindingAddressed},
	}
	s.LastRun = &reviewing.RunRecord{Turns: 9, CostUSD: 2.1}
	c := Cells(s, now)
	assert.Equal(t, "changes", c.Status)
	assert.Equal(
		t,
		"mono#62436 versiondata: add epochs[2J",
		c.PR,
		"control characters are stripped",
	)
	assert.Equal(t, "tcnghia", c.Author)
	assert.Equal(t, "2b 1n", c.Open)
	assert.Equal(t, "9t $2.10", c.LastRun)
	assert.Equal(t, "14m", c.Updated)
	assert.Equal(t, "SUCCESS", c.CI)
}

func TestCellsEdgeCases(t *testing.T) {
	s := st("omnibump", 88, reviewing.StatusReviewing, 3*time.Hour)
	s.Pinned = true
	s.Title = strings.Repeat("x", 500)
	c := Cells(s, now)
	assert.Equal(t, "-", c.Open)
	assert.Equal(t, "…", c.LastRun, "a running review shows progress, not the previous run")
	assert.True(t, strings.HasSuffix(c.PR, "(added)"))
	assert.LessOrEqual(t, len([]rune(c.PR)), maxTitleRunes+40, "titles are length-limited")

	b := st("mono", 1, reviewing.StatusBaseline, 48*time.Hour)
	c = Cells(b, now)
	assert.Equal(t, "baseline", c.Status)
	assert.Equal(t, "-", c.LastRun)
	assert.Equal(t, "2d", c.Updated)

	b.UpdatedAt = "garbage"
	assert.Equal(t, "", Cells(b, now).Updated)
}

func TestSummaryBeforeFirstPollAndOnError(t *testing.T) {
	ws := reviewing.WatchStatus{Name: "sustaining-automation", Mode: "dry run", DailyBudget: 100}
	assert.Equal(t,
		"sustaining-automation · dry run · $0.00/$100 today · 0 reviewing · polling GitHub…",
		Summary(ws))

	ws.LastPoll = now
	ws.QuotaRemaining = 3912
	ws.Err = "non-200 OK status code: 502 Bad Gateway\x1b[2J " + strings.Repeat("x", 200)
	got := Summary(ws)
	assert.Contains(t, got, "quota 3,912 · error: non-200 OK status code: 502 Bad Gateway[2J")
	assert.NotContains(t, got, "\x1b")
	assert.Less(t, len([]rune(got)), 200, "long errors are clipped")
}

func TestSummaryLeadsWithPrompt(t *testing.T) {
	ws := reviewing.WatchStatus{
		Name:           "sustaining-automation",
		Mode:           "live",
		DailyBudget:    100,
		LastPoll:       now,
		QuotaRemaining: 4000,
		Prompt:         "Touch your security key to fetch chainguard-dev/stereo#367322",
	}
	assert.True(
		t,
		strings.HasPrefix(
			Summary(ws),
			"▶ Touch your security key to fetch chainguard-dev/stereo#367322 · sustaining-automation · live",
		),
	)
}

func TestSummary(t *testing.T) {
	ws := reviewing.WatchStatus{
		Name: "sustaining-automation", Mode: "live", SpentToday: 14.2, DailyBudget: 100,
		InFlight: 2, QuotaRemaining: 3912, LastPoll: now,
	}
	assert.Equal(t,
		"sustaining-automation · live · $14.20/$100 today · 2 reviewing · quota 3,912",
		Summary(ws))

	ws.Paused, ws.QuotaRemaining = "budget", 0
	assert.Equal(t,
		"sustaining-automation · paused: budget · $14.20/$100 today · 2 reviewing · quota unknown",
		Summary(ws))
}

func TestPRData(t *testing.T) {
	s := st("stereo", 357448, reviewing.StatusApproved, time.Minute)
	s.Title = "kev: bump"
	d := PRData(s)
	require.NotNil(t, d.Primary)
	assert.Equal(t, "https://github.com/chainguard-dev/stereo/pull/357448", d.Primary.Url)
	assert.Equal(t, "chainguard-dev/stereo", d.Primary.GetRepoNameWithOwner())
	assert.Equal(t, 357448, d.Primary.Number)
	assert.Equal(t, "kev: bump", d.Primary.Title)
	assert.False(t, d.IsEnriched, "the sidebar enriches it on selection")
}

func TestCellsMarksDryRunStatus(t *testing.T) {
	s := st("stereo", 367322, reviewing.StatusApproved, time.Minute)
	assert.Equal(t, "approved", Cells(s, now).Status)
	s.DryRun = true
	assert.Equal(t, "approved (dry)", Cells(s, now).Status, "nothing was posted")
}
