package reviewing

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
)

// reviewHarness wires an engine to a real git origin carrying
// refs/pull/7/head, a fake GitHub, and the fake claude script.
type reviewHarness struct {
	e    *Engine
	gh   *fakeGitHub
	head string
	job  Job
	now  time.Time
}

func newReviewHarness(t *testing.T, mode string, watch func(*config.ReviewWatch)) *reviewHarness {
	t.Helper()
	t.Setenv("FAKE_CLAUDE_MODE", mode)
	origin, head := prOrigin(t)
	useRemotes(t, origin, origin)
	touchKeyAvailable(t, false)
	clone := filepath.Join(t.TempDir(), "clone")
	gitT(t, filepath.Dir(clone), "clone", "-q", origin, clone)
	base := strings.TrimSpace(gitT(t, origin, "rev-parse", "main"))

	k := PRKey{"chainguard-dev", "mono", 7}
	d := detail(7, head)
	d.Key, d.NodeID, d.Files, d.BaseOID = k, "N7", []string{"kev.go"}, base
	gh := &fakeGitHub{
		viewer: "kbsteere", details: map[string]PRDetail{"N7": d},
		threadRefs: []ThreadRef{{ID: "PRRT_new", Path: "kev.go", Line: 1}},
	}

	w := config.ReviewWatch{
		Name:    "sustaining-automation",
		Authors: []string{"dnegreira"},
		Scopes:  []string{"repo:chainguard-dev/mono"},
	}
	if watch != nil {
		watch(&w)
	}
	h := &reviewHarness{gh: gh, head: head, now: t0}
	e, err := NewEngine(Deps{
		Watch:      w,
		Limits:     config.DefaultReviewingLimits(),
		StateRoot:  t.TempDir(),
		GitHub:     gh,
		Claude:     fakeClaudeBin(t),
		LookupPath: func(name string) (string, bool) { return clone, name == "chainguard-dev/mono" },
		Now:        func() time.Time { return h.now },
	})
	require.NoError(t, err)
	t.Cleanup(e.Shutdown)
	require.NoError(t, e.store.MarkInitialized())

	_, err = e.update(k, func(st *PRState) {
		st.NodeID = "N7"
		st.Claim = &Claim{Since: t0, Head: head}
	})
	require.NoError(t, err)
	e.claimed[k] = struct{}{}

	h.e = e
	h.job = Job{Key: k, Trigger: TriggerChange, Pass: PassFull, Detail: d}
	return h
}

func (h *reviewHarness) state(t *testing.T) PRState { return stateOf(t, h.e, h.job.Key) }

func TestReviewCleanApprovesWithEmptyBody(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted)

	require.Len(t, h.gh.posted, 1)
	assert.Equal(
		t,
		ReviewPayload{CommitID: h.head, Event: "APPROVE"},
		h.gh.posted[0],
		"no body, no comments",
	)
	st := h.state(t)
	assert.Equal(t, StatusApproved, st.Status)
	assert.Equal(t, h.head, st.ApprovedHead)
	assert.Nil(t, st.Claim)
	assert.Equal(t, 0, h.e.Status().InFlight)
	require.NotNil(t, st.LastRun)
	assert.Equal(t, 7, st.LastRun.Turns)
	assert.Equal(t, 1.25, h.e.store.Spend(Day(h.now)))
	assert.Equal(t, 1, st.ReviewsByDay[Day(h.now)])
}

func TestReviewFindingPostsCommentInline(t *testing.T) {
	h := newReviewHarness(t, "finding", nil)
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)

	require.Len(t, h.gh.posted, 1)
	p := h.gh.posted[0]
	assert.Equal(t, "COMMENT", p.Event)
	assert.Equal(t, h.head, p.CommitID)
	assert.Equal(t, "", p.Body, "no summary body")
	require.Len(t, p.Comments, 1)
	assert.Equal(t, "kev.go", p.Comments[0].Path)
	assert.Equal(t, 1, p.Comments[0].Line)

	st := h.state(t)
	assert.Equal(t, StatusChangesRequested, st.Status)
	assert.Equal(t, []string{"PRRT_new"}, st.OurThreads)
	require.Len(t, st.Ledger, 1)
	assert.Equal(t, "PRRT_new", st.Ledger[0].ThreadID)
	assert.True(t, st.Ledger[0].Blocking)
	assert.Nil(t, st.Claim)
	assert.Equal(t, h.head, st.ReviewedSig.Head)
	assert.Equal(t, 2.0, h.e.store.Spend(Day(h.now)))
}

