package reviewing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestLintBodyRejectsBotNarration(t *testing.T) {
	// Taken from autorev's reviewer instructions and the reviews it posted.
	bad := []string{
		"LGTM. Verified at `acd7e58b2f`: applied go-standards and go-security-review; conforms to sentinel/docs/go-pseudo-versions.md (stages 1–2). Earlier findings are addressed.",
		"Fixed at 5dd9df4a1c.",
		"Seconding: confirmed at `bots/kev/main.go:42`, a nil map write panics on first run.",
		"Verified in the codebase: the handler returns early.",
		"I checked the callers and this is fine.",
		"I've verified the retry path.",
		"No design doc found.",
		"Standards applied: go-standards. 2 blocking, 1 non-blocking.",
		"Great work on this! The retry loop never stops after ctx is cancelled.",
		"Looks good overall, but the loop never exits.",
		"Design check at abc1234def after my earlier approval found a conflict.",
		"No issues found.",
		"The change at ACD7E58B is the cause.",
		// mono#47520
		"These can't be tested today because `worker.slackAPI` is the concrete `*slack.Client`.",
		"Notably, the guard runs after the post.",
		"It's worth noting that the TTL is 7 days.",
		"They are the security-relevant part of this change.",
	}
	for _, b := range bad {
		assert.NotEmpty(t, LintBody(b), "should reject: %q", b)
	}
}

func TestLintBodyAllowsRealFindings(t *testing.T) {
	good := []string{
		"`fetchAdvisories` drops the error from `rows.Err()`, so a truncated BigQuery page reads as \"no CVEs\" and the bot closes open remediation PRs. Return `rows.Err()` after the loop.",
		"The JWT signature is never verified before `claims.Subject` is trusted, so any caller can impersonate a user. Call `jwt.ParseWithClaims` with the key func.",
		"This contradicts the design doc at `lifecycle/secfeed/docs/feeds.md:48`, which says OSV ingestion is fail-closed; here a fetch error is logged and skipped.",
		"nit: `defer resp.Body.Close()` should come after the error check, otherwise a nil `resp` panics.",
		"These can't be tested today because `worker.slackAPI` is a `*slack.Client`, not an interface.",
		"non-blocking: nothing exercises the path where `ListPackages` returns a partial page; add a case with a 2-page fake.",
		"Missing test: the regression in #1234 (equal-severity CVEs sorted non-deterministically) has no test that feeds two equal-severity CVEs in both orders.",
		"```suggestion\nconst emptySHA = \"e69de29bb2d1d6434b8b29ae775ad8c2e48c5391\"\n```\nThe constant is duplicated in three packages.",
		"The cache key is built with `fmt.Sprintf(\"%d\", 1234567)` and collides across tenants.",
		"`defaced` images are skipped silently; return an error instead.",
	}
	for _, g := range good {
		assert.Empty(t, LintBody(g), "should allow: %q", g)
	}
}
