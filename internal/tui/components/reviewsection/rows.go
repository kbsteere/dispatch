// Package reviewsection renders a reviewingFor watch as a table built from
// dispatch's local review state.
package reviewsection

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/kbsteere/dispatch/v4/internal/data"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/prrow"
)

// maxTitleRunes bounds how much of a PR title is rendered.
const maxTitleRunes = 120

// statusRank orders rows: what needs the user first, baselined PRs last.
var statusRank = map[reviewing.Status]int{
	reviewing.StatusNeedsAttention:   0,
	reviewing.StatusChangesRequested: 1,
	reviewing.StatusReviewing:        2,
	reviewing.StatusWaitingOnCI:      3,
	reviewing.StatusWaitingOnAuthor:  4,
	reviewing.StatusWaitingOnOthers:  5,
	reviewing.StatusApproved:         6,
	reviewing.StatusThrottled:        7,
	reviewing.StatusFailed:           8,
	reviewing.StatusBaseline:         9,
}

var statusShort = map[reviewing.Status]string{
	reviewing.StatusNeedsAttention:   "attention",
	reviewing.StatusChangesRequested: "changes",
	reviewing.StatusReviewing:        "reviewing",
	reviewing.StatusWaitingOnCI:      "waiting-ci",
	reviewing.StatusWaitingOnAuthor:  "waiting",
	reviewing.StatusWaitingOnOthers:  "others",
	reviewing.StatusApproved:         "approved",
	reviewing.StatusThrottled:        "throttled",
	reviewing.StatusFailed:           "failed",
	reviewing.StatusBaseline:         "baseline",
}

func rank(s reviewing.Status) int {
	if r, ok := statusRank[s]; ok {
		return r
	}
	return len(statusRank)
}

func updatedAt(s reviewing.PRState) time.Time {
	t, _ := time.Parse(time.RFC3339, s.UpdatedAt)
	return t
}

// SortStates returns the active, valid states in display order without
// modifying the input. Inactive PRs (merged, closed, back to draft) and
// states whose key fails validation are dropped.
func SortStates(states []reviewing.PRState) []reviewing.PRState {
	out := make([]reviewing.PRState, 0, len(states))
	for _, s := range states {
		if !s.Inactive && reviewing.ValidName(s.Key.Owner) && reviewing.ValidName(s.Key.Repo) &&
			s.Key.Number > 0 {
			out = append(out, s)
		}
	}
	slices.SortStableFunc(out, func(a, b reviewing.PRState) int {
		if c := cmp.Compare(rank(a.Status), rank(b.Status)); c != 0 {
			return c
		}
		return updatedAt(b).Compare(updatedAt(a))
	})
	return out
}

// RowCells are the text cells of one table row.
type RowCells struct {
	Status  string
	PR      string
	Author  string
	Open    string
	CI      string
	LastRun string
	Updated string
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// Cells renders a state as row text. Text from GitHub is sanitised and
// length-limited.
func Cells(s reviewing.PRState, now time.Time) RowCells {
	clean := reviewing.SanitizeTerminal
	c := RowCells{
		Status: statusShort[s.Status],
		PR: fmt.Sprintf(
			"%s#%d %s",
			s.Key.Repo,
			s.Key.Number,
			clip(clean(s.Title), maxTitleRunes),
		),
		Author:  clip(clean(s.Author), 39),
		Open:    "-",
		CI:      s.CI,
		LastRun: "-",
	}
	if c.Status == "" {
		c.Status = string(s.Status)
	}
	if s.DryRun {
		c.Status += " (dry)"
	}
	if s.Pinned {
		c.PR += " (added)"
	}
	if b, nb := s.OutstandingCounts(); b+nb > 0 {
		var parts []string
		if b > 0 {
			parts = append(parts, fmt.Sprintf("%db", b))
		}
		if nb > 0 {
			parts = append(parts, fmt.Sprintf("%dn", nb))
		}
		c.Open = strings.Join(parts, " ")
	}
	switch {
	case s.Status == reviewing.StatusReviewing:
		c.LastRun = "…"
	case s.LastRun != nil:
		c.LastRun = fmt.Sprintf("%dt $%.2f", s.LastRun.Turns, s.LastRun.CostUSD)
	}
	if t := updatedAt(s); !t.IsZero() {
		c.Updated = shortElapsed(now.Sub(t))
	}
	return c
}

func shortElapsed(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Summary is the one-line overview shown above a watch's table.
func Summary(ws reviewing.WatchStatus) string {
	state := ws.Mode
	if ws.Paused != "" {
		state = "paused: " + ws.Paused
	}
	quota := "quota unknown"
	switch {
	case ws.LastPoll.IsZero():
		quota = "polling GitHub…"
	case ws.QuotaRemaining > 0:
		quota = "quota " + thousands(ws.QuotaRemaining)
	}
	s := fmt.Sprintf("%s · %s · $%.2f/$%s today · %d reviewing · %s",
		ws.Name, state, ws.SpentToday, trimFloat(ws.DailyBudget), ws.InFlight, quota)
	if ws.Err != "" {
		s += " · error: " + clip(reviewing.SanitizeTerminal(ws.Err), 80)
	}
	if ws.Prompt != "" {
		s = "▶ " + ws.Prompt + " · " + s
	}
	return s
}

func thousands(n int) string {
	s := fmt.Sprint(n)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}

func trimFloat(f float64) string {
	return strings.TrimSuffix(strings.TrimRight(fmt.Sprintf("%.2f", f), "0"), ".")
}

// PRData is the minimal PR row the sidebar needs; selecting it fetches the
// full PR.
func PRData(s reviewing.PRState) *prrow.Data {
	pr := &data.PullRequestData{
		Url:    s.Key.URL(),
		Number: s.Key.Number,
		Title:  reviewing.SanitizeTerminal(s.Title),
		Repository: data.Repository{
			Name:          s.Key.Repo,
			NameWithOwner: s.Key.NameWithOwner(),
			Owner:         data.Owner{Login: s.Key.Owner},
		},
		UpdatedAt: updatedAt(s),
	}
	pr.Author.Login = reviewing.SanitizeTerminal(s.Author)
	return &prrow.Data{Primary: pr}
}
