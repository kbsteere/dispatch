package reviewing

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
)

type fakeGitHub struct {
	mu         sync.Mutex
	viewer     string
	hits       []SearchHit
	details    map[string]PRDetail
	required   RequiredContexts
	posted     []ReviewPayload
	replies    map[string]string
	resolved   []string
	threadRefs []ThreadRef
	// headAfterRun, when set, is the head Details reports once a review is
	// running, to simulate a push during review.
	headAfterRun string
	detailCalls  int
	requiredErr  error
	// requested are the hits a review-requested:@me search returns;
	// requestedErr fails that search.
	requested    []SearchHit
	requestedErr error
	searchErr    error
	filters      []string
}

func (f *fakeGitHub) Viewer(context.Context) (string, error) { return f.viewer, nil }

func (f *fakeGitHub) Search(
	_ context.Context,
	filter string,
	_ int,
) ([]SearchHit, RateLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.filters = append(f.filters, filter)
	if f.searchErr != nil {
		return nil, RateLimit{}, f.searchErr
	}
	hits := slices.Clone(f.hits)
	if strings.HasSuffix(filter, " "+requestedQualifier) {
		if f.requestedErr != nil {
			return nil, RateLimit{}, f.requestedErr
		}
		hits = slices.Clone(f.requested)
	}
	return hits, RateLimit{Cost: 1, Remaining: 4000, ResetAt: t0.Add(time.Hour)}, nil
}

func (f *fakeGitHub) Details(_ context.Context, ids []string) ([]PRDetail, RateLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.detailCalls++
	var out []PRDetail
	for _, id := range ids {
		if d, ok := f.details[id]; ok {
			if f.headAfterRun != "" {
				d.HeadOID = f.headAfterRun
			}
			out = append(out, d)
		}
	}
	return out, RateLimit{Cost: len(ids), Remaining: 4000, ResetAt: t0.Add(time.Hour)}, nil
}

func (f *fakeGitHub) Resolve(_ context.Context, k PRKey) (string, error) {
	return "node-" + k.String(), nil
}

func (f *fakeGitHub) RequiredContexts(context.Context, PRKey, string) (RequiredContexts, error) {
	return f.required, f.requiredErr
}

func (f *fakeGitHub) PostReview(_ context.Context, _ PRKey, p ReviewPayload) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.posted = append(f.posted, p)
	return fmt.Sprintf("https://github.com/o/r/pull/1#pullrequestreview-%d", len(f.posted)), nil
}

func (f *fakeGitHub) ReplyThread(_ context.Context, id, body string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.replies == nil {
		f.replies = map[string]string{}
	}
	f.replies[id] = body
	return "c1", nil
}

func (f *fakeGitHub) ResolveThread(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resolved = append(f.resolved, id)
	return nil
}

func (f *fakeGitHub) ReviewThreads(context.Context, PRKey, string) ([]ThreadRef, error) {
	return f.threadRefs, nil
}

func detail(n int, head string) PRDetail {
	return PRDetail{
		Key: PRKey{
			"chainguard-dev",
			"stereo",
			n,
		},
		NodeID:    fmt.Sprintf("N%d", n),
		State:     "OPEN",
		Author:    "dnegreira",
		HeadOID:   head,
		BaseRef:   "main",
		BaseOID:   strings.Repeat("0", 40),
		Mergeable: "MERGEABLE",
		UpdatedAt: head,
		Files: []string{
			"kev.yaml",
		},
		Checks: []CheckNode{
			{
				Typename:   "CheckRun",
				Name:       "build",
				Status:     "COMPLETED",
				Conclusion: "SUCCESS",
				IsRequired: true,
			},
		},
	}
}

func newTestEngine(t *testing.T, gh *fakeGitHub, now *time.Time) *Engine {
	t.Helper()
	e, err := NewEngine(Deps{
		Watch: config.ReviewWatch{
			Name:    "sustaining-automation",
			Authors: []string{"dnegreira"},
			Scopes:  []string{"repo:chainguard-dev/stereo"},
		},
		Limits:     config.DefaultReviewingLimits(),
		StateRoot:  t.TempDir(),
		GitHub:     gh,
		LookupPath: func(string) (string, bool) { return "", false },
		Now:        func() time.Time { return *now },
	})
	require.NoError(t, err)
	t.Cleanup(e.Shutdown)
	return e
}

