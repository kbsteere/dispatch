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

// viewGitHub returns one open PR from search and details.
type viewGitHub struct{ reviewing.GitHub }

func (viewGitHub) Viewer(goctx.Context) (string, error) { return "kbsteere", nil }

func (viewGitHub) Search(
	goctx.Context,
	string,
	int,
) ([]reviewing.SearchHit, reviewing.RateLimit, error) {
	return []reviewing.SearchHit{{
		Key:    reviewing.PRKey{Owner: "chainguard-dev", Repo: "stereo", Number: 366936},
		NodeID: "N1", UpdatedAt: "2026-10-05T20:22:04Z",
	}}, reviewing.RateLimit{Cost: 1, Remaining: 4000, ResetAt: time.Now().Add(time.Hour)}, nil
}

func (viewGitHub) Details(
	goctx.Context,
	[]string,
) ([]reviewing.PRDetail, reviewing.RateLimit, error) {
	return []reviewing.PRDetail{{
		Key:    reviewing.PRKey{Owner: "chainguard-dev", Repo: "stereo", Number: 366936},
		NodeID: "N1", State: "OPEN", Author: "tcnghia", Title: "version_data: add EOL policy URLs",
		HeadOID: strings.Repeat("a", 40), BaseRef: "main", UpdatedAt: "2026-10-05T20:22:04Z",
	}}, reviewing.RateLimit{Cost: 1, Remaining: 3999, ResetAt: time.Now().Add(time.Hour)}, nil
}

func (viewGitHub) RequiredContexts(
	goctx.Context,
	reviewing.PRKey,
	string,
) (reviewing.RequiredContexts, error) {
	return nil, nil
}

func TestReviewingViewShowsPRsAfterFirstPoll(t *testing.T) {
	cfg, err := config.ParseConfig(config.Location{
		ConfigFlag:       "../config/testdata/test-config.yml",
		SkipGlobalConfig: true,
	})
	require.NoError(t, err)
	w := config.ReviewWatch{
		Name:    "sustaining-automation",
		Authors: []string{"tcnghia"},
		Scopes:  []string{"repo:chainguard-dev/stereo"},
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
		GitHub: viewGitHub{}, LookupPath: func(string) (string, bool) { return "", false },
	})
	require.NoError(t, err)
	t.Cleanup(e.Shutdown)
	m.reviewers = reviewers{e}

	// The user switches to the Reviewing view before the first poll ends.
	m.ctx.View = config.NotificationsView
	m.switchSelectedView()
	require.Equal(t, config.ReviewingView, m.ctx.View)
	rs, ok := m.getCurrSection().(*reviewsection.Model)
	require.True(t, ok)
	require.Equal(t, 0, rs.NumRows())

	// The first poll finishes.
	next, _ := m.Update(pollCmd(e)())
	m = next.(Model)

	rs, ok = m.getCurrSection().(*reviewsection.Model)
	require.True(t, ok)
	require.Equal(t, 1, rs.NumRows(), "the table shows the watch's PRs after the poll")
	require.Contains(t, rs.View(), "stereo#366936")
	require.NotContains(t, rs.View(), "quota unknown")

	// Refreshing reloads from watch state instead of blanking the table.
	for _, k := range []string{"r", "R"} {
		next, _ = m.Update(tea.KeyPressMsg{Code: rune(k[0]), Text: k})
		m = next.(Model)
		rs, ok = m.getCurrSection().(*reviewsection.Model)
		require.True(t, ok)
		require.Equal(t, 1, rs.NumRows(), "after %q the table still shows the watch's PRs", k)
		require.False(t, rs.GetIsLoading())
	}
}

func TestReviewNowKeyQueuesAReview(t *testing.T) {
	cfg, err := config.ParseConfig(config.Location{
		ConfigFlag:       "../config/testdata/test-config.yml",
		SkipGlobalConfig: true,
	})
	require.NoError(t, err)
	w := config.ReviewWatch{
		Name:    "sustaining-automation",
		Authors: []string{"tcnghia"},
		Scopes:  []string{"repo:chainguard-dev/stereo"},
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
		GitHub: viewGitHub{}, LookupPath: func(string) (string, bool) { return "", false },
	})
	require.NoError(t, err)
	t.Cleanup(e.Shutdown)
	m.reviewers = reviewers{e}
	m.ctx.View = config.NotificationsView
	m.switchSelectedView()
	next, _ := m.Update(pollCmd(e)())
	m = next.(Model)

	next, cmd := m.Update(tea.KeyPressMsg{Code: 'N', Text: "N"})
	m = next.(Model)
	require.NotNil(t, cmd)
	var forced bool
	for _, st := range e.States() {
		forced = forced || st.ForceReview
	}
	require.True(t, forced, "N in the Reviewing view queues a review of the selected PR")
}

