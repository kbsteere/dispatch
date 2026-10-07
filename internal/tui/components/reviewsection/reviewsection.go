package reviewsection

import (
	"fmt"
	"image/color"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/data"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/section"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/table"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/tasks"
	"github.com/kbsteere/dispatch/v4/internal/tui/context"
	"github.com/kbsteere/dispatch/v4/internal/utils"
)

const SectionType = "review"

// Model is one watch's table in the Reviewing view. Its rows come from the
// watch's local state, not from a GitHub search.
type Model struct {
	section.BaseModel
	states   []reviewing.PRState
	status   reviewing.WatchStatus
	now      func() time.Time
	enriched map[string]data.EnrichedPullRequestData // full PR data by URL, once fetched
}

func NewModel(id int, ctx *context.ProgramContext, watch string) Model {
	m := Model{now: time.Now, enriched: map[string]data.EnrichedPullRequestData{}}
	m.BaseModel = section.NewModel(ctx, section.NewSectionOptions{
		Id:          id,
		Config:      config.SectionConfig{Title: watch},
		Type:        SectionType,
		Columns:     Columns(),
		Singular:    "PR",
		Plural:      "PRs",
		LastUpdated: time.Now(),
		CreatedAt:   time.Now(),
	})
	m.status = reviewing.WatchStatus{Name: watch}
	return m
}

// Columns are the Reviewing view's table columns.
func Columns() []table.Column {
	grow := true
	return []table.Column{
		{Title: "Status", Width: utils.IntPtr(22)},
		{Title: "PR", Grow: &grow},
		{Title: "Author", Width: utils.IntPtr(14)},
		{Title: "You", Width: utils.IntPtr(4)},
		{Title: "Open", Width: utils.IntPtr(7)},
		{Title: "CI", Width: utils.IntPtr(4)},
		{Title: "Spend", Width: utils.IntPtr(11)},
		{Title: "Opened", Width: utils.IntPtr(13)},
		{Title: "Updated", Width: utils.IntPtr(8)},
	}
}

// Watch is the name of the watch this table shows.
func (m *Model) Watch() string { return m.Config.Title }

// SetData replaces the table's contents with the watch's current state.
func (m *Model) SetData(ws reviewing.WatchStatus, states []reviewing.PRState) {
	m.status = ws
	m.states = SortStates(states, m.now())
	m.TotalCount = len(m.states)
	m.Table.SetRows(m.BuildRows())
	// Until the first poll finishes there is nothing to show yet; say so
	// instead of claiming no PRs match.
	m.SetIsLoading(ws.LastPoll.IsZero() && len(m.states) == 0)
}

func ciGlyph(ci string) string {
	switch ci {
	case "SUCCESS":
		return "✓"
	case "FAILURE":
		return "✗"
	case "PENDING":
		return "◌"
	case "":
		return ""
	default:
		return "?"
	}
}

func (m Model) BuildRows() []table.Row {
	now := m.now()
	rows := make([]table.Row, 0, len(m.states))
	for _, s := range m.states {
		c := Cells(s, now)
		status := lipgloss.NewStyle().Foreground(m.toneColor(StatusTone(s))).Render(c.Status)
		rows = append(
			rows,
			table.Row{
				status,
				c.PR,
				c.Author,
				c.You,
				c.Open,
				ciGlyph(c.CI),
				c.Spend,
				c.Opened,
				c.Updated,
			},
		)
	}
	return rows
}

func (m Model) toneColor(t Tone) color.Color {
	switch t {
	case ToneGood:
		return m.Ctx.Theme.SuccessText
	case ToneBad:
		return m.Ctx.Theme.ErrorText
	case ToneBusy:
		return m.Ctx.Theme.WarningText
	default:
		return m.Ctx.Theme.FaintText
	}
}

func (m *Model) NumRows() int { return len(m.states) }

// GetCurrRow returns the selected PR, or nil when the selection is out of
// range.
func (m *Model) GetCurrRow() data.RowData {
	i := m.Table.GetCurrItem()
	if i < 0 || i >= len(m.states) {
		return nil
	}
	d := PRData(m.states[i])
	if e, ok := m.enriched[d.Primary.Url]; ok {
		// The fetched PR carries what the sidebar header shows (author
		// role, dates, branches) that local state does not.
		pd := e.ToPullRequestData()
		if pd.Repository.NameWithOwner == "" {
			pd.Repository = d.Primary.Repository
		}
		d.Primary = &pd
		d.Enriched, d.IsEnriched = e, true
	}
	return d
}

