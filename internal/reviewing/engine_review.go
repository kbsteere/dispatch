package reviewing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Review runs one claimed pass to completion: it checks out the head, runs
// a bounded claude session, and posts (or holds) the result. It blocks
// until the session ends.
func (e *Engine) Review(ctx context.Context, job Job) Outcome {
	out := Outcome{Key: job.Key}
	d := job.Detail
	if !ValidSHA(d.HeadOID) || !ValidSHA(d.BaseOID) {
		e.release(job.Key)
		out.Err = fmt.Errorf("invalid head or base for %s", job.Key)
		return out
	}

	repo, err := e.localRepo(ctx, job.Key)
	if err == nil {
		err = FetchPR(ctx, repo, job.Key, d.BaseRef, func() {
			e.setPrompt(fmt.Sprintf("Touch your security key to fetch %s", job.Key))
		})
		e.setPrompt("")
	}
	if errors.Is(err, ErrKeyTouch) {
		return e.holdForTouch(job.Key, out)
	}
	mergeBase := d.BaseOID
	if err == nil {
		mergeBase, err = MergeBase(ctx, repo, d.HeadOID, d.BaseOID)
	}
	if err != nil {
		return e.fail(job.Key, out, fmt.Errorf("preparing %s: %w", job.Key, err))
	}

	slot, releaseSlot, err := e.pool(job.Key, repo).Acquire(ctx, d.HeadOID)
	if errors.Is(err, ErrPoolBusy) {
		e.release(job.Key)
		out.Note = "no free worktree; retrying next poll"
		return out
	}
	if err != nil {
		return e.fail(job.Key, out, err)
	}
	defer releaseSlot()

	res, files, sessionID, err := e.runSession(ctx, job, repo, slot, mergeBase)
	if sessionID == "" {
		return e.fail(job.Key, out, err)
	}
	out.CostUSD = res.CostUSD
	e.recordRun(job.Key, sessionID, res, err)

	// A poll may have superseded this pass (new head, closed, draft) or
	// dispatch may be shutting down; either way the result is stale.
	st, _ := e.load(job.Key)
	if st.Claim == nil || st.Claim.SessionID != sessionID {
		out.Note = "superseded"
		return out
	}
	if err == nil {
		err = res.Err()
	}
	if err != nil {
		return e.fail(job.Key, out, err)
	}
	review, err := res.Review()
	if err != nil {
		return e.fail(job.Key, out, err)
	}
	gaps := reviewGaps(coverageInput{
		Pass:     job.Pass,
		PROnly:   e.prOnly(job.Key),
		Worktree: slot,
		Diff:     files.Diff,
		Changed:  changedFor(job, files),
		Reads:    res.Reads,
		Searches: res.Searches,
		Examined: review.Examined,
	})
	_, _ = e.update(job.Key, func(s *PRState) {
		s.Examined = &review.Examined
		s.ReviewGaps = gaps
	})
	return e.apply(ctx, job, st, review, gaps)
}

func (e *Engine) load(k PRKey) (PRState, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.store.LoadPR(k)
}

func (e *Engine) pool(k PRKey, repo LocalRepo) *Pool {
	e.mu.Lock()
	defer e.mu.Unlock()
	name := k.NameWithOwner()
	if p, ok := e.pools[name]; ok {
		return p
	}
	p := NewPool(
		repo,
		filepath.Join(e.d.StateRoot, "worktrees", fmt.Sprintf("%s__%s", k.Owner, k.Repo)),
		e.d.Limits.MaxInFlight,
	)
	e.pools[name] = p
	return p
}

