package reviewing

import (
	"context"
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
}

func (f *fakeGitHub) Viewer(context.Context) (string, error) { return f.viewer, nil }
func (f *fakeGitHub) Search(context.Context, string, int) ([]SearchHit, RateLimit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	hits := slices.Clone(f.hits)
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
	_, _, err := e.Poll(context.Background())
	require.NoError(t, err)

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