func stateOf(t *testing.T, e *Engine, k PRKey) PRState {
	t.Helper()
	st, err := e.load(k)
	require.NoError(t, err)
	return st
}

func TestPollBaselinesOnFirstStart(t *testing.T) {
	now := t0
	sha := strings.Repeat("a", 40)
	gh := &fakeGitHub{
		viewer: "kbsteere",
		hits: []SearchHit{
			{Key: PRKey{"chainguard-dev", "stereo", 1}, NodeID: "N1", UpdatedAt: sha},
		},
		details: map[string]PRDetail{"N1": detail(1, sha)},
	}
	e := newTestEngine(t, gh, &now)

	jobs, next, err := e.Poll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, jobs, "existing PRs are baselined, not reviewed")
	assert.Equal(t, 3*time.Minute, next)
	assert.Equal(t, StatusBaseline, stateOf(t, e, PRKey{"chainguard-dev", "stereo", 1}).Status)

	calls := gh.detailCalls
	jobs, _, err = e.Poll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, jobs)
	assert.Equal(t, calls, gh.detailCalls, "unchanged PRs are not re-read")
}

func TestPollRecordsDisplayFieldsAndStatus(t *testing.T) {
	now := t0
	sha := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "stereo", 1}
	d := detail(1, sha)
	d.Title, d.Author = "kev: bump to 1.4.2", "dnegreira"
	gh := &fakeGitHub{
		viewer:  "kbsteere",
		hits:    []SearchHit{{Key: k, NodeID: "N1", UpdatedAt: sha}},
		details: map[string]PRDetail{"N1": d},
	}
	e := newTestEngine(t, gh, &now)
	assert.True(t, e.Status().LastPoll.IsZero(), "not polled yet")
	_, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, now, e.Status().LastPoll)

	st := stateOf(t, e, k)
	assert.Equal(t, "kev: bump to 1.4.2", st.Title)
	assert.Equal(t, "dnegreira", st.Author)
	assert.Equal(t, "SUCCESS", st.CI)

	ws := e.Status()
	assert.Equal(t, 4000, ws.QuotaRemaining)
	assert.Equal(t, 100.0, ws.DailyBudget)
}

func TestPollClaimsAfterNewPushSettles(t *testing.T) {
	now := t0
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	k := PRKey{"chainguard-dev", "stereo", 1}
	gh := &fakeGitHub{
		viewer: "kbsteere", hits: []SearchHit{{Key: k, NodeID: "N1", UpdatedAt: a}},
		details: map[string]PRDetail{"N1": detail(1, a)},
	}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(context.Background()) // baseline
	_, _ = e.update(k, func(s *PRState) { s.CodeReviewedHead = a })

	gh.hits[0].UpdatedAt = b
	gh.details["N1"] = detail(1, b)
	jobs, _, _ := e.Poll(context.Background())
	assert.Empty(t, jobs, "head must settle")

	now = now.Add(HeadSettle + time.Second)
	jobs, _, _ = e.Poll(context.Background())
	require.Len(t, jobs, 1, "a settling head is re-read even though updatedAt did not move")
	assert.Equal(t, PassIncremental, jobs[0].Pass)
	assert.Equal(t, StatusReviewing, stateOf(t, e, k).Status)
	assert.Equal(t, 1, e.Status().InFlight)

	jobs, _, _ = e.Poll(context.Background())
	assert.Empty(t, jobs, "a claimed PR is not claimed twice")
}

func TestPollForeignCommentTriggersLedgerPass(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "stereo", 1}
	gh := &fakeGitHub{
		viewer: "kbsteere", hits: []SearchHit{{Key: k, NodeID: "N1", UpdatedAt: "u1"}},
		details: map[string]PRDetail{"N1": detail(1, a)},
	}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(context.Background())
	_, _ = e.update(k, func(s *PRState) { s.CodeReviewedHead = a })

	d := detail(1, a)
	d.Comments = []Comment{
		{Author: "dnegreira", Body: "addressed, ptal", CreatedAt: t0.Add(time.Minute)},
	}
	gh.details["N1"] = d
	gh.hits[0].UpdatedAt = "u2"
	jobs, _, _ := e.Poll(context.Background())
	require.Len(t, jobs, 1)
	assert.Equal(t, PassLedger, jobs[0].Pass)

	// The pass finishes and records what it saw.
	seen := e.signature(context.Background(), d, "kbsteere")
	_, _ = e.update(
		k,
		func(s *PRState) { s.Claim = nil; s.ReviewedSig = &seen; s.Status = StatusWaitingOnAuthor },
	)
	e.release(k)

	d.Comments = append(
		d.Comments,
		Comment{Author: "kbsteere", Body: "nit: rename", CreatedAt: t0.Add(2 * time.Minute)},
	)
	gh.details["N1"] = d
	gh.hits[0].UpdatedAt = "u3"
	jobs, _, _ = e.Poll(context.Background())
	assert.Empty(t, jobs, "our own comment never triggers a pass")
}

