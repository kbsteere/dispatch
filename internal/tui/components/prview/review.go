package prview

import (
	goctx "context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/common"
	"github.com/kbsteere/dispatch/v4/internal/tui/constants"
	"github.com/kbsteere/dispatch/v4/internal/tui/context"
	"github.com/kbsteere/dispatch/v4/internal/tui/markdown"
)

// reviewTimeout bounds how long a background Claude review may run.
const reviewTimeout = 20 * time.Minute

type PrReviewMsg struct {
	Url       string
	Output    string
	SessionId string
	Err       error
}

// resolveReviewDir picks the working directory for the claude subprocess:
// the repo's configured local checkout when available, the home directory
// otherwise. Bare repos resolve to their main worktree.
func resolveReviewDir(repoName string, repoPaths map[string]string) string {
	home, _ := os.UserHomeDir()

	path, ok := common.GetRepoLocalPath(repoName, repoPaths)
	if !ok {
		return home
	}
	repo, err := reviewing.ResolveLocalRepo(path)
	if err != nil || repo.Browse == "" {
		return home
	}
	return repo.Browse
}

func (m *Model) currReview() *context.ClaudeReview {
	if m.pr == nil {
		return nil
	}
	return m.ctx.ClaudeReviews[m.pr.Data.Primary.Url]
}

// StartReview kicks off a background `claude /review` for the viewed PR.
// It's a no-op while a review for the same PR is in flight.
func (m *Model) StartReview() tea.Cmd {
	if m.pr == nil {
		return nil
	}
	url := m.pr.Data.Primary.Url
	if r := m.ctx.ClaudeReviews[url]; r != nil && r.State == context.ClaudeReviewRunning {
		return nil
	}
	m.ctx.ClaudeReviews[url] = &context.ClaudeReview{State: context.ClaudeReviewRunning}

	dir := resolveReviewDir(m.pr.Data.Primary.GetRepoNameWithOwner(), m.ctx.Config.RepoPaths)
	return func() tea.Msg {
		ctx, cancel := goctx.WithTimeout(goctx.Background(), reviewTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "claude", "-p", "--output-format", "json", "/review "+url)
		cmd.Dir = dir
		out, err := cmd.Output()
		if err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) && len(exitErr.Stderr) > 0 {
				err = fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
			}
			return PrReviewMsg{Url: url, Err: err}
		}

		res, err := reviewing.ParseResult(out)
		if err == nil {
			err = res.Err()
		}
		if err != nil {
			return PrReviewMsg{Url: url, Err: err}
		}
		return PrReviewMsg{Url: url, Output: res.Text, SessionId: res.SessionID}
	}
}

// SetReview records a finished review. Results are keyed by PR URL so a late
// response can never land on another PR's row.
func (m *Model) SetReview(msg PrReviewMsg) {
	if msg.Err != nil {
		m.ctx.ClaudeReviews[msg.Url] = &context.ClaudeReview{
			State: context.ClaudeReviewFailed,
			Err:   msg.Err,
		}
		return
	}
	m.ctx.ClaudeReviews[msg.Url] = &context.ClaudeReview{
		State:     context.ClaudeReviewDone,
		Output:    msg.Output,
		SessionId: msg.SessionId,
	}
}

// OpenReviewSession resumes the completed review's claude session
// interactively, suspending the TUI until the user exits.
func (m *Model) OpenReviewSession() tea.Cmd {
	r := m.currReview()
	if r == nil || r.State != context.ClaudeReviewDone || r.SessionId == "" {
		return m.watchSessionCmd()
	}

	dir := resolveReviewDir(m.pr.Data.Primary.GetRepoNameWithOwner(), m.ctx.Config.RepoPaths)
	c := exec.Command("claude", "--resume", r.SessionId)
	c.Dir = dir
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return constants.ErrMsg{Err: err}
		}
		return nil
	})
}

func (m *Model) GoToReviewTab() {
	m.carousel.SetCursor(6)
}

func (m *Model) renderReviewTab() string {
	manual := m.renderManualReview()
	st, ok := m.currWatch()
	if !ok {
		return manual
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		renderWatchBlock(st, m.getIndentedContentWidth()),
		"",
		manual,
	)
}

func (m *Model) renderManualReview() string {
	r := m.currReview()
	hintStyle := m.ctx.Styles.Common.FaintTextStyle

	switch {
	case r == nil:
		return hintStyle.Render("Press b to run a Claude review of this PR.")

	case r.State == context.ClaudeReviewRunning:
		return lipgloss.JoinHorizontal(
			lipgloss.Top,
			m.ctx.Styles.Common.WaitingGlyph,
			" ",
			hintStyle.Render("Claude is reviewing this PR..."),
		)

	case r.State == context.ClaudeReviewFailed:
		return lipgloss.NewStyle().
			Foreground(m.ctx.Theme.ErrorText).
			Render(fmt.Sprintf("Review failed: %v", r.Err))
	}

	width := m.getIndentedContentWidth()
	renderer := markdown.GetMarkdownRenderer(width, m.ctx)
	rendered, err := renderer.Render(r.Output)
	if err != nil {
		rendered = r.Output
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		hintStyle.Render("Press B to open this review in an interactive Claude session."),
		"",
		lipgloss.NewStyle().Width(width).MaxWidth(width).Render(rendered),
	)
}
