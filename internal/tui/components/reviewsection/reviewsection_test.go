package reviewsection

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/data"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/prrow"
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
	m := NewModel(1, ctx, "sustaining-automation")
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
		"mono#66173 version-reconciler: enable heuristic Actions EOL for every project",
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