// runSession builds the prompt and runs claude. It returns the session id
// once the process started, so spend can be recorded even on failure.
func (e *Engine) runSession(
	ctx context.Context,
	job Job,
	repo LocalRepo,
	slot, mergeBase string,
) (RunResult, ReviewFiles, string, error) {
	st, _ := e.load(job.Key)
	prOnly := e.prOnly(job.Key)
	var contextDirs []string
	if !prOnly {
		contextDirs = e.contextDirs()
	}
	viewer, _ := e.ensureViewer(ctx)
	in := PromptInput{
		Watch: e.d.Watch, Detail: job.Detail, Pass: job.Pass,
		Head: job.Detail.HeadOID, Base: job.Detail.BaseOID, MergeBase: mergeBase,
		Worktree: slot, ContextDirs: contextDirs, Ledger: st.Ledger, Viewer: viewer,
		PROnly:    prOnly,
		Standards: LoadStandards(ctx, repo, job.Detail.BaseOID, job.Detail.Files),
	}
	maxTurns := e.d.Limits.MaxTurns
	if job.Pass == PassLedger {
		maxTurns = e.d.Limits.MaxTurnsLedgerOnly
	}
	sessionID := NewSessionID()
	cwd := filepath.Join(e.d.StateRoot, "sessions", sessionID)
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		return RunResult{}, ReviewFiles{}, "", err
	}
	lastReviewed := st.CodeReviewedHead
	if job.Pass == PassFull {
		lastReviewed = ""
	}
	files, err := WriteReviewFiles(ctx, repo, cwd, mergeBase, job.Detail.HeadOID, lastReviewed)
	if err != nil {
		return RunResult{}, ReviewFiles{}, "", err
	}
	in.Files = files
	run, err := Start(ctx, RunSpec{
		EventsPath:   filepath.Join(cwd, "events.jsonl"),
		Binary:       e.d.Claude,
		SessionID:    sessionID,
		Prompt:       BuildPrompt(in),
		Schema:       ReviewSchema,
		CWD:          cwd,
		AddDirs:      append([]string{slot}, in.ContextDirs...),
		MaxTurns:     maxTurns,
		MaxBudgetUSD: e.d.Limits.MaxBudgetUsd,
		Timeout:      e.d.Limits.RunTimeout,
	})
	if err != nil {
		return RunResult{}, files, "", err
	}

	e.mu.Lock()
	closing := e.closing
	if !closing {
		e.inflight[job.Key] = run
	}
	e.mu.Unlock()
	if closing {
		run.Kill()
	}
	_, _ = e.update(job.Key, func(s *PRState) {
		if s.Claim != nil {
			s.Claim.SessionID = sessionID
			s.Claim.PID = run.PID()
		}
	})
	res, err := run.Wait()
	e.mu.Lock()
	if e.inflight[job.Key] == run {
		delete(e.inflight, job.Key)
	}
	e.mu.Unlock()
	return res, files, sessionID, err
}

// contextDirs are the configured local repos the agent may browse. Paths
// that cannot appear in a permission rule are skipped.
func (e *Engine) contextDirs() []string {
	var out []string
	for _, d := range e.d.ContextDirs {
		if safeDirRe.MatchString(d) && !strings.Contains(d, "..") {
			out = append(out, d)
		}
	}
	return out
}

func (e *Engine) recordRun(k PRKey, sessionID string, res RunResult, err error) {
	now := e.d.Now()
	if res.CostUSD > 0 {
		e.mu.Lock()
		_, _ = e.store.AddSpend(Day(now), res.CostUSD)
		e.mu.Unlock()
	}
	outcome := res.Subtype
	if err != nil {
		outcome = err.Error()
	}
	_, _ = e.update(k, func(st *PRState) {
		st.LastRun = &RunRecord{
			SessionID: sessionID,
			Turns:     res.Turns,
			CostUSD:   res.CostUSD,
			Outcome:   outcome,
			At:        now,
		}
		if st.ReviewsByDay == nil {
			st.ReviewsByDay = map[string]int{}
		}
		st.ReviewsByDay[Day(now)]++
	})
}

// touchNote tells the user how to recover from a missed security key touch.
const touchNote = "fetching over SSH needs your security key: press N, then touch the key when it blinks"

// holdForTouch records a pass that could not fetch because the security
// key was not touched. It waits for the user rather than backing off.
func (e *Engine) holdForTouch(k PRKey, out Outcome) Outcome {
	st, _ := e.update(k, func(st *PRState) {
		st.Claim = nil
		st.Status = StatusNeedsAttention
		st.Note = touchNote
	})
	e.mu.Lock()
	delete(e.claimed, k)
	e.mu.Unlock()
	out.Status, out.Note = st.Status, touchNote
	return out
}

// fail records a failed pass and backs the PR off.
func (e *Engine) fail(k PRKey, out Outcome, err error) Outcome {
	now := e.d.Now()
	st, _ := e.update(k, func(st *PRState) {
		st.Claim = nil
		st.Status = StatusFailed
		st.Note = err.Error()
		step := min(st.Backoff.Step, len(backoffSteps)-1)
		st.Backoff = Backoff{Until: now.Add(backoffSteps[step]), Step: st.Backoff.Step + 1}
	})
	e.mu.Lock()
	delete(e.claimed, k)
	e.mu.Unlock()
	out.Status = st.Status
	out.Err = err
	return out
}