func TestReviewBotspeakIsHeld(t *testing.T) {
	h := newReviewHarness(t, "botspeak", nil)
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)

	assert.Empty(t, h.gh.posted, "bot narration is never posted")
	st := h.state(t)
	assert.Equal(t, StatusNeedsAttention, st.Status)
	require.NotEmpty(t, st.HeldDraft)
	b, err := os.ReadFile(st.HeldDraft)
	require.NoError(t, err)
	assert.Contains(t, string(b), "missing doc comment")
	assert.NotNil(t, st.ReviewedSig, "a held pass does not re-trigger")

	// The user edits the held review on disk, then sends it.
	draft, err := readDraft(st.HeldDraft)
	require.NoError(t, err)
	draft.Comments[0].Body = "`package kev` has no doc comment; the repo CLAUDE.md requires one for every package."
	require.NoError(t, writeDraft(st.HeldDraft, draft))
	_, _, err = h.e.PostDraft(t.Context(), h.job.Key)
	require.NoError(t, err)
	require.Len(t, h.gh.posted, 1)
	assert.Equal(t, "COMMENT", h.gh.posted[0].Event)
	st = h.state(t)
	assert.Empty(t, st.HeldDraft)
	assert.Equal(t, StatusChangesRequested, st.Status)
	assert.Equal(t, []string{"PRRT_new"}, st.OurThreads)
}

func TestPostDraftRefusesUneditedBotspeak(t *testing.T) {
	h := newReviewHarness(t, "botspeak", nil)
	h.e.Review(context.Background(), h.job)
	_, _, err := h.e.PostDraft(t.Context(), h.job.Key)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "tone check")
	assert.Empty(t, h.gh.posted)
}

func TestReviewResolvesOwnThreadSilently(t *testing.T) {
	h := newReviewHarness(t, "resolve", nil)
	_, _ = h.e.update(h.job.Key, func(st *PRState) {
		st.OurThreads = []string{"PRRT_1"}
		st.Ledger = []Finding{
			{
				ID:       "f1",
				ThreadID: "PRRT_1",
				Path:     "kev.go",
				Line:     1,
				Blocking: true,
				Summary:  "doc comment",
				Status:   FindingOutstanding,
			},
		}
		sig := Signature{Head: strings.Repeat("9", 40)}
		st.ReviewedSig = &sig
	})
	h.job.Pass = PassIncremental
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)

	assert.Equal(t, []string{"PRRT_1"}, h.gh.resolved)
	assert.Empty(t, h.gh.replies, "fixed threads are resolved without a reply")
	require.Len(t, h.gh.posted, 1)
	assert.Equal(t, "APPROVE", h.gh.posted[0].Event)
	assert.Equal(t, FindingAddressed, h.state(t).Ledger[0].Status)
}

func TestReviewNeverResolvesOthersThread(t *testing.T) {
	h := newReviewHarness(t, "resolve", nil)
	_, _ = h.e.update(h.job.Key, func(st *PRState) {
		st.Ledger = []Finding{
			{
				ID:       "f1",
				ThreadID: "PRRT_powersj",
				Path:     "kev.go",
				Line:     1,
				Blocking: true,
				Status:   FindingOutstanding,
			},
		}
	})
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.resolved)
	assert.Equal(
		t,
		FindingAddressed,
		h.state(t).Ledger[0].Status,
		"the agreed finding no longer holds the approval",
	)
}

func TestReviewRepliesOnceToPushback(t *testing.T) {
	h := newReviewHarness(t, "pushback", nil)
	_, _ = h.e.update(h.job.Key, func(st *PRState) {
		st.OurThreads = []string{"PRRT_1"}
		st.Ledger = []Finding{
			{
				ID:       "f1",
				ThreadID: "PRRT_1",
				Path:     "kev.go",
				Line:     1,
				Blocking: true,
				Status:   FindingOutstanding,
			},
		}
	})
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(
		t,
		map[string]string{
			"PRRT_1": "`run` still ignores `ctx.Done()`, so cancelling the watch leaks the goroutine.",
		},
		h.gh.replies,
	)
	assert.Empty(t, h.gh.posted, "outstanding blocking item: nothing new to post")
	assert.Equal(t, StatusChangesRequested, h.state(t).Status)
	assert.True(t, h.state(t).Ledger[0].Replied)

	h.gh.replies = nil
	_, _ = h.e.update(
		h.job.Key,
		func(st *PRState) { st.Claim = &Claim{Since: h.now, Head: h.head} },
	)
	h.e.Review(context.Background(), h.job)
	assert.Empty(t, h.gh.replies, "never replies twice")
}

func TestReviewWithdrawsOwnApproval(t *testing.T) {
	h := newReviewHarness(t, "finding", nil)
	_, _ = h.e.update(h.job.Key, func(st *PRState) { st.ApprovedHead = strings.Repeat("9", 40) })
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	require.Len(t, h.gh.posted, 1)
	assert.Equal(t, "REQUEST_CHANGES", h.gh.posted[0].Event)
	assert.Equal(t, "", h.gh.posted[0].Body)
	assert.Empty(t, h.state(t).ApprovedHead)
}

func TestReviewMaxTurnsBacksOffAndRecordsSpend(t *testing.T) {
	h := newReviewHarness(t, "max_turns", nil)
	out := h.e.Review(context.Background(), h.job)
	require.Error(t, out.Err)
	assert.Empty(t, h.gh.posted)
	st := h.state(t)
	assert.Equal(t, StatusFailed, st.Status)
	assert.Equal(t, h.now.Add(30*time.Minute), st.Backoff.Until)
	assert.Equal(t, 3.5, h.e.store.Spend(Day(h.now)))
	assert.Nil(t, st.ReviewedSig, "the trigger persists for the retry")
	assert.Nil(t, st.Claim)
}

