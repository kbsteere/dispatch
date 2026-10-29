package reviewing

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kbsteere/dispatch/v4/internal/config"
)

const sustainingTooling = "chainguard-dev/sustaining-tooling-team-write"

func hit(repo string, n int) SearchHit {
	return SearchHit{
		Key:       PRKey{"chainguard-dev", repo, n},
		NodeID:    fmt.Sprintf("N%d", n),
		UpdatedAt: strings.Repeat(fmt.Sprint(n%10), 40),
	}
}

func botHit(repo string, n int) SearchHit {
	h := hit(repo, n)
	h.Bot = true
	return h
}

func keySet(keys ...PRKey) map[PRKey]struct{} {
	out := make(map[PRKey]struct{}, len(keys))
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out
}

func TestMergeHits(t *testing.T) {
	tests := map[string]struct {
		byAuthor, byTeam, kept []SearchHit
		want                   searchResult
	}{
		"authors only": {
			byAuthor: []SearchHit{hit("mono", 1)},
			want: searchResult{
				hits:          []SearchHit{hit("mono", 1)},
				teamOnly:      keySet(),
				teamRequested: keySet(),
			},
		},
		"teammate PR that also requests the team is not team-only": {
			byAuthor: []SearchHit{hit("mono", 1)},
			byTeam:   []SearchHit{hit("mono", 1), hit("stereo", 2)},
			want: searchResult{
				hits:          []SearchHit{hit("mono", 1), hit("stereo", 2)},
				teamOnly:      keySet(hit("stereo", 2).Key),
				teamRequested: keySet(hit("mono", 1).Key, hit("stereo", 2).Key),
			},
		},
		"bots requesting the team are dropped": {
			byTeam: []SearchHit{botHit("mono", 3), hit("mono", 4)},
			want: searchResult{
				hits:          []SearchHit{hit("mono", 4)},
				teamOnly:      keySet(hit("mono", 4).Key),
				teamRequested: keySet(hit("mono", 4).Key),
			},
		},
		"kept team PRs are team-only but no longer requested": {
			byTeam: []SearchHit{hit("mono", 4)},
			kept:   []SearchHit{hit("stereo", 5), hit("mono", 4)},
			want: searchResult{
				hits:          []SearchHit{hit("mono", 4), hit("stereo", 5)},
				teamOnly:      keySet(hit("mono", 4).Key, hit("stereo", 5).Key),
				teamRequested: keySet(hit("mono", 4).Key),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := mergeHits(tt.byAuthor, tt.byTeam, tt.kept)
			if diff := cmp.Diff(tt.want, got, cmp.AllowUnexported(searchResult{})); diff != "" {
				t.Errorf("mergeHits (-want, +got):\n%s", diff)
			}
		})
	}
}

func TestKeptTeamIDs(t *testing.T) {
	states := []PRState{
		{Key: hit("mono", 1).Key, NodeID: "N1", ViaTeam: true},
		{Key: hit("mono", 2).Key, NodeID: "N2", ViaTeam: true},
		{Key: hit("mono", 3).Key, NodeID: "N3", ViaTeam: true, Inactive: true},
		{Key: hit("mono", 4).Key, NodeID: "N4"},
		{Key: hit("mono", 5).Key, ViaTeam: true},
		{Key: hit("mono", 6).Key, NodeID: "N6", ViaTeam: true},
	}
	got := keptTeamIDs(states, []SearchHit{hit("mono", 6)}, []SearchHit{hit("mono", 1)})
	assert.Equal(t, []string{"N2"}, got,
		"only active team PRs neither search returned are looked up")
}

func TestByPriority(t *testing.T) {
	w := config.ReviewWatch{Authors: []string{"dnegreira", "powersj"}}
	in := []PRDetail{
		{NodeID: "outside-1", Author: "iainlane"},
		{NodeID: "mate-1", Author: "PowerSJ"},
		{NodeID: "outside-2", Author: "nskaggs"},
		{NodeID: "mate-2", Author: "dnegreira"},
	}
	var got []string
	for _, d := range byPriority(in, w) {
		got = append(got, d.NodeID)
	}
	assert.Equal(t, []string{"mate-1", "mate-2", "outside-1", "outside-2"}, got)
	assert.Equal(t, "outside-1", in[0].NodeID, "the input is not reordered")
}

// newTeamEngine is a watch over dnegreira's stereo PRs and review requests
// to the sustaining tooling team, past its first-start baseline.
func newTeamEngine(t *testing.T, gh *fakeGitHub, now *time.Time) *Engine {
	t.Helper()
	e := newTestEngine(t, gh, now)
	e.d.Watch.Teams = []string{sustainingTooling}
	require.NoError(t, e.store.MarkInitialized())
	return e
}

// teamDetail is an open PR by author found by the team search.
func teamDetail(n int, author string) PRDetail {
	h := hit("mono", n)
	d := detail(n, h.UpdatedAt)
	d.Key = h.Key
	d.Author = author
	return d
}

func TestPollTeamSearchUsesTeamFilter(t *testing.T) {
	now := t0
	gh := &fakeGitHub{viewer: "kbsteere", details: map[string]PRDetail{}}
	e := newTeamEngine(t, gh, &now)
	_, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	assert.Contains(t, gh.filters, e.d.Watch.TeamFilter())
	assert.Contains(t, gh.filters, e.d.Watch.SearchFilter())
}

