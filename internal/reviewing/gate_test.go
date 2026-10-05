package reviewing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func cleanSnap() Snapshot {
	return Snapshot{
		State:        "OPEN",
		Author:       "dnegreira",
		Viewer:       "kbsteere",
		Mergeable:    "MERGEABLE",
		Head:         "h2",
		RequiredCI:   "SUCCESS",
		CodeReviewed: true,
	}
}

func TestDecide(t *testing.T) {
	tests := []struct {
		name       string
		mut        func(*Snapshot)
		tally      Tally
		wantAction Action
		wantStatus Status
	}{
		{"all clear approves", nil, Tally{}, ActionApprove, StatusApproved},
		{
			"already approved at head posts nothing",
			func(s *Snapshot) { s.ApprovedHead = "h2" },
			Tally{},
			ActionNone,
			StatusApproved,
		},
		{
			"approved older head re-approves",
			func(s *Snapshot) { s.ApprovedHead = "h1" },
			Tally{},
			ActionApprove,
			StatusApproved,
		},
		{
			"new blocking finding comments",
			nil,
			Tally{NewFindings: 1, NewBlocking: 1, OutstandingBlocking: 1},
			ActionComment,
			StatusChangesRequested,
		},
		{
			"new nit only approves with the comment",
			nil,
			Tally{NewFindings: 1, OutstandingNonBlocking: 1},
			ActionApprove,
			StatusApproved,
		},
		{
			"new nit while ci is pending only comments",
			func(s *Snapshot) { s.RequiredCI = "PENDING" },
			Tally{NewFindings: 1, OutstandingNonBlocking: 1},
			ActionComment,
			StatusWaitingOnCI,
		},
		{
			"blocking after own approval withdraws",
			func(s *Snapshot) { s.ApprovedHead = "h1" },
			Tally{NewFindings: 1, NewBlocking: 1, OutstandingBlocking: 1},
			ActionRequestChanges,
			StatusChangesRequested,
		},
		{
			"nit on a new head re-approves with the comment",
			func(s *Snapshot) { s.ApprovedHead = "h1" },
			Tally{NewFindings: 1, OutstandingNonBlocking: 1},
			ActionApprove,
			StatusApproved,
		},
		{
			"nit at the approved head only comments",
			func(s *Snapshot) { s.ApprovedHead = "h2" },
			Tally{NewFindings: 1, OutstandingNonBlocking: 1},
			ActionComment,
			StatusApproved,
		},
		{
			"outstanding old blocking, nothing new",
			nil,
			Tally{OutstandingBlocking: 1},
			ActionNone,
			StatusChangesRequested,
		},
		{
			"outstanding old nit, nothing new, approves",
			nil,
			Tally{OutstandingNonBlocking: 1},
			ActionApprove,
			StatusApproved,
		},
		{
			"code not reviewed at head never approves",
			func(s *Snapshot) { s.CodeReviewed = false },
			Tally{},
			ActionNone,
			StatusNeedsAttention,
		},
		{
			"code not reviewed at head with a nit only comments",
			func(s *Snapshot) { s.CodeReviewed = false },
			Tally{NewFindings: 1, OutstandingNonBlocking: 1},
			ActionComment,
			StatusNeedsAttention,
		},
		{
			"ci pending waits",
			func(s *Snapshot) { s.RequiredCI = "PENDING" },
			Tally{},
			ActionNone,
			StatusWaitingOnCI,
		},
		{
			"ci failing waits",
			func(s *Snapshot) { s.RequiredCI = "FAILURE" },
			Tally{},
			ActionNone,
			StatusWaitingOnCI,
		},
		{
			"conflict waits on author",
			func(s *Snapshot) { s.Mergeable = "CONFLICTING" },
			Tally{},
			ActionNone,
			StatusWaitingOnAuthor,
		},
		{
			"own PR never approves",
			func(s *Snapshot) { s.Author = "kbsteere" },
			Tally{},
			ActionNone,
			StatusWaitingOnAuthor,
		},
		{
			"own PR with blocking comments, never request changes",
			func(s *Snapshot) { s.Author = "kbsteere"; s.ApprovedHead = "h1" },
			Tally{NewFindings: 1, NewBlocking: 1, OutstandingBlocking: 1},
			ActionComment,
			StatusChangesRequested,
		},
		{
			"draft never approves",
			func(s *Snapshot) { s.IsDraft = true },
			Tally{},
			ActionNone,
			StatusWaitingOnAuthor,
		},
		{
			"closed never approves",
			func(s *Snapshot) { s.State = "MERGED" },
			Tally{},
			ActionNone,
			StatusWaitingOnAuthor,
		},
		{
			"others still block after approval",
			func(s *Snapshot) { s.OthersRequestChanges = true },
			Tally{},
			ActionApprove,
			StatusWaitingOnOthers,
		},
		{
			"unknown ci state never approves",
			func(s *Snapshot) { s.RequiredCI = "" },
			Tally{},
			ActionNone,
			StatusWaitingOnCI,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := cleanSnap()
			if tt.mut != nil {
				tt.mut(&s)
			}
			a, st := Decide(s, tt.tally)
			assert.Equal(t, tt.wantAction, a)
			assert.Equal(t, tt.wantStatus, st)
		})
	}
}