// EnrichPR caches the full PR data fetched for the sidebar.
func (m *Model) EnrichPR(e data.EnrichedPullRequestData) {
	if m.enriched == nil {
		m.enriched = map[string]data.EnrichedPullRequestData{}
	}
	m.enriched[e.Url] = e
}

// CurrState returns the selected PR's review state.
func (m *Model) CurrState() (reviewing.PRState, bool) {
	i := m.Table.GetCurrItem()
	if i < 0 || i >= len(m.states) {
		return reviewing.PRState{}, false
	}
	return m.states[i], true
}

// FetchNextPageSectionRows has nothing to fetch: rows come from local state.
func (m *Model) FetchNextPageSectionRows() []tea.Cmd { return nil }

func (m *Model) ResetRows() {
	m.states = nil
	m.BaseModel.ResetRows()
}

func (m *Model) SetIsLoading(val bool) {
	m.IsLoading = val
	m.Table.SetIsLoading(val)
}

// SetIsSearching is a no-op: the Reviewing view has no search query.
func (m *Model) SetIsSearching(bool) tea.Cmd { return nil }

func (m Model) GetItemSingularForm() string { return "PR" }
func (m Model) GetItemPluralForm() string   { return "PRs" }
func (m Model) GetTotalCount() int          { return len(m.states) }
func (m Model) GetPagerContent() string {
	return fmt.Sprintf("%d %s", len(m.states), m.GetItemPluralForm())
}

// Update handles the PR action confirmation prompt; navigation and other
// keys are handled by the view.
func (m *Model) Update(msg tea.Msg) (section.Section, tea.Cmd) {
	key, ok := msg.(tea.KeyMsg)
	if !ok || !m.IsPromptConfirmationFocused() {
		return m, nil
	}
	switch key.String() {
	case "ctrl+c", "esc":
		m.PromptConfirmationBox.Reset()
		return m, m.SetIsPromptConfirmationShown(false)
	case "enter":
		var cmd tea.Cmd
		pr := m.GetCurrRow()
		v := m.PromptConfirmationBox.Value()
		if m.GetPromptConfirmationAction() == section.AskedInputAction {
			cmd = m.SubmittedCmd(v)
		} else if m.GetPromptConfirmationAction() == section.AskedConfirmationAction &&
			(v == "y" || v == "Y") {
			cmd = m.ConfirmedCmd()
		} else if pr != nil &&
			(v == "y" || v == "Y") {
			sid := tasks.SectionIdentifier{Id: m.Id, Type: SectionType}
			switch m.GetPromptConfirmationAction() {
			case "close":
				cmd = tasks.ClosePR(m.Ctx, sid, pr)
			case "reopen":
				cmd = tasks.ReopenPR(m.Ctx, sid, pr)
			case "ready":
				cmd = tasks.PRReady(m.Ctx, sid, pr)
			case "merge":
				cmd = tasks.MergePR(m.Ctx, sid, pr)
			case "update":
				cmd = tasks.UpdatePR(m.Ctx, sid, pr)
			case "approveWorkflows":
				cmd = tasks.ApproveWorkflows(m.Ctx, sid, pr)
			}
		}
		m.PromptConfirmationBox.Reset()
		return m, tea.Batch(cmd, m.SetIsPromptConfirmationShown(false))
	}
	var cmd tea.Cmd
	m.PromptConfirmationBox, cmd = m.PromptConfirmationBox.Update(msg)
	return m, cmd
}

// View shows the watch summary where other sections show their search bar.
func (m *Model) View() string {
	height := lipgloss.Height(m.SearchBar.View(m.Ctx))
	summary := lipgloss.NewStyle().
		Height(height).
		Padding(0, 1).
		Foreground(m.Ctx.Theme.FaintText).
		Render(summaryLine(m.status, m.now()))
	return m.Ctx.Styles.Section.ContainerStyle.
		Width(m.Ctx.MainContentWidth).
		Render(lipgloss.JoinVertical(lipgloss.Left, summary, m.GetMainContent()))
}

func summaryLine(ws reviewing.WatchStatus, now time.Time) string {
	if next := NextPoll(ws, now); next != "" {
		return Summary(ws) + " · " + next
	}
	return Summary(ws)
}
