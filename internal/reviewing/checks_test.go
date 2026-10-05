package reviewing

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func checkRun(name, status, conclusion string, app int, required bool) CheckNode {
	return CheckNode{Typename: "CheckRun", Name: name, Status: status, Conclusion: conclusion, AppID: app, IsRequired: required}
}

func TestRequiredState(t *testing.T) {
	tests := []struct {
		name  string
		nodes []CheckNode
		req   RequiredContexts
		want  string
	}{
		{"no required checks", []CheckNode{checkRun("lint", "COMPLETED", "FAILURE", 1, false)}, nil, "SUCCESS"},
		{"required success", []CheckNode{checkRun("build", "COMPLETED", "SUCCESS", 1, true)}, nil, "SUCCESS"},
		{"required pending", []CheckNode{checkRun("build", "IN_PROGRESS", "", 1, true)}, nil, "PENDING"},
		{"required failure", []CheckNode{checkRun("build", "COMPLETED", "FAILURE", 1, true)}, nil, "FAILURE"},
		{"required cancelled", []CheckNode{checkRun("build", "COMPLETED", "CANCELLED", 1, true)}, nil, "FAILURE"},
		{"skipped counts as pass", []CheckNode{checkRun("build", "COMPLETED", "SKIPPED", 1, true)}, nil, "SUCCESS"},
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
			[]CheckNode{checkRun("build", "COMPLETED", "SUCCESS", 1, true), checkRun("build", "COMPLETED", "FAILURE", 1, true)},
			nil,
			"FAILURE",
		},
		{
			"approval gate ignored",
			[]CheckNode{checkRun("doc-approval", "COMPLETED", "FAILURE", 1, true), checkRun("build", "COMPLETED", "SUCCESS", 1, true)},
			nil,
			"SUCCESS",
		},
		{
			"status context pending",
			[]CheckNode{{Typename: "StatusContext", Name: "ci/legacy", State: "PENDING", IsRequired: true}},
			nil,
			"PENDING",
		},
		{
			"status context error is failure",
			[]CheckNode{{Typename: "StatusContext", Name: "ci/legacy", State: "ERROR", IsRequired: true}},
			nil,
			"FAILURE",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, RequiredState(tt.nodes, tt.req))
		})
	}
}
