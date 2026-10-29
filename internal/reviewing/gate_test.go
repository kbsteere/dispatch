package reviewing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func cleanSnap() Snapshot {
	return Snapshot{
		State:            "OPEN",
		Author:           "dnegreira",
		Viewer:           "kbsteere",
		Mergeable:        "MERGEABLE",
		Head:             "h2",
		RequiredCI:       "SUCCESS",
		CodeReviewed:     true,
		ReviewerApproves: true,
		Trusted:          true,
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
			"someone else's clean PR waits for the viewer",
			func(s *Snapshot) { s.Trusted = false },
			Tally{},
			ActionNone,
			StatusNeedsAttention,
		},
		{
			"someone else's PR gets its nits as a comment",
			func(s *Snapshot) { s.Trusted = false },
			Tally{NewFindings: 1, OutstandingNonBlocking: 1},
			ActionComment,
			StatusNeedsAttention,
		},
		{
			"someone else's PR never gets changes requested",
			func(s *Snapshot) { s.Trusted, s.ApprovedHead = false, "h1" },
			Tally{NewFindings: 1, NewBlocking: 1, OutstandingBlocking: 1},
			ActionComment,
			StatusChangesRequested,
		},
		{
			"someone else's PR still waits on CI first",
			func(s *Snapshot) { s.Trusted, s.RequiredCI = false, "PENDING" },
			Tally{},
			ActionNone,
			StatusWaitingOnCI,
		},
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
			"reviewer did not say approve",
			func(s *Snapshot) { s.ReviewerApproves = false },
			Tally{},
			ActionNone,
			StatusWaitingOnAuthor,
		},
		{
			"reviewer did not say approve, only nits",
			func(s *Snapshot) { s.ReviewerApproves = false },
			Tally{NewFindings: 1, OutstandingNonBlocking: 1},
			ActionComment,
			StatusWaitingOnAuthor,
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

func TestBlockerMatchesDecide(t *testing.T) {
	muts := map[string]func(*Snapshot, *Tally){
		"clear":           func(*Snapshot, *Tally) {},
		"blocking open":   func(_ *Snapshot, t *Tally) { t.OutstandingBlocking = 1 },
		"nit open":        func(_ *Snapshot, t *Tally) { t.OutstandingNonBlocking, t.NewFindings = 1, 1 },
		"merged":          func(s *Snapshot, _ *Tally) { s.State = "MERGED" },
		"draft":           func(s *Snapshot, _ *Tally) { s.IsDraft = true },
		"own PR":          func(s *Snapshot, _ *Tally) { s.Author = s.Viewer },
		"conflict":        func(s *Snapshot, _ *Tally) { s.Mergeable = "CONFLICTING" },
		"code not read":   func(s *Snapshot, _ *Tally) { s.CodeReviewed = false },
		"no approve":      func(s *Snapshot, _ *Tally) { s.ReviewerApproves = false },
		"ci pending":      func(s *Snapshot, _ *Tally) { s.RequiredCI = "PENDING" },
		"ci excused":      func(s *Snapshot, _ *Tally) { s.RequiredCI, s.CIExcused = "FAILURE", true },
		"approved here":   func(s *Snapshot, _ *Tally) { s.ApprovedHead = s.Head },
		"others blocking": func(s *Snapshot, _ *Tally) { s.OthersRequestChanges = true },
		"outside author":  func(s *Snapshot, _ *Tally) { s.Trusted = false },
	}
	for name, mut := range muts {
		t.Run(name, func(t *testing.T) {
			s, tally := cleanSnap(), Tally{}
			mut(&s, &tally)
			action, _ := Decide(s, tally)
			assert.Equal(t, action == ActionApprove, Blocker(s, tally) == "",
				"Decide gave %s; Blocker said %q", action, Blocker(s, tally))
		})
	}
}
