package reviewing

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kbsteere/dispatch/v4/internal/config"
)

const (
	claimTTL = 45 * time.Minute
	// pollTimeout bounds one poll; pollRetry is how soon a failed poll is
	// retried.
	pollTimeout = 3 * time.Minute
	pollRetry   = time.Minute
	// wakeSettle is how long after waking failures are blamed on the network
	// coming back.
	wakeSettle    = 5 * time.Minute
	sessionMaxAge = 14 * 24 * time.Hour
	searchPages   = 3
)

var backoffSteps = []time.Duration{30 * time.Minute, 2 * time.Hour, 24 * time.Hour}

// incompleteRetrySteps are the waits before an incomplete review is retried
// on its own, growing with each incomplete retry.
var incompleteRetrySteps = []time.Duration{
	15 * time.Minute, 30 * time.Minute, time.Hour, 2 * time.Hour, 4 * time.Hour,
}

// nextBackoff is b advanced one step along steps, which repeat their last
// wait once exhausted.
func nextBackoff(b Backoff, steps []time.Duration, now time.Time) Backoff {
	return Backoff{Until: now.Add(steps[min(b.Step, len(steps)-1)]), Step: b.Step + 1}
}

// Deps are an Engine's collaborators and settings.
type Deps struct {
	Watch       config.ReviewWatch
	Limits      config.ReviewingLimits
	StateRoot   string
	GitHub      GitHub
	LookupPath  func(nameWithOwner string) (string, bool)
	ContextDirs []string
	Claude      string
	Now         func() time.Time
}

// Job is a claimed review pass.
type Job struct {
	Key     PRKey
	Trigger Trigger
	Pass    Pass
	Detail  PRDetail
}

// Outcome is what a review pass did.
type Outcome struct {
	Key     PRKey
	Status  Status
	Posted  Action
	Note    string
	Err     error
	CostUSD float64
}

// WatchStatus summarises a watch for the footer.
type WatchStatus struct {
	Name     string
	InFlight int
	// Queued counts PRs waiting for a review the user asked for (N or E)
	// that has not started yet; see QueuedCount. Status leaves it zero.
	Queued     int
	SpentToday float64
	// SpentWeek and SpentMonth are the cost since Monday and since the 1st,
	// including today.
	SpentWeek  float64
	SpentMonth float64
	Mode       string
	Paused     string
	Err        string
	// QuotaRemaining is the GitHub GraphQL quota left, or 0 when unknown.
	QuotaRemaining int
	DailyBudget    float64
	// LastPoll is when the last successful poll finished; zero before the
	// first one.
	LastPoll time.Time
	// Prompt is an action the user must take now, such as touching a
	// security key; empty when none.
	Prompt string
	// NextPoll is when the next poll is due; zero when one is due now.
	NextPoll time.Time
}

// Engine runs one watch: it polls, claims PRs, runs review passes, and
// posts their results. Its methods are safe for concurrent use.
type Engine struct {
	d        Deps
	store    Store
	unlock   func()
	readOnly bool

	mu       sync.Mutex
	viewer   string
	quota    Quota
	inflight map[PRKey]*Run
	claimed  map[PRKey]struct{}
	pools    map[string]*Pool
	paused   bool
	pauseWhy string
	lastErr  string
	lastPoll time.Time
	prompt   string // an action the user must take now, such as touching a security key
	closing  bool
	// refreshed is set once a poll has re-read every PR's details, which
	// the first poll after startup does so derived fields are current.
	refreshed bool
	// polling is set while a poll runs; nextPoll is when the next one is
	// due by the wall clock; wokeAt is when the machine last woke.
	polling  bool
	nextPoll time.Time
	wokeAt   time.Time
}

// NewEngine opens the watch's state. When another dispatch holds the
// watch's lock the engine is read-only: it reports state but never polls.
func NewEngine(d Deps) (*Engine, error) {
	if d.Now == nil {
		d.Now = time.Now
	}
	store, err := OpenStore(d.StateRoot, d.Watch.Name)
	if err != nil {
		return nil, err
	}
	e := &Engine{
		d:        d,
		store:    store,
		inflight: map[PRKey]*Run{},
		claimed:  map[PRKey]struct{}{},
		pools:    map[string]*Pool{},
	}
	unlock, err := store.Lock()
	if errors.Is(err, ErrLocked) {
		e.readOnly = true
		e.lastErr = err.Error()
		return e, nil
	}
	if err != nil {
		return nil, err
	}
	e.unlock = unlock
	return e, nil
}

