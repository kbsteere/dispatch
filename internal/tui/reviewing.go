package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	log "charm.land/log/v2"

	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/data"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/common"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/reviewsection"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/section"
)

// reviewers are the running reviewingFor watches, one engine each.
type reviewers []*reviewing.Engine

type (
	reviewTickMsg   struct{ watch string }
	reviewPolledMsg struct {
		watch string
		jobs  []reviewing.Job
		next  time.Duration
		err   error
		// manual marks a poll the user asked for; it does not schedule the
		// next poll, so there is only ever one poll loop per watch.
		manual bool
	}
	reviewDoneMsg struct {
		watch string
		out   reviewing.Outcome
	}
	// reviewActionMsg reports a user action on a watch (add, remove, send).
	reviewActionMsg struct {
		note string
		err  error
	}
)

// newReviewers opens an engine per configured watch. A watch that fails to
// open is logged and skipped.
func newReviewers(cfg *config.Config) reviewers {
	lookup := func(name string) (string, bool) { return common.GetRepoLocalPath(name, cfg.RepoPaths) }
	var out reviewers
	for _, w := range cfg.ReviewingFor.Watches {
		e, err := reviewing.NewEngine(reviewing.Deps{
			Watch:       w,
			Limits:      cfg.ReviewingFor.LimitsFor(w),
			StateRoot:   reviewing.DefaultStateRoot(),
			GitHub:      reviewing.DefaultGitHub(),
			LookupPath:  lookup,
			ContextDirs: contextDirs(cfg.RepoPaths),
		})
		if err != nil {
			log.Error("opening review watch", "watch", w.Name, "err", err)
			continue
		}
		out = append(out, e)
	}
	return out
}

// contextDirs are the browsable checkouts of every exactly-mapped repo in
// repoPaths, offered to reviewers as reference code.
func contextDirs(repoPaths map[string]string) []string {
	var dirs []string
	for name, p := range repoPaths {
		if strings.ContainsAny(name, "*:") {
			continue
		}
		if repo, err := reviewing.ResolveLocalRepo(p); err == nil && repo.Browse != "" {
			dirs = append(dirs, repo.Browse)
		}
	}
	slices.Sort(dirs)
	return slices.Compact(dirs)
}

func (r reviewers) byName(name string) *reviewing.Engine {
	for _, e := range r {
		if e.Name() == name {
			return e
		}
	}
	return nil
}

func tickCmd(watch string, d time.Duration) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return reviewTickMsg{watch: watch} })
}

func pollCmd(e *reviewing.Engine) tea.Cmd {
	return func() tea.Msg {
		jobs, next, err := e.Poll(context.Background())
		return reviewPolledMsg{watch: e.Name(), jobs: jobs, next: next, err: err}
	}
}

// manualPollCmd polls now without affecting the regular poll schedule.
func manualPollCmd(e *reviewing.Engine) tea.Cmd {
	return func() tea.Msg {
		jobs, next, err := e.Poll(context.Background())
		return reviewPolledMsg{watch: e.Name(), jobs: jobs, next: next, err: err, manual: true}
	}
}

// reschedule reports whether a finished poll schedules the next one.
func reschedule(msg reviewPolledMsg) bool { return !msg.manual }

// reviewStatusTickMsg refreshes watch state on screen while reviews run, so
// prompts such as a security key touch show up while they are needed.
type reviewStatusTickMsg struct{}

const statusTickInterval = time.Second

func statusTickCmd() tea.Cmd {
	return tea.Tick(statusTickInterval, func(time.Time) tea.Msg { return reviewStatusTickMsg{} })
}

// startStatusTicks starts the status refresh loop unless it is running.
func (m *Model) startStatusTicks() tea.Cmd {
	if m.statusTicking {
		return nil
	}
	m.statusTicking = true
	return statusTickCmd()
}

// onStatusTick refreshes watch state and keeps ticking while any review is
// in flight.
func (m *Model) onStatusTick() tea.Cmd {
	m.syncWatchState()
	for _, s := range m.reviewers.statuses() {
		if s.InFlight > 0 {
			return statusTickCmd()
		}
	}
	m.statusTicking = false
	return nil
}