func TestReviewRequiredChecksLookupFailureNeverApproves(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	h.gh.requiredErr = errors.New("HTTP 502: Bad Gateway")
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.posted)
	assert.Equal(t, StatusWaitingOnCI, h.state(t).Status)
}

func TestReviewHoldsFindingsOutsideTheDiff(t *testing.T) {
	// A finding on a file dispatch did not fetch (or with no line) must
	// never be dropped on the way to an approval.
	h := newReviewHarness(t, "badpath", nil)
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.posted, "nothing is posted, approval least of all")
	st := h.state(t)
	assert.Equal(t, StatusNeedsAttention, st.Status)
	draft, err := readDraft(st.HeldDraft)
	require.NoError(t, err)
	require.Len(t, draft.Unanchored, 1)
	assert.Equal(t, "not-in-pr.go", draft.Unanchored[0].Path)
	assert.Contains(t, strings.Join(draft.Reasons, "\n"), "not-in-pr.go:3")

	// Sending the held review as is never approves; with no comment to
	// send it posts nothing.
	_, why, err := h.e.PostDraft(t.Context(), h.job.Key)
	require.ErrorContains(t, err, "nothing to post")
	assert.Equal(t, "the review did not read everything it had to at this commit", why)
	assert.Empty(t, h.gh.posted)
}

func TestReviewHeadMovedDiscards(t *testing.T) {
	h := newReviewHarness(t, "finding", nil)
	h.gh.headAfterRun = strings.Repeat("e", 40)
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.posted)
	st := h.state(t)
	assert.Nil(t, st.Claim)
	assert.Nil(t, st.ReviewedSig, "the new head is reviewed on a later poll")
}

func TestReviewDryRunWritesDraftOnly(t *testing.T) {
	h := newReviewHarness(t, "finding", func(w *config.ReviewWatch) { w.DryRun = true })
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.posted)
	drafts, _ := filepath.Glob(filepath.Join(string(h.e.store), "drafts", "*.json"))
	require.Len(t, drafts, 1)
	b, _ := os.ReadFile(drafts[0])
	assert.Contains(t, string(b), `"event": "COMMENT"`)
	assert.Equal(t, StatusChangesRequested, h.state(t).Status)
}

func TestReviewOwnPRNeverApproves(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	d := h.gh.details["N7"]
	d.Author = "kbsteere"
	h.gh.details["N7"] = d
	h.job.Detail.Author = "kbsteere"
	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.posted)
	assert.Equal(t, StatusWaitingOnAuthor, h.state(t).Status)
}

func TestReviewSupersededResultIsDropped(t *testing.T) {
	h := newReviewHarness(t, "finding", nil)
	// A poll released the claim (new push) while claude was running.
	h.e.d.Claude = filepath.Join(t.TempDir(), "claude")
	require.NoError(
		t,
		os.WriteFile(h.e.d.Claude, []byte("#!/usr/bin/env bash\ncat >/dev/null\nsleep 1\n"+
			"printf '[{\"type\":\"result\",\"subtype\":\"success\",\"num_turns\":1,\"total_cost_usd\":0.5,\"session_id\":\"x\",\"structured_output\":{\"findings\":[],\"ledger\":[],\"agreed_threads\":[],\"design_refs\":\"\",\"note\":\"\"}}]'\n"), 0o755),
	)
	released := make(chan struct{})
	go func() {
		defer close(released)
		for range 500 {
			if st, err := h.e.load(
				h.job.Key,
			); err == nil && st.Claim != nil &&
				st.Claim.SessionID != "" {
				_, _ = h.e.update(h.job.Key, func(s *PRState) { s.Claim = nil })
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	out := h.e.Review(context.Background(), h.job)
	<-released
	assert.Equal(t, "superseded", out.Note)
	assert.Empty(t, h.gh.posted)
	assert.Equal(
		t,
		0.5,
		h.e.store.Spend(Day(h.now)),
		"spend is recorded even when the result is dropped",
	)
	_, _, runs := h.state(t).Spend()
	assert.Equal(t, 1, runs, "a superseded run counts toward the PR's total")
}

func TestReviewMissedKeyTouchNeedsAttentionWithoutBackoff(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	out := h.e.holdForTouch(h.job.Key, Outcome{Key: h.job.Key})
	assert.Equal(t, StatusNeedsAttention, out.Status)
	st := h.state(t)
	assert.Equal(t, StatusNeedsAttention, st.Status)
	assert.Contains(t, st.Note, "touch the key")
	assert.True(t, st.Backoff.Until.IsZero(), "waits for the user, not a timer")
	assert.Nil(t, st.Claim)
	assert.Equal(t, 0, h.e.Status().InFlight)
}

func TestReviewSSHFallbackClearsPrompt(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	origin, _ := remoteURLs(h.job.Key)
	useRemotes(t, filepath.Join(t.TempDir(), "https-unreachable"), origin)
	touchKeyAvailable(t, true)

	out := h.e.Review(context.Background(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted, "the SSH fetch worked and the pass completed")
	assert.Empty(t, h.e.Status().Prompt, "the touch prompt is cleared once the fetch ends")
}

func TestReviewNonBlockingFindingApprovesWithLabelledComment(t *testing.T) {
	h := newReviewHarness(t, "nit", nil)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted)

	require.Len(t, h.gh.posted, 1)
	p := h.gh.posted[0]
	assert.Equal(t, "APPROVE", p.Event)
	require.Len(t, p.Comments, 1, "the non-blocking comment is posted with the approval")
	assert.Equal(
		t,
		"non-blocking: `Sync` could return early when the feed is empty, which skips one allocation.",
		p.Comments[0].Body,
	)

	st := h.state(t)
	assert.Equal(t, StatusApproved, st.Status)
	assert.Equal(t, h.head, st.ApprovedHead)
	assert.Equal(t, h.head, st.CodeReviewedHead)
	assert.False(t, st.DryRun)
	require.Len(t, st.Ledger, 1)
	assert.False(t, st.Ledger[0].Blocking)
}

func TestReviewCommentsOnlyPassNeverApprovesUnreadCode(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	h.job.Pass = PassLedger
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	assert.Empty(t, h.gh.posted, "nothing read the code at this head")
	st := h.state(t)
	assert.Equal(t, StatusNeedsAttention, st.Status)
	assert.Empty(t, st.CodeReviewedHead, "a comments-only pass never marks code as read")
	assert.Empty(t, st.ApprovedHead)
}

func TestReviewCommentsOnlyPassApprovesCodeReadAtHead(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	_, err := h.e.update(h.job.Key, func(s *PRState) { s.CodeReviewedHead = h.head })
	require.NoError(t, err)
	h.job.Pass = PassLedger
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted)
}

func TestReviewDryRunMarksStatusAsUnposted(t *testing.T) {
	h := newReviewHarness(t, "success", func(w *config.ReviewWatch) { w.DryRun = true })
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.posted)
	st := h.state(t)
	assert.Equal(t, StatusApproved, st.Status)
	assert.True(t, st.DryRun)
	assert.Equal(t, h.head, st.CodeReviewedHead)
	assert.Empty(t, st.ApprovedHead, "nothing was posted")
}

func TestLabelSeverity(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		blocking bool
		want     string
	}{
		{
			"blocking is unlabelled",
			"`run` leaks the goroutine.",
			true,
			"`run` leaks the goroutine.",
		},
		{"non-blocking gets a label", "Rename `x`.", false, "non-blocking: Rename `x`."},
		{"nit label is kept", "nit: rename `x`.", false, "nit: rename `x`."},
		{"existing label is kept", "Non-blocking: rename `x`.", false, "Non-blocking: rename `x`."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, labelSeverity(tt.body, tt.blocking))
		})
	}
}