// apply validates and lints the reviewer's output, then posts it, holds it,
// or writes it as a dry-run draft, and records the outcome.
func (e *Engine) apply(
	ctx context.Context,
	job Job,
	st PRState,
	review ReviewOutput,
	gaps []string,
) Outcome {
	out := Outcome{Key: job.Key, Note: review.Note}
	if len(gaps) > 0 {
		out.Note = strings.TrimSpace(
			"review incomplete: " + strings.Join(gaps, "; ") + ". " + review.Note,
		)
	}
	d := job.Detail
	files := map[string]bool{}
	for _, f := range d.Files {
		files[f] = true
	}
	ourThreads := map[string]bool{}
	for _, t := range st.OurThreads {
		ourThreads[t] = true
	}

	draft := Draft{Head: d.HeadOID}
	for _, f := range review.Findings {
		if !files[f.Path] || f.Line <= 0 {
			// A finding dispatch cannot anchor to a changed line is still a
			// finding; hold the pass for the user rather than dropping it,
			// which could turn a reported problem into an approval.
			draft.Unanchored = append(draft.Unanchored, f)
			draft.Reasons = append(
				draft.Reasons,
				fmt.Sprintf(
					"finding on %s:%d is not on a changed file dispatch knows about",
					f.Path,
					f.Line,
				),
			)
			continue
		}
		draft.Comments = append(
			draft.Comments,
			ReviewComment{
				Path: f.Path, Line: f.Line, StartLine: f.StartLine,
				Body: labelSeverity(f.Body, f.Blocking),
			},
		)
		draft.Findings = append(draft.Findings, Finding{
			ID: nextFindingID(append(st.Ledger, draft.Findings...)), Path: f.Path, Line: f.Line,
			Blocking: f.Blocking, Summary: summarize(f.Body), Status: FindingOutstanding,
		})
	}
	known := map[string]Finding{}
	for _, f := range st.Ledger {
		known[f.ID] = f
	}
	for _, u := range review.Ledger {
		f, ok := known[u.ID]
		if !ok ||
			!slices.Contains(
				[]string{FindingAddressed, FindingAnswered, FindingOutstanding},
				u.Status,
			) {
			continue
		}
		draft.Ledger = append(draft.Ledger, u)
		if !ourThreads[f.ThreadID] {
			continue
		}
		if u.Status == FindingAddressed || u.Status == FindingAnswered {
			draft.Resolve = append(draft.Resolve, f.ThreadID)
		} else if strings.TrimSpace(u.Reply) != "" && !f.Replied {
			draft.Replies = append(
				draft.Replies,
				DraftReply{ThreadID: f.ThreadID, FindingID: f.ID, Body: u.Reply},
			)
		}
	}
	threads := map[string]Thread{}
	for _, t := range d.Threads {
		threads[t.ID] = t
	}
	for _, id := range review.AgreedThreads {
		t, ok := threads[id]
		if !ok || ourThreads[id] ||
			slices.ContainsFunc(st.Ledger, func(f Finding) bool { return f.ThreadID == id }) ||
			len(t.Comments) == 0 {
			continue
		}
		draft.Findings = append(draft.Findings, Finding{
			ID: nextFindingID(
				append(st.Ledger, draft.Findings...),
			),
			ThreadID: id,
			Path:     t.Path,
			Line:     t.Line,
			Blocking: true,
			Summary:  summarize(t.Comments[0].Body),
			Status:   FindingOutstanding,
		})
	}

	for _, c := range draft.Comments {
		draft.Reasons = append(draft.Reasons, LintBody(c.Body)...)
	}
	for _, r := range draft.Replies {
		draft.Reasons = append(draft.Reasons, LintBody(r.Body)...)
	}

	viewer, _ := e.ensureViewer(ctx)
	fresh, _, err := e.d.GitHub.Details(ctx, []string{d.NodeID})
	if err != nil || len(fresh) == 0 {
		return e.fail(job.Key, out, fmt.Errorf("re-reading %s before posting: %v", job.Key, err))
	}
	if fresh[0].HeadOID != d.HeadOID || fresh[0].State != "OPEN" || fresh[0].IsDraft {
		e.release(job.Key)
		out.Note = "head moved or PR changed state during review; re-queued"
		return out
	}
	sig := e.signature(ctx, fresh[0], viewer)

	// A comments-only pass reads no code, so it relies on an earlier full or
	// incremental pass having read this head. A pass with gaps read too little
	// to count.
	codeReviewed := len(gaps) == 0 &&
		(job.Pass != PassLedger || st.CodeReviewedHead == fresh[0].HeadOID)
	tally, ledger := tallyFor(st.Ledger, draft)
	snap := Snapshot{
		State:                fresh[0].State,
		IsDraft:              fresh[0].IsDraft,
		Author:               fresh[0].Author,
		Viewer:               viewer,
		Mergeable:            fresh[0].Mergeable,
		Head:                 fresh[0].HeadOID,
		RequiredCI:           sig.CI,
		ApprovedHead:         st.ApprovedHead,
		OthersRequestChanges: othersRequestChanges(fresh[0].Reviews, viewer),
		CodeReviewed:         codeReviewed,
	}
	action, status := Decide(snap, tally)
	draft.Event = string(action)

	if len(draft.Reasons) > 0 {
		path := e.store.DraftPath(job.Key, e.d.Now())
		if err := writeDraft(path, draft); err != nil {
			return e.fail(job.Key, out, err)
		}
		return e.record(job.Key, out, StatusNeedsAttention, func(s *PRState) {
			s.HeldDraft = path
			s.ReviewedSig = &sig
			s.DesignRefs = review.DesignRefs
		})
	}

	if e.d.Watch.DryRun {
		if action != ActionNone || len(draft.Resolve)+len(draft.Replies) > 0 {
			if err := writeDraft(e.store.DraftPath(job.Key, e.d.Now()), draft); err != nil {
				return e.fail(job.Key, out, err)
			}
		}
		out.Posted = action
		return e.record(job.Key, out, status, func(s *PRState) {
			s.Ledger = ledger
			s.ReviewedSig = &sig
			s.DesignRefs = review.DesignRefs
			s.DryRun = true
			markCodeReviewed(s, job.Pass, d.HeadOID, gaps)
		})
	}

	ledger, ourNew, err := e.post(ctx, job.Key, draft, ledger)
	if err != nil {
		return e.fail(job.Key, out, err)
	}
	out.Posted = action
	return e.record(job.Key, out, status, func(s *PRState) {
		s.Ledger = ledger
		s.OurThreads = append(s.OurThreads, ourNew...)
		s.ReviewedSig = &sig
		s.DesignRefs = review.DesignRefs
		s.DryRun = false
		markCodeReviewed(s, job.Pass, d.HeadOID, gaps)
		switch action {
		case ActionApprove:
			s.ApprovedHead = d.HeadOID
		case ActionRequestChanges:
			s.ApprovedHead = ""
		}
	})
}

