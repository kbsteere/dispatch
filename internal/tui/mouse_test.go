package tui

import (
	goctx "context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/common"
	"github.com/kbsteere/dispatch/v4/internal/tui/context"
	"github.com/kbsteere/dispatch/v4/internal/tui/theme"
)

func TestOverSidebar(t *testing.T) {
	m := NewModel(config.Location{}, Repositories{})
	m.ctx.Config = &config.Config{}
	m.ctx.ScreenWidth, m.ctx.ScreenHeight = 200, 50

	m.sidebar.IsOpen = false
	assert.False(t, m.overSidebar(190, 10), "no preview pane, so the wheel moves the table")

	m.sidebar.IsOpen = true
	m.ctx.PreviewPosition = "right"
	m.ctx.MainContentWidth, m.ctx.MainContentHeight = 120, 40
	assert.False(t, m.overSidebar(119, 10), "left of the pane is the table")
	assert.True(t, m.overSidebar(120, 10), "the pane starts where the table ends")

	m.ctx.PreviewPosition = "bottom"
	m.ctx.MainContentWidth, m.ctx.MainContentHeight = 200, 20
	assert.False(t, m.overSidebar(190, common.TabsHeight+19))
	assert.True(t, m.overSidebar(10, common.TabsHeight+20), "below the table is the pane")
}

// twoPRGitHub returns two open PRs.
type twoPRGitHub struct{ viewGitHub }

func (twoPRGitHub) Search(
	goctx.Context,
	string,
	int,
) ([]reviewing.SearchHit, reviewing.RateLimit, error) {
	hits := make([]reviewing.SearchHit, 0, 2)
	for _, n := range []int{366936, 367322} {
		hits = append(hits, reviewing.SearchHit{
			Key:    reviewing.PRKey{Owner: "chainguard-dev", Repo: "stereo", Number: n},
			NodeID: fmt.Sprintf("N%d", n), UpdatedAt: "2026-10-05T20:22:04Z",
		})
	}
	return hits, reviewing.RateLimit{
		Cost:      1,
		Remaining: 4000,
		ResetAt:   time.Now().Add(time.Hour),
	}, nil
}

func (twoPRGitHub) Details(
	_ goctx.Context,
	ids []string,
) ([]reviewing.PRDetail, reviewing.RateLimit, error) {
	out := make([]reviewing.PRDetail, 0, len(ids))
	for _, id := range ids {
		var n int
		_, _ = fmt.Sscanf(id, "N%d", &n)
		out = append(out, reviewing.PRDetail{
			Key:    reviewing.PRKey{Owner: "chainguard-dev", Repo: "stereo", Number: n},
			NodeID: id, State: "OPEN", Author: "tcnghia", Title: "version_data change",
			HeadOID: strings.Repeat("a", 40), BaseRef: "main", UpdatedAt: "2026-10-05T20:22:04Z",
		})
	}
	return out, reviewing.RateLimit{
		Cost:      1,
		Remaining: 3999,
		ResetAt:   time.Now().Add(time.Hour),
	}, nil
}

func TestMouseWheelMovesTheTableSelection(t *testing.T) {
	cfg, err := config.ParseConfig(config.Location{
		ConfigFlag:       "../config/testdata/test-config.yml",
		SkipGlobalConfig: true,
	})
	require.NoError(t, err)
	w := config.ReviewWatch{
		Name: "sustaining-automation", Authors: []string{"tcnghia"},
		Scopes: []string{"repo:chainguard-dev/stereo"},
	}
	cfg.ReviewingFor.Watches = []config.ReviewWatch{w}
	m := NewModel(config.Location{}, Repositories{})
	m.ctx.Config = &cfg
	m.ctx.Theme = theme.ParseTheme(&cfg)
	m.ctx.Styles = context.InitStyles(m.ctx.Theme)
	m.ctx.ScreenWidth, m.ctx.ScreenHeight = 200, 50
	m.ctx.MainContentWidth, m.ctx.MainContentHeight = 200, 30
	e, err := reviewing.NewEngine(reviewing.Deps{
		Watch: w, Limits: config.DefaultReviewingLimits(), StateRoot: t.TempDir(),
		GitHub: twoPRGitHub{}, LookupPath: func(string) (string, bool) { return "", false },
	})
	require.NoError(t, err)
	t.Cleanup(e.Shutdown)
	m.reviewers = reviewers{e}
	m.ctx.View = config.PRsView
	m.switchSelectedView()
	next, _ := m.Update(pollCmd(e)())
	m = next.(Model)
	require.Equal(t, 2, m.getCurrSection().NumRows())
	require.Equal(t, 0, m.getCurrSection().CurrRow())

	next, _ = m.Update(tea.MouseWheelMsg{X: 10, Y: 10, Button: tea.MouseWheelDown})
	m = next.(Model)
	assert.Equal(t, 1, m.getCurrSection().CurrRow(), "wheel down selects the next row")
	next, _ = m.Update(tea.MouseWheelMsg{X: 10, Y: 10, Button: tea.MouseWheelUp})
	m = next.(Model)
	assert.Equal(t, 0, m.getCurrSection().CurrRow(), "wheel up selects the previous row")
}
