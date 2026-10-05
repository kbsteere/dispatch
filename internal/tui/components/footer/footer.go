package footer

import (
	"fmt"
	"path"
	"strings"

	bbHelp "charm.land/bubbles/v2/help"
	"charm.land/lipgloss/v2"
	"charm.land/lipgloss/v2/compat"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/git"
	"github.com/kbsteere/dispatch/v4/internal/tui/constants"
	"github.com/kbsteere/dispatch/v4/internal/tui/context"
	"github.com/kbsteere/dispatch/v4/internal/tui/keys"
	"github.com/kbsteere/dispatch/v4/internal/utils"
)

const viewSeparator = " │ "

type Model struct {
	ctx             *context.ProgramContext
	leftSection     *string
	rightSection    *string
	help            bbHelp.Model
	ShowAll         bool
	ShowConfirmQuit bool
}

func NewModel(ctx *context.ProgramContext) Model {
	help := bbHelp.New()
	help.ShowAll = true
	help.Styles = ctx.Styles.Help.BubbleStyles
	l := ""
	r := ""
	return Model{
		ctx:          ctx,
		help:         help,
		leftSection:  &l,
		rightSection: &r,
	}
}

func (m Model) View() string {
	var footer string

	if m.ShowConfirmQuit {
		footer = lipgloss.NewStyle().
			Render("Really quit? (Press y/enter to confirm, any other key to cancel)")
	} else {
		helpIndicator := lipgloss.NewStyle().
			Background(m.ctx.Theme.FaintText).
			Foreground(m.ctx.Theme.SelectedBackground).
			Padding(0, 1).
			Render("? help")
		viewSwitcher := m.renderViewSwitcher(m.ctx)
		leftSection := ""
		if m.leftSection != nil {
			leftSection = *m.leftSection
		}
		rightSection := ""
		if m.rightSection != nil {
			rightSection = *m.rightSection
		}
		watch := m.renderWatch()
		spacing := lipgloss.NewStyle().
			Background(m.ctx.Theme.SelectedBackground).
			Render(
				strings.Repeat(
					" ",
					utils.Max(0,
						m.ctx.ScreenWidth-lipgloss.Width(
							viewSwitcher,
						)-lipgloss.Width(leftSection)-
							lipgloss.Width(watch)-
							lipgloss.Width(rightSection)-
							lipgloss.Width(
								helpIndicator,
							),
					)))

		footer = m.ctx.Styles.Common.FooterStyle.
			Render(lipgloss.JoinHorizontal(lipgloss.Top, viewSwitcher, leftSection, spacing,
				watch, rightSection, helpIndicator))
	}

	if m.ShowAll {
		keymap := keys.CreateKeyMapForView(m.ctx.View)
		fullHelp := m.help.View(keymap)
		return lipgloss.JoinVertical(lipgloss.Top, footer, fullHelp)
	}

	return footer
}

// renderWatch shows the reviewingFor summary, if any watches are running.
func (m Model) renderWatch() string {
	if m.ctx.WatchFooter == "" {
		return ""
	}
	return lipgloss.NewStyle().
		Background(m.ctx.Theme.SelectedBackground).
		Foreground(m.ctx.Theme.FaintText).
		Padding(0, 1).
		Render(m.ctx.WatchFooter)
}

func (m *Model) SetShowConfirmQuit(val bool) {
	m.ShowConfirmQuit = val
}

func (m *Model) SetWidth(width int) {
	m.help.SetWidth(width)
}

func (m *Model) UpdateProgramContext(ctx *context.ProgramContext) {
	m.ctx = ctx
	m.help.Styles = ctx.Styles.Help.BubbleStyles
}

func (m *Model) renderViewButton(view config.ViewType) string {
	isActive := m.ctx.View == view

	// Define icons and labels for each view
	var icon, label string
	// Define icons - notifications has solid/outline variants
	solidBell := constants.BellIcon
	outlineBell := constants.BellOutlineIcon

	switch view {
	case config.NotificationsView:
		if m.ctx.View == config.NotificationsView {
			icon = solidBell
		} else {
			icon = outlineBell
		}
		label = ""
	case config.PRsView:
		icon = constants.OpenIcon
		label = " PRs"
	case config.ReviewingView:
		icon = constants.RobotIcon
		label = " Reviewing"
	case config.IssuesView:
		icon = constants.IssueOpenedIcon
		label = " Issues"
	}

	if isActive {
		// Active: colored icon + prominent background
		// Use gold for notifications bell, green for others
		iconColor := m.ctx.Theme.SuccessText
		if view == config.NotificationsView {
			iconColor = compat.AdaptiveColor{
				Light: lipgloss.Color("#B8860B"),
				Dark:  lipgloss.Color("#FFD700"),
			} // Gold
		}
		activeStyle := lipgloss.NewStyle().
			Foreground(iconColor).
			Background(m.ctx.Styles.ViewSwitcher.ActiveView.GetBackground()).
			Bold(true)
		if label != "" {
			return activeStyle.Render(icon) + activeStyle.Render(label)
		}
		return activeStyle.Render(icon)
	}

	// Inactive: faint styling
	return m.ctx.Styles.ViewSwitcher.InactiveView.Render(icon + label)
}

func (m *Model) renderViewSwitcher(ctx *context.ProgramContext) string {
	var repo string
	if m.ctx.RepoPath != "" {
		name := path.Base(m.ctx.RepoPath)
		if m.ctx.RepoUrl != "" {
			name = git.GetRepoShortName(m.ctx.RepoUrl)
		}
		repo = ctx.Styles.Common.FooterStyle.Render(fmt.Sprintf(constants.RepoIcon+" %s", name))
	}

	var user string
	if ctx.User != "" {
		user = ctx.Styles.Common.FooterStyle.Render("@" + ctx.User)
	}

	buttons := []string{
		ctx.Styles.ViewSwitcher.ViewsSeparator.PaddingLeft(1).
			Render(m.renderViewButton(config.NotificationsView)),
		ctx.Styles.ViewSwitcher.ViewsSeparator.Render(viewSeparator),
		m.renderViewButton(config.PRsView),
	}
	if ctx.Config != nil && len(ctx.Config.ReviewingFor.Watches) > 0 {
		buttons = append(buttons,
			ctx.Styles.ViewSwitcher.ViewsSeparator.Render(viewSeparator),
			m.renderViewButton(config.ReviewingView))
	}
	buttons = append(buttons,
		ctx.Styles.ViewSwitcher.ViewsSeparator.Render(viewSeparator),
		m.renderViewButton(config.IssuesView))

	view := lipgloss.JoinHorizontal(
		lipgloss.Top,
		lipgloss.JoinHorizontal(lipgloss.Top, buttons...),
		lipgloss.NewStyle().Background(ctx.Styles.Common.FooterStyle.GetBackground()).Foreground(
			ctx.Styles.ViewSwitcher.ViewsSeparator.GetBackground(),
		).
			Render(constants.PowerlineRightIcon+" "),
		repo,
		ctx.Styles.Common.FooterStyle.Foreground(m.ctx.Theme.FaintText).Render(" • "),
		user,
		ctx.Styles.Common.FooterStyle.Foreground(m.ctx.Theme.FaintBorder).Render(" │"),
	)

	return ctx.Styles.ViewSwitcher.Root.Render(view)
}

func (m *Model) SetLeftSection(leftSection string) {
	*m.leftSection = leftSection
}

func (m *Model) SetRightSection(rightSection string) {
	*m.rightSection = rightSection
}