func (e *Engine) ReadOnly() bool { return e.readOnly }

// Name is the watch name.
func (e *Engine) Name() string { return e.d.Watch.Name }

// update loads, mutates, and saves one PR's state under the engine lock.
func (e *Engine) update(k PRKey, fn func(*PRState)) (PRState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, err := e.store.LoadPR(k)
	if err != nil && !errors.Is(err, ErrCorrupt) {
		return st, err
	}
	fn(&st)
	return st, e.store.SavePR(st)
}

func (e *Engine) release(k PRKey) {
	_, _ = e.update(k, func(st *PRState) { st.dropClaim() })
	e.mu.Lock()
	delete(e.claimed, k)
	delete(e.inflight, k)
	e.mu.Unlock()
}

func (e *Engine) kill(k PRKey) {
	e.mu.Lock()
	r := e.inflight[k]
	delete(e.inflight, k)
	delete(e.claimed, k)
	e.mu.Unlock()
	if r != nil {
		r.Kill()
	}
}

func claimLive(st PRState, now time.Time) bool {
	return st.Claim != nil && now.Sub(st.Claim.Since) < claimTTL
}

// interruptedNote explains a pass that stopped before it finished.
const interruptedNote = "interrupted before it finished; reviewed again on the next change"

// Startup kills claude processes left by an earlier dispatch, releases
// their claims, prunes worktree slots, and removes old session dirs.
func (e *Engine) Startup(ctx context.Context) error {
	if e.readOnly {
		return nil
	}
	states, err := e.store.AllPRs()
	if err != nil {
		return err
	}
	// A pass that was running when dispatch stopped never finished; so is
	// one marked reviewing with no claim, which earlier versions left behind.
	for _, st := range states {
		if st.Claim == nil && st.Status != StatusReviewing {
			continue
		}
		if st.Claim != nil {
			KillOrphan(st.Claim.PID)
		}
		_, _ = e.update(st.Key, func(s *PRState) {
			s.Claim = nil
			if s.Status == StatusReviewing {
				s.Status = StatusFailed
				s.Note = interruptedNote
			}
		})
	}
	slotDirs, _ := filepath.Glob(filepath.Join(e.d.StateRoot, "worktrees", "*__*"))
	for _, dir := range slotDirs {
		owner, repo, ok := strings.Cut(filepath.Base(dir), "__")
		if !ok || !ValidName(owner) || !ValidName(repo) {
			continue
		}
		if local, err := e.localRepo(ctx, PRKey{Owner: owner, Repo: repo}); err == nil {
			_ = NewPool(local, dir, 0).Prune(ctx)
		}
	}
	sessions, _ := filepath.Glob(filepath.Join(e.d.StateRoot, "sessions", "*"))
	for _, s := range sessions {
		if info, err := os.Stat(s); err == nil && e.d.Now().Sub(info.ModTime()) > sessionMaxAge {
			_ = os.RemoveAll(s)
		}
	}
	return nil
}

func (e *Engine) localRepo(ctx context.Context, k PRKey) (LocalRepo, error) {
	return EnsureLocalRepo(ctx, e.d.StateRoot, k, e.d.LookupPath)
}

// Shutdown kills every running session, releases its claim, and gives up
// the watch lock.
func (e *Engine) Shutdown() {
	e.mu.Lock()
	e.closing = true
	runs := e.inflight
	e.inflight = map[PRKey]*Run{}
	e.mu.Unlock()
	for k, r := range runs {
		r.Kill()
		e.release(k)
	}
	if e.unlock != nil {
		e.unlock()
		e.unlock = nil
	}
}

func (e *Engine) SetPaused(p bool) {
	e.mu.Lock()
	e.paused = p
	e.mu.Unlock()
}

func (e *Engine) Paused() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.paused
}

