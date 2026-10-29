package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const reviewingYAML = `
prSections:
  - title: Mine
    filters: is:open author:@me
reviewingFor:
  limits:
    maxBudgetUsd: 10
    runTimeout: 15m
  watches:
    - name: sustaining-automation
      authors: [tcnghia, powersj, dnegreira, AdamIsrael, ca-scribner, philroche, anushkamittal20, vishal-chdhry]
      scopes: [repo:chainguard-dev/mono, repo:chainguard-dev/stereo, repo:chainguard-dev/omnibump]
      dryRun: true
      prOnlyRepos: [chainguard-dev/stereo]
      nonBlockingChecks: [skillup]
      focus:
        - every new gRPC handler records a latency metric
      limits:
        maxInFlight: 2
`

func parseReviewingYAML(t *testing.T, body string) (Config, error) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))
	return ParseConfig(Location{ConfigFlag: p, SkipGlobalConfig: true})
}

func TestReviewingForParses(t *testing.T) {
	cfg, err := parseReviewingYAML(t, reviewingYAML)
	require.NoError(t, err)
	require.Len(t, cfg.ReviewingFor.Watches, 1)
	w := cfg.ReviewingFor.Watches[0]
	assert.Equal(t, "sustaining-automation", w.Name)
	assert.True(t, w.DryRun)
	assert.Len(t, w.Authors, 8)
	assert.Equal(t, []string{"every new gRPC handler records a latency metric"}, w.Focus)
	assert.Equal(t, 15*time.Minute, cfg.ReviewingFor.Limits.RunTimeout)
	assert.True(t, w.PROnly("chainguard-dev/stereo"))
	assert.True(t, w.PROnly("Chainguard-Dev/Stereo"), "GitHub names are case-insensitive")
	assert.False(t, w.PROnly("chainguard-dev/mono"))
	assert.Equal(t, []string{"skillup"}, w.NonBlockingChecks)

	lim := cfg.ReviewingFor.LimitsFor(w)
	assert.Equal(t, 10.0, lim.MaxBudgetUsd)
	assert.Equal(t, 100, lim.MaxTurns)
	assert.Equal(t, 15, lim.MaxTurnsLedgerOnly)
	assert.Equal(t, 15*time.Minute, lim.RunTimeout)
	assert.Equal(t, 100.0, lim.DailyBudgetUsd)
	assert.Equal(t, 6, lim.MaxReviewsPerPrPerDay)
	assert.Equal(t, 2, lim.MaxInFlight, "watch override wins")
	assert.Equal(t, 3*time.Minute, lim.PollInterval)
	assert.Equal(t, 1500, lim.MaxGithubPointsPerHour)
}

func TestReviewingForModel(t *testing.T) {
	cfg, err := parseReviewingYAML(t, `
prSections:
  - title: x
    filters: is:open
reviewingFor:
  model: claude-sonnet-5-5
  effort: high
  watches:
    - name: deep
      authors: [powersj]
      scopes: [repo:chainguard-dev/mono]
      model: opus[1m]
      effort: max
    - name: inherits
      authors: [tcnghia]
      scopes: [repo:chainguard-dev/stereo]
      effort: medium
`)
	require.NoError(t, err)
	rf := cfg.ReviewingFor
	require.Len(t, rf.Watches, 2)
	assert.Equal(t, ReviewModel{Name: "opus[1m]", Effort: "max"}, rf.ModelFor(rf.Watches[0]))
	assert.Equal(
		t,
		ReviewModel{Name: "claude-sonnet-5-5", Effort: "medium"},
		rf.ModelFor(rf.Watches[1]),
		"an unset watch model falls back to the global one",
	)
	assert.Equal(
		t,
		ReviewModel{Name: "claude-sonnet-5-5", Effort: "high"},
		rf.ModelFor(ReviewWatch{}),
		"manual reviews get the global settings",
	)

	cfg, err = parseReviewingYAML(t, reviewingYAML)
	require.NoError(t, err)
	assert.Zero(
		t,
		cfg.ReviewingFor.ModelFor(cfg.ReviewingFor.Watches[0]),
		"unset leaves the claude CLI defaults",
	)
}

func TestReviewingViewDefault(t *testing.T) {
	cfg, err := parseReviewingYAML(t, "defaults:\n  view: reviewing\n"+reviewingYAML)
	require.NoError(t, err)
	assert.Equal(t, ReviewingView, cfg.Defaults.View)

	cfg, err = parseReviewingYAML(
		t,
		"defaults:\n  view: reviewing\nprSections:\n  - title: x\n    filters: is:open\n",
	)
	require.NoError(t, err)
	assert.Equal(t, PRsView, cfg.Defaults.View, "falls back to PRs without watches")
}

func TestViewTypeIsPRLike(t *testing.T) {
	assert.True(t, PRsView.IsPRLike())
	assert.True(t, ReviewingView.IsPRLike())
	assert.False(t, IssuesView.IsPRLike())
	assert.False(t, NotificationsView.IsPRLike())
	assert.False(t, RepoView.IsPRLike())
}

func TestReviewingForAbsentIsEmpty(t *testing.T) {
	cfg, err := parseReviewingYAML(t, "prSections:\n  - title: x\n    filters: is:open\n")
	require.NoError(t, err)
	assert.Empty(t, cfg.ReviewingFor.Watches)
}

func TestReviewingForSearchFilter(t *testing.T) {
	w := ReviewWatch{
		Authors: []string{"tcnghia", "powersj"},
		Scopes:  []string{"repo:chainguard-dev/mono", "repo:chainguard-dev/stereo"},
	}
	assert.Equal(
		t,
		"is:open draft:false archived:false repo:chainguard-dev/mono repo:chainguard-dev/stereo author:tcnghia author:powersj",
		w.SearchFilter(),
	)
}

