package prview

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/common"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/cmpcontroller"
	"github.com/kbsteere/dispatch/v4/internal/tui/constants"
)

// AddReviewRefMsg carries a PR reference typed into the add-to-watch prompt.
type AddReviewRefMsg struct {
	Ref string
}

// SetIsAddingReviewRef opens or closes the prompt for adding a PR to the
// review watch by reference.
func (m *Model) SetIsAddingReviewRef(on bool) tea.Cmd {
	if m.pr == nil {
		return nil
	}
	if !on {
		if m.editor.Mode() == cmpcontroller.ModeAddReviewRef {
			m.editor.Exit()
		}
		return nil
	}
	return m.editor.Enter(cmpcontroller.EnterOptions{
		Mode:   cmpcontroller.ModeAddReviewRef,
		Prompt: constants.AddReviewRefPrompt,
		Repo:   m.repoRef(),
	})
}

func (m *Model) currWatch() (reviewing.PRState, bool) {
	if m.pr == nil || m.ctx.WatchPRs == nil {
		return reviewing.PRState{}, false
	}
	st, ok := m.ctx.WatchPRs[m.pr.Data.Primary.Url]
	return st, ok
}

// renderWatchBlock summarises a watched PR's autonomous review state.
// Everything that came from a PR or a model is sanitised before display.
func renderWatchBlock(st reviewing.PRState, width int, now time.Time) string {
	clean := reviewing.SanitizeTerminal
	var b strings.Builder
	fmt.Fprintf(&b, "Watch: %s", common.ReviewStatusWord(st.Status))
	if st.Pinned {
		b.WriteString(" · added by hand")
	}
	if r := st.LastRun; r != nil {
		fmt.Fprintf(
			&b,
			" · last run %d turns · $%.2f · session %s",
			r.Turns,
			r.CostUSD,
			clean(r.SessionID),
		)
	}
	b.WriteString("\n")
	if st.Status == reviewing.StatusFailed || st.Status == reviewing.StatusThrottled {
		b.WriteString(retryLine(st.Backoff.Until, now))
	}
	if st.Note != "" {
		fmt.Fprintf(&b, "Note: %s\n", clean(st.Note))
	}

	outstanding := slices.DeleteFunc(slices.Clone(st.Ledger), func(f reviewing.Finding) bool {
		return f.Status != reviewing.FindingOutstanding
	})
	slices.SortStableFunc(outstanding, func(a, b reviewing.Finding) int {
		switch {
		case a.Blocking == b.Blocking:
			return 0
		case a.Blocking:
			return -1
		default:
			return 1
		}
	})
	if len(outstanding) > 0 {
		b.WriteString("\nOutstanding:\n")
		for _, f := range outstanding {
			kind := "non-blocking"
			if f.Blocking {
				kind = "blocking"
			}
			fmt.Fprintf(&b, "- %s:%d (%s) %s\n", clean(f.Path), f.Line, kind, clean(f.Summary))
		}
	}
	if st.HeldDraft != "" {
		fmt.Fprintf(
			&b,
			"\nHeld review: %s\nEdit it if needed, then press S to send it.\n",
			clean(st.HeldDraft),
		)
	}
	return lipgloss.NewStyle().Width(width).MaxWidth(width).Render(b.String())
}

// retryLine says when a failed or throttled PR is retried on its own.
func retryLine(until, now time.Time) string {
	if d := until.Sub(now); d > 0 {
		return fmt.Sprintf("Retries in %s · press N to retry now\n", roughDuration(d))
	}
	return "Retries on the next poll · press N to retry now\n"
}

// roughDuration renders a wait as minutes, hours, or days.
func roughDuration(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", max(1, int(d.Round(time.Minute).Minutes())))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Round(time.Hour).Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Round(24*time.Hour).Hours()/24))
	}
}

// watchSessionCmd resumes the last autonomous review session for the PR.
func (m *Model) watchSessionCmd() tea.Cmd {
	st, ok := m.currWatch()
	if !ok || st.LastRun == nil || !reviewing.ValidSessionID(st.LastRun.SessionID) {
		return nil
	}
	c := exec.Command("claude", "--resume", st.LastRun.SessionID)
	c.Dir = filepath.Join(reviewing.DefaultStateRoot(), "sessions", st.LastRun.SessionID)
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return constants.ErrMsg{Err: err}
		}
		return nil
	})
}