func (e *Engine) Status() WatchStatus {
	e.mu.Lock()
	defer e.mu.Unlock()
	mode := "live"
	if e.d.Watch.DryRun {
		mode = "dry run"
	}
	paused := e.pauseWhy
	if e.paused {
		paused = "paused"
	}
	if e.readOnly {
		paused = "read-only"
	}
	now := e.d.Now()
	return WatchStatus{
		Name:           e.d.Watch.Name,
		InFlight:       len(e.claimed),
		SpentToday:     e.store.Spend(Day(now)),
		SpentWeek:      e.store.SpendSince(WeekStart(now), now),
		SpentMonth:     e.store.SpendSince(MonthStart(now), now),
		Mode:           mode,
		Paused:         paused,
		Err:            e.lastErr,
		QuotaRemaining: e.quota.Remaining,
		DailyBudget:    e.d.Limits.DailyBudgetUsd,
		LastPoll:       e.lastPoll,
		Prompt:         e.prompt,
		NextPoll:       e.nextPoll,
	}
}

// States returns every PR the watch knows about.
func (e *Engine) States() []PRState {
	e.mu.Lock()
	defer e.mu.Unlock()
	st, _ := e.store.AllPRs()
	return st
}

// Pin adds a PR to the watch by hand. Its first review is claimed on the
// next poll.
func (e *Engine) Pin(ctx context.Context, k PRKey) error {
	if e.readOnly {
		return errors.New("watch is read-only in this dispatch")
	}
	id, err := e.d.GitHub.Resolve(ctx, k)
	if err != nil {
		return err
	}
	_, err = e.update(k, func(st *PRState) {
		st.Pinned = true
		st.NodeID = id
		st.ReviewedSig = nil
		st.UpdatedAt = ""
	})
	return err
}

// ResolveNumber finds the open PR numbered n in the repositories the watch
// scopes with repo:. It fails when none or more than one of them has it open.
func (e *Engine) ResolveNumber(ctx context.Context, n int) (PRKey, error) {
	repos := scopeRepos(e.d.Watch.Scopes)
	if len(repos) == 0 {
		return PRKey{}, fmt.Errorf(
			"%s has no repo: scopes to look in; type owner/repo#%d",
			e.Name(),
			n,
		)
	}
	keys := make([]PRKey, 0, len(repos))
	names := make([]string, 0, len(repos))
	for _, r := range repos {
		keys = append(keys, PRKey{Owner: r.Owner, Repo: r.Repo, Number: n})
		names = append(names, r.Repo)
	}
	open, err := e.d.GitHub.OpenPRs(ctx, keys)
	if err != nil {
		return PRKey{}, err
	}
	switch len(open) {
	case 1:
		return open[0], nil
	case 0:
		return PRKey{}, fmt.Errorf("no open PR #%d in %s", n, strings.Join(names, ", "))
	default:
		in := make([]string, 0, len(open))
		for _, k := range open {
			in = append(in, k.Repo)
		}
		return PRKey{}, fmt.Errorf(
			"#%d is open in %s; type owner/repo#%d",
			n,
			strings.Join(in, " and "),
			n,
		)
	}
}

// scopeRepos returns the repositories named by repo: scopes, skipping any
// whose owner or name is invalid.
func scopeRepos(scopes []string) []PRKey {
	var out []PRKey
	for _, s := range scopes {
		name, ok := strings.CutPrefix(s, "repo:")
		if !ok {
			continue
		}
		owner, repo, ok := strings.Cut(name, "/")
		if ok && ValidName(owner) && ValidName(repo) {
			out = append(out, PRKey{Owner: owner, Repo: repo})
		}
	}
	return out
}

func (e *Engine) Unpin(k PRKey) error {
	_, err := e.update(k, func(st *PRState) { st.Pinned = false })
	return err
}

// Force claims a review on the next poll regardless of triggers, backoff,
// and the daily per-PR limit. The daily budget still applies.
func (e *Engine) Force(k PRKey) error {
	_, err := e.update(k, func(st *PRState) { st.ForceReview = true })
	return err
}

func (e *Engine) observe(rl RateLimit) {
	if rl.ResetAt.IsZero() {
		return
	}
	e.mu.Lock()
	e.quota.Observe(rl.Remaining, rl.ResetAt, rl.Cost, e.d.Now())
	e.mu.Unlock()
}

func (e *Engine) setPause(why string) {
	e.mu.Lock()
	e.pauseWhy = why
	e.mu.Unlock()
}

func (e *Engine) setErr(err error) {
	e.mu.Lock()
	if err == nil {
		e.lastErr = ""
	} else {
		e.lastErr = err.Error()
	}
	e.mu.Unlock()
}

