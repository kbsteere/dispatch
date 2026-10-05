package common

import (
	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/constants"
)

// ReviewStatusGlyph is the single-cell icon for a watched PR's status in
// the PR table. Icons follow the configured iconStyle.
func ReviewStatusGlyph(s reviewing.Status) string {
	switch s {
	case reviewing.StatusApproved:
		return constants.SuccessIcon
	case reviewing.StatusChangesRequested:
		return constants.ChangesRequestedIcon
	case reviewing.StatusWaitingOnAuthor, reviewing.StatusWaitingOnCI, reviewing.StatusWaitingOnOthers:
		return constants.WaitingIcon
	case reviewing.StatusNeedsAttention:
		return constants.ActionRequiredIcon
	case reviewing.StatusThrottled:
		return constants.BlockedIcon
	case reviewing.StatusFailed:
		return constants.FailureIcon
	case reviewing.StatusReviewing:
		return constants.DotIcon
	default:
		return ""
	}
}

var reviewStatusWords = map[reviewing.Status]string{
	reviewing.StatusBaseline:         "baseline (not reviewed)",
	reviewing.StatusReviewing:        "reviewing",
	reviewing.StatusApproved:         "approved",
	reviewing.StatusChangesRequested: "changes requested",
	reviewing.StatusWaitingOnAuthor:  "waiting on author",
	reviewing.StatusWaitingOnCI:      "waiting on CI",
	reviewing.StatusWaitingOnOthers:  "waiting on other reviewers",
	reviewing.StatusNeedsAttention:   "needs your attention",
	reviewing.StatusThrottled:        "throttled (daily review limit)",
	reviewing.StatusFailed:           "failed",
}

// ReviewStatusWord describes a watched PR's status in words.
func ReviewStatusWord(s reviewing.Status) string {
	if w, ok := reviewStatusWords[s]; ok {
		return w
	}
	return string(s)
}
