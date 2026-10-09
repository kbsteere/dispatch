package footer

import (
	"strings"
	"testing"

	"charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/tui/context"
	"github.com/kbsteere/dispatch/v4/internal/tui/theme"
)

func switcher(t *testing.T, watches []config.ReviewWatch) string {
	t.Helper()
	cfg, err := config.ParseConfig(config.Location{
		ConfigFlag:       "../../../config/testdata/test-config.yml",
		SkipGlobalConfig: true,
	})
	require.NoError(t, err)
	cfg.ReviewingFor.Watches = watches
	thm := theme.ParseTheme(&cfg)
	ctx := &context.ProgramContext{
		Config: &cfg,
		Theme:  thm,
		Styles: context.InitStyles(thm),
		View:   config.PRsView,
	}
	m := NewModel(ctx)
	return lipgloss.NewStyle().Render(m.renderViewSwitcher(ctx))
}

func TestViewSwitcherPutsReviewingFirst(t *testing.T) {
	out := switcher(t, []config.ReviewWatch{{Name: "sustaining-automation"}})
	reviewing, prs, issues := strings.Index(out, "Reviewing"), strings.Index(out, "PRs"),
		strings.Index(out, "Issues")
	require.NotEqual(t, -1, reviewing)
	assert.Less(t, reviewing, prs)
	assert.Less(t, prs, issues)
}

func TestViewSwitcherWithoutWatches(t *testing.T) {
	out := switcher(t, nil)
	assert.NotContains(t, out, "Reviewing")
	assert.Less(t, strings.Index(out, "PRs"), strings.Index(out, "Issues"))
}

func TestFooterStaysOnOneLine(t *testing.T) {
	cfg, err := config.ParseConfig(config.Location{
		ConfigFlag:       "../../../config/testdata/test-config.yml",
		SkipGlobalConfig: true,
	})
	require.NoError(t, err)
	cfg.ReviewingFor.Watches = []config.ReviewWatch{{Name: "sustaining-automation"}}
	thm := theme.ParseTheme(&cfg)
	ctx := &context.ProgramContext{
		Config:      &cfg,
		Theme:       thm,
		Styles:      context.InitStyles(thm),
		View:        config.ReviewingView,
		User:        "kbsteere",
		ScreenWidth: 120,
		WatchFooter: "sustaining-automation · 3 reviewing · 2 queued · $5.29 today · $106.74 wk · live",
	}
	m := NewModel(ctx)
	m.SetLeftSection(" 1/24 ")
	for _, width := range []int{200, 120, 90, 40} {
		ctx.ScreenWidth = width
		out := m.View()
		assert.Equal(t, 1, lipgloss.Height(out), "width %d", width)
		if width >= 90 {
			assert.Equal(t, width, lipgloss.Width(out), "width %d", width)
		}
	}
	ctx.ScreenWidth = 200
	assert.Contains(t, m.View(), "$106.74 wk · live", "the whole line fits when there is room")
}
