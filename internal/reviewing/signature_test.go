package reviewing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var t0 = time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)

func TestActivityDigestIgnoresOwnActivity(t *testing.T) {
	ignore := map[string]bool{"kbsteere": true}
	before := []Event{{At: t0, Kind: "c", Login: "dnegreira"}}
	after := append(
		append([]Event{}, before...),
		Event{At: t0.Add(time.Minute), Kind: "r", Login: "kbsteere"},
	)
	assert.Equal(t, ActivityDigest(before, nil, ignore), ActivityDigest(after, nil, ignore))
}

func TestActivityDigestWindowEviction(t *testing.T) {
	// mono#54695: our approval pushed an older depthfirst review out of the
	// reviews(last:50) window. Only the newest foreign event matters, so
	// losing an older one must not change the digest.
	ignore := map[string]bool{"kbsteere": true}
	full := []Event{
		{At: t0, Kind: "r", Login: "depthfirst-app"},
		{At: t0.Add(time.Hour), Kind: "c", Login: "dnegreira"},
	}
	evicted := full[1:]
	assert.Equal(t, ActivityDigest(full, nil, ignore), ActivityDigest(evicted, nil, ignore))
}

func TestActivityDigestChangesOnForeignEventAndResolution(t *testing.T) {
	ignore := map[string]bool{"kbsteere": true}
	base := []Event{{At: t0, Kind: "c", Login: "dnegreira"}}
	d0 := ActivityDigest(base, nil, ignore)
	assert.Len(t, d0, 12)
	assert.NotEqual(
		t,
		d0,
		ActivityDigest(
			append(
				append([]Event{}, base...),
				Event{At: t0.Add(time.Minute), Kind: "t", Login: "powersj"},
			),
			nil,
			ignore,
		),
	)
	assert.NotEqual(t, d0, ActivityDigest(base, []string{"dnegreira"}, ignore))
	assert.Equal(
		t,
		d0,
		ActivityDigest(base, []string{"kbsteere"}, ignore),
		"our own resolutions are ignored",
	)
	assert.Equal(
		t,
		ActivityDigest(nil, nil, ignore),
		ActivityDigest([]Event{{At: t0, Kind: "c", Login: "kbsteere"}}, nil, ignore),
	)
}

func TestNeedsReview(t *testing.T) {
	sig := Signature{Head: "h1", Digest: "d1", CI: "SUCCESS"}

	t.Run("never reviewed", func(t *testing.T) {
		st := &PRState{}
		assert.Equal(t, TriggerChange, NeedsReview(st, sig, t0))
	})
	t.Run("unchanged", func(t *testing.T) {
		st := &PRState{ReviewedSig: &sig}
		assert.Equal(t, TriggerNone, NeedsReview(st, sig, t0))
	})
	t.Run("new head waits to settle", func(t *testing.T) {
		st := &PRState{ReviewedSig: &sig}
		next := Signature{Head: "h2", Digest: "d1", CI: "PENDING"}
		ObserveHead(st, "h2", t0)
		ObserveHead(st, "h2", t0.Add(time.Minute))
		assert.Equal(t, t0, st.HeadSeen.At, "first sighting is kept")
		assert.Equal(t, TriggerNone, NeedsReview(st, next, t0.Add(100*time.Second)))
		assert.Equal(t, TriggerChange, NeedsReview(st, next, t0.Add(151*time.Second)))
	})
	t.Run("draft flip", func(t *testing.T) {
		draft := Signature{Head: "h1", Digest: "d1", CI: "SUCCESS", Draft: true}
		st := &PRState{ReviewedSig: &draft}
		assert.Equal(t, TriggerChange, NeedsReview(st, sig, t0))
	})
	t.Run("foreign activity", func(t *testing.T) {
		st := &PRState{ReviewedSig: &sig}
		assert.Equal(
			t,
			TriggerChange,
			NeedsReview(st, Signature{Head: "h1", Digest: "d2", CI: "SUCCESS"}, t0),
		)
	})
	t.Run("ci wake only when parked on ci, stable, and cooled down", func(t *testing.T) {
		reviewed := Signature{Head: "h1", Digest: "d1", CI: "PENDING"}
		now := Signature{Head: "h1", Digest: "d1", CI: "FAILURE"}

		st := &PRState{ReviewedSig: &reviewed, Status: StatusWaitingOnAuthor}
		ObserveCI(st, "FAILURE")
		ObserveCI(st, "FAILURE")
		assert.Equal(t, TriggerNone, NeedsReview(st, now, t0), "not parked on CI")

		st = &PRState{ReviewedSig: &reviewed, Status: StatusWaitingOnCI}
		ObserveCI(st, "FAILURE")
		assert.Equal(t, TriggerNone, NeedsReview(st, now, t0), "one poll is not stable")
		ObserveCI(st, "FAILURE")
		assert.Equal(t, TriggerCI, NeedsReview(st, now, t0))

		st.LastCIWake = t0.Add(-5 * time.Minute)
		assert.Equal(t, TriggerNone, NeedsReview(st, now, t0), "cooldown")
		st.LastCIWake = t0.Add(-11 * time.Minute)
		assert.Equal(t, TriggerCI, NeedsReview(st, now, t0))
	})
	t.Run("ci pending is never a wake", func(t *testing.T) {
		reviewed := Signature{Head: "h1", Digest: "d1", CI: "FAILURE"}
		st := &PRState{ReviewedSig: &reviewed, Status: StatusWaitingOnCI}
		ObserveCI(st, "PENDING")
		ObserveCI(st, "PENDING")
		assert.Equal(
			t,
			TriggerNone,
			NeedsReview(st, Signature{Head: "h1", Digest: "d1", CI: "PENDING"}, t0),
		)
	})
	t.Run("ci flapping resets stability", func(t *testing.T) {
		reviewed := Signature{Head: "h1", Digest: "d1", CI: "PENDING"}
		st := &PRState{ReviewedSig: &reviewed, Status: StatusWaitingOnCI}
		ObserveCI(st, "FAILURE")
		ObserveCI(st, "PENDING")
		ObserveCI(st, "FAILURE")
		assert.Equal(
			t,
			TriggerNone,
			NeedsReview(st, Signature{Head: "h1", Digest: "d1", CI: "FAILURE"}, t0),
		)
	})
}

func TestNeedsReviewWakesForStuckCI(t *testing.T) {
	now := t0
	st := &PRState{
		Status:      StatusWaitingOnCI,
		ReviewedSig: &Signature{Head: "h", Digest: "d", CI: "PENDING"},
		CIObs:       CIObs{State: CIStuck, N: CIStablePolls},
	}
	assert.Equal(t, TriggerCI, NeedsReview(st, Signature{Head: "h", Digest: "d", CI: CIStuck}, now),
		"a check stuck for over two hours gets a pass that can set it aside")
}
