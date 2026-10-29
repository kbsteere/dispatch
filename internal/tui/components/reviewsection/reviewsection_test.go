package reviewsection

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/data"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/prrow"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/table"
	"github.com/kbsteere/dispatch/v4/internal/tui/context"
	"github.com/kbsteere/dispatch/v4/internal/tui/theme"
)

func newTestSection(t *testing.T) Model {
	t.Helper()
	cfg, err := config.ParseConfig(config.Location{
		ConfigFlag:       "../../../config/testdata/test-config.yml",
		SkipGlobalConfig: true,
	})
	require.NoError(t, err)
	thm := theme.ParseTheme(&cfg)
	ctx := &context.ProgramContext{
		Config: &cfg, Theme: thm, Styles: context.InitStyles(thm),
		ScreenWidth: 160, ScreenHeight: 40, MainContentWidth: 160, MainContentHeight: 30,
		StartTask: func(context.Task) tea.Cmd { return nil },
	}
	m := NewModel(1, ctx, "sustaining-automation", GroupAll)
	m.now = func() time.Time { return now }
	return m
}

func TestSectionShowsWatchStateInOrder(t *testing.T) {
	m := newTestSection(t)
	assert.Equal(t, "sustaining-automation", m.Watch())
	assert.Nil(t, m.GetCurrRow(), "no rows: no selection")

	approved := st("stereo", 357102, reviewing.StatusApproved, time.Hour)
	attention := st("mono", 66173, reviewing.StatusNeedsAttention, 10*time.Minute)
	attention.Title = "version-reconciler: enable heuristic Actions EOL for every project"
	attention.Author = "tcnghia"
	m.SetData(
		reviewing.WatchStatus{Name: "sustaining-automation", Mode: "dry run", DailyBudget: 100},
		[]reviewing.PRState{approved, attention},
	)

	require.Equal(t, 2, m.NumRows())
	assert.Equal(t, 2, m.GetTotalCount())
	rows := m.BuildRows()
	assert.Equal(t, "attention", ansi.Strip(rows[0][0]))
	assert.NotEqual(t, "attention", rows[0][0], "the status is colored")
	assert.Equal(
		t,
		"mono#66173\nversion-reconciler: enable heuristic Actions EOL for every project",
		rows[0][1],
	)
	assert.Equal(t, "-", rows[0][3], "you have not reviewed it")
	assert.Equal(t, "✓", rows[0][5])

	row, ok := m.GetCurrRow().(*prrow.Data)
	require.True(t, ok)
	assert.Equal(
		t,
		"https://github.com/chainguard-dev/mono/pull/66173",
		row.Primary.Url,
		"selection follows display order",
	)
	cur, ok := m.CurrState()
	require.True(t, ok)
	assert.Equal(t, 66173, cur.Key.Number)

	view := m.View()
	assert.Contains(t, view, "sustaining-automation · dry run · $0.00/$100 today")
	assert.Contains(t, view, "mono#66173")
}

func TestSectionCachesEnrichedPR(t *testing.T) {
	m := newTestSection(t)
	m.SetData(
		reviewing.WatchStatus{},
		[]reviewing.PRState{st("stereo", 366936, reviewing.StatusBaseline, time.Minute)},
	)
	row := m.GetCurrRow().(*prrow.Data)
	assert.False(t, row.IsEnriched)

	m.EnrichPR(
		data.EnrichedPullRequestData{
			Url:   "https://github.com/chainguard-dev/stereo/pull/366936",
			Title: "full",
		},
	)
	m.SetData(
		reviewing.WatchStatus{},
		[]reviewing.PRState{st("stereo", 366936, reviewing.StatusBaseline, time.Minute)},
	)
	row = m.GetCurrRow().(*prrow.Data)
	assert.True(t, row.IsEnriched, "a refresh keeps the fetched PR")
	assert.Equal(t, "full", row.Enriched.Title)
	assert.Equal(t, "full", row.Primary.Title, "the header uses the fetched PR")
	assert.Equal(t, "chainguard-dev/stereo", row.Primary.GetRepoNameWithOwner())
}

