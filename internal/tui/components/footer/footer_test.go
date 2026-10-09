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