func TestReviewShallowPassCannotApprove(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	t.Setenv("FAKE_CLAUDE_READS", "kev.go") // no docs, no tests
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	assert.Empty(t, h.gh.posted)
	st := h.state(t)
	assert.Equal(t, StatusNeedsAttention, st.Status)
	assert.Empty(t, st.CodeReviewedHead, "an incomplete pass never counts as reading the code")
	assert.Equal(t, []string{
		"read no documentation or design docs",
		"read no existing tests for the changed code",
	}, st.ReviewGaps)
	assert.True(t, strings.HasPrefix(st.Note, "review incomplete: "), st.Note)
	assert.Equal(t, maxContinuations, st.LastRun.Continuations,
		"the session was asked to finish before the gaps stood")
}

func TestReviewShallowPassStillPostsFindings(t *testing.T) {
	h := newReviewHarness(t, "finding", nil)
	t.Setenv("FAKE_CLAUDE_READS", "")
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	require.Len(t, h.gh.posted, 1, "real problems are posted even from an incomplete review")
	assert.Equal(t, "COMMENT", h.gh.posted[0].Event)
	assert.NotEmpty(t, h.state(t).ReviewGaps)
}

func TestReviewDropsClaimedReadsThatNeverHappened(t *testing.T) {
	h := newReviewHarness(t, "phantom", nil)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted, "the reads it needed did happen")
	st := h.state(t)
	assert.Empty(t, st.ReviewGaps)
	require.NotNil(t, st.Examined)
	assert.NotContains(
		t,
		st.Examined.Docs,
		"docs/design.md",
		"a read that never happened is not shown",
	)
	assert.Equal(t, []string{"kev.go"}, st.Examined.Code)
}

func TestReviewCompletePassRecordsWhatItRead(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted)

	st := h.state(t)
	assert.Empty(t, st.ReviewGaps)
	require.NotNil(t, st.Examined)
	assert.Equal(t, []string{"kev_test.go"}, st.Examined.Tests)

	events := filepath.Join(h.e.d.StateRoot, "sessions", st.LastRun.SessionID, "events.jsonl")
	b, err := os.ReadFile(events)
	require.NoError(t, err, "the session's event stream is kept")
	assert.Contains(t, string(b), `"name":"Read"`)
}

