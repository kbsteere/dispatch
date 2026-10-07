package reviewing

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// Review runs one claimed pass to completion: it checks out the head, runs
// a bounded claude session, and posts (or holds) the result. It blocks
// until the session ends.
func (e *Engine) Review(ctx context.Context, job Job) Outcome {
	out := Outcome{Key: job.Key}
	started := e.d.Now()
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
		err = fmt.Errorf("preparing %s: %w", job.Key, err)
		if e.disturbedBySleep(started) {
			return e.requeue(job.Key, out, err)
		}
		return e.fail(job.Key, out, err)
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

	res, files, spec, err := e.runSession(ctx, job, repo, slot, mergeBase)
	sessionID := spec.SessionID
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
		// A session that hit a cap ended normally; only one with no result
		// may have been cut off.
		if e.disturbedBySleep(started) && res.Subtype == "" {
			return e.requeue(job.Key, out, err)
		}
		return e.fail(job.Key, out, err)
	}
	review, err := res.Review()
	if err != nil {
		return e.fail(job.Key, out, err)
	}
	cov := coverageInput{
		Pass:         job.Pass,
		PROnly:       e.prOnly(job.Key),
		Worktree:     slot,
		Diff:         files.Diff,
		Changed:      changedFor(job, files),
		Reads:        res.Reads,
		Ranges:       res.Ranges,
		Searches:     res.Searches,
		SinceLast:    files.SinceLast,
		SecurityRefs: files.SecurityRefs,
	}
	gaps := reviewGaps(cov)
	sessionCost := res.CostUSD
	for i := 0; len(gaps) > 0 && i < maxContinuations; i++ {
		more, next, ok := e.continueReview(
			ctx,
			job.Key,
			spec,
			gaps,
			files.SecurityRefs,
			sessionCost,
		)
		out.CostUSD += more.CostUSD
		sessionCost += more.CostUSD
		if !ok {
			break
		}
		review = next
		res.Reads = append(res.Reads, more.Reads...)
		slices.Sort(res.Reads)
		res.Reads = slices.Compact(res.Reads)
		if res.Ranges == nil {
			res.Ranges = map[string][]LineRange{}
		}
		for p, r := range more.Ranges {
			res.Ranges[p] = append(res.Ranges[p], r...)
		}
		res.Searches += more.Searches
		cov.Reads, cov.Ranges, cov.Searches = res.Reads, res.Ranges, res.Searches
		gaps = reviewGaps(cov)
	}
	if st, _ = e.load(job.Key); st.Claim == nil || st.Claim.SessionID != sessionID {
		out.Note = "superseded"
		return out
	}
	_, _ = e.update(job.Key, func(s *PRState) {
		verified := verifiedExamined(review.Examined, slot, res.Reads)
		s.Examined = &verified
		s.ReviewGaps = gaps
		s.Verdict, s.VerdictReason = review.Verdict, review.VerdictReason
		s.CIAssessment = review.CI
	})
	applied := e.apply(ctx, job, st, review, gaps, files.CIBase)
	applied.CostUSD = out.CostUSD
	return applied
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
) (RunResult, ReviewFiles, RunSpec, error) {
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
		return RunResult{}, ReviewFiles{}, RunSpec{}, err
	}
	lastReviewed := st.CodeReviewedHead
	if job.Pass == PassFull {
		lastReviewed = ""
	}
	files, err := WriteReviewFiles(ctx, repo, cwd, mergeBase, job.Detail.HeadOID, lastReviewed)
	if err != nil {
		return RunResult{}, ReviewFiles{}, RunSpec{}, err
	}
	in.CI, files.CIBase = e.loadCIProblems(ctx, job.Detail, cwd)
	if job.Pass != PassLedger && !prOnly {
		if g, ok := LoadSecurityGuide(ctx, repo, job.Detail.BaseOID, cwd); ok {
			in.Security = &g
			files.SecurityRefs = g.References
		}
	}
	in.Files = files
	spec := RunSpec{
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
	}
	run, err := Start(ctx, spec)
	if err != nil {
		return RunResult{}, files, RunSpec{}, err
	}
	res, err := e.track(job.Key, run, sessionID)
	return res, files, spec, err
}

