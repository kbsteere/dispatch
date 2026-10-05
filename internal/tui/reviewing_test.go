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

func TestReviewingSections(t *testing.T) {
	cfg := config.ReviewingForConfig{Watches: []config.ReviewWatch{
		{
			Name:    "sustaining-automation",
			Authors: []string{"tcnghia"},
			Scopes:  []string{"repo:chainguard-dev/mono"},
		},
	}}
	got := ReviewingSections(
		cfg,
		map[string][]string{
			"sustaining-automation": {"https://github.com/chainguard-dev/omnibump/pull/88"},
		},
	)
	assert.Equal(t, []config.PrsSectionConfig{
		{
			Title:   "Reviewing: sustaining-automation",
			Filters: "is:open draft:false archived:false repo:chainguard-dev/mono author:tcnghia",
		},
		{
			Title: "Reviewing: sustaining-automation · added",
			Urls:  []string{"https://github.com/chainguard-dev/omnibump/pull/88"},
		},
	}, got)
	assert.Len(t, ReviewingSections(cfg, nil), 1, "no added section without hand-added PRs")
	assert.Empty(t, ReviewingSections(config.ReviewingForConfig{}, nil))
}

func TestWatchFromSectionTitle(t *testing.T) {
	name, ok := watchFromSectionTitle("Reviewing: sustaining-automation · added")
	assert.True(t, ok)
	assert.Equal(t, "sustaining-automation", name)
	name, ok = watchFromSectionTitle("Reviewing: sustaining-automation")
	assert.True(t, ok)
	assert.Equal(t, "sustaining-automation", name)
	_, ok = watchFromSectionTitle("My Pull Requests")
	assert.False(t, ok)
}

func TestContextDirsSkipsWildcardsAndMissing(t *testing.T) {
	dirs := contextDirs(map[string]string{
		"chainguard-dev/*":    "/nowhere/*",
		":owner/:repo":        "/nowhere/:owner/:repo",
		"chainguard-dev/nope": t.TempDir(),
	})
	assert.Empty(t, dirs, "wildcards are skipped and non-repos are dropped")
}