func TestReviewPROnlyRepoNeedsOnlyTheDiff(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("FAKE_CLAUDE_STDIN", stdin)
	h := newReviewHarness(t, "resolve", func(w *config.ReviewWatch) {
		w.PROnlyRepos = []string{"chainguard-dev/mono"}
	})
	h.e.d.ContextDirs = []string{"/home/u/Dev/cg/mono/main"}
	t.Setenv("FAKE_CLAUDE_READS", "")
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	assert.Equal(t, ActionApprove, out.Posted, "reading the diff is enough for a pr-only repo")
	assert.Empty(t, h.state(t).ReviewGaps)
	prompt, _ := os.ReadFile(stdin)
	assert.Contains(t, string(prompt), "## Scope: this pull request only")
	assert.NotContains(t, string(prompt), "context repos:", "pr-only reviews get no other repos")
}

func TestReviewNeverApprovesWithoutReviewerVerdict(t *testing.T) {
	h := newReviewHarness(t, "hesitant", nil)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	require.Len(t, h.gh.posted, 1)
	assert.Equal(t, "COMMENT", h.gh.posted[0].Event, "only nits, but Claude did not say approve")
	st := h.state(t)
	assert.Equal(t, StatusWaitingOnAuthor, st.Status)
	assert.Equal(t, "comment", st.Verdict)
	assert.Equal(t, "needs the author to confirm the retry budget", st.VerdictReason)
	assert.Empty(t, st.ApprovedHead)
}

func dryRunHarness(t *testing.T, mode string) *reviewHarness {
	t.Helper()
	h := newReviewHarness(t, mode, func(w *config.ReviewWatch) { w.DryRun = true })
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	require.Empty(t, h.gh.posted, "dry run posts nothing")
	require.True(t, h.state(t).DryRun)
	return h
}

func TestPostDraftSendsDryRunApproval(t *testing.T) {
	h := dryRunHarness(t, "nit")
	action, why, err := h.e.PostDraft(t.Context(), h.job.Key)
	require.NoError(t, err)
	assert.Equal(t, ActionApprove, action)
	assert.Empty(t, why)

	require.Len(t, h.gh.posted, 1)
	p := h.gh.posted[0]
	assert.Equal(t, "APPROVE", p.Event)
	require.Len(t, p.Comments, 1)
	assert.True(t, strings.HasPrefix(p.Comments[0].Body, "non-blocking: "))

	st := h.state(t)
	assert.False(t, st.DryRun)
	assert.Equal(t, StatusApproved, st.Status)
	assert.Equal(t, h.head, st.ApprovedHead)
	assert.Equal(t, []string{"PRRT_new"}, st.OurThreads)

	_, _, err = h.e.PostDraft(t.Context(), h.job.Key)
	require.ErrorContains(t, err, "has no review to post", "a draft is posted once")
	assert.Len(t, h.gh.posted, 1)
}

func TestPostDraftKeepsVerdictFromApproving(t *testing.T) {
	h := dryRunHarness(t, "hesitant")
	action, why, err := h.e.PostDraft(t.Context(), h.job.Key)
	require.NoError(t, err)
	assert.Equal(t, ActionComment, action)
	assert.Equal(t, "Claude's verdict was not approve", why)
	require.Len(t, h.gh.posted, 1)
	assert.Equal(t, "COMMENT", h.gh.posted[0].Event)
}

func TestPostDraftDropsApprovalWhenCIIsNotGreen(t *testing.T) {
	h := dryRunHarness(t, "nit")
	h.gh.requiredErr = errors.New("502 Bad Gateway")
	action, why, err := h.e.PostDraft(t.Context(), h.job.Key)
	require.NoError(t, err)
	assert.Equal(t, ActionComment, action, "the nit is still posted")
	assert.Equal(t, "required CI is not green", why)
	assert.Equal(t, "COMMENT", h.gh.posted[0].Event)
}

func TestPostDraftRefusesAfterNewCommit(t *testing.T) {
	h := dryRunHarness(t, "nit")
	d := h.gh.details["N7"]
	d.HeadOID = strings.Repeat("e", 40)
	h.gh.details["N7"] = d
	_, _, err := h.e.PostDraft(t.Context(), h.job.Key)
	require.ErrorContains(t, err, "changed since the review was written")
	assert.Empty(t, h.gh.posted)
}

func TestPostDraftIgnoresOldDraftsWithoutVerdict(t *testing.T) {
	h := dryRunHarness(t, "nit")
	st := h.state(t)
	paths, _ := filepath.Glob(filepath.Join(string(h.e.store), "drafts", "*.json"))
	require.Len(t, paths, 1)
	draft, err := readDraft(paths[0])
	require.NoError(t, err)
	draft.Verdict, draft.CodeReviewed = "", false // written before verdicts existed
	require.NoError(t, writeDraft(paths[0], draft))

	action, why, err := h.e.PostDraft(t.Context(), st.Key)
	require.NoError(t, err)
	assert.Equal(t, ActionComment, action)
	assert.Equal(t, "the review did not read everything it had to at this commit", why)
}

func TestReviewSpendAddsUpAcrossRuns(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	// A run from before totals were kept.
	_, err := h.e.update(h.job.Key, func(s *PRState) {
		s.LastRun = &RunRecord{SessionID: "old", Turns: 6, CostUSD: 0.26}
	})
	require.NoError(t, err)

	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	cost, turns, runs := h.state(t).Spend()
	assert.InDelta(t, 0.26+1.25, cost, 1e-9)
	assert.Equal(t, 6+7, turns)
	assert.Equal(t, 2, runs)
}