// track makes a started session the PR's in-flight run, waits for it to end,
// and clears it. A session started while dispatch is closing is killed.
func (e *Engine) track(k PRKey, run *Run, sessionID string) (RunResult, error) {
	e.mu.Lock()
	closing := e.closing
	if !closing {
		e.inflight[k] = run
	}
	e.mu.Unlock()
	if closing {
		run.Kill()
	}
	_, _ = e.update(k, func(s *PRState) {
		if s.Claim != nil {
			s.Claim.SessionID = sessionID
			s.Claim.PID = run.PID()
		}
	})
	res, err := run.Wait()
	e.mu.Lock()
	if e.inflight[k] == run {
		delete(e.inflight, k)
	}
	e.mu.Unlock()
	return res, err
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
		st.TotalCostUSD, st.TotalTurns, st.Runs = st.Spend()
		st.TotalCostUSD += res.CostUSD
		st.TotalTurns += res.Turns
		st.Runs++
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
	out.Note = touchNote
	return e.finish(k, out, func(st *PRState) {
		st.Status = StatusNeedsAttention
		st.Note = touchNote
	})
}

// fail records a failed pass and backs the PR off.
func (e *Engine) fail(k PRKey, out Outcome, err error) Outcome {
	now := e.d.Now()
	out.Err = err
	return e.finish(k, out, func(st *PRState) {
		st.Status = StatusFailed
		st.Note = err.Error()
		step := min(st.Backoff.Step, len(backoffSteps)-1)
		st.Backoff = Backoff{Until: now.Add(backoffSteps[step]), Step: st.Backoff.Step + 1}
	})
}

// apply validates and lints the reviewer's output, then posts it, holds it,
// or writes it as a dry-run draft, and records the outcome.
func (e *Engine) apply(
	ctx context.Context,
	job Job,
	st PRState,
	review ReviewOutput,
	gaps []string,
	ciBase map[string]string,
) Outcome {
	// Findings the author never saw were offered to the reviewer to raise
	// again; whatever it re-raised is in review.Findings.
	st.Ledger = slices.DeleteFunc(slices.Clone(st.Ledger), Finding.Unposted)
	out := Outcome{Key: job.Key, Note: review.Note}
	if len(gaps) > 0 {
		out.Note = strings.TrimSpace(
			"review incomplete: " + strings.Join(gaps, "; ") + ". " + review.Note,
		)
	}
	d := job.Detail

	draft := Draft{
		Head: d.HeadOID, Verdict: review.Verdict, VerdictReason: review.VerdictReason,
		CI: review.CI, CIBase: ciBase,
	}
	for _, f := range review.Findings {
		if f.Category == CategorySecurity {
			f.Blocking = true
		}
		// A data-heavy repo's test suggestions never hold up a PR.
		if f.Category == CategoryTestGap && e.prOnly(job.Key) {
			f.Blocking = false
		}
		if !slices.Contains(d.Files, f.Path) || f.Line <= 0 {
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
			Category: f.Category,
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
		if !slices.Contains(st.OurThreads, f.ThreadID) {
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
		if !ok || slices.Contains(st.OurThreads, id) ||
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
			Blocking: !nonBlockingThread(t.Comments[0].Body),
			Summary:  summarize(t.Comments[0].Body),
			Status:   FindingOutstanding,
		})
	}

	draft.Reasons = append(draft.Reasons, lintReasons(draft)...)

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
	draft.CodeReviewed = codeReviewed
	dec := e.decide(ctx, fresh[0], st.ApprovedHead, viewer, sig, draft, tally)
	action, status := dec.action, dec.status
	draft.Event, draft.Body = string(action), dec.body

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
		recordPosted(s, action, d.HeadOID, ledger, ourNew)
		s.ReviewedSig = &sig
		s.DesignRefs = review.DesignRefs
		markCodeReviewed(s, job.Pass, d.HeadOID, gaps)
	})
}

// decision is the review event to post and the status it leaves.
type decision struct {
	snap   Snapshot
	action Action
	status Status
	// body explains an approval given with required checks set aside.
	body string
}

