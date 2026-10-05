package common

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/kbsteere/dispatch/v4/internal/reviewing"
	"github.com/kbsteere/dispatch/v4/internal/tui/constants"
)

func TestReviewStatusGlyph(t *testing.T) {
	assert.Equal(t, constants.SuccessIcon, ReviewStatusGlyph(reviewing.StatusApproved))
	assert.Equal(
		t,
		constants.ChangesRequestedIcon,
		ReviewStatusGlyph(reviewing.StatusChangesRequested),
	)
	assert.Equal(t, constants.ActionRequiredIcon, ReviewStatusGlyph(reviewing.StatusNeedsAttention))
	assert.Equal(t, constants.FailureIcon, ReviewStatusGlyph(reviewing.StatusFailed))
	assert.Equal(t, "", ReviewStatusGlyph(reviewing.StatusBaseline), "baselined PRs show nothing")
	assert.Equal(t, "", ReviewStatusGlyph(reviewing.Status("unknown")))
}

func TestReviewStatusWord(t *testing.T) {
	assert.Equal(t, "approved", ReviewStatusWord(reviewing.StatusApproved))
	assert.Equal(t, "needs your attention", ReviewStatusWord(reviewing.StatusNeedsAttention))
	assert.Equal(t, "waiting on CI", ReviewStatusWord(reviewing.StatusWaitingOnCI))
	assert.Equal(t, "baseline (not reviewed)", ReviewStatusWord(reviewing.StatusBaseline))
}