func TestPollRespectsBudget(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	gh := &fakeGitHub{viewer: "kbsteere"}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(context.Background()) // initialise with no PRs

	_, err := e.store.AddSpend(Day(now), 100)
	require.NoError(t, err)
	gh.hits = []SearchHit{{Key: PRKey{"chainguard-dev", "stereo", 2}, NodeID: "N2", UpdatedAt: a}}
	gh.details = map[string]PRDetail{"N2": detail(2, a)}
	jobs, _, _ := e.Poll(context.Background())
	assert.Empty(t, jobs)
	assert.Equal(t, "budget", e.Status().Paused)
}

func TestPollThrottlesAndBacksOff(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "stereo", 3}
	gh := &fakeGitHub{viewer: "kbsteere"}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(context.Background())
	gh.hits = []SearchHit{{Key: k, NodeID: "N3", UpdatedAt: a}}
	gh.details = map[string]PRDetail{"N3": detail(3, a)}

	_, _ = e.update(k, func(s *PRState) { s.ReviewsByDay = map[string]int{Day(now): 6} })
	jobs, _, _ := e.Poll(context.Background())
	assert.Empty(t, jobs)
	assert.Equal(t, StatusThrottled, stateOf(t, e, k).Status)

	require.NoError(t, e.Force(k))
	jobs, _, _ = e.Poll(context.Background())
	require.Len(t, jobs, 1, "a forced review ignores the per-PR limit")
	e.release(k)

	_, _ = e.update(k, func(s *PRState) {
		s.ReviewsByDay = nil
		s.Backoff = Backoff{Until: now.Add(time.Hour)}
		s.Status = StatusFailed
	})
	jobs, _, _ = e.Poll(context.Background())
	assert.Empty(t, jobs, "backoff holds retries")
	now = now.Add(61 * time.Minute)
	jobs, _, _ = e.Poll(context.Background())
	assert.Len(t, jobs, 1)
}

func TestPollMaxInFlight(t *testing.T) {
	now := t0
	gh := &fakeGitHub{viewer: "kbsteere", details: map[string]PRDetail{}}
	e := newTestEngine(t, gh, &now)
	e.d.Limits.MaxInFlight = 2
	_, _, _ = e.Poll(context.Background())
	for i := 1; i <= 3; i++ {
		sha := strings.Repeat(fmt.Sprint(i), 40)
		gh.hits = append(
			gh.hits,
			SearchHit{
				Key:       PRKey{"chainguard-dev", "stereo", i},
				NodeID:    fmt.Sprintf("N%d", i),
				UpdatedAt: sha,
			},
		)
		gh.details[fmt.Sprintf("N%d", i)] = detail(i, sha)
	}
	jobs, _, _ := e.Poll(context.Background())
	assert.Len(t, jobs, 2)
}

func TestPinnedPRReviewedImmediatelyAndUnpinnedOnMerge(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	gh := &fakeGitHub{viewer: "kbsteere", details: map[string]PRDetail{}}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(context.Background())

	k := PRKey{"chainguard-dev", "omnibump", 88}
	d := detail(88, a)
	d.Key, d.NodeID = k, "node-"+k.String()
	gh.details[d.NodeID] = d
	require.NoError(t, e.Pin(context.Background(), k))

	jobs, _, _ := e.Poll(context.Background())
	require.Len(t, jobs, 1)
	assert.Equal(t, PassFull, jobs[0].Pass)

	e.release(k)
	d.State = "MERGED"
	gh.details[d.NodeID] = d
	_, _, _ = e.Poll(context.Background())
	assert.False(t, stateOf(t, e, k).Pinned)
}

