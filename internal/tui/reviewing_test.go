package tui

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
)

func TestFooterText(t *testing.T) {
	assert.Equal(
		t,
		"sustaining-automation · 2 reviewing · $14.20 today · $61.35 wk · live",
		footerText(
			[]reviewing.WatchStatus{{
				Name: "sustaining-automation", InFlight: 2, SpentToday: 14.2, SpentWeek: 61.35,
				Mode: "live",
			}},
		),
	)
	assert.Equal(
		t,
		"w · 0 reviewing · $100.00 today · $0.00 wk · paused: budget",
		footerText(
			[]reviewing.WatchStatus{{Name: "w", SpentToday: 100, Mode: "live", Paused: "budget"}},
		),
	)
	assert.Equal(t, "w · 0 reviewing · $0.00 today · $0.00 wk · dry run",
		footerText([]reviewing.WatchStatus{{Name: "w", Mode: "dry run"}}))
	assert.Equal(t, "", footerText(nil))
}

func TestContextDirsSkipsWildcardsAndMissing(t *testing.T) {
	dirs := contextDirs(map[string]string{
		"chainguard-dev/*":    "/nowhere/*",
		":owner/:repo":        "/nowhere/:owner/:repo",
		"chainguard-dev/nope": t.TempDir(),
	})
	assert.Empty(t, dirs, "wildcards are skipped and non-repos are dropped")
}

func TestNextView(t *testing.T) {
	tests := []struct {
		from            config.ViewType
		reviewing, repo bool
		want            config.ViewType
	}{
		{config.NotificationsView, true, false, config.ReviewingView},
		{config.ReviewingView, true, false, config.PRsView},
		{config.PRsView, true, false, config.IssuesView},
		{config.NotificationsView, false, false, config.PRsView},
		{config.IssuesView, true, false, config.NotificationsView},
		{config.PRsView, false, false, config.IssuesView},
		{config.IssuesView, false, true, config.RepoView},
		{config.RepoView, true, true, config.NotificationsView},
		{config.ReviewingView, false, false, config.NotificationsView},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.want, nextView(tt.from, tt.reviewing, tt.repo), "from %s", tt.from)
	}
}

func TestFooterTextShowsQueue(t *testing.T) {
	assert.Equal(t,
		"sustaining-automation · 3 reviewing · 25 queued · $7.17 today · $0.00 wk · live",
		footerText([]reviewing.WatchStatus{{
			Name: "sustaining-automation", Mode: "live", InFlight: 3, Queued: 25, SpentToday: 7.17,
		}}))
}

func TestSleptBetween(t *testing.T) {
	wall := time.Date(2026, 10, 6, 16, 22, 0, 0, time.UTC)
	prev := reviewHeartbeatMsg{wall: wall, awake: 3 * time.Hour}
	assert.Zero(
		t,
		sleptBetween(reviewHeartbeatMsg{}, prev),
		"the first beat has nothing to compare",
	)
	awake := reviewHeartbeatMsg{wall: wall.Add(time.Minute), awake: 3*time.Hour + time.Minute}
	assert.Zero(t, sleptBetween(prev, awake))
	// The laptop slept from 16:22 to 06:47; only one awake minute passed.
	woke := reviewHeartbeatMsg{
		wall:  time.Date(2026, 10, 7, 6, 48, 0, 0, time.UTC),
		awake: 3*time.Hour + time.Minute,
	}
	assert.Equal(t, 14*time.Hour+25*time.Minute, sleptBetween(prev, woke))
}
