package reviewsection

import (
	"fmt"
	"image/color"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
	states []reviewing.PRState
	status reviewing.WatchStatus
	// hidden counts the approved PRs left out of states; see HideApproved.
	hidden   int
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
	m.fitColumns()
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
	m.states, m.hidden = SortStates(states, m.now()), 0
	if !m.Ctx.ShowApproved {
		m.states, m.hidden = HideApproved(m.states)
	}
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
		// The reference gets its own line so a narrow column cuts the title,
		// never the PR number; the table truncates each line instead of
		// wrapping it.
		sep := "\n"
		if m.Ctx.Config.Theme.Ui.Table.Compact {
			sep = " "
		}
		status := lipgloss.NewStyle().Foreground(m.toneColor(StatusTone(s))).Render(c.Status)
		if s.Key == m.status.TouchFor {
			status = m.touchCell(now)
		}
		rows = append(
			rows,
			table.Row{
				status,
				c.PR + sep + c.Title,
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

// touchCell marks the row whose fetch waits for a security key touch. It
// flashes, inverting each second the table redraws, so the row stands out
// while the key waits.
func (m Model) touchCell(now time.Time) string {
	return lipgloss.NewStyle().
		Foreground(m.Ctx.Theme.WarningText).
		Bold(true).
		Reverse(now.Unix()%2 == 0).
		Render(touchStatus)
}

// touchStatus is the Status cell of a PR waiting for a security key touch.
const touchStatus = "🔑 touch key"

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
	// The summary is cut to one line: a wrapped one makes the view taller than
	// the screen and pushes the footer off it.
	width := m.Ctx.MainContentWidth - 2*m.Ctx.Styles.Section.ContainerPadding - 2
	summary := lipgloss.NewStyle().
		Height(height).
		Padding(0, 1).
		Foreground(m.Ctx.Theme.FaintText).
		Render(ansi.Truncate(summaryLine(m.status, m.hidden, m.now()), max(width, 0), "…"))
	return m.Ctx.Styles.Section.ContainerStyle.
		Width(m.Ctx.MainContentWidth).
		Render(lipgloss.JoinVertical(lipgloss.Left, summary, m.GetMainContent()))
}

func summaryLine(ws reviewing.WatchStatus, hidden int, now time.Time) string {
	s := Summary(ws)
	if hidden > 0 {
		s += fmt.Sprintf(" · %d approved hidden", hidden)
	}
	if next := NextPoll(ws, now); next != "" {
		s += " · " + next
	}
	return s
}

// minPRWidth is the narrowest the PR column gets before other columns are
// hidden to make room: enough for a reference like "stereo#386277" and the
// start of its title.
const minPRWidth = 24

// dropOrder lists the columns hidden, first to last, when the table is too
// narrow for the PR column.
var dropOrder = []string{"Updated", "Opened", "Open", "You"}

// FitColumns hides the columns in dropOrder, in turn, until the PR column
// can be at least minPRWidth wide in a table width cells wide.
func FitColumns(cols []table.Column, width int) {
	fixed := 0
	for i := range cols {
		cols[i].Hidden = nil
		if cols[i].Width != nil {
			fixed += *cols[i].Width
		}
	}
	for _, title := range dropOrder {
		if width-fixed >= minPRWidth {
			return
		}
		i := slices.IndexFunc(cols, func(c table.Column) bool { return c.Title == title })
		if i < 0 || cols[i].Width == nil {
			continue
		}
		cols[i].Hidden = utils.BoolPtr(true)
		fixed -= *cols[i].Width
	}
}

func (m *Model) fitColumns() {
	FitColumns(m.Table.Columns, m.GetDimensions().Width)
}

// UpdateProgramContext refits the columns to the section's width.
func (m *Model) UpdateProgramContext(ctx *context.ProgramContext) {
	m.BaseModel.UpdateProgramContext(ctx)
	m.fitColumns()
	m.Table.SyncViewPortContent()
}
