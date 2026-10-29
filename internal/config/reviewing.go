package config

import (
	"cmp"
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

// ReviewWatch is a set of authors and repos whose PRs dispatch reviews, and
// teams whose review requests it answers. A watch needs authors, teams, or
// both.
type ReviewWatch struct {
	Name    string   `yaml:"name"              validate:"required,watchname"`
	Authors []string `yaml:"authors,omitempty" validate:"dive,required"`
	Scopes  []string `yaml:"scopes"            validate:"required,min=1,dive,reviewscope"`
	// Teams are org/team slugs. A PR that requests review from any of them
	// is reviewed whoever wrote it, unless a bot did, with the slots the
	// authors' PRs leave free, until it is merged, closed, or back to draft.
	Teams []string `yaml:"teams,omitempty" validate:"dive,teamslug"`
	// TeamScopes narrow the search for team review requests; empty searches
	// each team's whole org.
	TeamScopes   []string `yaml:"teamScopes,omitempty"   validate:"dive,reviewscope"`
	DryRun       bool     `yaml:"dryRun"`
	Focus        []string `yaml:"focus,omitempty"`
	DesignRefs   []string `yaml:"designRefs,omitempty"`
	IgnoreLogins []string `yaml:"ignoreLogins,omitempty"`
	// PROnlyRepos are owner/repo names whose reviews cover only the PR's
	// own contents, for repositories that are mostly data.
	PROnlyRepos []string `yaml:"prOnlyRepos,omitempty" validate:"dive,reponame"`
	// NonBlockingChecks are CI checks whose failures never hold up an
	// approval, even where a repository marks them required.
	NonBlockingChecks []string        `yaml:"nonBlockingChecks,omitempty" validate:"dive,required"`
	Limits            ReviewingLimits `yaml:"limits,omitempty"`
	// Model and Effort override the global claude model and effort for
	// this watch's reviews.
	Model  string `yaml:"model,omitempty"  validate:"omitempty,claudemodel"`
	Effort string `yaml:"effort,omitempty" validate:"omitempty,claudeeffort"`
}

// ReviewingForConfig configures autonomous reviews of other people's PRs.
type ReviewingForConfig struct {
	Limits  ReviewingLimits `yaml:"limits,omitempty"`
	Watches []ReviewWatch   `yaml:"watches,omitempty" validate:"unique=Name,dive"`
	// Model and Effort are the claude model and effort every review runs
	// with, manual ones included; empty leaves the claude CLI's defaults.
	Model  string `yaml:"model,omitempty"  validate:"omitempty,claudemodel"`
	Effort string `yaml:"effort,omitempty" validate:"omitempty,claudeeffort"`
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
		MaxGithubPointsPerHour: 1500,
	}
}

// LimitsFor layers built-in defaults, then global limits, then the watch's
// own overrides.
func (c ReviewingForConfig) LimitsFor(w ReviewWatch) ReviewingLimits {
	out := DefaultReviewingLimits()
	for _, l := range []ReviewingLimits{c.Limits, w.Limits} {
		// Validation allows only zero (inherit) or positive values.
		out = ReviewingLimits{
			MaxBudgetUsd:           cmp.Or(l.MaxBudgetUsd, out.MaxBudgetUsd),
			MaxTurns:               cmp.Or(l.MaxTurns, out.MaxTurns),
			MaxTurnsLedgerOnly:     cmp.Or(l.MaxTurnsLedgerOnly, out.MaxTurnsLedgerOnly),
			RunTimeout:             cmp.Or(l.RunTimeout, out.RunTimeout),
			DailyBudgetUsd:         cmp.Or(l.DailyBudgetUsd, out.DailyBudgetUsd),
			MaxReviewsPerPrPerDay:  cmp.Or(l.MaxReviewsPerPrPerDay, out.MaxReviewsPerPrPerDay),
			MaxInFlight:            cmp.Or(l.MaxInFlight, out.MaxInFlight),
			PollInterval:           cmp.Or(l.PollInterval, out.PollInterval),
			MaxGithubPointsPerHour: cmp.Or(l.MaxGithubPointsPerHour, out.MaxGithubPointsPerHour),
		}
	}
	return out
}

