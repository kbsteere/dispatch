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
)

const reviewingSectionPrefix = "Reviewing: "

// reviewers are the running reviewingFor watches, one engine each.
type reviewers []*reviewing.Engine

type (
	reviewTickMsg   struct{ watch string }
	reviewPolledMsg struct {
		watch string
		jobs  []reviewing.Job
		next  time.Duration
		err   error
	}
	reviewDoneMsg struct {
		watch string
		out   reviewing.Outcome
	}
	// reviewActionMsg reports a user action on a watch (add, remove, send).
	reviewActionMsg struct {
		note    string
		err     error
		refresh bool
	}
)

// newReviewers opens an engine per configured watch. A watch that fails to
// open is logged and skipped.
func newReviewers(cfg *config.Config) reviewers {
	lookup := func(name string) (string, bool) { return common.GetRepoLocalPath(name, cfg.RepoPaths) }
	var out reviewers
	for _, w := range cfg.ReviewingFor.Watches {
		e, err := reviewing.NewEngine(reviewing.Deps{
			Watch: w, Limits: cfg.ReviewingFor.LimitsFor(w), StateRoot: reviewing.DefaultStateRoot(),
			GitHub: reviewing.DefaultGitHub(), LookupPath: lookup, ContextDirs: contextDirs(cfg.RepoPaths),
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
			out[st.Key.URL()] = st
		}
	}
	return out
}

func (r reviewers) pinnedURLs() map[string][]string {
	out := map[string][]string{}
	for _, e := range r {
		for _, st := range e.States() {
			if st.Pinned {
				out[e.Name()] = append(out[e.Name()], st.Key.URL())
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
		parts = append(parts, fmt.Sprintf("%s · %d reviewing · $%.2f today · %s", s.Name, s.InFlight, s.SpentToday, state))
	}
	return strings.Join(parts, " │ ")
}

// ReviewingSections are the PR sections for each watch: its search, plus a
// section listing PRs added by hand when there are any.
func ReviewingSections(cfg config.ReviewingForConfig, pinned map[string][]string) []config.PrsSectionConfig {
	var out []config.PrsSectionConfig
	for _, w := range cfg.Watches {
		out = append(out, config.PrsSectionConfig{Title: reviewingSectionPrefix + w.Name, Filters: w.SearchFilter()})
		if urls := pinned[w.Name]; len(urls) > 0 {
			out = append(out, config.PrsSectionConfig{Title: reviewingSectionPrefix + w.Name + " · added", Urls: urls})
		}
	}
	return out
}

// watchFromSectionTitle returns the watch a reviewing section belongs to.
func watchFromSectionTitle(title string) (string, bool) {
	name, ok := strings.CutPrefix(title, reviewingSectionPrefix)
	if !ok {
		return "", false
	}
	name, _ = strings.CutSuffix(name, " · added")
	return name, true
}

func (m *Model) syncWatchState() {
	m.ctx.WatchPRs = m.reviewers.watchPRs()
	m.ctx.WatchFooter = footerText(m.reviewers.statuses())
}

// rebuildReviewSections replaces the reviewing sections at the end of the
// configured PR sections, so hand-added PRs appear and disappear.
func (m *Model) rebuildReviewSections() {
	if m.ctx.Config == nil {
		return
	}
	m.ctx.Config.PRSections = append(slices.Clone(m.userPRSections),
		ReviewingSections(m.ctx.Config.ReviewingFor, m.reviewers.pinnedURLs())...)
}

// targetEngine is the watch for the focused reviewing section, or the
// first watch.
func (m *Model) targetEngine() *reviewing.Engine {
	if len(m.reviewers) == 0 {
		return nil
	}
	if s := m.getCurrSection(); s != nil {
		if name, ok := watchFromSectionTitle(s.GetConfig().Title); ok {
			if e := m.reviewers.byName(name); e != nil {
				return e
			}
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
	pinned := m.ctx.WatchPRs[k.URL()].Pinned
	return func() tea.Msg {
		if pinned {
			if err := e.Unpin(k); err != nil {
				return reviewActionMsg{err: err}
			}
			return reviewActionMsg{note: fmt.Sprintf("Removed %s from %s", k, e.Name()), refresh: true}
		}
		if err := e.Pin(context.Background(), k); err != nil {
			return reviewActionMsg{err: err}
		}
		return reviewActionMsg{note: fmt.Sprintf("Added %s to %s; it will be reviewed on the next poll", k, e.Name()), refresh: true}
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
		return reviewActionMsg{note: fmt.Sprintf("Added %s to %s; it will be reviewed on the next poll", k, e.Name()), refresh: true}
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
			return reviewActionMsg{err: fmt.Errorf("%s is not in a review watch; press f to add it", k)}
		}
	}
	if err := e.Force(k); err != nil {
		return func() tea.Msg { return reviewActionMsg{err: err} }
	}
	return tea.Batch(m.notify(fmt.Sprintf("Reviewing %s on the next poll", k)), pollCmd(e))
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