func TestPollSkipsPinnedDrafts(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	gh := &fakeGitHub{viewer: "kbsteere", details: map[string]PRDetail{}}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(context.Background())

	k := PRKey{"chainguard-dev", "mono", 5}
	d := detail(5, a)
	d.Key, d.NodeID, d.IsDraft = k, "node-"+k.String(), true
	gh.details[d.NodeID] = d
	require.NoError(t, e.Pin(context.Background(), k))
	jobs, _, _ := e.Poll(context.Background())
	assert.Empty(t, jobs, "pinned drafts wait until ready")
}

func TestPollReleasesClaimWhenPRLeavesResults(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "stereo", 9}
	gh := &fakeGitHub{viewer: "kbsteere"}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(context.Background())
	gh.hits = []SearchHit{{Key: k, NodeID: "N9", UpdatedAt: a}}
	gh.details = map[string]PRDetail{"N9": detail(9, a)}
	jobs, _, _ := e.Poll(context.Background())
	require.Len(t, jobs, 1)

	gh.hits = nil // converted to draft: search excludes it
	_, _, _ = e.Poll(context.Background())
	st := stateOf(t, e, k)
	assert.Nil(t, st.Claim)
	assert.Equal(t, 0, e.Status().InFlight)
}

func TestPollMarksPRsThatLeaveTheSearchInactive(t *testing.T) {
	// mono#66155 merged after a failed pass; it must not linger in the view.
	now := t0
	a := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "mono", 66155}
	d := detail(1, a)
	d.Key, d.NodeID = k, "N66155"
	gh := &fakeGitHub{
		viewer: "kbsteere", hits: []SearchHit{{Key: k, NodeID: "N66155", UpdatedAt: a}},
		details: map[string]PRDetail{"N66155": d},
	}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(context.Background())
	_, _ = e.update(k, func(s *PRState) { s.Status = StatusFailed; s.ForceReview = true })

	gh.hits = nil // merged: the search no longer returns it
	_, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	st := stateOf(t, e, k)
	assert.True(t, st.Inactive)
	assert.False(t, st.ForceReview, "a forced review of a gone PR is dropped")

	gh.hits = []SearchHit{{Key: k, NodeID: "N66155", UpdatedAt: "reopened"}}
	_, _, err = e.Poll(context.Background())
	require.NoError(t, err)
	assert.False(t, stateOf(t, e, k).Inactive, "a PR back in the search is active again")
}

func TestSecondEngineIsReadOnly(t *testing.T) {
	now := t0
	root := t.TempDir()
	mk := func() *Engine {
		e, err := NewEngine(Deps{
			Watch: config.ReviewWatch{
				Name:    "w",
				Authors: []string{"a"},
				Scopes:  []string{"repo:o/r"},
			},
			Limits:     config.DefaultReviewingLimits(),
			StateRoot:  root,
			GitHub:     &fakeGitHub{},
			LookupPath: func(string) (string, bool) { return "", false },
			Now:        func() time.Time { return now },
		})
		require.NoError(t, err)
		return e
	}
	e1 := mk()
	defer e1.Shutdown()
	e2 := mk()
	defer e2.Shutdown()
	assert.False(t, e1.ReadOnly())
	assert.True(t, e2.ReadOnly())
	assert.Equal(t, "read-only", e2.Status().Paused)
	jobs, _, err := e2.Poll(context.Background())
	assert.NoError(t, err)
	assert.Empty(t, jobs)
}

func TestCorruptStateRebuildsFromMarkers(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "stereo", 4}
	d := detail(4, a)
	d.Threads = []Thread{
		{
			ID:       "PRRT_ours",
			Path:     "kev.yaml",
			Line:     3,
			Comments: []Comment{{Author: "kbsteere", Body: "epoch not bumped\n\n" + Marker}},
		},
		{
			ID:       "PRRT_theirs",
			Path:     "kev.yaml",
			Line:     5,
			Comments: []Comment{{Author: "powersj", Body: "typo"}},
		},
	}
	gh := &fakeGitHub{
		viewer:  "kbsteere",
		hits:    []SearchHit{{Key: k, NodeID: "N4", UpdatedAt: a}},
		details: map[string]PRDetail{"N4": d},
	}
	e := newTestEngine(t, gh, &now)
	require.NoError(t, e.store.MarkInitialized())
	require.NoError(
		t,
		os.WriteFile(
			filepath.Join(string(e.store), "prs", k.FileName()+".json"),
			[]byte("{oops"),
			0o600,
		),
	)

	jobs, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, jobs, "rebuilt state counts as reviewed; nothing is reposted")
	st := stateOf(t, e, k)
	assert.Equal(t, []string{"PRRT_ours"}, st.OurThreads)
	require.Len(t, st.Ledger, 1)
	assert.Equal(t, "epoch not bumped", st.Ledger[0].Summary)
}