// refreshReviewing reloads the Reviewing tables from watch state and polls
// every watch now.
func (m *Model) refreshReviewing() tea.Cmd {
	m.syncWatchState()
	cmds := make([]tea.Cmd, 0, len(m.reviewers))
	for _, e := range m.reviewers {
		cmds = append(cmds, manualPollCmd(e))
	}
	return tea.Batch(cmds...)
}

func reviewCmd(e *reviewing.Engine, j reviewing.Job) tea.Cmd {
	return func() tea.Msg {
		return reviewDoneMsg{watch: e.Name(), out: e.Review(context.Background(), j)}
	}
}

// startCmds cleans up after any earlier dispatch and starts each watch's
// poll loop.
func (r reviewers) startCmds() tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(r))
	for _, e := range r {
		cmds = append(cmds, func() tea.Msg {
			if err := e.Startup(context.Background()); err != nil {
				log.Error("review watch startup", "watch", e.Name(), "err", err)
			}
			return reviewTickMsg{watch: e.Name()}
		})
	}
	return tea.Batch(cmds...)
}

func (r reviewers) shutdown() {
	for _, e := range r {
		e.Shutdown()
	}
}

func (r reviewers) statuses() []reviewing.WatchStatus {
	out := make([]reviewing.WatchStatus, 0, len(r))
	for _, e := range r {
		out = append(out, e.Status())
	}
	return out
}

// watchPRs indexes every watched PR's state by URL.
func (r reviewers) watchPRs() map[string]reviewing.PRState {
	out := map[string]reviewing.PRState{}
	for _, e := range r {
		for _, st := range e.States() {
			if !st.Inactive {
				out[st.Key.URL()] = st
			}
		}
	}
	return out
}

func footerText(statuses []reviewing.WatchStatus) string {
	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		state := s.Mode
		if s.Paused != "" {
			state = "paused: " + s.Paused
		}
		if s.Prompt != "" {
			parts = append(parts, "▶ "+s.Prompt)
		}
		parts = append(
			parts,
			fmt.Sprintf(
				"%s · %d reviewing · $%.2f today · %s",
				s.Name,
				s.InFlight,
				s.SpentToday,
				state,
			),
		)
	}
	return strings.Join(parts, " │ ")
}

func (m *Model) syncWatchState() {
	m.ctx.WatchPRs = m.reviewers.watchPRs()
	m.ctx.WatchFooter = footerText(m.reviewers.statuses())
	m.refreshReviewSections()
}

// buildReviewSections creates the Reviewing view's tables, one per watch.
func (m *Model) buildReviewSections() []section.Section {
	out := make([]section.Section, 0, len(m.reviewers))
	for i, e := range m.reviewers {
		s := reviewsection.NewModel(i, m.ctx, e.Name())
		s.SetData(e.Status(), e.States())
		out = append(out, &s)
	}
	return out
}

// refreshReviewSections reloads each table from its watch's state.
func (m *Model) refreshReviewSections() {
	for _, s := range m.reviewSections {
		rs, ok := s.(*reviewsection.Model)
		if !ok {
			continue
		}
		if e := m.reviewers.byName(rs.Watch()); e != nil {
			rs.SetData(e.Status(), e.States())
		}
	}
}

// targetEngine is the watch of the focused Reviewing table, or the first
// watch.
func (m *Model) targetEngine() *reviewing.Engine {
	if len(m.reviewers) == 0 {
		return nil
	}
	if rs, ok := m.getCurrSection().(*reviewsection.Model); ok {
		if e := m.reviewers.byName(rs.Watch()); e != nil {
			return e
		}
	}
	return m.reviewers[0]
}

func noWatches() tea.Msg {
	return reviewActionMsg{err: fmt.Errorf("no reviewingFor watches are configured")}
}

func rowKey(row data.RowData) (reviewing.PRKey, error) {
	if row == nil {
		return reviewing.PRKey{}, fmt.Errorf("no PR selected")
	}
	return reviewing.ParsePRRef(row.GetUrl())
}

