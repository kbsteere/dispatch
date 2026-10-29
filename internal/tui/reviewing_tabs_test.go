package tui

import (
	goctx "context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/reviewsection"
	"github.com/kbsteere/dispatch/v4/internal/tui/context"
	"github.com/kbsteere/dispatch/v4/internal/tui/theme"
)

var (
	matePR    = reviewing.PRKey{Owner: "chainguard-dev", Repo: "stereo", Number: 366936}
	outsidePR = reviewing.PRKey{Owner: "chainguard-dev", Repo: "mono", Number: 75471}
)

// teamGitHub returns a teammate's PR from the author search and someone
// else's from the team search.
type teamGitHub struct{ viewGitHub }

func (teamGitHub) Search(
	_ goctx.Context,
	filter string,
	_ int,
) ([]reviewing.SearchHit, reviewing.RateLimit, error) {
	rl := reviewing.RateLimit{Cost: 1, Remaining: 4000, ResetAt: time.Now().Add(time.Hour)}
	if strings.Contains(filter, "team-review-requested:") {
		return []reviewing.SearchHit{
			{Key: outsidePR, NodeID: "N2", UpdatedAt: "2026-10-28T12:42:13Z"},
		}, rl, nil
	}
	return []reviewing.SearchHit{
		{Key: matePR, NodeID: "N1", UpdatedAt: "2026-10-05T20:22:04Z"},
	}, rl, nil
}

func (teamGitHub) Details(
	_ goctx.Context,
	ids []string,
) ([]reviewing.PRDetail, reviewing.RateLimit, error) {
	all := map[string]reviewing.PRDetail{
		"N1": {
			Key: matePR, NodeID: "N1", State: "OPEN", Author: "tcnghia",
			Title: "version_data: add EOL policy URLs", HeadOID: strings.Repeat("a", 40),
			BaseRef: "main", UpdatedAt: "2026-10-05T20:22:04Z",
		},
		"N2": {
			Key: outsidePR, NodeID: "N2", State: "OPEN", Author: "brianmcarey",
			Title: "fix(cve-remediation): raise existing Go pins", HeadOID: strings.Repeat("b", 40),
			BaseRef: "main", UpdatedAt: "2026-10-28T12:42:13Z",
		},
	}
	var out []reviewing.PRDetail
	for _, id := range ids {
		out = append(out, all[id])
	}
	return out, reviewing.RateLimit{
		Cost:      1,
		Remaining: 3999,
		ResetAt:   time.Now().Add(time.Hour),
	}, nil
}

func newTeamTabsModel(t *testing.T) (Model, *reviewing.Engine) {
	t.Helper()
	cfg, err := config.ParseConfig(config.Location{
		ConfigFlag:       "../config/testdata/test-config.yml",
		SkipGlobalConfig: true,
	})
	require.NoError(t, err)
	w := config.ReviewWatch{
		Name:    "sustaining-automation",
		Authors: []string{"tcnghia"},
		Scopes:  []string{"repo:chainguard-dev/stereo"},
		Teams:   []string{"chainguard-dev/sustaining-tooling-team-write"},
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
		GitHub: teamGitHub{}, LookupPath: func(string) (string, bool) { return "", false },
	})
	require.NoError(t, err)
	t.Cleanup(e.Shutdown)
	m.reviewers = reviewers{e}
	m.ctx.View = config.NotificationsView
	m.switchSelectedView()
	next, _ := m.Update(pollCmd(e)()) // the first poll baselines both PRs
	return next.(Model), e
}

func TestReviewingTabsSplitTeammatesFromOutside(t *testing.T) {
	m, _ := newTeamTabsModel(t)
	require.Len(t, m.reviewSections, 2, "a watch with teams gets a second tab")

	mates, ok := m.reviewSections[0].(*reviewsection.Model)
	require.True(t, ok)
	outside, ok := m.reviewSections[1].(*reviewsection.Model)
	require.True(t, ok)

	assert.Equal(t, "sustaining-automation", mates.GetConfig().Title)
	assert.Equal(t, "sustaining-automation · outside", outside.GetConfig().Title)
	assert.Equal(t, "sustaining-automation", outside.Watch(), "both tabs act on the same watch")

	require.Equal(t, 1, mates.NumRows())
	assert.Contains(t, mates.View(), "stereo#366936")
	assert.NotContains(t, mates.View(), "mono#75471")

	require.Equal(t, 1, outside.NumRows())
	assert.Contains(t, outside.View(), "mono#75471")
	assert.NotContains(t, outside.View(), "(outside)", "the tab already says so")
	assert.NotContains(t, outside.View(), "stereo#366936")

	titles := make([]string, 0, 2)
	for _, c := range m.ctx.GetViewSectionsConfig() {
		titles = append(titles, c.Title)
	}
	assert.Equal(t, []string{"sustaining-automation", "sustaining-automation · outside"}, titles,
		"the tab bar lists the same tables")
}

func TestReviewAllBaselineQueuesOnlyTheCurrentTab(t *testing.T) {
	m, e := newTeamTabsModel(t)
	press := func(k tea.KeyPressMsg) {
		next, _ := m.Update(k)
		m = next.(Model)
	}
	forced := func() []reviewing.PRKey {
		var out []reviewing.PRKey
		for _, st := range e.States() {
			if st.ForceReview {
				out = append(out, st.Key)
			}
		}
		return out
	}

	m.setCurrSectionId(1)
	press(tea.KeyPressMsg{Code: 'E', Text: "E"})
	assert.Contains(t, m.getCurrSection().GetPromptConfirmation(),
		"Queue full reviews of 1 baseline PRs", "only the outside tab's PR is counted")
	press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.Equal(t, []reviewing.PRKey{outsidePR}, forced(),
		"E on the outside tab leaves teammates' baselines alone")
}
