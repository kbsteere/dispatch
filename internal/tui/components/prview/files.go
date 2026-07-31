package prview

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"github.com/kbsteere/dispatch/v4/internal/data"
	"github.com/kbsteere/dispatch/v4/internal/tui/constants"
	"github.com/kbsteere/dispatch/v4/internal/utils"
)

func (m *Model) renderChangesOverview() string {
	w := m.getIndentedContentWidth() - 2
	changes := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), false, false, true, false).
		BorderForeground(m.ctx.Theme.FaintBorder).
		Width(w).
		Padding(1)

	commits := lipgloss.NewStyle().
		Width(w).
		Padding(1)

	box := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder(), true).
		BorderForeground(m.ctx.Theme.FaintBorder).
		Width(m.getIndentedContentWidth())

	time := lipgloss.NewStyle().Render(utils.TimeElapsed(m.pr.Data.Primary.UpdatedAt))
	return box.Render(
		lipgloss.JoinVertical(lipgloss.Left,
			changes.Render(
				lipgloss.JoinHorizontal(lipgloss.Top,
					lipgloss.NewStyle().Foreground(m.ctx.Theme.FaintText).Render(constants.FileDiffIcon+" "),
					fmt.Sprintf("%d files changed", m.pr.Data.Enriched.Files.TotalCount),
					" ",
					m.pr.RenderLines(false)),
			),
			commits.Render(
				lipgloss.JoinHorizontal(
					lipgloss.Top,
					lipgloss.NewStyle().Foreground(m.ctx.Theme.FaintText).Render(constants.CommitHistoryIcon+" "),
					fmt.Sprintf("%d commits", m.pr.Data.Enriched.Commits.TotalCount),
					" ",
					lipgloss.NewStyle().
						Foreground(m.ctx.Theme.FaintText).
						Render(fmt.Sprintf("%s ago", time)),
				),
			),
		),
	)
}

func (m *Model) renderChangedFiles() string {
	files := make([]string, 0)
	for _, file := range m.pr.Data.Enriched.Files.Nodes {
		files = append(files, m.renderFile(file))
	}

	return lipgloss.JoinVertical(lipgloss.Left, files...)
}

func (m *Model) renderFile(file data.ChangedFile) string {
	icon := m.renderChangeTypeIcon(file.ChangeType)
	additions := lipgloss.NewStyle().
		Foreground(m.ctx.Theme.SuccessText).
		Width(5).
		Render(fmt.Sprintf("+%d", file.Additions))
	deletions := lipgloss.NewStyle().
		Foreground(m.ctx.Theme.ErrorText).
		Width(5).
		Render(fmt.Sprintf("-%d", file.Deletions))
	prefix := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.JoinHorizontal(lipgloss.Top, additions, deletions),
		" ",
		icon,
		" ")

	path := file.Path
	remaining := m.getIndentedContentWidth() - lipgloss.Width(prefix)
	if len(path) > remaining {
		path = lipgloss.JoinVertical(lipgloss.Left, path[0:remaining], " "+path[remaining:])
	}

	return lipgloss.JoinHorizontal(
		lipgloss.Top,
		prefix,
		path,
	)
}

func (m *Model) renderChangeTypeIcon(changeType string) string {
	switch changeType {
	case "ADDED":
		return lipgloss.NewStyle().Foreground(m.ctx.Theme.SuccessText).Render(constants.FileAddedIcon)
	case "DELETED":
		return lipgloss.NewStyle().Foreground(m.ctx.Theme.ErrorText).Render(constants.FileDeletedIcon)
	case "RENAMED":
		return lipgloss.NewStyle().Foreground(m.ctx.Theme.WarningText).Render(constants.FileRenamedIcon)
	case "COPIED":
		return lipgloss.NewStyle().Foreground(m.ctx.Theme.WarningText).Render(constants.FileCopiedIcon)
	case "MODIFIED":
		return lipgloss.NewStyle().Foreground(m.ctx.Theme.WarningText).Render(constants.FileModifiedIcon)
	case "CHANGED":
		return lipgloss.NewStyle().Foreground(m.ctx.Theme.WarningText).Render(constants.FileModifiedIcon)
	default:
		return ""
	}
}