// toggleFollow adds the selected PR to the watch, or removes it if it was
// added by hand.
func (m *Model) toggleFollow(row data.RowData) tea.Cmd {
	e := m.targetEngine()
	if e == nil {
		return noWatches
	}
	k, err := rowKey(row)
	if err != nil {
		return func() tea.Msg { return reviewActionMsg{err: err} }
	}
	st, watched := m.ctx.WatchPRs[k.URL()]
	pinned := st.Pinned
	if watched && !pinned {
		return func() tea.Msg {
			return reviewActionMsg{
				note: fmt.Sprintf("%s is already in a review watch; press N to review it now", k),
			}
		}
	}
	return func() tea.Msg {
		if pinned {
			if err := e.Unpin(k); err != nil {
				return reviewActionMsg{err: err}
			}
			return reviewActionMsg{
				note: fmt.Sprintf("Removed %s from %s", k, e.Name()),
			}
		}
		if err := e.Pin(context.Background(), k); err != nil {
			return reviewActionMsg{err: err}
		}
		return reviewActionMsg{
			note: fmt.Sprintf(
				"Added %s to %s; it will be reviewed on the next poll",
				k,
				e.Name(),
			),
		}
	}
}

// addRef adds a PR typed as owner/repo#N or a URL.
func (m *Model) addRef(ref string) tea.Cmd {
	e := m.targetEngine()
	if e == nil {
		return noWatches
	}
	k, err := reviewing.ParsePRRef(ref)
	if err != nil {
		return func() tea.Msg { return reviewActionMsg{err: err} }
	}
	return func() tea.Msg {
		if err := e.Pin(context.Background(), k); err != nil {
			return reviewActionMsg{err: err}
		}
		return reviewActionMsg{
			note: fmt.Sprintf(
				"Added %s to %s; it will be reviewed on the next poll",
				k,
				e.Name(),
			),
		}
	}
}

// sendHeld posts the selected PR's held review. The first press arms it;
// a second press on the same PR sends it.
func (m *Model) sendHeld(row data.RowData) tea.Cmd {
	k, err := rowKey(row)
	if err != nil {
		return func() tea.Msg { return reviewActionMsg{err: err} }
	}
	st, ok := m.ctx.WatchPRs[k.URL()]
	if !ok || st.HeldDraft == "" {
		return func() tea.Msg { return reviewActionMsg{err: fmt.Errorf("%s has no held review", k)} }
	}
	if m.pendingSend != k.URL() {
		m.pendingSend = k.URL()
		return m.notify(fmt.Sprintf("Press S again to post the held review on %s", k))
	}
	m.pendingSend = ""
	e := m.engineFor(k)
	if e == nil {
		return noWatches
	}
	return func() tea.Msg {
		if err := e.PostHeld(context.Background(), k); err != nil {
			return reviewActionMsg{err: err}
		}
		return reviewActionMsg{note: fmt.Sprintf("Posted the held review on %s", k)}
	}
}

// engineFor is the watch that tracks k.
func (m *Model) engineFor(k reviewing.PRKey) *reviewing.Engine {
	for _, e := range m.reviewers {
		for _, st := range e.States() {
			if st.Key == k {
				return e
			}
		}
	}
	return nil
}

// reviewNow forces a review of the selected PR on an immediate poll.
func (m *Model) reviewNow(row data.RowData) tea.Cmd {
	k, err := rowKey(row)
	if err != nil {
		return func() tea.Msg { return reviewActionMsg{err: err} }
	}
	e := m.engineFor(k)
	if e == nil {
		return func() tea.Msg {
			return reviewActionMsg{
				err: fmt.Errorf("%s is not in a review watch; press f to add it", k),
			}
		}
	}
	if _, active := m.ctx.WatchPRs[k.URL()]; !active {
		return func() tea.Msg {
			return reviewActionMsg{
				err: fmt.Errorf("%s is no longer open (merged, closed, or back to draft)", k),
			}
		}
	}
	if err := e.Force(k); err != nil {
		return func() tea.Msg { return reviewActionMsg{err: err} }
	}
	return tea.Batch(m.notify(fmt.Sprintf("Reviewing %s now", k)), manualPollCmd(e))
}

func (m *Model) togglePause() tea.Cmd {
	e := m.targetEngine()
	if e == nil {
		return noWatches
	}
	e.SetPaused(!e.Paused())
	m.syncWatchState()
	if e.Paused() {
		return m.notify(fmt.Sprintf("Paused review watch %s", e.Name()))
	}
	return tea.Batch(m.notify(fmt.Sprintf("Resumed review watch %s", e.Name())), manualPollCmd(e))
}