func TestSectionHeaderFieldsBeforeFetch(t *testing.T) {
	m := newTestSection(t)
	m.SetData(
		reviewing.WatchStatus{},
		[]reviewing.PRState{st("mono", 66155, reviewing.StatusFailed, time.Minute)},
	)
	row := m.GetCurrRow().(*prrow.Data)
	assert.Equal(
		t,
		"dnegreira",
		row.Primary.Author.Login,
		"author is known before the PR is fetched",
	)
}

func TestSectionLoadingUntilFirstPoll(t *testing.T) {
	m := newTestSection(t)
	m.SetData(reviewing.WatchStatus{Name: "sustaining-automation"}, nil)
	assert.True(t, m.GetIsLoading(), "first poll still running")
	assert.NotContains(t, m.View(), "No PRs were found")

	m.SetData(reviewing.WatchStatus{Name: "sustaining-automation", LastPoll: now}, nil)
	assert.False(t, m.GetIsLoading())
	assert.Contains(
		t,
		m.View(),
		"No PRs were found",
		"after a poll an empty table is a real result",
	)
}

func TestSectionHasNoSearchOrFetch(t *testing.T) {
	m := newTestSection(t)
	assert.Nil(t, m.SetIsSearching(true))
	assert.False(t, m.IsSearchFocused())
	assert.Nil(t, m.FetchNextPageSectionRows())
}

func TestSectionPromptConfirmation(t *testing.T) {
	m := newTestSection(t)
	m.SetData(
		reviewing.WatchStatus{},
		[]reviewing.PRState{st("mono", 66173, reviewing.StatusApproved, time.Minute)},
	)
	m.SetPromptConfirmationAction("close")
	m.SetIsPromptConfirmationShown(true)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.False(t, m.IsPromptConfirmationShown, "dismissed")
	_ = cmd

	m.SetIsPromptConfirmationShown(true)
	m.PromptConfirmationBox.SetValue("y")
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.NotNil(t, cmd, "y runs the action")
}

type addRefMsg struct{ ref string }

