package reviewing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestQuotaNext(t *testing.T) {
	base := 3 * time.Minute
	reset := t0.Add(40 * time.Minute)
	tests := []struct {
		name       string
		q          Quota
		wantWait   time.Duration
		wantPaused string
	}{
		{"unknown quota uses base", Quota{}, base, ""},
		{
			"healthy",
			Quota{Remaining: 4000, ResetAt: reset, HourStart: t0, UsedThisHour: 20},
			base,
			"",
		},
		{
			"low slows to 15m",
			Quota{Remaining: 1400, ResetAt: reset, HourStart: t0},
			15 * time.Minute,
			"",
		},
		{
			"critical pauses until reset",
			Quota{Remaining: 400, ResetAt: reset, HourStart: t0},
			40 * time.Minute,
			"github quota",
		},
		{
			"critical with past reset waits a minute",
			Quota{Remaining: 400, ResetAt: t0.Add(-time.Minute), HourStart: t0},
			time.Minute,
			"github quota",
		},
		{
			"own hourly cap pauses until hour rolls",
			Quota{
				Remaining:    4000,
				ResetAt:      reset,
				HourStart:    t0.Add(-30 * time.Minute),
				UsedThisHour: 300,
			},
			30 * time.Minute,
			"github quota",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wait, paused := tt.q.Next(base, 300, t0)
			assert.Equal(t, tt.wantWait, wait)
			assert.Equal(t, tt.wantPaused, paused)
		})
	}
}

func TestQuotaObserveRollsHour(t *testing.T) {
	var q Quota
	q.Observe(4900, t0.Add(time.Hour), 1, t0)
	q.Observe(4890, t0.Add(time.Hour), 3, t0.Add(10*time.Minute))
	assert.Equal(t, 4, q.UsedThisHour)
	q.Observe(4880, t0.Add(2*time.Hour), 2, t0.Add(61*time.Minute))
	assert.Equal(t, 2, q.UsedThisHour)
	assert.Equal(t, 4880, q.Remaining)
	assert.Equal(t, t0.Add(2*time.Hour), q.ResetAt)
}
