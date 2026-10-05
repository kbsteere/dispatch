package constants

import (
	"charm.land/bubbles/v2/key"
)

type KeyMap struct {
	Up            key.Binding
	Down          key.Binding
	FirstItem     key.Binding
	LastItem      key.Binding
	TogglePreview key.Binding
	OpenGithub    key.Binding
	Refresh       key.Binding
	PageDown      key.Binding
	PageUp        key.Binding
	NextSection   key.Binding
	PrevSection   key.Binding
	Help          key.Binding
	Quit          key.Binding
}

type Dimensions struct {
	Width  int
	Height int
}

// Icons default to Nerd Font glyphs; ApplyTextIcons swaps every value for a
// standard-font equivalent when the iconStyle config option is "text".
var (
	ApprovedIcon         = "\U000F012C" // nf-md-check
	ChangesRequestedIcon = ""          // nf-cod-request_changes
	DotIcon              = ""          // nf-oct-dot_fill
	EmptyIcon            = ""          // nf-cod-circle_slash
	FailureIcon          = "\U000F0159" // nf-md-close_circle
	PersonIcon           = ""          // nf-oct-person
	SuccessIcon          = ""          // nf-fa-check_circle
	TeamIcon             = ""          // nf-fa-group
	WaitingIcon          = ""          // nf-seti-clock
	ActionRequiredIcon   = ""          // nf-cod-warning (matches GitHub UI)

	BehindIcon         = "\U000F01EE" // nf-md-clock_alert_outline
	BranchDeletedIcon  = "\U000F071B" // nf-md-source_branch_remove
	BlockedIcon        = ""          // nf-fa-warning
	ClosedIcon         = ""          // nf-oct-x_circle
	CodeReviewIcon     = ""          // nf-cod-code_review
	CommentIcon        = ""          // nf-fa-commenting_o
	CommentsIcon       = ""          // nf-fa-comments_o
	DraftIcon          = ""          // nf-cod-git_pull_request_draft
	CommitIcon         = ""          // nf-oct-git_commit
	VerticalCommitIcon = "\U000F0718" // nf-md-source_commit
	LabelsIcon         = "\U000F0316" // nf-md-label_multiple
	MergedIcon         = ""          // nf-oct-git_merge
	MergeQueueIcon     = ""          // nf-oct-git_merge_queue
	OpenIcon           = ""          // nf-oct-git_pull_request

	// New contributors: users who created a PR for the repo for the first time
	NewContributorIcon = "\U000F0394" // nf-md-new_box

	// Contributors: everyone who has contributed something back to the project
	ContributorIcon = "" // nf-fa-user_check

	// Collaborator is a person who isn't explicitly a member of your organization,
	// but who has Read, Write, or Admin permissions to one or more repositories in your organization.
	CollaboratorIcon = "" // nf-fa-user_shield

	// A member of the organization
	MemberIcon = "" // nf-oct-organization

	// The person/s who has administrative ownership over the organization or repository (not always the same as the original author)
	OwnerIcon = "" // nf-oct-shield_lock

	UnknownRoleIcon = "\U000F0B59" // nf-md-account_question

	// Notification type icons
	WorkflowIcon     = ""          // nf-oct-checklist (for CheckSuite/CI)
	WorkflowRunIcon  = ""          // nf-cod-workflow (for CheckSuite default)
	SecurityIcon     = "\U000F0483" // nf-md-shield_alert (for security alerts)
	NotificationIcon = ""          // nf-cod-bell (generic notification fallback)
	SearchIcon       = ""          // nf-fa-search

	BellIcon            = ""          // nf-fa-bell
	BellOutlineIcon     = ""          // nf-fa-bell_o
	IssueOpenedIcon     = ""          // nf-oct-issue_opened
	IssueClosedIcon     = ""          // nf-oct-issue_closed
	RepoIcon            = ""          // nf-oct-repo
	AssigneesIcon       = ""          // nf-oct-person_add
	UpdatedAtIcon       = "\U000F19BB" // nf-md-update
	CreatedAtIcon       = "\U000F1862" // nf-md-invoice_text_clock
	DiscussionIcon      = ""          // nf-oct-comment_discussion
	GitPullRequestIcon  = ""          // nf-oct-git_pull_request (notification subject)
	GitCommitIcon       = ""          // nf-oct-git_commit (notification subject)
	BookmarkIcon        = ""          // nf-fa-bookmark
	BranchIcon          = ""          // nf-dev-git_branch
	NumCommentsIcon     = "\U000F0BE2" // nf-md-comment_text_multiple
	ChecksIcon          = ""          // nf-oct-checkbox
	DiffIcon            = ""          // nf-oct-diff
	StopwatchIcon       = ""          // nf-oct-stopwatch
	EyeIcon             = "\U000F0208" // nf-md-eye
	RobotIcon           = "\U000F06A9" // nf-md-robot
	FileDiffIcon        = ""          // nf-oct-file_diff
	CommitHistoryIcon   = ""          // nf-dev-git_commit
	FileAddedIcon       = ""          // nf-oct-diff_added
	FileDeletedIcon     = ""          // nf-cod-diff_removed
	FileRenamedIcon     = ""          // nf-oct-diff_renamed
	FileCopiedIcon      = ""          // nf-fa-copy
	FileModifiedIcon    = ""          // nf-oct-diff_modified
	DiffAddedIcon       = ""          // nf-cod-diff_added
	DiffRemovedIcon     = ""          // nf-oct-diff_removed
	OverviewIcon        = ""          // nf-cod-preview
	BranchArrowIcon     = ""          // nf-weather-direction_right
	SummaryIcon         = ""          // nf-seti-doc
	TipIcon             = ""          // nf-fa-info_circle
	UpdateAvailableIcon = ""          // nf-cod-cloud_download
	SearchSectionIcon   = ""          // nf-oct-search
	ProcessingIcon      = ""          // nf-weather-hourglass
	PowerlineLeftIcon   = ""          // powerline left half circle
	PowerlineRightIcon  = ""          // powerline right half circle
	MarkdownLinkIcon    = ""          // nf-oct-link_external
	MarkdownImageIcon   = ""          // nf-oct-device_camera
)