func TestReviewingForSearchFilterWithoutAuthors(t *testing.T) {
	w := ReviewWatch{
		Teams:  []string{"chainguard-dev/sustaining-tooling-team-write"},
		Scopes: []string{"repo:chainguard-dev/mono"},
	}
	assert.Empty(t, w.SearchFilter())
}

func TestReviewingForTeamFilter(t *testing.T) {
	tests := map[string]struct {
		w    ReviewWatch
		want string
	}{
		"no teams": {
			w:    ReviewWatch{Authors: []string{"tcnghia"}, Scopes: []string{"repo:chainguard-dev/mono"}},
			want: "",
		},
		"each team's org once": {
			w: ReviewWatch{
				Scopes: []string{"repo:chainguard-dev/mono"},
				Teams: []string{
					"chainguard-dev/sustaining-tooling-team-write",
					"chainguard-dev/sustaining-automation-team-write",
					"wolfi-dev/maintainers",
				},
			},
			want: "is:open draft:false archived:false org:chainguard-dev org:wolfi-dev " +
				"team-review-requested:chainguard-dev/sustaining-tooling-team-write " +
				"team-review-requested:chainguard-dev/sustaining-automation-team-write " +
				"team-review-requested:wolfi-dev/maintainers",
		},
		"team scopes": {
			w: ReviewWatch{
				Scopes:     []string{"repo:chainguard-dev/mono"},
				Teams:      []string{"chainguard-dev/sustaining-tooling-team-write"},
				TeamScopes: []string{"repo:chainguard-dev/stereo"},
			},
			want: "is:open draft:false archived:false repo:chainguard-dev/stereo " +
				"team-review-requested:chainguard-dev/sustaining-tooling-team-write",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.w.TeamFilter())
		})
	}
}

func TestReviewingForIsAuthor(t *testing.T) {
	w := ReviewWatch{Authors: []string{"AdamIsrael", "powersj"}}
	assert.True(t, w.IsAuthor("adamisrael"))
	assert.True(t, w.IsAuthor("powersj"))
	assert.False(t, w.IsAuthor("gaughen"))
	assert.False(t, ReviewWatch{}.IsAuthor(""))
}

func TestReviewingForTeamsOnlyWatch(t *testing.T) {
	cfg, err := parseReviewingYAML(
		t,
		"prSections:\n  - title: x\n    filters: is:open\n"+
			"reviewingFor:\n  watches:\n    - name: w\n      teams: [chainguard-dev/sustaining-tooling-team-write]\n      scopes: [repo:o/r]\n",
	)
	require.NoError(t, err)
	require.Len(t, cfg.ReviewingFor.Watches, 1)
	assert.Equal(
		t,
		[]string{"chainguard-dev/sustaining-tooling-team-write"},
		cfg.ReviewingFor.Watches[0].Teams,
	)
}

func TestReviewingForValidation(t *testing.T) {
	tests := map[string]struct{ body, rule string }{
		"bad name": {
			"reviewingFor:\n  watches:\n    - name: ../x\n      authors: [a]\n      scopes: [repo:o/r]\n",
			"'watchname' tag",
		},
		"no authors or teams": {
			"reviewingFor:\n  watches:\n    - name: w\n      authors: []\n      scopes: [repo:o/r]\n",
			"'authors' failed on the 'authorsorteams' tag",
		},
		"team without org": {
			"reviewingFor:\n  watches:\n    - name: w\n      teams: [sustaining-tooling-team-write]\n      scopes: [repo:o/r]\n",
			"'teamslug' tag",
		},
		"team with a qualifier": {
			"reviewingFor:\n  watches:\n    - name: w\n      teams: ['chainguard-dev/t author:x']\n      scopes: [repo:o/r]\n",
			"'teamslug' tag",
		},
		"bad team scope": {
			"reviewingFor:\n  watches:\n    - name: w\n      teams: [chainguard-dev/t]\n      teamScopes: [chainguard-dev]\n      scopes: [repo:o/r]\n",
			"'reviewscope' tag",
		},
		"bad scope": {
			"reviewingFor:\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [mono]\n",
			"'reviewscope' tag",
		},
		"short interval": {
			"reviewingFor:\n  limits:\n    pollInterval: 30s\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n",
			"'pollInterval' failed on the 'min' tag",
		},
		"negative budget": {
			"reviewingFor:\n  limits:\n    maxBudgetUsd: -1\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n",
			"'maxBudgetUsd' failed on the 'gt' tag",
		},
		"bad pr-only repo": {
			"reviewingFor:\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n      prOnlyRepos: [stereo]\n",
			"'reponame' tag",
		},
		"bad effort": {
			"reviewingFor:\n  effort: extreme\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n",
			"'claudeeffort' tag",
		},
		"model read as a flag": {
			"reviewingFor:\n  model: --dangerously-skip-permissions\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n",
			"'claudemodel' tag",
		},
		"watch model with a space": {
			"reviewingFor:\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n      model: sonnet --tools Bash\n",
			"'claudemodel' tag",
		},
		"duplicate names": {
			"reviewingFor:\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n    - name: w\n      authors: [b]\n      scopes: [repo:o/r]\n",
			"'unique' tag",
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseReviewingYAML(
				t,
				"prSections:\n  - title: x\n    filters: is:open\n"+tt.body,
			)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.rule)
		})
	}
}

func TestDefaultRunTimeoutCoversDeepReviews(t *testing.T) {
	assert.Equal(t, 25*time.Minute, DefaultReviewingLimits().RunTimeout)
}
