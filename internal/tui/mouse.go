package tui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/tui/common"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/section"
)

// wheelLines is how far one wheel notch scrolls the preview pane.
const wheelLines = 3

// onMouseWheel scrolls what is under the pointer: the preview pane's
// content, or the selected row of the table.
func (m *Model) onMouseWheel(msg tea.MouseWheelMsg) tea.Cmd {
	down := msg.Button == tea.MouseWheelDown
	if !down && msg.Button != tea.MouseWheelUp {
		return nil
	}
	if m.overSidebar(msg.X, msg.Y) {
		if down {
			m.sidebar.ScrollDown(wheelLines)
		} else {
			m.sidebar.ScrollUp(wheelLines)
		}
		return nil
	}
	s := m.getCurrSection()
	if s == nil {
		return nil
	}
	if down {
		return m.selectNextRow(s)
	}
	s.PrevRow()
	return m.onViewedRowChanged()
}

// overSidebar reports whether the cell at x, y is in the preview pane, which
// sits to the right of the table or below it.
func (m *Model) overSidebar(x, y int) bool {
	if !m.sidebar.IsOpen {
		return false
	}
	if m.ctx.PreviewPosition == "bottom" {
		return y >= common.TabsHeight+m.ctx.MainContentHeight
	}
	return x >= m.ctx.MainContentWidth
}

// selectNextRow moves the selection down one row, fetching the next page
// when it reaches the last loaded row.
func (m *Model) selectNextRow(s section.Section) tea.Cmd {
	var cmds []tea.Cmd
	prevRow := s.CurrRow()
	nextRow := s.NextRow()
	if prevRow != nextRow && nextRow == s.NumRows()-1 && m.ctx.View != config.RepoView {
		cmds = append(cmds, s.FetchNextPageSectionRows()...)
	}
	return tea.Batch(append(cmds, m.onViewedRowChanged())...)
}
