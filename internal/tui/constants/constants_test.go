package constants

import (
	"testing"
	"unicode"
)

// TestApplyTextIconsLeavesNoPrivateUseGlyphs verifies text mode replaces every
// Nerd Font glyph with characters standard fonts can render.
func TestApplyTextIconsLeavesNoPrivateUseGlyphs(t *testing.T) {
	ApplyTextIcons()

	icons := map[string]string{
		"ApprovedIcon":         ApprovedIcon,
		"ChangesRequestedIcon": ChangesRequestedIcon,
		"DotIcon":              DotIcon,
		"EmptyIcon":            EmptyIcon,
		"FailureIcon":          FailureIcon,
		"PersonIcon":           PersonIcon,
		"SuccessIcon":          SuccessIcon,
		"TeamIcon":             TeamIcon,
		"WaitingIcon":          WaitingIcon,
		"ActionRequiredIcon":   ActionRequiredIcon,
		"BehindIcon":           BehindIcon,
		"BranchDeletedIcon":    BranchDeletedIcon,
		"BlockedIcon":          BlockedIcon,
		"ClosedIcon":           ClosedIcon,
		"CodeReviewIcon":       CodeReviewIcon,
		"CommentIcon":          CommentIcon,
		"CommentsIcon":         CommentsIcon,
		"DonateIcon":           DonateIcon,
		"DraftIcon":            DraftIcon,
		"CommitIcon":           CommitIcon,
		"VerticalCommitIcon":   VerticalCommitIcon,
		"LabelsIcon":           LabelsIcon,
		"MergedIcon":           MergedIcon,
		"MergeQueueIcon":       MergeQueueIcon,
		"OpenIcon":             OpenIcon,
		"NewContributorIcon":   NewContributorIcon,
		"ContributorIcon":      ContributorIcon,
		"CollaboratorIcon":     CollaboratorIcon,
		"MemberIcon":           MemberIcon,
		"OwnerIcon":            OwnerIcon,
		"UnknownRoleIcon":      UnknownRoleIcon,
		"WorkflowIcon":         WorkflowIcon,
		"WorkflowRunIcon":      WorkflowRunIcon,
		"SecurityIcon":         SecurityIcon,
		"NotificationIcon":     NotificationIcon,
		"SearchIcon":           SearchIcon,
		"BellIcon":             BellIcon,
		"BellOutlineIcon":      BellOutlineIcon,
		"IssueOpenedIcon":      IssueOpenedIcon,
		"IssueClosedIcon":      IssueClosedIcon,
		"RepoIcon":             RepoIcon,
		"AssigneesIcon":        AssigneesIcon,
		"UpdatedAtIcon":        UpdatedAtIcon,
		"CreatedAtIcon":        CreatedAtIcon,
		"DiscussionIcon":       DiscussionIcon,
		"GitPullRequestIcon":   GitPullRequestIcon,
		"GitCommitIcon":        GitCommitIcon,
		"BookmarkIcon":         BookmarkIcon,
		"BranchIcon":           BranchIcon,
		"NumCommentsIcon":      NumCommentsIcon,
		"ChecksIcon":           ChecksIcon,
		"DiffIcon":             DiffIcon,
		"StopwatchIcon":        StopwatchIcon,
		"EyeIcon":              EyeIcon,
		"FileDiffIcon":         FileDiffIcon,
		"CommitHistoryIcon":    CommitHistoryIcon,
		"FileAddedIcon":        FileAddedIcon,
		"FileDeletedIcon":      FileDeletedIcon,
		"FileRenamedIcon":      FileRenamedIcon,
		"FileCopiedIcon":       FileCopiedIcon,
		"FileModifiedIcon":     FileModifiedIcon,
		"DiffAddedIcon":        DiffAddedIcon,
		"DiffRemovedIcon":      DiffRemovedIcon,
		"OverviewIcon":         OverviewIcon,
		"BranchArrowIcon":      BranchArrowIcon,
		"SummaryIcon":          SummaryIcon,
		"TipIcon":              TipIcon,
		"UpdateAvailableIcon":  UpdateAvailableIcon,
		"SearchSectionIcon":    SearchSectionIcon,
		"ProcessingIcon":       ProcessingIcon,
		"PowerlineLeftIcon":    PowerlineLeftIcon,
		"PowerlineRightIcon":   PowerlineRightIcon,
		"MarkdownLinkIcon":     MarkdownLinkIcon,
		"MarkdownImageIcon":    MarkdownImageIcon,
	}

	for name, icon := range icons {
		for _, r := range icon {
			if unicode.Is(unicode.Co, r) {
				t.Errorf("%s still contains private use rune %U after ApplyTextIcons", name, r)
			}
		}
	}
}