func TestReviewSpendCountsCappedRuns(t *testing.T) {
	h := newReviewHarness(t, "max_turns", nil)
	out := h.e.Review(t.Context(), h.job)
	require.Error(t, out.Err)
	cost, turns, runs := h.state(t).Spend()
	assert.Equal(t, 3.5, cost, "a run that hit its cap still cost money")
	assert.Equal(t, 100, turns)
	assert.Equal(t, 1, runs)
}

func TestPostAllDraftsAfterGoingLive(t *testing.T) {
	h := dryRunHarness(t, "nit")
	h.e.d.Watch.DryRun = false // the user turned dry run off

	drafts := h.e.UnpostedDrafts()
	require.Len(t, drafts, 1)
	assert.Equal(t, "APPROVE", drafts[h.job.Key].Event)

	results := h.e.PostAllDrafts(t.Context())
	require.Len(t, results, 1)
	assert.NoError(t, results[0].Err)
	assert.Equal(t, ActionApprove, results[0].Action)
	assert.Equal(t, "APPROVE", h.gh.posted[0].Event)
	assert.Empty(t, h.e.UnpostedDrafts(), "posted drafts are not offered again")
}

func TestPostAllDraftsReportsSkips(t *testing.T) {
	h := dryRunHarness(t, "nit")
	d := h.gh.details["N7"]
	d.HeadOID = strings.Repeat("e", 40)
	h.gh.details["N7"] = d
	results := h.e.PostAllDrafts(t.Context())
	require.Len(t, results, 1)
	assert.ErrorContains(t, results[0].Err, "changed since the review was written")
	assert.Empty(t, h.gh.posted)
}

func TestReviewSecurityFindingIsAlwaysBlocking(t *testing.T) {
	h := newReviewHarness(t, "security", nil)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	require.Len(t, h.gh.posted, 1)
	assert.NotEqual(
		t,
		"APPROVE",
		h.gh.posted[0].Event,
		"a security finding blocks approval even when marked non-blocking and the verdict says approve",
	)
	assert.False(t, strings.HasPrefix(h.gh.posted[0].Comments[0].Body, "non-blocking:"))
	st := h.state(t)
	require.Len(t, st.Ledger, 1)
	assert.True(t, st.Ledger[0].Blocking)
	assert.Equal(t, CategorySecurity, st.Ledger[0].Category)
	assert.Equal(t, StatusChangesRequested, st.Status)
}

func TestReviewCutOffBySleepIsQueuedAgain(t *testing.T) {
	h := newReviewHarness(t, "crash", nil)
	h.e.Woke(h.now) // the machine just woke
	out := h.e.Review(t.Context(), h.job)
	require.Error(t, out.Err)

	st := h.state(t)
	assert.True(t, st.ForceReview, "queued again")
	assert.Zero(t, st.Backoff, "no backoff for a review sleep interrupted")
	assert.True(t, strings.HasPrefix(st.Note, "interrupted by sleep; queued again: "), st.Note)
	assert.Equal(t, 0, h.e.Status().InFlight)
}

func TestReviewFailureWithoutSleepBacksOff(t *testing.T) {
	h := newReviewHarness(t, "crash", nil)
	out := h.e.Review(t.Context(), h.job)
	require.Error(t, out.Err)
	st := h.state(t)
	assert.False(t, st.ForceReview)
	assert.Equal(t, 1, st.Backoff.Step)
}

// seedUnposted records a blocking finding from a dry-run pass that never
// reached the PR, as mono#66370's f1 did.
func seedUnposted(t *testing.T, h *reviewHarness) {
	t.Helper()
	_, err := h.e.update(h.job.Key, func(s *PRState) {
		s.Ledger = []Finding{{
			ID: "f1", Path: "kev.go", Line: 1, Blocking: true,
			Summary: "runs at 13:00 with dlq", Status: FindingOutstanding,
		}}
	})
	require.NoError(t, err)
}

func TestReviewDropsUnpostedFindingTheReviewerDidNotRaiseAgain(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	seedUnposted(t, h)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted, "a finding the author never saw no longer blocks")
	assert.Empty(t, h.state(t).Ledger)
}

func TestReviewPostsUnpostedFindingRaisedAgain(t *testing.T) {
	h := newReviewHarness(t, "finding", nil)
	seedUnposted(t, h)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	require.Len(t, h.gh.posted, 1)
	require.Len(t, h.gh.posted[0].Comments, 1, "raised again, it reaches the PR")
	st := h.state(t)
	require.Len(t, st.Ledger, 1, "no duplicate of the old finding")
	assert.True(t, st.Ledger[0].Posted)
	assert.Equal(t, "PRRT_new", st.Ledger[0].ThreadID)
	assert.False(t, st.Ledger[0].Unposted())
}