func TestSectionAskInputOnEmptyTable(t *testing.T) {
	m := newTestSection(t)
	m.SetData(reviewing.WatchStatus{LastPoll: now}, nil)
	var got []string
	m.AskInput("Add PR to the review watch (URL or owner/repo#N):", func(ref string) tea.Cmd {
		got = append(got, ref)
		return func() tea.Msg { return addRefMsg{ref} }
	})

	assert.Contains(t, ansi.Strip(m.GetPromptConfirmation()), "Add PR to the review watch",
		"the prompt shows even with no PR selected")

	for _, r := range "  https://github.com/chainguard-dev/mono/pull/66370 " {
		m.Update(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.False(t, m.IsPromptConfirmationShown, "enter closes the prompt")
	require.Equal(t, []string{"https://github.com/chainguard-dev/mono/pull/66370"}, got,
		"the typed reference is submitted once, trimmed")
	require.NotNil(t, cmd)
	assert.Contains(
		t,
		batchMsgs(cmd),
		tea.Msg(addRefMsg{"https://github.com/chainguard-dev/mono/pull/66370"}),
	)
	assert.Empty(t, m.PromptConfirmationBox.Value(), "the prompt is cleared")

	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.Len(t, got, 1, "a closed prompt does not submit again")
}

func TestSectionAskInputCancelled(t *testing.T) {
	m := newTestSection(t)
	called := false
	m.AskInput("Add PR:", func(string) tea.Cmd { called = true; return nil })
	m.PromptConfirmationBox.SetValue("chainguard-dev/stereo#216533")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	assert.False(t, m.IsPromptConfirmationShown)
	assert.False(t, called, "esc submits nothing")

	m.AskInput("Add PR:", func(string) tea.Cmd { called = true; return nil })
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	assert.False(t, called, "empty text submits nothing")
}

// batchMsgs runs cmd, expanding batches, and returns the messages it
// produces.
func batchMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	b, ok := msg.(tea.BatchMsg)
	if !ok {
		return []tea.Msg{msg}
	}
	var out []tea.Msg
	for _, c := range b {
		out = append(out, batchMsgs(c)...)
	}
	return out
}

func TestSectionSummaryNeverWraps(t *testing.T) {
	m := newTestSection(t)
	ws := reviewing.WatchStatus{
		Name: "sustaining-automation", Mode: "live", DailyBudget: 100, SpentToday: 5.29,
		SpentWeek: 106.74, SpentMonth: 106.74, InFlight: 3, Queued: 2, QuotaRemaining: 4321,
		LastPoll: now,
	}
	states := []reviewing.PRState{st("mono", 66173, reviewing.StatusApproved, time.Hour)}
	m.SetData(ws, states)
	m.UpdateProgramContext(m.Ctx)
	wide := lipgloss.Height(m.View())

	// The preview pane leaves the table less room than the summary needs.
	m.Ctx.MainContentWidth = 80
	m.UpdateProgramContext(m.Ctx)
	view := m.View()
	assert.Equal(t, wide, lipgloss.Height(view), "a wrapped summary pushes the footer off screen")
	summary, _, _ := strings.Cut(view, "\n")
	assert.LessOrEqual(t, lipgloss.Width(summary), 80)
	assert.Contains(t, ansi.Strip(view), "sustaining-automation · live · $5.29/$100 today")
}

func TestSectionHidesStandingApprovals(t *testing.T) {
	m := newTestSection(t)
	done := st("stereo", 357102, reviewing.StatusApproved, time.Hour)
	done.MyReview, done.MyReviewOnHead = "APPROVED", true
	pushed := st("stereo", 357103, reviewing.StatusApproved, time.Hour)
	pushed.MyReview = "APPROVED" // on an older commit
	open := st("mono", 66173, reviewing.StatusWaitingOnAuthor, time.Hour)
	ws := reviewing.WatchStatus{Name: "sustaining-automation", Mode: "live", DailyBudget: 100}

	m.SetData(ws, []reviewing.PRState{done, pushed, open})
	assert.Equal(t, 2, m.NumRows())
	assert.Contains(t, ansi.Strip(m.View()), "· 1 approved hidden")
	assert.NotContains(t, ansi.Strip(m.View()), "stereo#357102")

	m.Ctx.ShowApproved = true
	m.SetData(ws, []reviewing.PRState{done, pushed, open})
	assert.Equal(t, 3, m.NumRows())
	assert.NotContains(t, ansi.Strip(m.View()), "approved hidden")
}

func TestNarrowSectionShowsWholePRNumbers(t *testing.T) {
	m := newTestSection(t)
	// A run of PRs whose numbers differ only in the last digit, as when one
	// author opens a batch of PRs at once.
	var states []reviewing.PRState
	for n := 386270; n <= 386278; n++ {
		s := st("stereo", n, reviewing.StatusNeedsAttention, time.Hour)
		s.Author = "powersj"
		s.Title = "chore: set explicit language on bump steps (batch 70)"
		states = append(states, s)
	}
	m.SetData(reviewing.WatchStatus{Name: "sustaining-automation", Mode: "live"}, states)
	for _, width := range []int{160, 98, 70} {
		m.Ctx.MainContentWidth = width
		m.UpdateProgramContext(m.Ctx)
		out := ansi.Strip(m.View())
		for n := 386270; n <= 386278; n++ {
			assert.Contains(t, out, fmt.Sprintf("stereo#%d", n), "width %d", width)
		}
		for line := range strings.SplitSeq(out, "\n") {
			if strings.Contains(line, "stereo#") {
				assert.NotContains(t, line, "chore", "the title has its own line; width %d", width)
			}
			assert.LessOrEqual(t, lipgloss.Width(line), width, "width %d", width)
		}
	}
	m.Ctx.MainContentWidth = 160
	m.UpdateProgramContext(m.Ctx)
	assert.Contains(t, ansi.Strip(m.View()), "Update", "a wide table shows every column")
}

func TestFitColumns(t *testing.T) {
	shown := func(width int) []string {
		cols := Columns()
		FitColumns(cols, width)
		var titles []string
		for _, c := range cols {
			if c.Hidden == nil || !*c.Hidden {
				titles = append(titles, c.Title)
			}
		}
		return titles
	}
	all := []string{"Status", "PR", "Author", "You", "Open", "CI", "Spend", "Opened", "Updated"}
	assert.Equal(t, all, shown(160))
	assert.Equal(t, all, shown(83+minPRWidth), "exactly enough room hides nothing")
	assert.Equal(t, []string{"Status", "PR", "Author", "You", "Open", "CI", "Spend", "Opened"},
		shown(83+minPRWidth-1))
	assert.Equal(t, []string{"Status", "PR", "Author", "CI", "Spend"}, shown(70))
	assert.Equal(t, []string{"Status", "PR", "Author", "CI", "Spend"}, shown(0),
		"columns outside the drop order stay even when nothing fits")
	cols := Columns()
	FitColumns(cols, 0)
	FitColumns(cols, 160)
	visible := slices.DeleteFunc(cols, func(c table.Column) bool { return c.Hidden != nil })
	assert.Len(t, visible, len(all), "widening shows hidden columns again")
}

func TestTouchRowFlashes(t *testing.T) {
	m := newTestSection(t)
	waiting := st("stereo", 385039, reviewing.StatusReviewing, time.Minute)
	other := st("stereo", 385042, reviewing.StatusReviewing, 2*time.Minute)
	ws := reviewing.WatchStatus{
		Name:     "sustaining-automation",
		Mode:     "live",
		InFlight: 2,
		Prompt:   "Touch your security key to fetch chainguard-dev/stereo#385039",
		TouchFor: waiting.Key,
	}
	m.SetData(ws, []reviewing.PRState{waiting, other})

	rows := m.BuildRows()
	require.Len(t, rows, 2)
	assert.Equal(t, "🔑 touch key", ansi.Strip(rows[0][0]), "the PR waiting on the key")
	assert.Equal(t, "reviewing", ansi.Strip(rows[1][0]), "other reviews are unmarked")
	assert.Contains(t, m.View(), "🔑 touch key")

	m.now = func() time.Time { return now.Add(time.Second) }
	next := m.BuildRows()
	assert.Equal(t, ansi.Strip(rows[0][0]), ansi.Strip(next[0][0]))
	assert.NotEqual(t, rows[0][0], next[0][0], "the marker flashes from one second to the next")
	assert.Equal(t, rows[1][0], next[1][0], "other rows stay still")

	ws.Prompt, ws.TouchFor = "", reviewing.PRKey{}
	m.SetData(ws, []reviewing.PRState{waiting, other})
	assert.Equal(
		t,
		"reviewing",
		ansi.Strip(m.BuildRows()[0][0]),
		"the marker goes once the fetch ends",
	)
}

func TestSectionListsOutsidePRsAfterTeammates(t *testing.T) {
	m := newTestSection(t)
	m.Ctx.Config.ReviewingFor.Watches = []config.ReviewWatch{
		{Name: "other-watch", Authors: []string{"brianmcarey"}},
		{
			Name:    "sustaining-automation",
			Authors: []string{"dnegreira"},
			Teams:   []string{"chainguard-dev/sustaining-tooling-team-write"},
		},
	}
	outside := st("mono", 75471, reviewing.StatusNeedsAttention, time.Minute)
	outside.Author, outside.ReviewRequested = "brianmcarey", true
	mate := st("stereo", 401855, reviewing.StatusWaitingOnCI, time.Hour)
	m.SetData(
		reviewing.WatchStatus{Name: "sustaining-automation"},
		[]reviewing.PRState{outside, mate},
	)

	rows := m.BuildRows()
	require.Len(t, rows, 2)
	assert.True(t, strings.HasPrefix(ansi.Strip(rows[0][1]), "stereo#401855\n"),
		"the teammate's PR is listed first: %q", rows[0][1])
	assert.True(t, strings.HasPrefix(ansi.Strip(rows[1][1]), "mono#75471 (outside)\n"),
		"another watch's authors are outside this one: %q", rows[1][1])
}

func TestGroups(t *testing.T) {
	assert.Equal(t, []Group{GroupAll}, Groups(config.ReviewWatch{Authors: []string{"tcnghia"}}))
	assert.Equal(t, []Group{GroupTeammates, GroupOutside}, Groups(config.ReviewWatch{
		Authors: []string{"tcnghia"},
		Teams:   []string{"chainguard-dev/sustaining-tooling-team-write"},
	}))
}
