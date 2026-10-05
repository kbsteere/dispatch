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

	lim := cfg.ReviewingFor.LimitsFor(w)
	assert.Equal(t, 10.0, lim.MaxBudgetUsd)
	assert.Equal(t, 100, lim.MaxTurns)
	assert.Equal(t, 15, lim.MaxTurnsLedgerOnly)
	assert.Equal(t, 15*time.Minute, lim.RunTimeout)
	assert.Equal(t, 100.0, lim.DailyBudgetUsd)
	assert.Equal(t, 6, lim.MaxReviewsPerPrPerDay)
	assert.Equal(t, 2, lim.MaxInFlight, "watch override wins")
	assert.Equal(t, 3*time.Minute, lim.PollInterval)
	assert.Equal(t, 300, lim.MaxGithubPointsPerHour)
}

func TestReviewingForAbsentIsEmpty(t *testing.T) {
	cfg, err := parseReviewingYAML(t, "prSections:\n  - title: x\n    filters: is:open\n")
	require.NoError(t, err)
	assert.Empty(t, cfg.ReviewingFor.Watches)
}

func TestReviewingForSearchFilter(t *testing.T) {
	w := ReviewWatch{Authors: []string{"tcnghia", "powersj"}, Scopes: []string{"repo:chainguard-dev/mono", "repo:chainguard-dev/stereo"}}
	assert.Equal(t,
		"is:open draft:false archived:false repo:chainguard-dev/mono repo:chainguard-dev/stereo author:tcnghia author:powersj",
		w.SearchFilter())
}

func TestReviewingForValidation(t *testing.T) {
	tests := map[string]struct{ body, rule string }{
		"bad name":        {"reviewingFor:\n  watches:\n    - name: ../x\n      authors: [a]\n      scopes: [repo:o/r]\n", "'watchname' tag"},
		"no authors":      {"reviewingFor:\n  watches:\n    - name: w\n      authors: []\n      scopes: [repo:o/r]\n", "'authors' failed on the 'min' tag"},
		"bad scope":       {"reviewingFor:\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [mono]\n", "'reviewscope' tag"},
		"short interval":  {"reviewingFor:\n  limits:\n    pollInterval: 30s\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n", "'pollInterval' failed on the 'min' tag"},
		"negative budget": {"reviewingFor:\n  limits:\n    maxBudgetUsd: -1\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n", "'maxBudgetUsd' failed on the 'gt' tag"},
		"duplicate names": {"reviewingFor:\n  watches:\n    - name: w\n      authors: [a]\n      scopes: [repo:o/r]\n    - name: w\n      authors: [b]\n      scopes: [repo:o/r]\n", "'unique' tag"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := parseReviewingYAML(t, "prSections:\n  - title: x\n    filters: is:open\n"+tt.body)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.rule)
		})
	}
}
