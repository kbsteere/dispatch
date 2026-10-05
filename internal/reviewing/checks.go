package reviewing

import "slices"

// CheckNode is one entry of a commit's statusCheckRollup. Name is the check
// run's name or the status context's context.
type CheckNode struct {
	Typename   string
	Name       string
	Status     string
	Conclusion string
	State      string
	IsRequired bool
	AppID      int
}

// RequiredContexts maps a required context name to the app ids allowed to
// satisfy it; a nil slice means any source.
type RequiredContexts map[string][]int

// ApprovalGates are required checks that pass only when specific people
// approve. Waiting on them would deadlock dispatch's own approval.
var ApprovalGates = map[string]bool{
	"doc-approval":        true,
	"CODEOWNERS Approval": true,
	"codeowners-approval": true,
}

var bucketRank = map[string]int{"pass": 0, "skipping": 1, "pending": 2, "cancel": 3, "fail": 4}

// bucket groups a check the same way `gh pr checks` does.
func bucket(n CheckNode) string {
	if n.Typename == "CheckRun" {
		if n.Status != "COMPLETED" {
			return "pending"
		}
		switch n.Conclusion {
		case "SUCCESS":
			return "pass"
		case "SKIPPED", "NEUTRAL":
			return "skipping"
		case "CANCELLED":
			return "cancel"
		default:
			return "fail"
		}
	}
	switch n.State {
	case "SUCCESS":
		return "pass"
	case "PENDING", "EXPECTED":
		return "pending"
	default:
		return "fail"
	}
}

// RequiredState reduces required checks to SUCCESS, PENDING, or FAILURE.
// A required context that has not reported on the head yet is pending.
func RequiredState(nodes []CheckNode, req RequiredContexts) string {
	worst := map[string]string{}
	for _, n := range nodes {
		allowed, inReq := req[n.Name]
		want := inReq
		if want && n.Typename == "CheckRun" && allowed != nil {
			want = slices.Contains(allowed, n.AppID)
		}
		if !want && !n.IsRequired {
			continue
		}
		b := bucket(n)
		if cur, ok := worst[n.Name]; !ok || bucketRank[b] > bucketRank[cur] {
			worst[n.Name] = b
		}
	}
	for name := range req {
		if _, ok := worst[name]; !ok {
			worst[name] = "pending"
		}
	}
	state := "SUCCESS"
	for name, b := range worst {
		if ApprovalGates[name] {
			continue
		}
		switch b {
		case "fail", "cancel":
			return "FAILURE"
		case "pending":
			state = "PENDING"
		}
	}
	return state
}