func (e *Engine) ensureViewer(ctx context.Context) (string, error) {
	e.mu.Lock()
	v := e.viewer
	e.mu.Unlock()
	if v != "" {
		return v, nil
	}
	v, err := e.d.GitHub.Viewer(ctx)
	if err != nil {
		return "", err
	}
	e.mu.Lock()
	e.viewer = v
	e.mu.Unlock()
	return v, nil
}

func (e *Engine) signature(ctx context.Context, d PRDetail, viewer string) Signature {
	ignore := map[string]struct{}{viewer: {}}
	for _, l := range e.d.Watch.IgnoreLogins {
		ignore[l] = struct{}{}
	}
	events, resolved := d.Events()
	ci := ciUnknown
	if checks, err := e.requiredChecks(ctx, d); err == nil {
		ci = checksState(checks)
	}
	return Signature{
		Head:   d.HeadOID,
		Draft:  d.IsDraft,
		Digest: ActivityDigest(events, resolved, ignore),
		CI:     ci,
	}
}

// ciUnknown is the CI state when required checks could not be determined.
// It never satisfies the approval gate.
const ciUnknown = "UNKNOWN"

// needsDetail reports whether a PR must be re-read even though its
// updatedAt may not have moved: CI results, head settling, retries after
// backoff, and throttling do not update a PR.
func needsDetail(st PRState, updatedAt string, now time.Time) bool {
	switch {
	case st.UpdatedAt != updatedAt, st.ForceReview, st.Pinned:
		return true
	case st.Claim != nil && !claimLive(st, now):
		return true
	case st.Status == StatusWaitingOnCI, st.Status == StatusFailed, st.Status == StatusThrottled:
		return true
	case len(st.ReviewGaps) > 0 && !now.Before(st.Backoff.Until):
		return true // an incomplete review is due a retry
	case st.HeadSeen != nil && (st.ReviewedSig == nil || st.HeadSeen.OID != st.ReviewedSig.Head):
		return true
	}
	return false
}

// Poll runs one polling cycle, unless one is already running, and returns
// the passes it claimed and how long until the next poll is due. Each poll
// is bounded by pollTimeout so a connection lost to sleep cannot hang it.
func (e *Engine) Poll(ctx context.Context) ([]Job, time.Duration, error) {
	e.mu.Lock()
	if e.polling {
		e.mu.Unlock()
		return nil, 0, nil
	}
	e.polling = true
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()
	jobs, next, err := e.poll(ctx)
	if err != nil {
		// Retry soon: the usual cause is a network still coming back.
		next = min(next, pollRetry)
	}
	e.mu.Lock()
	e.polling = false
	e.nextPoll = e.d.Now().Round(0).Add(next)
	e.mu.Unlock()
	return jobs, next, err
}

// PollDue reports whether the next poll is due at now by the wall clock, so
// time spent asleep counts toward it. It reads only memory.
func (e *Engine) PollDue(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.polling && !e.nextPoll.After(now.Round(0))
}

// Woke tells the engine the machine just woke from sleep: a poll is due now,
// and a review that fails while the network settles is queued again rather
// than backed off.
func (e *Engine) Woke(now time.Time) {
	e.mu.Lock()
	e.wokeAt = now
	e.nextPoll = time.Time{}
	e.mu.Unlock()
}

