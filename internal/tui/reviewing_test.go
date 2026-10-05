package tui

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
)

func TestFooterText(t *testing.T) {
	assert.Equal(
		t,
		"sustaining-automation · 2 reviewing · $14.20 today · live",
		footerText(
			[]reviewing.WatchStatus{
				{Name: "sustaining-automation", InFlight: 2, SpentToday: 14.2, Mode: "live"},
			},
		),
	)
	assert.Equal(
		t,
		"w · 0 reviewing · $100.00 today · paused: budget",
		footerText(
			[]reviewing.WatchStatus{{Name: "w", SpentToday: 100, Mode: "live", Paused: "budget"}},
		),
	)
	assert.Equal(t, "w · 0 reviewing · $0.00 today · dry run",
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
		{config.NotificationsView, true, false, config.PRsView},
		{config.PRsView, true, false, config.ReviewingView},
		{config.ReviewingView, true, false, config.IssuesView},
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