// tallyFor applies a draft's ledger updates and new findings to the ledger
// and counts what is outstanding.
func tallyFor(ledger []Finding, draft Draft) (Tally, []Finding) {
	next := slices.Clone(ledger)
	for _, u := range draft.Ledger {
		for i := range next {
			if next[i].ID == u.ID {
				next[i].Status = u.Status
			}
		}
	}
	var t Tally
	for _, f := range draft.Findings {
		if f.ThreadID == "" {
			t.NewFindings++
			if f.Blocking {
				t.NewBlocking++
			}
		}
	}
	next = append(next, draft.Findings...)
	t.OutstandingBlocking, t.OutstandingNonBlocking = PRState{Ledger: next}.OutstandingCounts()
	return t, next
}

func othersRequestChanges(reviews []Review, viewer string) bool {
	latest := map[string]Review{}
	for _, r := range reviews {
		if r.Author == viewer || r.SubmittedAt.IsZero() ||
			(r.State != "APPROVED" && r.State != "CHANGES_REQUESTED" && r.State != "DISMISSED") {
			continue
		}
		if cur, ok := latest[r.Author]; !ok || r.SubmittedAt.After(cur.SubmittedAt) {
			latest[r.Author] = r
		}
	}
	for _, r := range latest {
		if r.State == "CHANGES_REQUESTED" {
			return true
		}
	}
	return false
}

// post performs a draft's GitHub writes: resolves, one-time replies, then
// the review. It returns the ledger with thread ids filled in and the
// threads the review opened.
func (e *Engine) post(
	ctx context.Context,
	k PRKey,
	draft Draft,
	ledger []Finding,
) ([]Finding, []string, error) {
	for _, id := range draft.Resolve {
		if err := e.d.GitHub.ResolveThread(ctx, id); err != nil {
			return ledger, nil, err
		}
	}
	for _, r := range draft.Replies {
		if _, err := e.d.GitHub.ReplyThread(ctx, r.ThreadID, r.Body); err != nil {
			return ledger, nil, err
		}
		for i := range ledger {
			if ledger[i].ID == r.FindingID {
				ledger[i].Replied = true
			}
		}
	}
	if draft.Event == string(ActionNone) || draft.Event == "" {
		return ledger, nil, nil
	}
	// Non-blocking comments are posted with an approval.
	p := ReviewPayload{CommitID: draft.Head, Event: draft.Event, Comments: draft.Comments}
	url, err := e.d.GitHub.PostReview(ctx, k, p)
	if err != nil {
		return ledger, nil, err
	}
	if len(p.Comments) == 0 {
		return ledger, nil, nil
	}
	refs, err := e.d.GitHub.ReviewThreads(ctx, k, url)
	if err != nil {
		return ledger, nil, nil
	}
	var ours []string
	for _, ref := range refs {
		ours = append(ours, ref.ID)
		for i := range ledger {
			if ledger[i].ThreadID == "" && ledger[i].Path == ref.Path &&
				ledger[i].Line == ref.Line {
				ledger[i].ThreadID = ref.ID
				break
			}
		}
	}
	return ledger, ours, nil
}