// decide picks the review event for draft from the PR as re-read just
// before posting. Live passes and posted drafts both use it, so they follow
// the same rules.
func (e *Engine) decide(
	ctx context.Context,
	fresh PRDetail,
	approvedHead, viewer string,
	sig Signature,
	draft Draft,
	tally Tally,
) decision {
	snap := Snapshot{
		State:                fresh.State,
		IsDraft:              fresh.IsDraft,
		Author:               fresh.Author,
		Viewer:               viewer,
		Mergeable:            fresh.Mergeable,
		Head:                 fresh.HeadOID,
		RequiredCI:           sig.CI,
		ApprovedHead:         approvedHead,
		OthersRequestChanges: othersRequestChanges(fresh.Reviews, viewer),
		// Findings that could not be anchored were never shown to the
		// author, so they rule out an approval.
		CodeReviewed:     draft.CodeReviewed && len(draft.Unanchored) == 0,
		ReviewerApproves: draft.Verdict == VerdictApprove,
	}
	var note string
	if sig.CI != "SUCCESS" {
		if checks, err := e.requiredChecks(ctx, fresh); err == nil {
			snap.CIExcused, note = ciExcuse(checks, draft.CI, draft.CIBase)
		}
	}
	action, status := Decide(snap, tally)
	dec := decision{snap: snap, action: action, status: status}
	if action == ActionApprove && snap.CIExcused {
		dec.body = note
	}
	return dec
}

// lintReasons are the tone check's objections to a draft's text.
func lintReasons(d Draft) []string {
	var reasons []string
	for _, c := range d.Comments {
		reasons = append(reasons, LintBody(c.Body)...)
	}
	for _, r := range d.Replies {
		reasons = append(reasons, LintBody(r.Body)...)
	}
	return reasons
}