func (e *Engine) poll(ctx context.Context) ([]Job, time.Duration, error) {
	now := e.d.Now()
	lim := e.d.Limits
	if e.readOnly || e.Paused() {
		return nil, lim.PollInterval, nil
	}
	if e.store.Spend(Day(now)) >= lim.DailyBudgetUsd {
		e.setPause("budget")
		return nil, lim.PollInterval, nil
	}
	viewer, err := e.ensureViewer(ctx)
	if err != nil {
		e.setErr(err)
		return nil, lim.PollInterval, err
	}

	hits, rl, err := e.d.GitHub.Search(ctx, e.d.Watch.SearchFilter(), searchPages)
	e.observe(rl)
	if err != nil {
		e.setErr(err)
		return nil, lim.PollInterval, err
	}

	states, _ := e.store.AllPRs()
	byKey := map[PRKey]PRState{}
	for _, st := range states {
		byKey[st.Key] = st
	}
	e.mu.Lock()
	refreshAll := !e.refreshed
	e.mu.Unlock()
	inResults := make(map[PRKey]struct{}, len(hits))
	var ids []string
	var left []string // PRs that left the search, to learn how they ended
	for _, h := range hits {
		inResults[h.Key] = struct{}{}
		if refreshAll || needsDetail(byKey[h.Key], h.UpdatedAt, now) {
			ids = append(ids, h.NodeID)
		}
	}
	for _, st := range states {
		if _, ok := inResults[st.Key]; ok {
			continue
		}
		switch {
		case st.Pinned && st.NodeID != "":
			ids = append(ids, st.NodeID)
		case refreshAll && st.Inactive && st.ClosedState == "" && st.NodeID != "" &&
			recentlyUpdated(st, now):
			// Learn whether a PR that left before this dispatch started was
			// merged or closed.
			left = append(left, st.NodeID)
		case st.Claim != nil || !st.Inactive || st.ForceReview:
			// Merged, closed, back to draft, or out of scope: stop
			// reviewing it and hide it until the search returns it.
			if !st.Inactive && st.NodeID != "" {
				left = append(left, st.NodeID)
			}
			if st.Claim != nil {
				e.kill(st.Key)
			}
			_, _ = e.update(st.Key, func(s *PRState) {
				if s.Claim != nil {
					s.Status = StatusWaitingOnAuthor
					s.Note = "closed or returned to draft"
				}
				s.Claim = nil
				s.Inactive = true
				s.ForceReview = false
			})
		}
	}

	var details []PRDetail
	if len(ids) > 0 {
		details, rl, err = e.d.GitHub.Details(ctx, ids)
		e.observe(rl)
		if err != nil {
			e.setErr(err)
			return nil, lim.PollInterval, err
		}
	}

	e.recordClosed(ctx, left, viewer, now)

	initialized := e.store.Initialized()
	var jobs []Job
	for _, d := range details {
		if job, ok := e.evaluate(ctx, d, viewer, now, initialized); ok {
			jobs = append(jobs, job)
		}
	}
	e.markRequested(ctx, hits)
	if !initialized {
		if err := e.store.MarkInitialized(); err != nil {
			e.setErr(err)
		}
	}
	e.setErr(nil)

	e.mu.Lock()
	e.refreshed = true
	e.lastPoll = e.d.Now()
	next, why := e.quota.Next(lim.PollInterval, lim.MaxGithubPointsPerHour, now)
	e.mu.Unlock()
	e.setPause(why)
	return jobs, next, nil
}

// evaluate records what a poll saw for one PR and claims a pass when one
// is due.
func (e *Engine) evaluate(
	ctx context.Context,
	d PRDetail,
	viewer string,
	now time.Time,
	initialized bool,
) (Job, bool) {
	lim := e.d.Limits
	sig := e.signature(ctx, d, viewer)

	var job Job
	var claimed, kill bool
	e.mu.Lock()
	st, err := e.store.LoadPR(d.Key)
	if errors.Is(err, ErrCorrupt) {
		rebuildFromMarkers(&st, d, viewer, sig)
	}
	st.NodeID = d.NodeID
	st.Inactive = false
	st.UpdatedAt = d.UpdatedAt
	st.CreatedAt = d.CreatedAt
	st.Title = d.Title
	st.Author = d.Author
	st.CI = sig.CI
	st.MyReview, st.MyReviewOnHead = myReview(d.Reviews, viewer, d.HeadOID)

	switch {
	case d.State != "OPEN":
		st.Pinned = false
		kill = st.Claim != nil
		st.dropClaim()
	case d.IsDraft:
		kill = st.Claim != nil
		st.dropClaim()
	default:
		ObserveHead(&st, d.HeadOID, now)
		ObserveCI(&st, sig.CI)
		if claimLive(st, now) && st.Claim.Head != d.HeadOID {
			kill = true
			st.dropClaim()
		}
		job, claimed = e.claimLocked(&st, d, sig, now, initialized, lim)
	}
	_ = e.store.SavePR(st)
	if claimed {
		e.claimed[d.Key] = struct{}{}
	}
	e.mu.Unlock()
	if kill {
		e.kill(d.Key)
	}
	return job, claimed
}

