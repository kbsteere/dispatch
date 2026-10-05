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
// It approves only an open, ready, mergeable PR by someone else whose
// required checks passed and that has nothing outstanding.
func Decide(s Snapshot, t Tally) (Action, Status) {
	self := s.Author == s.Viewer
	outstanding := StatusWaitingOnAuthor
	if t.OutstandingBlocking > 0 {
		outstanding = StatusChangesRequested
	}

	if t.NewFindings > 0 {
		if t.NewBlocking > 0 && s.ApprovedHead != "" && !self {
			return ActionRequestChanges, StatusChangesRequested
		}
		return ActionComment, outstanding
	}
	if t.OutstandingBlocking+t.OutstandingNonBlocking > 0 {
		return ActionNone, outstanding
	}
	if s.State != "OPEN" || s.IsDraft || self || s.Mergeable == "CONFLICTING" {
		return ActionNone, StatusWaitingOnAuthor
	}
	if s.RequiredCI != "SUCCESS" {
		return ActionNone, StatusWaitingOnCI
	}
	approved := StatusApproved
	if s.OthersRequestChanges {
		approved = StatusWaitingOnOthers
	}
	if s.ApprovedHead == s.Head {
		return ActionNone, approved
	}
	return ActionApprove, approved
}
