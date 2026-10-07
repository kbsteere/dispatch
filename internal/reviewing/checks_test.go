package reviewing

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func checkRun(name, status, conclusion string, app int, required bool) CheckNode {
	return CheckNode{
		Typename:   "CheckRun",
		Name:       name,
		Status:     status,
		Conclusion: conclusion,
		AppID:      app,
		IsRequired: required,
	}
}

func TestRequiredState(t *testing.T) {
	tests := []struct {
		name  string
		nodes []CheckNode
		req   RequiredContexts
		want  string
	}{
		{
			"no required checks",
			[]CheckNode{checkRun("lint", "COMPLETED", "FAILURE", 1, false)},
			nil,
			"SUCCESS",
		},
		{
			"required success",
			[]CheckNode{checkRun("build", "COMPLETED", "SUCCESS", 1, true)},
			nil,
			"SUCCESS",
		},
		{
			"required pending",
			[]CheckNode{checkRun("build", "IN_PROGRESS", "", 1, true)},
			nil,
			"PENDING",
		},
		{
			"required failure",
			[]CheckNode{checkRun("build", "COMPLETED", "FAILURE", 1, true)},
			nil,
			"FAILURE",
		},
		{
			"required cancelled",
			[]CheckNode{checkRun("build", "COMPLETED", "CANCELLED", 1, true)},
			nil,
			"FAILURE",
		},
		{
			"skipped counts as pass",
			[]CheckNode{checkRun("build", "COMPLETED", "SKIPPED", 1, true)},
			nil,
			"SUCCESS",
		},
		{
			// mono's check-go-complete is created after its shards finish;
			// a required context missing from the head is pending.
			"required context not yet reported",
			[]CheckNode{checkRun("build", "COMPLETED", "SUCCESS", 1, true)},
			RequiredContexts{"build": nil, "check-go-complete": nil},
			"PENDING",
		},
		{
			"same-named run from another app does not satisfy",
			[]CheckNode{checkRun("build", "COMPLETED", "SUCCESS", 99, false)},
			RequiredContexts{"build": {15368}},
			"PENDING",
		},
		{
			"run from the allowed app satisfies",
			[]CheckNode{checkRun("build", "COMPLETED", "SUCCESS", 15368, false)},
			RequiredContexts{"build": {15368}},
			"SUCCESS",
		},
		{
			"worst run wins among re-runs",
			[]CheckNode{
				checkRun("build", "COMPLETED", "SUCCESS", 1, true),
				checkRun("build", "COMPLETED", "FAILURE", 1, true),
			},
			nil,
			"FAILURE",
		},
		{
			"approval gate ignored",
			[]CheckNode{
				checkRun("doc-approval", "COMPLETED", "FAILURE", 1, true),
				checkRun("build", "COMPLETED", "SUCCESS", 1, true),
			},
			nil,
			"SUCCESS",
		},
		{
			"status context pending",
			[]CheckNode{
				{Typename: "StatusContext", Name: "ci/legacy", State: "PENDING", IsRequired: true},
			},
			nil,
			"PENDING",
		},
		{
			"status context error is failure",
			[]CheckNode{
				{Typename: "StatusContext", Name: "ci/legacy", State: "ERROR", IsRequired: true},
			},
			nil,
			"FAILURE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RequiredState(tt.nodes, tt.req, t0))
		})
	}
}

func TestRequiredStateStuckChecks(t *testing.T) {
	run := func(name string, started time.Duration) CheckNode {
		return CheckNode{
			Typename: "CheckRun", Name: name, Status: "IN_PROGRESS", IsRequired: true,
			StartedAt: t0.Add(-started),
		}
	}
	done := CheckNode{
		Typename:   "CheckRun",
		Name:       "build",
		Status:     "COMPLETED",
		Conclusion: "SUCCESS",
		IsRequired: true,
	}

	assert.Equal(t, "PENDING", RequiredState([]CheckNode{done, run("e2e", time.Hour)}, nil, t0))
	assert.Equal(t, CIStuck, RequiredState([]CheckNode{done, run("e2e", 3*time.Hour)}, nil, t0),
		"running for over two hours")
	assert.Equal(t, "PENDING",
		RequiredState([]CheckNode{run("e2e", 3*time.Hour), run("lint", time.Minute)}, nil, t0),
		"a check still within the limit keeps it pending")
	failed := CheckNode{
		Typename:   "CheckRun",
		Name:       "scan-urls",
		Status:     "COMPLETED",
		Conclusion: "FAILURE",
		IsRequired: true,
	}
	assert.Equal(t, "FAILURE", RequiredState([]CheckNode{failed, run("e2e", 3*time.Hour)}, nil, t0))

	problems := ciProblems(
		RequiredChecks([]CheckNode{done, failed, run("e2e", 3*time.Hour)}, nil, t0),
	)
	require.Len(t, problems, 2)
	assert.Equal(t, "e2e", problems[0].Name)
	assert.Equal(t, "stuck", problems[0].Bucket)
	assert.Equal(t, "scan-urls", problems[1].Name)
	assert.Equal(t, "fail", problems[1].Bucket)
}

func TestRequiredCheckThatNeverReportedBecomesStuck(t *testing.T) {
	build := CheckNode{
		Typename: "CheckRun", Name: "build", Status: "COMPLETED", Conclusion: "SUCCESS",
		IsRequired: true, StartedAt: t0,
	}
	req := RequiredContexts{"build": nil, "acid-sandbox (presubmit)": nil}
	assert.Equal(t, "PENDING", RequiredState([]CheckNode{build}, req, t0.Add(time.Hour)))
	assert.Equal(t, CIStuck, RequiredState([]CheckNode{build}, req, t0.Add(3*time.Hour)))
	assert.Equal(t, "PENDING", RequiredState(nil, req, t0.Add(3*time.Hour)),
		"with nothing reported there is no start time to measure from")
}
