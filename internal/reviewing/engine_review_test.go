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
	e.claimed[k] = true

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
	require.NoError(t, h.e.PostHeld(context.Background(), h.job.Key))
	require.Len(t, h.gh.posted, 1)
	assert.Equal(t, "COMMENT", h.gh.posted[0].Event)
	st = h.state(t)
	assert.Empty(t, st.HeldDraft)
	assert.Equal(t, StatusChangesRequested, st.Status)
	assert.Equal(t, []string{"PRRT_new"}, st.OurThreads)
}

func TestPostHeldRefusesUneditedBotspeak(t *testing.T) {
	h := newReviewHarness(t, "botspeak", nil)
	h.e.Review(context.Background(), h.job)
	err := h.e.PostHeld(context.Background(), h.job.Key)
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

	// Sending the held review as is never approves.
	require.NoError(t, h.e.PostHeld(context.Background(), h.job.Key))
	for _, p := range h.gh.posted {
		assert.NotEqual(t, "APPROVE", p.Event)
	}
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
}