// failScanURLs makes a required URL scanner fail on the harness PR.
func failScanURLs(h *reviewHarness) {
	d := h.gh.details["N7"]
	d.Checks = append(d.Checks, CheckNode{
		Typename: "CheckRun", Name: "scan-urls", Status: "COMPLETED", Conclusion: "FAILURE",
		IsRequired: true, ID: 4242, StartedAt: t0,
	})
	h.gh.details["N7"] = d
	h.job.Detail = d
	h.gh.reports = map[int64]CheckReport{4242: {
		Title:       "1 URL failed",
		Annotations: []string{"README.md:3 failure: https://example.invalid returned 404"},
	}}
	h.gh.base = map[string]string{"scan-urls": "fail", "build": "pass"}
}

func TestReviewApprovesPastAnUnrelatedCIFailure(t *testing.T) {
	stdin := filepath.Join(t.TempDir(), "stdin")
	t.Setenv("FAKE_CLAUDE_STDIN", stdin)
	h := newReviewHarness(t, "ci_unrelated", nil)
	failScanURLs(h)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	require.Len(t, h.gh.posted, 1)
	p := h.gh.posted[0]
	assert.Equal(t, "APPROVE", p.Event)
	assert.Equal(
		t,
		"`scan-urls` is failing for a reason unrelated to this change: the URL scanner flags links that already exist on main.",
		p.Body,
		"one plain line says why it was approved with red CI",
	)
	st := h.state(t)
	assert.Equal(t, StatusApproved, st.Status)
	require.Len(t, st.CIAssessment, 1)
	assert.False(t, st.CIAssessment[0].Related)

	prompt, _ := os.ReadFile(stdin)
	assert.Contains(t, string(prompt), "- scan-urls: failing (on base: fail); output in ")
	report := filepath.Join(
		h.e.d.StateRoot,
		"sessions",
		st.LastRun.SessionID,
		"ci",
		"scan-urls.txt",
	)
	b, err := os.ReadFile(report)
	require.NoError(t, err)
	assert.Contains(t, string(b), "https://example.invalid returned 404")
}

func TestReviewWaitsOnARelatedCIFailure(t *testing.T) {
	h := newReviewHarness(t, "ci_related", nil)
	failScanURLs(h)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.posted)
	assert.Equal(t, StatusWaitingOnCI, h.state(t).Status)
}

func TestReviewWaitsWhenTheCheckPassesOnMain(t *testing.T) {
	h := newReviewHarness(t, "ci_unrelated", nil)
	failScanURLs(h)
	h.gh.base["scan-urls"] = "pass"
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Empty(t, h.gh.posted, "a check that passes on main was broken by the PR")
}

func TestReviewTestSuggestionInPROnlyRepoNeverBlocks(t *testing.T) {
	// stereo#216533: a smoke-test suggestion was posted as blocking.
	h := newReviewHarness(t, "smoke", func(w *config.ReviewWatch) {
		w.PROnlyRepos = []string{"chainguard-dev/mono"}
	})
	t.Setenv("FAKE_CLAUDE_READS", "")
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)

	require.Len(t, h.gh.posted, 1)
	p := h.gh.posted[0]
	assert.Equal(t, "APPROVE", p.Event, "a test suggestion does not hold up the PR")
	require.Len(t, p.Comments, 1)
	assert.True(
		t,
		strings.HasPrefix(p.Comments[0].Body, "non-blocking: You could add a smoke test"),
	)
	st := h.state(t)
	require.Len(t, st.Ledger, 1)
	assert.False(t, st.Ledger[0].Blocking)
}

func TestNonBlockingThread(t *testing.T) {
	assert.True(
		t,
		nonBlockingThread(
			"**Suggestion (non-blocking).** Have you thought about trying this in staging first?",
		),
	)
	assert.True(t, nonBlockingThread("nit: rename `x`"))
	assert.True(t, nonBlockingThread("Optional: add a log line"))
	assert.False(
		t,
		nonBlockingThread("Could we scope the `build.v1` match to the scanning subscriptions?"),
	)
	assert.False(t, nonBlockingThread("This panics on a nil map; a minor change fixes it."))
}

func TestReviewContinuesAnIncompletePass(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	dir := t.TempDir()
	resumeStdin, resumeArgs := filepath.Join(dir, "stdin"), filepath.Join(dir, "args")
	t.Setenv("FAKE_CLAUDE_READS", "kev.go") // skips the docs and tests
	t.Setenv("FAKE_CLAUDE_RESUME_READS", "README.md kev_test.go")
	t.Setenv("FAKE_CLAUDE_RESUME_STDIN", resumeStdin)
	t.Setenv("FAKE_CLAUDE_RESUME_ARGS", resumeArgs)

	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted, "the continued review is complete")
	assert.InDelta(t, 2.5, out.CostUSD, 1e-9, "the continuation's cost is part of the pass")

	st := h.state(t)
	assert.Empty(t, st.ReviewGaps)
	assert.Equal(t, h.head, st.CodeReviewedHead)
	assert.Equal(t, 1, st.LastRun.Continuations)
	assert.Equal(t, 14, st.LastRun.Turns)
	cost, _, runs := st.Spend()
	assert.InDelta(t, 2.5, cost, 1e-9)
	assert.Equal(t, 1, runs, "a continuation is not another review")
	assert.Equal(t, 1, st.ReviewsByDay[Day(h.now)], "nor does it count toward the daily limit")

	prompt, err := os.ReadFile(resumeStdin)
	require.NoError(t, err)
	assert.Contains(t, string(prompt), "- read no documentation or design docs\n")
	assert.Contains(t, string(prompt), "- read no existing tests for the changed code\n")
	args, err := os.ReadFile(resumeArgs)
	require.NoError(t, err)
	assert.Contains(t, string(args), "--resume "+st.LastRun.SessionID)
	assert.NotContains(t, string(args), "--session-id")
}