func TestStartupReleasesStaleClaims(t *testing.T) {
	now := t0
	gh := &fakeGitHub{viewer: "kbsteere"}
	e := newTestEngine(t, gh, &now)
	k := PRKey{"chainguard-dev", "stereo", 6}
	_, _ = e.update(
		k,
		func(s *PRState) { s.Claim = &Claim{PID: os.Getpid(), Since: now}; s.Status = StatusReviewing },
	)
	require.NoError(t, e.Startup(context.Background()))
	st := stateOf(t, e, k)
	assert.Nil(t, st.Claim)
	assert.Equal(t, StatusFailed, st.Status, "retried on the next poll")
}

func TestDraftFor(t *testing.T) {
	now := time.Date(2026, 10, 5, 16, 0, 0, 0, time.UTC)
	e := newTestEngine(t, &fakeGitHub{}, &now)
	k := PRKey{"chainguard-dev", "stereo", 367322}
	approve := Draft{Event: "APPROVE", Head: "d5dcdad"}
	require.NoError(t, writeDraft(e.store.DraftPath(k, now), approve))
	st := PRState{Key: k, ReviewedSig: &Signature{Head: "d5dcdad"}, DryRun: true}

	d, ok := e.DraftFor(st)
	require.True(t, ok, "an unposted dry-run draft stays postable after the watch goes live")
	assert.Equal(t, approve, d)

	e.d.Watch.DryRun = true
	d, ok = e.DraftFor(st)
	require.True(t, ok)
	assert.Equal(t, approve, d)

	st.DryRun = false
	_, ok = e.DraftFor(st)
	assert.False(t, ok, "a draft that was posted is not shown again")
	st.DryRun = true

	st.ReviewedSig.Head = "e0f1a2b"
	_, ok = e.DraftFor(st)
	assert.False(t, ok, "a draft for an older head is not shown")

	held := Draft{Event: "COMMENT", Head: "e0f1a2b", Reasons: []string{"narrates verification"}}
	st.HeldDraft = e.store.DraftPath(k, now.Add(time.Minute))
	require.NoError(t, writeDraft(st.HeldDraft, held))
	e.d.Watch.DryRun = false
	d, ok = e.DraftFor(st)
	require.True(t, ok)
	assert.Equal(t, held, d, "a held review shows in any mode")

	st.HeldDraft = filepath.Join(t.TempDir(), "x.json")
	require.NoError(t, writeDraft(st.HeldDraft, held))
	_, ok = e.DraftFor(st)
	assert.False(t, ok, "a held path outside the drafts dir is not read")
}

func TestBaselineActivityGetsFullPass(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "stereo", 1}
	gh := &fakeGitHub{
		viewer: "kbsteere", hits: []SearchHit{{Key: k, NodeID: "N1", UpdatedAt: "u1"}},
		details: map[string]PRDetail{"N1": detail(1, a)},
	}
	e := newTestEngine(t, gh, &now)
	_, _, _ = e.Poll(t.Context())
	require.Equal(t, StatusBaseline, stateOf(t, e, k).Status)

	d := detail(1, a)
	d.Comments = []Comment{
		{Author: "powersj", Body: "lgtm once CI is green", CreatedAt: t0.Add(time.Minute)},
	}
	gh.details["N1"] = d
	gh.hits[0].UpdatedAt = "u2"
	jobs, _, _ := e.Poll(t.Context())
	require.Len(t, jobs, 1)
	assert.Equal(t, PassFull, jobs[0].Pass, "a baseline PR's code was never read")
}

func TestChoosePass(t *testing.T) {
	a, b := strings.Repeat("a", 40), strings.Repeat("b", 40)
	tests := []struct {
		name     string
		reviewed string
		forced   bool
		want     Pass
	}{
		{"code never read", "", false, PassFull},
		{"head moved since the code review", a, false, PassIncremental},
		{"only comments changed", b, false, PassLedger},
		{"forced review of an unchanged head", b, true, PassFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, choosePass(tt.reviewed, b, tt.forced))
		})
	}
}