func TestPollBaselinesFirstTeamSearch(t *testing.T) {
	now := t0
	old, mate := hit("mono", 1), hit("stereo", 2)
	gh := &fakeGitHub{
		viewer:   "kbsteere",
		hits:     []SearchHit{mate},
		teamHits: []SearchHit{old},
		details: map[string]PRDetail{
			old.NodeID:  teamDetail(1, "iainlane"),
			mate.NodeID: detail(2, mate.UpdatedAt),
		},
	}
	e := newTeamEngine(t, gh, &now)

	jobs, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, mate.Key, jobs[0].Key, "the watch's authors are past their baseline")
	st := stateOf(t, e, old.Key)
	assert.Equal(t, StatusBaseline, st.Status, "team PRs open before the first team search wait")
	assert.True(t, st.ViaTeam)
	assert.True(t, st.ReviewRequested)
	assert.True(t, e.store.TeamsInitialized())

	later := hit("mono", 3)
	gh.teamHits = append(gh.teamHits, later)
	gh.details[later.NodeID] = teamDetail(3, "nskaggs")
	jobs, _, err = e.Poll(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, later.Key, jobs[0].Key, "team PRs opened later are reviewed")
}

func TestPollTeammatesClaimSlotsFirst(t *testing.T) {
	now := t0
	gh := &fakeGitHub{viewer: "kbsteere", details: map[string]PRDetail{}}
	e := newTeamEngine(t, gh, &now)
	require.NoError(t, e.store.MarkTeamsInitialized())
	e.d.Limits.MaxInFlight = 2

	// The team search lists two outside PRs ahead of a teammate's PR in a
	// repo outside the watch's scopes.
	for n, author := range map[int]string{1: "iainlane", 2: "nskaggs"} {
		h := hit("mono", n)
		gh.teamHits = append(gh.teamHits, h)
		gh.details[h.NodeID] = teamDetail(n, author)
	}
	mate := hit("mono", 3)
	gh.teamHits = append(gh.teamHits, mate)
	gh.details[mate.NodeID] = teamDetail(3, "dnegreira")

	jobs, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 2)
	got := []PRKey{jobs[0].Key, jobs[1].Key}
	assert.Equal(t, mate.Key, got[0], "the teammate's PR claims a slot first")
	assert.Contains(t, []PRKey{hit("mono", 1).Key, hit("mono", 2).Key}, got[1],
		"an outside PR takes the slot left over")
}

func TestPollKeepsTeamPRUntilClosed(t *testing.T) {
	now := t0
	h := hit("mono", 1)
	gh := &fakeGitHub{
		viewer:   "kbsteere",
		teamHits: []SearchHit{h},
		details:  map[string]PRDetail{h.NodeID: teamDetail(1, "iainlane")},
	}
	e := newTeamEngine(t, gh, &now)
	_, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	require.True(t, stateOf(t, e, h.Key).ViaTeam)

	// A teammate's review answered the team's request, so the team search
	// no longer returns the PR.
	gh.teamHits = nil
	gh.openHits = map[string]SearchHit{h.NodeID: h}
	_, _, err = e.Poll(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []string{h.NodeID}, gh.keptAsked)
	st := stateOf(t, e, h.Key)
	assert.False(t, st.Inactive, "the PR stays watched while it is open")
	assert.False(t, st.ReviewRequested)

	// Merged: OpenHits no longer reports it.
	gh.openHits = nil
	d := gh.details[h.NodeID]
	d.State = "MERGED"
	gh.details[h.NodeID] = d
	_, _, err = e.Poll(context.Background())
	require.NoError(t, err)
	st = stateOf(t, e, h.Key)
	assert.True(t, st.Inactive)
	assert.Equal(t, "MERGED", st.ClosedState)

	gh.keptAsked = nil
	_, _, err = e.Poll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, gh.keptAsked, "a closed team PR is not looked up again")
}

func TestPollSkipsBotTeamPRs(t *testing.T) {
	now := t0
	bot := botHit("mono", 1)
	d := teamDetail(1, "dependabot")
	gh := &fakeGitHub{
		viewer:   "kbsteere",
		teamHits: []SearchHit{bot},
		details:  map[string]PRDetail{bot.NodeID: d},
	}
	e := newTeamEngine(t, gh, &now)
	require.NoError(t, e.store.MarkTeamsInitialized())
	jobs, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	assert.Empty(t, jobs)
	assert.Zero(t, gh.detailCalls, "a bot's PR is never read")
}

func TestPollTeamsOnlyWatch(t *testing.T) {
	now := t0
	h := hit("mono", 1)
	gh := &fakeGitHub{
		viewer:   "kbsteere",
		teamHits: []SearchHit{h},
		details:  map[string]PRDetail{h.NodeID: teamDetail(1, "iainlane")},
	}
	e := newTeamEngine(t, gh, &now)
	e.d.Watch.Authors = nil
	require.NoError(t, e.store.MarkTeamsInitialized())
	jobs, _, err := e.Poll(context.Background())
	require.NoError(t, err)
	require.Len(t, jobs, 1)
	assert.Equal(t, []string{e.d.Watch.TeamFilter()}, gh.filters,
		"a watch without authors runs only the team search")
}