func (e *Engine) record(k PRKey, out Outcome, status Status, fn func(*PRState)) Outcome {
	_, _ = e.update(k, func(st *PRState) {
		fn(st)
		st.Status = status
		st.Note = out.Note
		st.Claim = nil
		st.Backoff = Backoff{}
	})
	e.mu.Lock()
	delete(e.claimed, k)
	e.mu.Unlock()
	out.Status = status
	return out
}

// PostHeld posts a held draft, which the user may have edited on disk,
// after linting it again and checking the head has not moved.
func (e *Engine) PostHeld(ctx context.Context, k PRKey) error {
	st, err := e.load(k)
	if err != nil {
		return err
	}
	if st.HeldDraft == "" {
		return fmt.Errorf("%s has no held review", k)
	}
	draft, err := readDraft(st.HeldDraft)
	if err != nil {
		return err
	}
	var reasons []string
	for _, c := range draft.Comments {
		reasons = append(reasons, LintBody(c.Body)...)
	}
	for _, r := range draft.Replies {
		reasons = append(reasons, LintBody(r.Body)...)
	}
	if len(reasons) > 0 {
		return fmt.Errorf(
			"held review still fails the tone check (%s); edit %s",
			strings.Join(reasons, "; "),
			st.HeldDraft,
		)
	}
	fresh, _, err := e.d.GitHub.Details(ctx, []string{st.NodeID})
	if err != nil {
		return err
	}
	if len(fresh) == 0 || fresh[0].HeadOID != draft.Head {
		return fmt.Errorf("%s moved since the review was held; it will be reviewed again", k)
	}
	if draft.Event == string(ActionApprove) && len(draft.Unanchored) > 0 {
		// The reviewer reported problems that never made it into comments.
		draft.Event = string(ActionNone)
	}
	if draft.Event == string(ActionNone) && len(draft.Comments) > 0 {
		draft.Event = string(ActionComment)
	}
	_, ledger := tallyFor(st.Ledger, draft)
	ledger, ourNew, err := e.post(ctx, k, draft, ledger)
	if err != nil {
		return err
	}
	b, nb := PRState{Ledger: ledger}.OutstandingCounts()
	status := StatusWaitingOnAuthor
	if b > 0 {
		status = StatusChangesRequested
	} else if nb == 0 && draft.Event == string(ActionApprove) {
		status = StatusApproved
	}
	_, err = e.update(k, func(s *PRState) {
		s.Ledger = ledger
		s.OurThreads = append(s.OurThreads, ourNew...)
		s.HeldDraft = ""
		s.Status = status
		if draft.Event == string(ActionApprove) {
			s.ApprovedHead = draft.Head
		}
	})
	return err
}

// markCodeReviewed records head as code-reviewed when the pass read the
// code it was required to.
func markCodeReviewed(s *PRState, pass Pass, head string, gaps []string) {
	if pass != PassLedger && len(gaps) == 0 {
		s.CodeReviewedHead = head
	}
}

// labelSeverity marks a non-blocking finding as such so the author can tell
// it does not hold up the approval it is posted with.
func labelSeverity(body string, blocking bool) string {
	if blocking {
		return body
	}
	lower := strings.ToLower(strings.TrimSpace(body))
	if strings.HasPrefix(lower, "nit:") || strings.HasPrefix(lower, "non-blocking:") {
		return body
	}
	return "non-blocking: " + body
}

// prOnly reports whether k's repo is reviewed from the PR's contents alone.
func (e *Engine) prOnly(k PRKey) bool { return e.d.Watch.PROnly(k.NameWithOwner()) }

// changedFor is the files a pass must read: everything the PR changes, or on
// an incremental pass only what changed since the last review.
func changedFor(job Job, files ReviewFiles) []string {
	if job.Pass == PassIncremental && files.SinceLast != "" {
		return files.SinceLastFiles
	}
	return job.Detail.Files
}
