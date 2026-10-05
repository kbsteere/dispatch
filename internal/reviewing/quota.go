package reviewing

import "time"

const (
	quotaSlowBelow  = 1500
	quotaPauseBelow = 500
	quotaSlowPoll   = 15 * time.Minute
)

// Quota tracks GitHub GraphQL rate-limit state and this watch's own usage.
type Quota struct {
	Remaining    int
	ResetAt      time.Time
	HourStart    time.Time
	UsedThisHour int
}

// Observe records the rateLimit block of a GraphQL response.
func (q *Quota) Observe(remaining int, resetAt time.Time, cost int, now time.Time) {
	if q.HourStart.IsZero() || now.Sub(q.HourStart) >= time.Hour {
		q.HourStart = now
		q.UsedThisHour = 0
	}
	q.Remaining = remaining
	q.ResetAt = resetAt
	q.UsedThisHour += cost
}

// Next returns how long to wait before the next poll and, when polling is
// paused, why.
func (q Quota) Next(base time.Duration, maxPerHour int, now time.Time) (time.Duration, string) {
	if q.ResetAt.IsZero() {
		return base, ""
	}
	if q.Remaining < quotaPauseBelow {
		return max(q.ResetAt.Sub(now), time.Minute), "github quota"
	}
	if maxPerHour > 0 && q.UsedThisHour >= maxPerHour {
		return max(q.HourStart.Add(time.Hour).Sub(now), time.Minute), "github quota"
	}
	if q.Remaining < quotaSlowBelow {
		return max(base, quotaSlowPoll), ""
	}
	return base, ""
}
