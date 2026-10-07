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
	reviewPolledMsg struct {
		watch string
		jobs  []reviewing.Job
		err   error
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

func pollCmd(e *reviewing.Engine) tea.Cmd {
	return func() tea.Msg {
		jobs, _, err := e.Poll(context.Background())
		return reviewPolledMsg{watch: e.Name(), jobs: jobs, err: err}
	}
}

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
	for _, s := range m.syncWatchState() {
		if s.status.InFlight > 0 {
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
		cmds = append(cmds, pollCmd(e))
	}
	return tea.Batch(cmds...)
}

func reviewCmd(e *reviewing.Engine, j reviewing.Job) tea.Cmd {
	return func() tea.Msg {
		return reviewDoneMsg{watch: e.Name(), out: e.Review(context.Background(), j)}
	}
}

// startCmds cleans up after any earlier dispatch, polls each watch once,
// and starts the heartbeat that schedules every later poll.
func (r reviewers) startCmds() tea.Cmd {
	if len(r) == 0 {
		return nil
	}
	cmds := make([]tea.Cmd, 0, len(r)+1)
	for _, e := range r {
		cmds = append(cmds, func() tea.Msg {
			if err := e.Startup(context.Background()); err != nil {
				log.Error("review watch startup", "watch", e.Name(), "err", err)
			}
			return pollCmd(e)()
		})
	}
	return tea.Batch(append(cmds, heartbeatCmd())...)
}

func (r reviewers) shutdown() {
	for _, e := range r {
		e.Shutdown()
	}
}

// watchSnapshot is one read of a watch's state, shared by everything a sync
// renders so each refresh reads the state files once.
type watchSnapshot struct {
	engine *reviewing.Engine
	status reviewing.WatchStatus
	states []reviewing.PRState
}

func (r reviewers) snapshots() []watchSnapshot {
	out := make([]watchSnapshot, 0, len(r))
	for _, e := range r {
		states := e.States()
		status := e.Status()
		status.Queued = reviewing.QueuedCount(states)
		out = append(out, watchSnapshot{engine: e, status: status, states: states})
	}
	return out
}

// watchPRs returns the active watched PRs and their drafts, keyed by PR URL.
func watchPRs(snaps []watchSnapshot) (map[string]reviewing.PRState, map[string]reviewing.Draft) {
	prs := map[string]reviewing.PRState{}
	drafts := map[string]reviewing.Draft{}
	for _, s := range snaps {
		for _, st := range s.states {
			if st.Inactive {
				continue
			}
			prs[st.Key.URL()] = st
			if d, ok := s.engine.DraftFor(st); ok {
				drafts[st.Key.URL()] = d
			}
		}
	}
	return prs, drafts
}

func footerText(statuses []reviewing.WatchStatus) string {
	parts := make([]string, 0, len(statuses))
	for _, s := range statuses {
		state := s.StateLabel()
		if s.Prompt != "" {
			parts = append(parts, "▶ "+s.Prompt)
		}
		parts = append(
			parts,
			fmt.Sprintf(
				"%s · %s · $%.2f today · %s",
				s.Name,
				reviewsection.ReviewCounts(s),
				s.SpentToday,
				state,
			),
		)
	}
	return strings.Join(parts, " │ ")
}

func (m *Model) syncWatchState() []watchSnapshot {
	snaps := m.reviewers.snapshots()
	m.ctx.WatchPRs, m.ctx.WatchDrafts = watchPRs(snaps)
	statuses := make([]reviewing.WatchStatus, 0, len(snaps))
	for _, s := range snaps {
		statuses = append(statuses, s.status)
	}
	m.ctx.WatchFooter = footerText(statuses)
	m.refreshReviewSections(snaps)
	return snaps
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
func (m *Model) refreshReviewSections(snaps []watchSnapshot) {
	for _, s := range m.reviewSections {
		rs, ok := s.(*reviewsection.Model)
		if !ok {
			continue
		}
		for _, snap := range snaps {
			if snap.engine.Name() == rs.Watch() {
				rs.SetData(snap.status, snap.states)
			}
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

// addRefPrompt asks for the PR to add to the review watch.
const addRefPrompt = "Add PR to the review watch (URL or owner/repo#N):"

// askAddRef asks, in the current section, for a PR to add to the review
// watch.
func (m *Model) askAddRef() tea.Cmd {
	s := m.getCurrSection()
	if s == nil {
		return nil
	}
	return s.AskInput(addRefPrompt, m.addRef)
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

// sendDraft posts the review shown for the selected PR, a held review or a
// dry-run draft, after the user confirms.
func (m *Model) sendDraft(row data.RowData) tea.Cmd {
	k, err := rowKey(row)
	if err != nil {
		return func() tea.Msg { return reviewActionMsg{err: err} }
	}
	if _, ok := m.ctx.WatchDrafts[k.URL()]; !ok {
		return func() tea.Msg { return reviewActionMsg{err: fmt.Errorf("%s has no review to post", k)} }
	}
	e := m.engineFor(k)
	if e == nil {
		return noWatches
	}
	return m.confirm(fmt.Sprintf("Post the review on %s?", k), func() tea.Cmd {
		return func() tea.Msg {
			action, notApproved, err := e.PostDraft(context.Background(), k)
			if err != nil {
				return reviewActionMsg{err: err}
			}
			return reviewActionMsg{note: postedNote(k, action, notApproved)}
		}
	})
}

// postedNote tells the user what PostDraft sent.
func postedNote(k reviewing.PRKey, action reviewing.Action, notApproved string) string {
	switch {
	case action == reviewing.ActionApprove:
		return fmt.Sprintf("Approved %s", k)
	case notApproved != "":
		return fmt.Sprintf("Posted the review on %s without approving: %s", k, notApproved)
	default:
		return fmt.Sprintf("Posted the review on %s", k)
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
	return tea.Batch(m.notify(fmt.Sprintf("Reviewing %s now", k)), pollCmd(e))
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
	return tea.Batch(m.notify(fmt.Sprintf("Resumed review watch %s", e.Name())), pollCmd(e))
}

// reviewAllBaseline queues a full review of every baselined PR in the
// current watch after the user confirms the count and estimated cost.
func (m *Model) reviewAllBaseline() tea.Cmd {
	e := m.targetEngine()
	if e == nil {
		return noWatches
	}
	n := 0
	for _, st := range e.States() {
		if !st.Inactive && st.Status == reviewing.StatusBaseline {
			n++
		}
	}
	if n == 0 {
		return m.notify("No baseline PRs to review")
	}
	return m.confirm(baselinePrompt(n, e.Status()), func() tea.Cmd {
		queued, err := e.ForceBaseline()
		if err != nil {
			return func() tea.Msg { return reviewActionMsg{err: err} }
		}
		return tea.Batch(
			m.notify(fmt.Sprintf("Queued full reviews of %d baseline PRs", queued)),
			pollCmd(e),
		)
	})
}

// Rough per-review cost bounds for a full review, used only to warn
// before queueing many at once.
const (
	reviewCostLow  = 0.4
	reviewCostHigh = 1.4
)

func baselinePrompt(n int, ws reviewing.WatchStatus) string {
	return fmt.Sprintf(
		"Queue full reviews of %d baseline PRs (about $%.0f–%.0f; $%.2f of $%.0f spent today)?",
		n,
		float64(n)*reviewCostLow,
		float64(n)*reviewCostHigh,
		ws.SpentToday,
		ws.DailyBudget,
	)
}

// postAllDrafts posts every unposted dry-run draft in the current watch.
// It asks first, showing what it will post.
func (m *Model) postAllDrafts() tea.Cmd {
	e := m.targetEngine()
	if e == nil {
		return noWatches
	}
	drafts := e.UnpostedDrafts()
	if len(drafts) == 0 {
		return m.notify("No unposted drafts")
	}
	return m.confirm(postPrompt(drafts), func() tea.Cmd {
		return func() tea.Msg {
			return reviewActionMsg{note: postSummary(e.PostAllDrafts(context.Background()))}
		}
	})
}

func postPrompt(drafts map[reviewing.PRKey]reviewing.Draft) string {
	var approve, changes, comment int
	for _, d := range drafts {
		switch reviewing.Action(d.Event) {
		case reviewing.ActionApprove:
			approve++
		case reviewing.ActionRequestChanges:
			changes++
		default:
			comment++
		}
	}
	return fmt.Sprintf(
		"Post %d drafts (%d approvals, %d change requests, %d comments)? Approvals are checked again first.",
		len(drafts),
		approve,
		changes,
		comment,
	)
}

// maxSkippedShown bounds how many skipped PRs a post summary names.
const maxSkippedShown = 3

// postSummary reports what PostAllDrafts did.
func postSummary(results []reviewing.PostResult) string {
	var approved, changes, comments int
	var skipped []string
	for _, r := range results {
		switch {
		case r.Err != nil:
			skipped = append(skipped, fmt.Sprintf("%s#%d: %v", r.Key.Repo, r.Key.Number, r.Err))
		case r.Action == reviewing.ActionApprove:
			approved++
		case r.Action == reviewing.ActionRequestChanges:
			changes++
		default:
			comments++
		}
	}
	s := fmt.Sprintf(
		"Posted %d (%d approved, %d changes requested, %d comments)",
		approved+changes+comments,
		approved,
		changes,
		comments,
	)
	if n := len(skipped); n > 0 {
		shown := skipped[:min(n, maxSkippedShown)]
		s += fmt.Sprintf("; skipped %d: %s", n, strings.Join(shown, "; "))
		if n > maxSkippedShown {
			s += fmt.Sprintf("; and %d more", n-maxSkippedShown)
		}
	}
	return reviewing.SanitizeTerminal(s)
}

// confirm asks a y/N question in the current section and runs onYes when
// the user answers y.
func (m *Model) confirm(prompt string, onYes func() tea.Cmd) tea.Cmd {
	s := m.getCurrSection()
	if s == nil {
		return nil
	}
	return s.AskConfirmation(prompt, onYes)
}

// heartbeatInterval is how often dispatch checks whether a poll is due or
// the machine slept. A beat reads only clocks; polls run in background
// commands.
const heartbeatInterval = time.Minute

// wakeThreshold is how much more wall-clock time than awake time must pass
// between beats before dispatch treats it as waking from sleep.
const wakeThreshold = time.Minute

// processStart anchors the monotonic clock, which stops while the machine
// sleeps; the wall clock does not.
var processStart = time.Now()

// reviewHeartbeatMsg is one beat: the wall-clock time and the awake time
// since dispatch started.
type reviewHeartbeatMsg struct {
	wall  time.Time
	awake time.Duration
}

func heartbeatCmd() tea.Cmd {
	return tea.Tick(heartbeatInterval, func(t time.Time) tea.Msg {
		return reviewHeartbeatMsg{wall: t.Round(0), awake: time.Since(processStart)}
	})
}

// sleptBetween is how long the machine slept between two beats.
func sleptBetween(prev, cur reviewHeartbeatMsg) time.Duration {
	if prev.wall.IsZero() {
		return 0
	}
	return cur.wall.Sub(prev.wall) - (cur.awake - prev.awake)
}

// onHeartbeat runs on the UI loop, so it only compares clocks. After sleep
// it tells every watch it woke; then it polls, in background commands,
// each watch whose next poll is due.
func (m *Model) onHeartbeat(beat reviewHeartbeatMsg) tea.Cmd {
	cmds := make([]tea.Cmd, 0, len(m.reviewers)+2)
	if sleptBetween(m.lastBeat, beat) > wakeThreshold {
		for _, e := range m.reviewers {
			e.Woke(beat.wall)
		}
		cmds = append(cmds, m.notify("Woke from sleep — resyncing review watches"))
	}
	m.lastBeat = beat
	for _, e := range m.reviewers {
		cmds = append(cmds, pollIfDueCmd(e, beat.wall))
	}
	return tea.Batch(append(cmds, heartbeatCmd())...)
}

// pollIfDueCmd polls e in the background when its next poll is due at now.
func pollIfDueCmd(e *reviewing.Engine, now time.Time) tea.Cmd {
	return func() tea.Msg {
		if !e.PollDue(now) {
			return nil
		}
		return pollCmd(e)()
	}
}

// refreshWatchViews re-reads watch state and redraws everything that shows
// it: the Reviewing tables, PR rows, footer, and sidebar.
func (m *Model) refreshWatchViews() tea.Cmd {
	m.syncWatchState()
	m.resyncPrSectionRows()
	return m.syncSidebar()
}