// claimLocked decides whether to claim a pass. e.mu must be held.
func (e *Engine) claimLocked(
	st *PRState,
	d PRDetail,
	sig Signature,
	now time.Time,
	initialized bool,
	lim config.ReviewingLimits,
) (Job, bool) {
	if claimLive(*st, now) {
		return Job{}, false
	}
	st.dropClaim()
	if !initialized && !st.Pinned {
		st.ReviewedSig = &sig
		st.Status = StatusBaseline
		return Job{}, false
	}
	trig := NeedsReview(st, sig, now)
	if st.ForceReview {
		trig = TriggerChange
	}
	if trig == TriggerNone {
		return Job{}, false
	}
	if !st.ForceReview {
		if now.Before(st.Backoff.Until) {
			return Job{}, false
		}
		if st.ReviewsByDay[Day(now)] >= lim.MaxReviewsPerPrPerDay {
			st.Status = StatusThrottled
			return Job{}, false
		}
	}
	if len(e.claimed) >= lim.MaxInFlight {
		return Job{}, false
	}
	pass := choosePass(st.CodeReviewedHead, d.HeadOID, st.ForceReview)
	prev := st.Status
	if prev == StatusReviewing {
		prev = "" // left by an interrupted pass; nothing to put back
	}
	st.Claim = &Claim{Since: now, Head: d.HeadOID, Prev: prev}
	st.Status = StatusReviewing
	st.ForceReview = false
	if trig == TriggerCI {
		st.LastCIWake = now
	}
	return Job{Key: d.Key, Trigger: trig, Pass: pass, Detail: d}, true
}

// rebuildFromMarkers restores dispatch's own threads after a corrupt state
// file, so nothing is reposted.
func rebuildFromMarkers(st *PRState, d PRDetail, viewer string, sig Signature) {
	for _, t := range d.Threads {
		if len(t.Comments) == 0 || t.Comments[0].Author != viewer ||
			!strings.Contains(t.Comments[0].Body, Marker) {
			continue
		}
		st.OurThreads = append(st.OurThreads, t.ID)
		status := FindingOutstanding
		if t.IsResolved {
			status = FindingAddressed
		}
		st.Ledger = append(st.Ledger, Finding{
			ID: nextFindingID(
				st.Ledger,
			),
			ThreadID: t.ID,
			Path:     t.Path,
			Line:     t.Line,
			Blocking: true,
			Summary:  summarize(t.Comments[0].Body),
			Status:   status,
		})
	}
	st.ReviewedSig = &sig
}

func nextFindingID(ledger []Finding) string {
	return fmt.Sprintf("f%d", len(ledger)+1)
}

func summarize(body string) string {
	s := strings.TrimSpace(strings.ReplaceAll(body, Marker, ""))
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
}

// Draft is a pass's would-be GitHub writes, kept for dry runs and for
// passes the tone lint held.
type Draft struct {
	Event    string          `json:"event"`
	Head     string          `json:"head"`
	Comments []ReviewComment `json:"comments"`
	Replies  []DraftReply    `json:"replies,omitempty"`
	Resolve  []string        `json:"resolve,omitempty"`
	Findings []Finding       `json:"findings,omitempty"`
	Ledger   []LedgerUpdate  `json:"ledger,omitempty"`
	Reasons  []string        `json:"lint_reasons,omitempty"`
	// Unanchored are findings that could not be placed on a changed line.
	// They are never posted; move one into comments to send it.
	Unanchored []OutFinding `json:"unanchored,omitempty"`
	// Verdict is the reviewer's approve, comment, or request_changes call.
	Verdict       string `json:"verdict,omitempty"`
	VerdictReason string `json:"verdict_reason,omitempty"`
	// CodeReviewed records that the pass read the code it had to at Head.
	CodeReviewed bool `json:"code_reviewed,omitempty"`
	// Body is the review's text: empty, or the line explaining an approval
	// with checks set aside. CI and CIBase are what that decision used.
	Body   string            `json:"body,omitempty"`
	CI     []CIAssessment    `json:"ci,omitempty"`
	CIBase map[string]string `json:"ci_base,omitempty"`
}

type DraftReply struct {
	ThreadID  string `json:"thread_id"`
	FindingID string `json:"finding_id"`
	Body      string `json:"body"`
}