func TestPollMarksRequestedReviews(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	k1, k2 := PRKey{"chainguard-dev", "mono", 1}, PRKey{"chainguard-dev", "mono", 2}
	gh := &fakeGitHub{
		viewer: "kbsteere",
		hits: []SearchHit{
			{Key: k1, NodeID: "N1", UpdatedAt: "u1"},
			{Key: k2, NodeID: "N2", UpdatedAt: "u1"},
		},
		details:   map[string]PRDetail{},
		requested: []SearchHit{{Key: k2, NodeID: "N2"}},
	}
	gh.details["N1"] = func() PRDetail { d := detail(1, a); d.Key = k1; return d }()
	gh.details["N2"] = func() PRDetail { d := detail(2, a); d.Key = k2; return d }()
	e := newTestEngine(t, gh, &now)
	_, _, err := e.Poll(t.Context())
	require.NoError(t, err)

	assert.False(t, stateOf(t, e, k1).ReviewRequested)
	assert.True(t, stateOf(t, e, k2).ReviewRequested)
	assert.Contains(t, gh.filters, e.d.Watch.SearchFilter()+" review-requested:@me")

	// A failed lookup keeps the marks; a later one clears a request that
	// was answered.
	gh.requestedErr = errors.New("502 Bad Gateway")
	_, _, _ = e.Poll(t.Context())
	assert.True(t, stateOf(t, e, k2).ReviewRequested)
	gh.requestedErr, gh.requested = nil, nil
	_, _, _ = e.Poll(t.Context())
	assert.False(t, stateOf(t, e, k2).ReviewRequested)
}

func TestMyReview(t *testing.T) {
	head, old := strings.Repeat("b", 40), strings.Repeat("a", 40)
	at := func(m int) time.Time { return t0.Add(time.Duration(m) * time.Minute) }
	tests := []struct {
		name       string
		reviews    []Review
		wantState  string
		wantOnHead bool
	}{
		{"none", nil, "", false},
		{
			"approved at head",
			[]Review{{Author: "kbsteere", State: "APPROVED", CommitOID: head, SubmittedAt: at(1)}},
			"APPROVED", true,
		},
		{
			"a later comment does not replace an approval",
			[]Review{
				{Author: "kbsteere", State: "APPROVED", CommitOID: old, SubmittedAt: at(1)},
				{Author: "kbsteere", State: "COMMENTED", CommitOID: head, SubmittedAt: at(2)},
			},
			"APPROVED", false,
		},
		{
			"changes requested after an approval",
			[]Review{
				{Author: "kbsteere", State: "APPROVED", CommitOID: old, SubmittedAt: at(1)},
				{
					Author:      "kbsteere",
					State:       "CHANGES_REQUESTED",
					CommitOID:   head,
					SubmittedAt: at(2),
				},
			},
			"CHANGES_REQUESTED",
			true,
		},
		{
			"a dismissed approval no longer stands",
			[]Review{
				{Author: "kbsteere", State: "COMMENTED", CommitOID: old, SubmittedAt: at(1)},
				{Author: "kbsteere", State: "DISMISSED", CommitOID: old, SubmittedAt: at(2)},
			},
			"COMMENTED", false,
		},
		{
			"other reviewers do not count",
			[]Review{{Author: "powersj", State: "APPROVED", CommitOID: head, SubmittedAt: at(1)}},
			"", false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, onHead := myReview(tt.reviews, "kbsteere", head)
			assert.Equal(t, tt.wantState, state)
			assert.Equal(t, tt.wantOnHead, onHead)
		})
	}
}

func TestPollRefreshesEveryPRAfterStartup(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "stereo", 1}
	d := detail(1, a)
	d.Reviews = []Review{{Author: "kbsteere", State: "APPROVED", CommitOID: a, SubmittedAt: t0}}
	gh := &fakeGitHub{
		viewer: "kbsteere", hits: []SearchHit{{Key: k, NodeID: "N1", UpdatedAt: a}},
		details: map[string]PRDetail{"N1": d},
	}
	e := newTestEngine(t, gh, &now)
	_, _, err := e.Poll(t.Context())
	require.NoError(t, err)
	st := stateOf(t, e, k)
	assert.Equal(t, "APPROVED", st.MyReview)
	assert.True(t, st.MyReviewOnHead)

	calls := gh.detailCalls
	_, _, _ = e.Poll(t.Context())
	assert.Equal(t, calls, gh.detailCalls, "an unchanged PR is not re-read")

	e.mu.Lock()
	e.refreshed = false // a restarted dispatch
	e.mu.Unlock()
	_, _, _ = e.Poll(t.Context())
	assert.Equal(t, calls+1, gh.detailCalls, "the first poll after startup re-reads every PR")
}

