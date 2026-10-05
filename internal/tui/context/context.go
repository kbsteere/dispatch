package context

import (
	"time"

	tea "charm.land/bubbletea/v2"

	gitm "github.com/aymanbagabas/git-module"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/kbsteere/dispatch/v4/internal/config"
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/constants"
	"github.com/kbsteere/dispatch/v4/internal/tui/theme"
	"github.com/kbsteere/dispatch/v4/internal/utils"
)

type State = int

const (
	TaskStart State = iota
	TaskFinished
	TaskError
)

type Task struct {
	Id           string
	StartText    string
	FinishedText string
	State        State
	Error        error
	StartTime    time.Time
	FinishedTime *time.Time
}

type ProgramContext struct {
	GHRepo               *repository.Repository
	GitRepo              *gitm.Repository
	RepoPath             string
	RepoUrl              string
	User                 string
	ScreenHeight         int
	ScreenWidth          int
	MainContentWidth     int
	MainContentHeight    int
	DynamicPreviewWidth  int
	DynamicPreviewHeight int    // calculated preview height for bottom mode
	PreviewPosition      string // resolved "right" or "bottom"
	SidebarOpen          bool
	HasDarkBackground    bool
	BackgroundSource     string
	Config               *config.Config
	ConfigFlag           string
	Version              string
	View                 config.ViewType
	Error                error
	StartTask            func(task Task) tea.Cmd
	Theme                theme.Theme
	Styles               Styles
	// ClaudeReviews tracks background Claude review runs keyed by PR URL.
	// It's only accessed from the bubbletea update loop.
	ClaudeReviews map[string]*ClaudeReview
	// WatchPRs is the reviewingFor state of watched PRs keyed by PR URL.
	// It is replaced wholesale from the bubbletea update loop.
	WatchPRs map[string]reviewing.PRState
	// WatchFooter summarises the reviewingFor watches for the footer.
	WatchFooter string
}

type ClaudeReviewState int

const (
	ClaudeReviewRunning ClaudeReviewState = iota + 1
	ClaudeReviewDone
	ClaudeReviewFailed
)

type ClaudeReview struct {
	State     ClaudeReviewState
	Output    string
	SessionId string
	Err       error
}

func (ctx *ProgramContext) HasGHRepo() bool {
	return ctx.GHRepo != nil && *ctx.GHRepo != (repository.Repository{})
}

func (ctx *ProgramContext) GetViewSectionsConfig() []config.SectionConfig {
	var configs []config.SectionConfig
	switch ctx.View {
	case config.RepoView:
		t := config.RepoView
		configs = append(configs, config.PrsSectionConfig{
			Title:   "Local Branches",
			Filters: "author:@me is:open",
			Limit:   utils.IntPtr(20),
			Type:    &t,
		}.ToSectionConfig())
	case config.NotificationsView:
		for _, cfg := range ctx.Config.NotificationsSections {
			configs = append(configs, cfg.ToSectionConfig())
		}
	case config.PRsView:
		for _, cfg := range ctx.Config.PRSections {
			configs = append(configs, cfg.ToSectionConfig())
		}
	case config.ReviewingView:
		// One table per watch and no search section.
		for _, w := range ctx.Config.ReviewingFor.Watches {
			configs = append(configs, config.SectionConfig{Title: w.Name})
		}
		return configs
	case config.IssuesView:
		for _, cfg := range ctx.Config.IssuesSections {
			configs = append(configs, cfg.ToSectionConfig())
		}
	}

	return append([]config.SectionConfig{{Title: constants.SearchIcon}}, configs...)
}

func (ctx *ProgramContext) PreviewCursorPosition() tea.Position {
	if ctx.PreviewPosition == "right" {
		return tea.Position{
			X: ctx.MainContentWidth,
			Y: ctx.Styles.Pager.Height,
		}
	}

	return tea.Position{
		X: 0,
		Y: ctx.MainContentHeight,
	}
}