func writeDraft(path string, d Draft) error {
	return writeJSONAtomic(path, d)
}

// DraftFor returns the review dispatch would post for st: its held review,
// or the latest dry-run draft, if it was never posted and was written for
// the head the watch last reviewed. Unposted drafts stay postable after
// the watch leaves dry run.
func (e *Engine) DraftFor(st PRState) (Draft, bool) {
	if st.HeldDraft != "" {
		drafts := filepath.Join(string(e.store), "drafts") + string(filepath.Separator)
		if !strings.HasPrefix(filepath.Clean(st.HeldDraft), drafts) {
			return Draft{}, false
		}
		d, err := readDraft(st.HeldDraft)
		return d, err == nil
	}
	if !st.DryRun || st.ReviewedSig == nil {
		return Draft{}, false
	}
	d, ok := e.store.LatestDraft(st.Key)
	if !ok || d.Head != st.ReviewedSig.Head {
		return Draft{}, false
	}
	return d, true
}

func readDraft(path string) (Draft, error) {
	var d Draft
	b, err := os.ReadFile(path)
	if err != nil {
		return d, err
	}
	return d, json.Unmarshal(b, &d)
}

func (e *Engine) setPrompt(p string) {
	e.mu.Lock()
	e.prompt = p
	e.mu.Unlock()
}

// choosePass picks how much of a PR a pass reviews. Code never read, or a
// review the user asked for, gets a full pass; a head that moved since the
// last code review gets an incremental pass; otherwise only comments changed.
func choosePass(codeReviewedHead, head string, forced bool) Pass {
	switch {
	case codeReviewedHead == "" || forced:
		return PassFull
	case codeReviewedHead != head:
		return PassIncremental
	default:
		return PassLedger
	}
}

// requestedQualifier narrows a search to PRs where the viewer, or a team
// the viewer belongs to, is a requested reviewer.
const requestedQualifier = "review-requested:@me"

// markRequested records which of hits request the viewer's review. A failed
// search leaves the previous marks in place.
func (e *Engine) markRequested(ctx context.Context, hits []SearchHit) {
	requested, rl, err := e.d.GitHub.Search(
		ctx,
		e.d.Watch.SearchFilter()+" "+requestedQualifier,
		1,
	)
	e.observe(rl)
	if err != nil {
		return
	}
	want := make(map[PRKey]struct{}, len(requested))
	for _, h := range requested {
		want[h.Key] = struct{}{}
	}
	for _, h := range hits {
		_, req := want[h.Key]
		e.mu.Lock()
		st, err := e.store.LoadPR(h.Key)
		e.mu.Unlock()
		if err != nil || st.ReviewRequested == req {
			continue
		}
		_, _ = e.update(h.Key, func(s *PRState) { s.ReviewRequested = req })
	}
}