func TestForceBaselineQueuesOnlyBaselinedPRs(t *testing.T) {
	now := t0
	e := newTestEngine(t, &fakeGitHub{}, &now)
	keys := map[string]PRKey{
		"baseline": {"chainguard-dev", "mono", 1},
		"merged":   {"chainguard-dev", "mono", 2},
		"reviewed": {"chainguard-dev", "mono", 3},
	}
	for name, k := range keys {
		_, err := e.update(k, func(s *PRState) {
			s.Status = StatusBaseline
			s.Inactive = name == "merged"
			if name == "reviewed" {
				s.Status = StatusApproved
			}
		})
		require.NoError(t, err)
	}
	n, err := e.ForceBaseline()
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.True(t, stateOf(t, e, keys["baseline"]).ForceReview)
	assert.False(t, stateOf(t, e, keys["merged"]).ForceReview)
	assert.False(t, stateOf(t, e, keys["reviewed"]).ForceReview)
}

func TestStatusCountsQueuedReviews(t *testing.T) {
	now := t0
	e := newTestEngine(t, &fakeGitHub{}, &now)
	for n, mut := range map[int]func(*PRState){
		1: func(s *PRState) { s.ForceReview = true },
		2: func(s *PRState) { s.ForceReview = true },
		3: func(s *PRState) { s.ForceReview, s.Inactive = true, true },
		4: func(s *PRState) {},
	} {
		_, err := e.update(PRKey{"chainguard-dev", "mono", n}, mut)
		require.NoError(t, err)
	}
	e.mu.Lock()
	e.claimed[PRKey{"chainguard-dev", "mono", 2}] = true // already running
	e.mu.Unlock()
	ws := e.Status()
	assert.Equal(t, 1, ws.InFlight)
	assert.Equal(t, 1, ws.Queued, "running and merged PRs are not queued")
}

func TestPollIsScheduledByTheWallClock(t *testing.T) {
	now := t0
	a := strings.Repeat("a", 40)
	k := PRKey{"chainguard-dev", "stereo", 1}
	gh := &fakeGitHub{
		viewer: "kbsteere", hits: []SearchHit{{Key: k, NodeID: "N1", UpdatedAt: a}},
		details: map[string]PRDetail{"N1": detail(1, a)},
	}
	e := newTestEngine(t, gh, &now)
	assert.True(t, e.PollDue(now), "the first poll is due at once")
	_, next, err := e.Poll(t.Context())
	require.NoError(t, err)
	assert.Equal(t, e.d.Limits.PollInterval, next)
	assert.False(t, e.PollDue(now))
	assert.False(t, e.PollDue(now.Add(next-time.Second)))
	assert.True(t, e.PollDue(now.Add(next)))
	assert.Equal(t, now.Add(next), e.Status().NextPoll)

	// Fourteen hours asleep: by the wall clock the poll is long overdue.
	assert.True(t, e.PollDue(now.Add(14*time.Hour)))
	e.Woke(now.Add(time.Minute))
	assert.True(t, e.PollDue(now.Add(time.Minute)), "waking makes a poll due now")
}

func TestPollRetriesSoonAfterAFailure(t *testing.T) {
	now := t0
	gh := &fakeGitHub{
		viewer:    "kbsteere",
		searchErr: errors.New("dial tcp: lookup api.github.com: no such host"),
	}
	e := newTestEngine(t, gh, &now)
	_, next, err := e.Poll(t.Context())
	require.Error(t, err)
	assert.Equal(
		t,
		pollRetry,
		next,
		"a failed poll, such as one right after waking, retries in a minute",
	)
	assert.True(t, e.PollDue(now.Add(pollRetry)))
}

func TestPollSkipsWhileOneIsRunning(t *testing.T) {
	now := t0
	gh := &fakeGitHub{viewer: "kbsteere"}
	e := newTestEngine(t, gh, &now)
	e.mu.Lock()
	e.polling = true
	e.mu.Unlock()
	jobs, next, err := e.Poll(t.Context())
	require.NoError(t, err)
	assert.Empty(t, jobs)
	assert.Zero(t, next)
	assert.Empty(t, gh.filters, "no second poll runs alongside the first")
	assert.False(t, e.PollDue(now), "nothing is due while a poll runs")
}
