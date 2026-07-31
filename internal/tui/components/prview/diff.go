package prview

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kbsteere/dispatch/v4/internal/data"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/prssection"
)

// maxDiffRenderedLines caps how many diff lines are rendered in the sidebar.
const maxDiffRenderedLines = 1000

type PrDiffMsg struct {
	Id   int
	Type string
	Url  string
	Diff string
	Err  error
}

type diffLineKind int

const (
	diffLineContext diffLineKind = iota
	diffLineHeader
	diffLineHunk
	diffLineAddition
	diffLineDeletion
)

type diffStyles struct {
	Header   lipgloss.Style
	Hunk     lipgloss.Style
	Addition lipgloss.Style
	Deletion lipgloss.Style
	Context  lipgloss.Style
	Hint     lipgloss.Style
}

// fetchDiff returns a command that fetches the diff for the currently viewed
// PR. It's a no-op if a fetch already happened or is in flight.
func (m *Model) fetchDiff() tea.Cmd {
	if m.pr == nil || m.diffIsLoading || m.diffIsLoaded {
		return nil
	}

	m.diffIsLoading = true
	url := m.pr.Data.Primary.Url
	repo := m.pr.Data.Primary.GetRepoNameWithOwner()
	number := m.pr.Data.Primary.GetNumber()
	sectionId := m.sectionId
	return func() tea.Msg {
		diff, err := data.FetchPRDiff(repo, number)
		return PrDiffMsg{
			Id:   sectionId,
			Type: prssection.SectionType,
			Url:  url,
			Diff: diff,
			Err:  err,
		}
	}
}

// maybeFetchDiff fetches the diff when the diff tab is the selected one.
func (m *Model) maybeFetchDiff() tea.Cmd {
	if m.carousel.SelectedItem() != tabs[5] {
		return nil
	}
	return m.fetchDiff()
}

// SetDiff stores a fetched diff, ignoring responses for a PR that is no
// longer the viewed one.
func (m *Model) SetDiff(msg PrDiffMsg) {
	if m.pr == nil || m.pr.Data.Primary.Url != msg.Url {
		return
	}
	m.diffIsLoading = false
	m.diffIsLoaded = true
	m.diff = msg.Diff
	m.diffErr = msg.Err
}

func (m *Model) resetDiff() {
	m.diff = ""
	m.diffErr = nil
	m.diffIsLoading = false
	m.diffIsLoaded = false
}

func (m *Model) renderDiffTab() string {
	if !m.diffIsLoaded {
		return lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.ctx.Styles.Common.WaitingGlyph,
			" ",
			m.ctx.Styles.Common.FaintTextStyle.Render("Loading..."),
		)
	}

	if m.diffErr != nil {
		return lipgloss.NewStyle().
			Foreground(m.ctx.Theme.ErrorText).
			Render(fmt.Sprintf("Failed loading diff: %v", m.diffErr))
	}

	styles := diffStyles{
		Header:   lipgloss.NewStyle().Bold(true).Foreground(m.ctx.Theme.PrimaryText),
		Hunk:     lipgloss.NewStyle().Foreground(m.ctx.Theme.FaintText),
		Addition: lipgloss.NewStyle().Foreground(m.ctx.Theme.SuccessText),
		Deletion: lipgloss.NewStyle().Foreground(m.ctx.Theme.ErrorText),
		Context:  lipgloss.NewStyle().Foreground(m.ctx.Theme.SecondaryText),
		Hint:     m.ctx.Styles.Common.FaintTextStyle.Italic(true),
	}
	return renderDiff(m.diff, m.getIndentedContentWidth(), maxDiffRenderedLines, styles)
}

// classifyDiffLine determines how a single line of a unified diff should be
// styled.
func classifyDiffLine(line string) diffLineKind {
	switch {
	case strings.HasPrefix(line, "diff --git"),
		strings.HasPrefix(line, "index "),
		strings.HasPrefix(line, "new file mode"),
		strings.HasPrefix(line, "deleted file mode"),
		strings.HasPrefix(line, "similarity index"),
		strings.HasPrefix(line, "rename from"),
		strings.HasPrefix(line, "rename to"),
		strings.HasPrefix(line, "Binary files"),
		strings.HasPrefix(line, "--- "),
		strings.HasPrefix(line, "+++ "):
		return diffLineHeader
	case strings.HasPrefix(line, "@@"):
		return diffLineHunk
	case strings.HasPrefix(line, "+"):
		return diffLineAddition
	case strings.HasPrefix(line, "-"):
		return diffLineDeletion
	default:
		return diffLineContext
	}
}

func renderDiff(diff string, width, maxLines int, styles diffStyles) string {
	if strings.TrimSpace(diff) == "" {
		return styles.Hint.Render("No changes.")
	}

	lines := strings.Split(strings.TrimRight(diff, "\n"), "\n")
	truncated := false
	if len(lines) > maxLines {
		lines = lines[:maxLines]
		truncated = true
	}

	rendered := make([]string, 0, len(lines)+2)
	for _, line := range lines {
		var style lipgloss.Style
		switch classifyDiffLine(line) {
		case diffLineHeader:
			style = styles.Header
		case diffLineHunk:
			style = styles.Hunk
		case diffLineAddition:
			style = styles.Addition
		case diffLineDeletion:
			style = styles.Deletion
		default:
			style = styles.Context
		}
		rendered = append(rendered, style.MaxWidth(width).Render(line))
	}

	if truncated {
		rendered = append(rendered, "", styles.Hint.Render(
			fmt.Sprintf("Diff truncated at %d lines. Press d to view the full diff.", maxLines),
		))
	}

	return strings.Join(rendered, "\n")
}