// ForceBaseline queues a full review of every open PR the watch has only
// baselined, and returns how many it queued. Reviews still run a few at a
// time and stop at the daily budget.
func (e *Engine) ForceBaseline() (int, error) {
	if e.readOnly {
		return 0, errors.New("watch is read-only in this dispatch")
	}
	n := 0
	for _, st := range e.States() {
		if st.Inactive || st.Status != StatusBaseline {
			continue
		}
		if err := e.Force(st.Key); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// UnpostedDrafts are the dry-run drafts that were never posted, keyed by PR.
// Held reviews are left out; they wait for the user to edit them.
func (e *Engine) UnpostedDrafts() map[PRKey]Draft {
	out := map[PRKey]Draft{}
	for _, st := range e.States() {
		if st.Inactive || st.HeldDraft != "" {
			continue
		}
		if d, ok := e.DraftFor(st); ok {
			out[st.Key] = d
		}
	}
	return out
}

// PostResult is what PostDraft did for one PR.
type PostResult struct {
	Key         PRKey
	Action      Action
	NotApproved string
	Err         error
}

// PostAllDrafts posts every unposted dry-run draft, in PR order, with the
// same checks PostDraft applies to each.
func (e *Engine) PostAllDrafts(ctx context.Context) []PostResult {
	drafts := e.UnpostedDrafts()
	keys := slices.SortedFunc(maps.Keys(drafts), func(a, b PRKey) int {
		return cmp.Compare(a.FileName(), b.FileName())
	})
	out := make([]PostResult, 0, len(keys))
	for _, k := range keys {
		action, why, err := e.PostDraft(ctx, k)
		out = append(out, PostResult{Key: k, Action: action, NotApproved: why, Err: err})
	}
	return out
}

// myReview is viewer's standing review on a PR: their latest approval or
// change request, or, with neither, COMMENTED if they commented in a
// review. A dismissed review no longer stands. onHead reports that the
// review was on head.
func myReview(reviews []Review, viewer, head string) (state string, onHead bool) {
	var decisive, commented *Review
	for i, r := range reviews {
		if !strings.EqualFold(r.Author, viewer) {
			continue
		}
		switch r.State {
		case "APPROVED", "CHANGES_REQUESTED", "DISMISSED":
			if decisive == nil || !r.SubmittedAt.Before(decisive.SubmittedAt) {
				decisive = &reviews[i]
			}
		case "COMMENTED":
			if commented == nil || !r.SubmittedAt.Before(commented.SubmittedAt) {
				commented = &reviews[i]
			}
		}
	}
	if decisive != nil && decisive.State != "DISMISSED" {
		return decisive.State, decisive.CommitOID == head
	}
	if commented != nil {
		return commented.State, commented.CommitOID == head
	}
	return "", false
}

// disturbedBySleep reports whether a pass that started at started may have
// failed because the machine slept: it woke since, or woke recently enough
// that the network may still be coming back.
func (e *Engine) disturbedBySleep(started time.Time) bool {
	e.mu.Lock()
	w := e.wokeAt
	e.mu.Unlock()
	return !w.IsZero() && (w.After(started) || e.d.Now().Sub(w) < wakeSettle)
}

// requeue records a pass cut off by sleep and queues it again without
// backing off.
func (e *Engine) requeue(k PRKey, out Outcome, err error) Outcome {
	note := "interrupted by sleep; queued again: " + err.Error()
	out.Note, out.Err = note, err
	return e.finish(k, out, func(st *PRState) {
		st.Status = StatusFailed
		st.Note = note
		st.ForceReview = true
	})
}

// maxClosedLookups bounds how many PRs that left the search one poll reads.
const maxClosedLookups = 20

func recentlyUpdated(st PRState, now time.Time) bool {
	t, err := time.Parse(time.RFC3339, st.UpdatedAt)
	return err == nil && now.Sub(t) < ClosedShownFor
}

// recordClosed reads PRs that left the watch's search and records whether
// they were merged or closed, and the user's standing review on them. A PR
// that is still open (back to draft or out of scope) records nothing.
func (e *Engine) recordClosed(ctx context.Context, ids []string, viewer string, now time.Time) {
	if len(ids) == 0 {
		return
	}
	details, rl, err := e.d.GitHub.Details(ctx, ids[:min(len(ids), maxClosedLookups)])
	e.observe(rl)
	if err != nil {
		return
	}
	for _, d := range details {
		if d.State != "MERGED" && d.State != "CLOSED" {
			continue
		}
		_, _ = e.update(d.Key, func(s *PRState) {
			s.ClosedState = d.State
			s.ClosedAt = cmp.Or(d.ClosedAt, now)
			s.MyReview, s.MyReviewOnHead = myReview(d.Reviews, viewer, d.HeadOID)
		})
	}
}

// StateLabel is the watch's mode, or why it is paused.
func (s WatchStatus) StateLabel() string {
	if s.Paused != "" {
		return "paused: " + s.Paused
	}
	return s.Mode
}

// requiredChecks reduces d's head checks to the required ones that can hold
// up an approval: the watch's non-blocking checks are left out.
func (e *Engine) requiredChecks(ctx context.Context, d PRDetail) ([]RequiredCheck, error) {
	req, err := e.d.GitHub.RequiredContexts(ctx, d.Key, d.BaseRef)
	if err != nil {
		return nil, err
	}
	skip := e.d.Watch.NonBlockingChecks
	nodes := slices.DeleteFunc(slices.Clone(d.Checks), func(n CheckNode) bool {
		return slices.Contains(skip, n.Name)
	})
	kept := make(RequiredContexts, len(req))
	for name, apps := range req {
		if !slices.Contains(skip, name) {
			kept[name] = apps
		}
	}
	return RequiredChecks(nodes, kept, e.d.Now()), nil
}