// ApplyTextIcons replaces every Nerd Font icon with a plain-Unicode
// equivalent renderable by standard monospace fonts. Icons that only
// decorate an adjacent text label become empty strings.
func ApplyTextIcons() {
	ApprovedIcon = "✓"
	ChangesRequestedIcon = "✗"
	DotIcon = "•"
	EmptyIcon = ""
	FailureIcon = "✗"
	PersonIcon = "@"
	SuccessIcon = "✓"
	TeamIcon = "&"
	WaitingIcon = "◌"
	ActionRequiredIcon = "!"

	BehindIcon = "↓"
	BranchDeletedIcon = "✗"
	BlockedIcon = "⊘"
	ClosedIcon = "✗"
	CodeReviewIcon = ""
	CommentIcon = "»"
	CommentsIcon = "»"
	DraftIcon = "◐"
	CommitIcon = "•"
	VerticalCommitIcon = "•"
	LabelsIcon = ""
	MergedIcon = "◆"
	MergeQueueIcon = "≫"
	OpenIcon = "○"

	NewContributorIcon = "+"
	ContributorIcon = "·"
	CollaboratorIcon = "◇"
	MemberIcon = "●"
	OwnerIcon = "★"
	UnknownRoleIcon = "?"

	WorkflowIcon = "≡"
	WorkflowRunIcon = "▷"
	SecurityIcon = "!"
	NotificationIcon = "•"
	SearchIcon = "/"

	BellIcon = "●"
	BellOutlineIcon = "○"
	IssueOpenedIcon = "○"
	IssueClosedIcon = "●"
	RepoIcon = ""
	AssigneesIcon = "@"
	UpdatedAtIcon = "↻"
	CreatedAtIcon = "+"
	DiscussionIcon = "»"
	GitPullRequestIcon = "⇄"
	GitCommitIcon = "•"
	BookmarkIcon = "⚑"
	BranchIcon = ""
	NumCommentsIcon = "»"
	ChecksIcon = "✓"
	DiffIcon = "±"
	StopwatchIcon = "↻"
	EyeIcon = "◎"
	FileDiffIcon = ""
	CommitHistoryIcon = ""
	FileAddedIcon = "+"
	FileDeletedIcon = "-"
	FileRenamedIcon = "→"
	FileCopiedIcon = "⇒"
	FileModifiedIcon = "~"
	DiffAddedIcon = "+"
	DiffRemovedIcon = "-"
	OverviewIcon = ""
	BranchArrowIcon = "→"
	SummaryIcon = ""
	TipIcon = ""
	UpdateAvailableIcon = "↑"
	SearchSectionIcon = "/"
	ProcessingIcon = "…"
	PowerlineLeftIcon = " "
	PowerlineRightIcon = " "
	RobotIcon = "AI"
	MarkdownLinkIcon = "↗"
	MarkdownImageIcon = "□"
}

const (
	Ellipsis = "…"

	SmallDotIcon       = "⋅"
	HorizontalLineIcon = "─"
	SelectionIcon      = "→"

	AutocompleteColumnGap              = 2
	AutocompleteMinValueWidth          = 8
	AutocompleteMinDetailWidth         = 10
	AutocompletePreferredValueRatioNum = 2
	AutocompletePreferredValueRatioDen = 3

	// Prompts
	AssignPrompt   = "Assign users (whitespace-separated)" + Ellipsis
	UnassignPrompt = "Unassign users (whitespace-separated)" + Ellipsis
	CommentPrompt  = "Leave a comment" + Ellipsis
	ApprovalPrompt = "Approve with comment" + Ellipsis
	LabelPrompt    = "Add/remove labels (comma-separated)" + Ellipsis

	AddReviewRefPrompt = "PR to add to the review watch (owner/repo#N or URL), ctrl+d to submit" + Ellipsis
)

const Logo = `  ╷ )))
  │
┌─┴────────────┐    ╭──╮
│ ▁▃▅▇▅▃▁   ◉  │ ∿∿∿┤◉ │
│ ≡≡≡≡≡≡≡  ◉ ∘ │    ╰──╯
└─◌──────────◌─┘`