// recordPosted records a review post sent at head.
func recordPosted(s *PRState, action Action, head string, ledger []Finding, ourNew []string) {
	s.Ledger = ledger
	s.OurThreads = append(s.OurThreads, ourNew...)
	s.DryRun = false
	switch action {
	case ActionApprove:
		s.ApprovedHead = head
	case ActionRequestChanges:
		s.ApprovedHead = ""
	}
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
	p := ReviewPayload{
		CommitID: draft.Head, Event: draft.Event, Body: draft.Body, Comments: draft.Comments,
	}
	url, err := e.d.GitHub.PostReview(ctx, k, p)
	if err != nil {
		return ledger, nil, err
	}
	if len(p.Comments) == 0 {
		return ledger, nil, nil
	}
	for i := range ledger {
		if slices.ContainsFunc(
			draft.Findings,
			func(f Finding) bool { return f.ID == ledger[i].ID },
		) {
			ledger[i].Posted = true
		}
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
	return e.finish(k, out, func(st *PRState) {
		fn(st)
		st.Status = status
		st.Note = out.Note
		st.Backoff = Backoff{}
	})
}

// finish ends k's claim: it applies mutate and clears the claim in the
// saved state, frees the in-memory slot, and reports the resulting status.
func (e *Engine) finish(k PRKey, out Outcome, mutate func(*PRState)) Outcome {
	st, _ := e.update(k, func(st *PRState) {
		mutate(st)
		st.Claim = nil
	})
	e.mu.Lock()
	delete(e.claimed, k)
	e.mu.Unlock()
	out.Status = st.Status
	return out
}

// PostDraft posts the review shown for k: a held review, which the user may
// have edited on disk, or the latest dry-run draft. It re-reads the PR,
// lints the text again, and decides the review event afresh, so an
// approval goes out only if the reviewer said approve and every approval
// check still passes. It returns what was posted and, when a drafted
// approval was not posted, why.
func (e *Engine) PostDraft(ctx context.Context, k PRKey) (Action, string, error) {
	st, err := e.load(k)
	if err != nil {
		return ActionNone, "", err
	}
	held := st.HeldDraft != ""
	draft, ok := e.DraftFor(st)
	if !ok {
		return ActionNone, "", fmt.Errorf("%s has no review to post", k)
	}
	if reasons := lintReasons(draft); len(reasons) > 0 {
		return ActionNone, "", fmt.Errorf(
			"the review still fails the tone check (%s); edit %s",
			strings.Join(reasons, "; "),
			cmp.Or(st.HeldDraft, "the draft"),
		)
	}
	viewer, _ := e.ensureViewer(ctx)
	fresh, _, err := e.d.GitHub.Details(ctx, []string{st.NodeID})
	if err != nil {
		return ActionNone, "", err
	}
	if len(fresh) == 0 || fresh[0].HeadOID != draft.Head || fresh[0].State != "OPEN" ||
		fresh[0].IsDraft {
		return ActionNone, "", fmt.Errorf(
			"%s changed since the review was written; it will be reviewed again",
			k,
		)
	}
	sig := e.signature(ctx, fresh[0], viewer)

	ledger := st.Ledger
	var tally Tally
	if held {
		// A held pass never recorded its findings.
		tally, ledger = tallyFor(st.Ledger, draft)
	} else {
		tally = draftTally(st.Ledger, draft)
	}
	// Decided afresh: CI and other reviews may have changed since the draft.
	dec := e.decide(ctx, fresh[0], st.ApprovedHead, viewer, sig, draft, tally)
	action := dec.action
	if action == ActionNone && len(draft.Comments) > 0 {
		action = ActionComment
	}
	var notApproved string
	if action != ActionApprove {
		notApproved = Blocker(dec.snap, tally)
	}
	if action == ActionNone && len(draft.Replies)+len(draft.Resolve) == 0 {
		return ActionNone, notApproved, fmt.Errorf("%s has nothing to post", k)
	}
	draft.Event, draft.Body = string(action), dec.body
	ledger, ourNew, err := e.post(ctx, k, draft, ledger)
	if err != nil {
		return ActionNone, "", err
	}
	_, err = e.update(k, func(s *PRState) {
		recordPosted(s, action, draft.Head, ledger, ourNew)
		s.HeldDraft = ""
		s.Status = dec.status
	})
	return action, notApproved, err
}

// draftTally counts a dry-run draft against a ledger that already holds
// its findings.
func draftTally(ledger []Finding, draft Draft) Tally {
	b, nb := PRState{Ledger: ledger}.OutstandingCounts()
	t := Tally{NewFindings: len(draft.Findings), OutstandingBlocking: b, OutstandingNonBlocking: nb}
	for _, f := range draft.Findings {
		if f.Blocking {
			t.NewBlocking++
		}
	}
	return t
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

// nonBlockingRe matches a review thread that opens by calling itself a nit
// or a suggestion, as "**Suggestion (non-blocking).**" does.
var nonBlockingRe = regexp.MustCompile(`(?i)^\W*(nit|non-blocking|suggestion|optional|minor)\b`)

// nonBlockingThread reports whether another reviewer's thread, which dispatch
// agreed with, said it should not block.
func nonBlockingThread(body string) bool { return nonBlockingRe.MatchString(body) }

// maxContinuations is how many times an incomplete pass's session is resumed
// to finish what it skipped before its gaps stand.
const maxContinuations = 2

// continueReview resumes an incomplete pass's session, asking it to finish
// what it skipped, and returns what the continuation read and its updated
// review. It reports false when the continuation failed; the incomplete
// review then stands. A resumed session reports the session's total cost,
// so the result's CostUSD is what it added to sessionCost, the session's
// cost so far.
func (e *Engine) continueReview(
	ctx context.Context,
	k PRKey,
	spec RunSpec,
	gaps, securityRefs []string,
	sessionCost float64,
) (RunResult, ReviewOutput, bool) {
	spec.Resume = true
	spec.Prompt = ContinuationPrompt(gaps, securityRefs)
	run, err := Start(ctx, spec)
	if err != nil {
		return RunResult{}, ReviewOutput{}, false
	}
	res, err := e.track(k, run, spec.SessionID)
	res.CostUSD = max(res.CostUSD-sessionCost, 0)
	e.recordContinuation(k, res)
	if err == nil {
		err = res.Err()
	}
	if err != nil {
		return res, ReviewOutput{}, false
	}
	review, err := res.Review()
	if err != nil {
		return res, ReviewOutput{}, false
	}
	return res, review, true
}

// recordContinuation adds a continuation's spend to the pass it continued.
func (e *Engine) recordContinuation(k PRKey, res RunResult) {
	if res.CostUSD > 0 {
		e.mu.Lock()
		_, _ = e.store.AddSpend(Day(e.d.Now()), res.CostUSD)
		e.mu.Unlock()
	}
	_, _ = e.update(k, func(st *PRState) {
		st.TotalCostUSD, st.TotalTurns, st.Runs = st.Spend()
		st.TotalCostUSD += res.CostUSD
		st.TotalTurns += res.Turns
		if st.LastRun != nil {
			st.LastRun.CostUSD += res.CostUSD
			st.LastRun.Turns += res.Turns
			st.LastRun.Continuations++
		}
	})
}