func TestStatusTicksRunOnceAndStopWhenIdle(t *testing.T) {
	m := NewModel(config.Location{}, Repositories{})
	m.ctx.Config = &config.Config{}
	require.NotNil(t, m.startStatusTicks(), "starts the loop")
	require.Nil(t, m.startStatusTicks(), "never a second loop")
	require.Nil(t, m.onStatusTick(), "no reviews in flight: the loop stops")
	require.False(t, m.statusTicking)
	require.NotNil(t, m.startStatusTicks(), "and can start again later")
}

func TestHeartbeatAfterSleepResyncs(t *testing.T) {
	m := NewModel(config.Location{}, Repositories{})
	m.ctx.Config = &config.Config{}
	w := config.ReviewWatch{
		Name: "sustaining-automation", Authors: []string{"tcnghia"},
		Scopes: []string{"repo:chainguard-dev/stereo"},
	}
	e, err := reviewing.NewEngine(reviewing.Deps{
		Watch: w, Limits: config.DefaultReviewingLimits(), StateRoot: t.TempDir(),
		GitHub: viewGitHub{}, LookupPath: func(string) (string, bool) { return "", false },
	})
	require.NoError(t, err)
	t.Cleanup(e.Shutdown)
	m.reviewers = reviewers{e}
	_, _, err = e.Poll(t.Context())
	require.NoError(t, err)
	now := time.Now()
	require.False(t, e.PollDue(now), "just polled")

	m.lastBeat = reviewHeartbeatMsg{wall: now, awake: time.Hour}
	require.NotNil(
		t,
		m.onHeartbeat(
			reviewHeartbeatMsg{wall: now.Add(time.Minute), awake: time.Hour + time.Minute},
		),
	)
	assert.False(t, e.PollDue(now.Add(time.Minute)), "an ordinary beat polls only when due")

	// Eight hours asleep, then one awake minute.
	woke := reviewHeartbeatMsg{wall: now.Add(8 * time.Hour), awake: time.Hour + 2*time.Minute}
	require.NotNil(t, m.onHeartbeat(woke))
	assert.True(t, e.PollDue(woke.wall), "waking makes a poll due at once")
	assert.Equal(t, woke, m.lastBeat)
}

func TestReviewAllBaselineAsksThenQueues(t *testing.T) {
	cfg, err := config.ParseConfig(config.Location{
		ConfigFlag:       "../config/testdata/test-config.yml",
		SkipGlobalConfig: true,
	})
	require.NoError(t, err)
	w := config.ReviewWatch{
		Name:    "sustaining-automation",
		Authors: []string{"tcnghia"},
		Scopes:  []string{"repo:chainguard-dev/stereo"},
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
		GitHub: viewGitHub{}, LookupPath: func(string) (string, bool) { return "", false },
	})
	require.NoError(t, err)
	t.Cleanup(e.Shutdown)
	m.reviewers = reviewers{e}
	m.ctx.View = config.NotificationsView
	m.switchSelectedView()
	next, _ := m.Update(pollCmd(e)()) // the first poll baselines the PR
	m = next.(Model)

	press := func(k tea.KeyPressMsg) {
		next, _ := m.Update(k)
		m = next.(Model)
	}
	forced := func() bool {
		for _, st := range e.States() {
			if st.ForceReview {
				return true
			}
		}
		return false
	}

	press(tea.KeyPressMsg{Code: 'E', Text: "E"})
	require.True(t, m.getCurrSection().IsPromptConfirmationFocused(), "E asks before queueing")
	assert.Contains(t, m.getCurrSection().GetPromptConfirmation(),
		"Queue full reviews of 1 baseline PRs")
	assert.False(t, forced(), "nothing is queued before the answer")

	press(tea.KeyPressMsg{Code: 'n', Text: "n"})
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.False(t, forced(), "answering n queues nothing")

	press(tea.KeyPressMsg{Code: 'E', Text: "E"})
	press(tea.KeyPressMsg{Code: 'y', Text: "y"})
	press(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.False(t, m.getCurrSection().IsPromptConfirmationFocused())
	assert.True(t, forced(), "answering y queues the baseline PR")
}
