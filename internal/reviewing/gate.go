package reviewing

// Snapshot is the PR state the approval decision depends on.
type Snapshot struct {
	State                string
	IsDraft              bool
	Author               string
	Viewer               string
	Mergeable            string
	Head                 string
	RequiredCI           string
	ApprovedHead         string
	OthersRequestChanges bool
	// CodeReviewed reports that a full or incremental pass read the code at
	// Head. A comments-only pass never does, so it alone cannot approve.
	CodeReviewed bool
	// ReviewerApproves reports that the reviewer's verdict was approve.
	ReviewerApproves bool
	// CIExcused reports that every required check that has not passed is
	// stuck or was judged unrelated to the change; see ciExcuse.
	CIExcused bool
}

// Action is the single review event a pass posts.
type Action string

const (
	ActionNone           Action = "none"
	ActionComment        Action = "COMMENT"
	ActionApprove        Action = "APPROVE"
	ActionRequestChanges Action = "REQUEST_CHANGES"
)

// Tally counts findings after reconciling this pass with the ledger.
// Outstanding counts include this pass's new findings.
type Tally struct {
	NewFindings            int
	NewBlocking            int
	OutstandingBlocking    int
	OutstandingNonBlocking int
}

// Decide picks the single review action for a pass and the resulting status.
// It approves only an open, ready, mergeable PR by someone else whose code
// was reviewed at its head, whose reviewer said approve, whose required
// checks passed (or are excused as stuck or unrelated), and that has no
// outstanding blocking finding. Non-blocking findings are posted with the
// approval.
func Decide(s Snapshot, t Tally) (Action, Status) {
	self := s.Author == s.Viewer
	if t.OutstandingBlocking > 0 {
		switch {
		case t.NewBlocking > 0 && s.ApprovedHead != "" && !self:
			return ActionRequestChanges, StatusChangesRequested
		case t.NewFindings > 0:
			return ActionComment, StatusChangesRequested
		default:
			return ActionNone, StatusChangesRequested
		}
	}

	held := ActionNone
	if t.NewFindings > 0 {
		held = ActionComment
	}
	if s.State != "OPEN" || s.IsDraft || self || s.Mergeable == "CONFLICTING" {
		return held, StatusWaitingOnAuthor
	}
	if !s.CodeReviewed {
		return held, StatusNeedsAttention
	}
	if !s.ReviewerApproves {
		return held, StatusWaitingOnAuthor
	}
	if s.RequiredCI != "SUCCESS" && !s.CIExcused {
		return held, StatusWaitingOnCI
	}
	approved := StatusApproved
	if s.OthersRequestChanges {
		approved = StatusWaitingOnOthers
	}
	if s.ApprovedHead == s.Head {
		return held, approved
	}
	return ActionApprove, approved
}

// Blocker says why Decide does not approve s, or "" when it does. It checks
// in Decide's order; TestBlockerMatchesDecide keeps the two in step.
func Blocker(s Snapshot, t Tally) string {
	switch {
	case t.OutstandingBlocking > 0:
		return "blocking findings are still open"
	case s.State != "OPEN" || s.IsDraft:
		return "the PR is not open and ready"
	case s.Author == s.Viewer:
		return "it is your own PR"
	case s.Mergeable == "CONFLICTING":
		return "the PR has merge conflicts"
	case !s.CodeReviewed:
		return "the review did not read everything it had to at this commit"
	case !s.ReviewerApproves:
		return "Claude's verdict was not approve"
	case s.RequiredCI != "SUCCESS" && !s.CIExcused:
		return "required CI is not green"
	case s.ApprovedHead == s.Head:
		return "you already approved this commit"
	default:
		return ""
	}
}
