package reviewing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/kbsteere/dispatch/v4/internal/config"
)

const (
	claimTTL      = 45 * time.Minute
	sessionMaxAge = 14 * 24 * time.Hour
	searchPages   = 3
)

var backoffSteps = []time.Duration{30 * time.Minute, 2 * time.Hour, 24 * time.Hour}

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
	Name       string
	InFlight   int
	SpentToday float64
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
	claimed  map[PRKey]bool
	pools    map[string]*Pool
	paused   bool
	pauseWhy string
	lastErr  string
	lastPoll time.Time
	prompt   string // an action the user must take now, such as touching a security key
	closing  bool
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
		claimed:  map[PRKey]bool{},
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
	_, _ = e.update(k, func(st *PRState) { st.Claim = nil })
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
	for _, st := range states {
		if st.Claim == nil {
			continue
		}
		KillOrphan(st.Claim.PID)
		_, _ = e.update(st.Key, func(s *PRState) {
			s.Claim = nil
			if s.Status == StatusReviewing {
				s.Status = StatusFailed
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
	return WatchStatus{
		Name:           e.d.Watch.Name,
		InFlight:       len(e.claimed),
		SpentToday:     e.store.Spend(Day(e.d.Now())),
		Mode:           mode,
		Paused:         paused,
		Err:            e.lastErr,
		QuotaRemaining: e.quota.Remaining,
		DailyBudget:    e.d.Limits.DailyBudgetUsd,
		LastPoll:       e.lastPoll,
		Prompt:         e.prompt,
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
	ignore := map[string]bool{viewer: true}
	for _, l := range e.d.Watch.IgnoreLogins {
		ignore[l] = true
	}
	events, resolved := d.Events()
	ci := ciUnknown
	if req, err := e.d.GitHub.RequiredContexts(ctx, d.Key, d.BaseRef); err == nil {
		ci = RequiredState(d.Checks, req)
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
	case st.HeadSeen != nil && (st.ReviewedSig == nil || st.HeadSeen.OID != st.ReviewedSig.Head):
		return true
	}
	return false
}

// Poll runs one polling cycle and returns the passes it claimed and how
// long to wait before the next poll.
func (e *Engine) Poll(ctx context.Context) ([]Job, time.Duration, error) {
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
	inResults := map[PRKey]bool{}
	var ids []string
	for _, h := range hits {
		inResults[h.Key] = true
		if needsDetail(byKey[h.Key], h.UpdatedAt, now) {
			ids = append(ids, h.NodeID)
		}
	}
	for _, st := range states {
		if inResults[st.Key] {
			continue
		}
		switch {
		case st.Pinned && st.NodeID != "":
			ids = append(ids, st.NodeID)
		case st.Claim != nil || !st.Inactive || st.ForceReview:
			// Merged, closed, back to draft, or out of scope: stop
			// reviewing it and hide it until the search returns it.
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

	initialized := e.store.Initialized()
	var jobs []Job
	for _, d := range details {
		if job, ok := e.evaluate(ctx, d, viewer, now, initialized); ok {
			jobs = append(jobs, job)
		}
	}
	if !initialized {
		if err := e.store.MarkInitialized(); err != nil {
			e.setErr(err)
		}
	}
	e.setErr(nil)

	e.mu.Lock()
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
	st.Title = d.Title
	st.Author = d.Author
	st.CI = sig.CI

	switch {
	case d.State != "OPEN":
		st.Pinned = false
		kill = st.Claim != nil
		st.Claim = nil
	case d.IsDraft:
		kill = st.Claim != nil
		st.Claim = nil
	default:
		ObserveHead(&st, d.HeadOID, now)
		ObserveCI(&st, sig.CI)
		if claimLive(st, now) && st.Claim.Head != d.HeadOID {
			kill = true
			st.Claim = nil
		}
		job, claimed = e.claimLocked(&st, d, sig, now, initialized, lim)
	}
	_ = e.store.SavePR(st)
	if claimed {
		e.claimed[d.Key] = true
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
	st.Claim = nil
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
	pass := PassLedger
	switch {
	case st.ReviewedSig == nil:
		pass = PassFull
	case st.ReviewedSig.Head != d.HeadOID:
		pass = PassIncremental
	}
	st.Claim = &Claim{Since: now, Head: d.HeadOID}
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
// or in dry run the latest draft written for the head it last reviewed.
func (e *Engine) DraftFor(st PRState) (Draft, bool) {
	if st.HeldDraft != "" {
		drafts := filepath.Join(string(e.store), "drafts") + string(filepath.Separator)
		if !strings.HasPrefix(filepath.Clean(st.HeldDraft), drafts) {
			return Draft{}, false
		}
		d, err := readDraft(st.HeldDraft)
		return d, err == nil
	}
	if !e.d.Watch.DryRun || st.ReviewedSig == nil {
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
