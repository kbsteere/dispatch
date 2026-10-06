package config

import (
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/go-playground/validator/v10"
)

// ReviewingLimits bounds what a watch may spend. Zero values inherit from the
// global limits, then from DefaultReviewingLimits.
type ReviewingLimits struct {
	MaxBudgetUsd           float64       `yaml:"maxBudgetUsd,omitempty"           validate:"omitempty,gt=0"`
	MaxTurns               int           `yaml:"maxTurns,omitempty"               validate:"omitempty,gt=0"`
	MaxTurnsLedgerOnly     int           `yaml:"maxTurnsLedgerOnly,omitempty"     validate:"omitempty,gt=0"`
	RunTimeout             time.Duration `yaml:"runTimeout,omitempty"             validate:"omitempty,min=1m"`
	DailyBudgetUsd         float64       `yaml:"dailyBudgetUsd,omitempty"         validate:"omitempty,gt=0"`
	MaxReviewsPerPrPerDay  int           `yaml:"maxReviewsPerPrPerDay,omitempty"  validate:"omitempty,gt=0"`
	MaxInFlight            int           `yaml:"maxInFlight,omitempty"            validate:"omitempty,gt=0"`
	PollInterval           time.Duration `yaml:"pollInterval,omitempty"           validate:"omitempty,min=1m"`
	MaxGithubPointsPerHour int           `yaml:"maxGithubPointsPerHour,omitempty" validate:"omitempty,gt=0"`
}

// ReviewWatch is a set of authors and repos whose PRs dispatch reviews.
type ReviewWatch struct {
	Name         string   `yaml:"name"                   validate:"required,watchname"`
	Authors      []string `yaml:"authors"                validate:"required,min=1,dive,required"`
	Scopes       []string `yaml:"scopes"                 validate:"required,min=1,dive,reviewscope"`
	DryRun       bool     `yaml:"dryRun"`
	Focus        []string `yaml:"focus,omitempty"`
	DesignRefs   []string `yaml:"designRefs,omitempty"`
	IgnoreLogins []string `yaml:"ignoreLogins,omitempty"`
	// PROnlyRepos are owner/repo names whose reviews cover only the PR's
	// own contents, for repositories that are mostly data.
	PROnlyRepos []string        `yaml:"prOnlyRepos,omitempty" validate:"dive,reponame"`
	Limits      ReviewingLimits `yaml:"limits,omitempty"`
}

// ReviewingForConfig configures autonomous reviews of other people's PRs.
type ReviewingForConfig struct {
	Limits  ReviewingLimits `yaml:"limits,omitempty"`
	Watches []ReviewWatch   `yaml:"watches,omitempty" validate:"unique=Name,dive"`
}

func DefaultReviewingLimits() ReviewingLimits {
	return ReviewingLimits{
		MaxBudgetUsd:           10,
		MaxTurns:               100,
		MaxTurnsLedgerOnly:     15,
		RunTimeout:             25 * time.Minute,
		DailyBudgetUsd:         100,
		MaxReviewsPerPrPerDay:  6,
		MaxInFlight:            3,
		PollInterval:           3 * time.Minute,
		MaxGithubPointsPerHour: 300,
	}
}

// LimitsFor layers built-in defaults, then global limits, then the watch's
// own overrides.
func (c ReviewingForConfig) LimitsFor(w ReviewWatch) ReviewingLimits {
	out := DefaultReviewingLimits()
	for _, l := range []ReviewingLimits{c.Limits, w.Limits} {
		if l.MaxBudgetUsd > 0 {
			out.MaxBudgetUsd = l.MaxBudgetUsd
		}
		if l.MaxTurns > 0 {
			out.MaxTurns = l.MaxTurns
		}
		if l.MaxTurnsLedgerOnly > 0 {
			out.MaxTurnsLedgerOnly = l.MaxTurnsLedgerOnly
		}
		if l.RunTimeout > 0 {
			out.RunTimeout = l.RunTimeout
		}
		if l.DailyBudgetUsd > 0 {
			out.DailyBudgetUsd = l.DailyBudgetUsd
		}
		if l.MaxReviewsPerPrPerDay > 0 {
			out.MaxReviewsPerPrPerDay = l.MaxReviewsPerPrPerDay
		}
		if l.MaxInFlight > 0 {
			out.MaxInFlight = l.MaxInFlight
		}
		if l.PollInterval > 0 {
			out.PollInterval = l.PollInterval
		}
		if l.MaxGithubPointsPerHour > 0 {
			out.MaxGithubPointsPerHour = l.MaxGithubPointsPerHour
		}
	}
	return out
}

// SearchFilter is the GitHub search for this watch's reviewable PRs.
func (w ReviewWatch) SearchFilter() string {
	parts := []string{"is:open", "draft:false", "archived:false"}
	parts = append(parts, w.Scopes...)
	for _, a := range w.Authors {
		parts = append(parts, "author:"+a)
	}
	return strings.Join(parts, " ")
}

var (
	watchNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	scopeRe     = regexp.MustCompile(`^(repo:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+|org:[A-Za-z0-9_.-]+)$`)
	repoNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

func validateWatchName(fl validator.FieldLevel) bool {
	return watchNameRe.MatchString(fl.Field().String())
}

func validateReviewScope(fl validator.FieldLevel) bool {
	return scopeRe.MatchString(fl.Field().String())
}

func validateRepoName(fl validator.FieldLevel) bool {
	return repoNameRe.MatchString(fl.Field().String())
}

// PROnly reports whether reviews of nameWithOwner cover only the PR's own
// contents.
func (w ReviewWatch) PROnly(nameWithOwner string) bool {
	return slices.ContainsFunc(w.PROnlyRepos, func(r string) bool {
		return strings.EqualFold(r, nameWithOwner)
	})
}
