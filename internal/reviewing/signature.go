package reviewing

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"time"
)

const (
	// HeadSettle is how long a new head must be seen before it triggers a
	// pass, so review bots that post on every push land first.
	HeadSettle = 150 * time.Second
	// CIStablePolls is how many consecutive polls a terminal CI result must
	// hold before it triggers a pass.
	CIStablePolls = 2
	// CICooldown is the minimum time between CI-only passes on one PR.
	CICooldown = 10 * time.Minute
)

// Event is one piece of PR activity: a comment (c), review (r), or review
// thread reply (t).
type Event struct {
	At    time.Time
	Kind  string
	Login string
}

// Trigger says why a PR needs a pass.
type Trigger string

const (
	TriggerNone   Trigger = ""
	TriggerChange Trigger = "change"
	TriggerCI     Trigger = "ci"
	// TriggerRetry retries a pass that was incomplete once its wait is over.
	TriggerRetry Trigger = "retry"
)

// ActivityDigest fingerprints activity by others. Only the newest foreign
// event is hashed, plus the count of threads others resolved, so dispatch's
// own posts never change it and a fetch window that drops older events does
// not either.
func ActivityDigest(events []Event, resolvedBy []string, ignore map[string]struct{}) string {
	var newest *Event
	for i := range events {
		e := events[i]
		if _, skip := ignore[e.Login]; skip {
			continue
		}
		if newest == nil || e.At.After(newest.At) {
			newest = &e
		}
	}
	resolved := 0
	for _, who := range resolvedBy {
		if _, skip := ignore[who]; !skip {
			resolved++
		}
	}
	key := "-"
	if newest != nil {
		key = fmt.Sprintf(
			"%s:%s:%s",
			newest.At.UTC().Format(time.RFC3339),
			newest.Kind,
			newest.Login,
		)
	}
	sum := sha1.Sum([]byte(fmt.Sprintf("%s\nresolved:%d", key, resolved)))
	return hex.EncodeToString(sum[:])[:12]
}

// ObserveHead records when a head was first seen.
func ObserveHead(st *PRState, head string, now time.Time) {
	if st.HeadSeen == nil || st.HeadSeen.OID != head {
		st.HeadSeen = &HeadSeen{OID: head, At: now}
	}
}

// ObserveCI counts consecutive polls the required-check state has held.
func ObserveCI(st *PRState, ci string) {
	if st.CIObs.State == ci {
		st.CIObs.N++
		return
	}
	st.CIObs = CIObs{State: ci, N: 1}
}

// terminalCI reports a CI state that will not change on its own soon:
// passed, failed, or stuck.
func terminalCI(ci string) bool { return ci == "SUCCESS" || ci == "FAILURE" || ci == CIStuck }

// NeedsReview decides whether the current signature warrants a pass.
func NeedsReview(st *PRState, sig Signature, now time.Time) Trigger {
	rs := st.ReviewedSig
	if rs == nil {
		return TriggerChange
	}
	if sig.Head != rs.Head {
		if st.HeadSeen != nil && st.HeadSeen.OID == sig.Head &&
			now.Sub(st.HeadSeen.At) < HeadSettle {
			return TriggerNone
		}
		return TriggerChange
	}
	if sig.Draft != rs.Draft || sig.Digest != rs.Digest {
		return TriggerChange
	}
	if len(st.ReviewGaps) > 0 && !now.Before(st.Backoff.Until) {
		return TriggerRetry
	}
	if !terminalCI(sig.CI) || sig.CI == rs.CI || st.Status != StatusWaitingOnCI {
		return TriggerNone
	}
	if st.CIObs.State != sig.CI || st.CIObs.N < CIStablePolls {
		return TriggerNone
	}
	if !st.LastCIWake.IsZero() && now.Sub(st.LastCIWake) < CICooldown {
		return TriggerNone
	}
	return TriggerCI
}