// ReviewModel is the claude model and effort a review session runs with.
// Empty fields leave the claude CLI's defaults.
type ReviewModel struct {
	Name   string
	Effort string
}

// ModelFor is the model and effort for w's reviews: the watch's own
// settings, then the global ones. Manual reviews pass a zero watch.
func (c ReviewingForConfig) ModelFor(w ReviewWatch) ReviewModel {
	return ReviewModel{Name: cmp.Or(w.Model, c.Model), Effort: cmp.Or(w.Effort, c.Effort)}
}

// openFilter limits a search to PRs that are open, ready, and not archived.
const openFilter = "is:open draft:false archived:false"

// SearchFilter is the GitHub search for PRs by this watch's authors, or ""
// when it has none.
func (w ReviewWatch) SearchFilter() string {
	if len(w.Authors) == 0 {
		return ""
	}
	parts := []string{openFilter}
	parts = append(parts, w.Scopes...)
	for _, a := range w.Authors {
		parts = append(parts, "author:"+a)
	}
	return strings.Join(parts, " ")
}

// TeamFilter is the GitHub search for PRs that request review from any of
// this watch's teams, or "" when it has none.
func (w ReviewWatch) TeamFilter() string {
	if len(w.Teams) == 0 {
		return ""
	}
	scopes := slices.Clone(w.TeamScopes)
	if len(scopes) == 0 {
		for _, t := range w.Teams {
			org, _, _ := strings.Cut(t, "/")
			if s := "org:" + org; !slices.Contains(scopes, s) {
				scopes = append(scopes, s)
			}
		}
	}
	parts := append([]string{openFilter}, scopes...)
	for _, t := range w.Teams {
		parts = append(parts, "team-review-requested:"+t)
	}
	// The viewer's own PRs often request their team through CODEOWNERS.
	parts = append(parts, "-author:@me")
	return strings.Join(parts, " ")
}

// IsAuthor reports whether login is one of the watch's authors.
func (w ReviewWatch) IsAuthor(login string) bool {
	return slices.ContainsFunc(w.Authors, func(a string) bool {
		return strings.EqualFold(a, login)
	})
}

var (
	watchNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	scopeRe     = regexp.MustCompile(`^(repo:[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+|org:[A-Za-z0-9_.-]+)$`)
	repoNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
	teamSlugRe  = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9_.-]+$`)
	// modelRe accepts names such as claude-sonnet-5-5, sonnet, and opus[1m];
	// the leading character keeps a value from reading as a claude flag.
	modelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._\[\]-]{0,99}$`)
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

func validateTeamSlug(fl validator.FieldLevel) bool {
	return teamSlugRe.MatchString(fl.Field().String())
}

// validateReviewWatch requires a watch to have authors or teams to find
// PRs by.
func validateReviewWatch(sl validator.StructLevel) {
	w, ok := sl.Current().Interface().(ReviewWatch)
	if ok && len(w.Authors) == 0 && len(w.Teams) == 0 {
		sl.ReportError(w.Authors, "authors", "Authors", "authorsorteams", "")
	}
}

func validateClaudeModel(fl validator.FieldLevel) bool {
	return modelRe.MatchString(fl.Field().String())
}

// claudeEfforts are the levels claude --effort accepts.
var claudeEfforts = []string{"low", "medium", "high", "xhigh", "max"}

func validateClaudeEffort(fl validator.FieldLevel) bool {
	return slices.Contains(claudeEfforts, fl.Field().String())
}

// PROnly reports whether reviews of nameWithOwner cover only the PR's own
// contents.
func (w ReviewWatch) PROnly(nameWithOwner string) bool {
	return slices.ContainsFunc(w.PROnlyRepos, func(r string) bool {
		return strings.EqualFold(r, nameWithOwner)
	})
}