func TestReviewKeepsIncompletePassWhenContinuationFails(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	t.Setenv("FAKE_CLAUDE_READS", "kev.go")
	t.Setenv("FAKE_CLAUDE_RESUME_MODE", "crash")

	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err, "a failed continuation does not fail the pass")
	assert.Empty(t, h.gh.posted)
	st := h.state(t)
	assert.Equal(t, StatusNeedsAttention, st.Status)
	assert.Equal(t, []string{
		"read no documentation or design docs",
		"read no existing tests for the changed code",
	}, st.ReviewGaps)
	assert.Equal(t, 1, st.LastRun.Continuations, "it stops after the first failure")
}

func TestReviewIncompleteSchedulesGrowingRetries(t *testing.T) {
	h := newReviewHarness(t, "success", nil)
	t.Setenv("FAKE_CLAUDE_READS", "kev.go") // stays incomplete after continuing
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	st := h.state(t)
	require.NotEmpty(t, st.ReviewGaps)
	assert.Equal(t, Backoff{Until: h.now.Add(15 * time.Minute), Step: 1}, st.Backoff)

	// The retry is incomplete too: the wait doubles.
	_, _ = h.e.update(h.job.Key, func(s *PRState) { s.Claim = &Claim{Since: h.now, Head: h.head} })
	h.e.claimed[h.job.Key] = struct{}{}
	require.NoError(t, h.e.Review(t.Context(), h.job).Err)
	assert.Equal(t, Backoff{Until: h.now.Add(30 * time.Minute), Step: 2}, h.state(t).Backoff)

	// A complete pass clears it.
	t.Setenv("FAKE_CLAUDE_READS", "kev.go README.md kev_test.go")
	_, _ = h.e.update(h.job.Key, func(s *PRState) { s.Claim = &Claim{Since: h.now, Head: h.head} })
	h.e.claimed[h.job.Key] = struct{}{}
	require.NoError(t, h.e.Review(t.Context(), h.job).Err)
	st = h.state(t)
	assert.Empty(t, st.ReviewGaps)
	assert.Equal(t, Backoff{}, st.Backoff)
}

func TestReviewPostsOffDiffFindingsAsFileThreads(t *testing.T) {
	h := newReviewHarness(t, "offdiff", nil)
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err)
	assert.Equal(t, ActionApprove, out.Posted, "non-blocking findings ride along with an approval")

	require.Len(t, h.gh.posted, 1)
	require.Len(t, h.gh.posted[0].Comments, 1, "only the changed line is commented inline")
	assert.Equal(t, 1, h.gh.posted[0].Comments[0].Line)
	assert.Empty(t, h.gh.posted[0].Comments[0].SubjectType)

	require.Len(t, h.gh.fileComments, 1, "line 304 is not in the diff, so it gets a file thread")
	assert.Equal(t, "kev.go", h.gh.fileComments[0].Path)
	assert.True(t, strings.HasPrefix(h.gh.fileComments[0].Body, "Line 304: non-blocking: "),
		h.gh.fileComments[0].Body)
	assert.NotContains(t, h.gh.posted[0].Body, "304", "nothing is folded into the review body")

	st := h.state(t)
	require.Len(t, st.Ledger, 2)
	assert.Equal(t, "PRRT_file1", st.Ledger[0].ThreadID, "the file thread is tracked")
	assert.True(t, st.Ledger[0].Posted)
	assert.Contains(t, st.OurThreads, "PRRT_file1", "so dispatch can resolve it later")

	// The author fixes it; the next pass resolves the file thread.
	t.Setenv("FAKE_CLAUDE_MODE", "resolve")
	_, _ = h.e.update(h.job.Key, func(s *PRState) { s.Claim = &Claim{Since: h.now, Head: h.head} })
	h.e.claimed[h.job.Key] = struct{}{}
	require.NoError(t, h.e.Review(t.Context(), Job{
		Key: h.job.Key, Trigger: TriggerChange, Pass: PassLedger, Detail: h.job.Detail,
	}).Err)
	assert.Contains(t, h.gh.resolved, "PRRT_file1")
}

func TestReviewLeavesFailedFileThreadUnposted(t *testing.T) {
	h := newReviewHarness(t, "offdiff", nil)
	h.gh.fileErr = errors.New("HTTP 502: Bad Gateway")
	out := h.e.Review(t.Context(), h.job)
	require.NoError(t, out.Err, "the review itself was posted")
	require.Len(t, h.gh.posted, 1)

	st := h.state(t)
	require.Len(t, st.Ledger, 2)
	assert.False(t, st.Ledger[0].Posted, "the next pass offers the line-304 finding again")
	assert.Empty(t, st.Ledger[0].ThreadID)
	assert.True(t, st.Ledger[1].Posted)
}
