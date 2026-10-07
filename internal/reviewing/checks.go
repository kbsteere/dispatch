package reviewing

import (
	"cmp"
	"slices"
	"time"
)

type CheckNode struct {
	Typename   string
	Name       string
	Status     string
	Conclusion string
	State      string
	IsRequired bool
	AppID      int
	// ID is the check run's database id; zero for a commit status.
	ID int64
	// StartedAt is when the check run started or the status was set.
	StartedAt time.Time
}

type RequiredContexts map[string][]int

var ApprovalGates = map[string]bool{
	"doc-approval":        true,
	"CODEOWNERS Approval": true,
	"codeowners-approval": true,
}

// CIStuck is the required-check state when nothing failed and every check
// still running has run longer than ciStuckAfter.
const CIStuck = "STUCK"

// ciStuckAfter is how long a required check may run before it counts as
// stuck rather than pending.
const ciStuckAfter = 2 * time.Hour

var bucketRank = map[string]int{
	"pass": 0, "skipping": 1, "stuck": 2, "pending": 3, "cancel": 4, "fail": 5,
}

func bucket(n CheckNode, now time.Time) string {
	b := rawBucket(n)
	if b == "pending" && !n.StartedAt.IsZero() && now.Sub(n.StartedAt) > ciStuckAfter {
		return "stuck"
	}
	return b
}

func rawBucket(n CheckNode) string {
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

// RequiredCheck is one required check's worst result on the head commit.
type RequiredCheck struct {
	Name   string
	Bucket string // pass, skipping, stuck, pending, cancel, or fail
	// Node is the run behind Bucket; zero when the check never reported.
	Node CheckNode
}

// RequiredChecks reduces the head commit's checks to one result per
// required check, sorted by name. Approval gates are left out.
func RequiredChecks(nodes []CheckNode, req RequiredContexts, now time.Time) []RequiredCheck {
	worst := map[string]RequiredCheck{}
	for _, n := range nodes {
		allowed, inReq := req[n.Name]
		want := inReq
		if want && n.Typename == "CheckRun" && allowed != nil {
			want = slices.Contains(allowed, n.AppID)
		}
		if !want && !n.IsRequired {
			continue
		}
		b := bucket(n, now)
		if cur, ok := worst[n.Name]; !ok || bucketRank[b] > bucketRank[cur.Bucket] {
			worst[n.Name] = RequiredCheck{Name: n.Name, Bucket: b, Node: n}
		}
	}
	// A required check that never reported is stuck once the commit's
	// other checks started more than ciStuckAfter ago.
	missing := "pending"
	if first := firstStart(nodes); !first.IsZero() && now.Sub(first) > ciStuckAfter {
		missing = "stuck"
	}
	for name := range req {
		if _, ok := worst[name]; !ok {
			worst[name] = RequiredCheck{Name: name, Bucket: missing}
		}
	}
	out := make([]RequiredCheck, 0, len(worst))
	for name, c := range worst {
		if !ApprovalGates[name] {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b RequiredCheck) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// RequiredState summarises required checks: FAILURE if any failed or was
// cancelled, PENDING if any is still running within ciStuckAfter, STUCK if
// the only unfinished ones are stuck, else SUCCESS.
func RequiredState(nodes []CheckNode, req RequiredContexts, now time.Time) string {
	return checksState(RequiredChecks(nodes, req, now))
}

// checksState summarises reduced required checks; see RequiredState.
func checksState(checks []RequiredCheck) string {
	state := "SUCCESS"
	for _, c := range checks {
		switch c.Bucket {
		case "fail", "cancel":
			return "FAILURE"
		case "pending":
			state = "PENDING"
		case "stuck":
			if state == "SUCCESS" {
				state = CIStuck
			}
		}
	}
	return state
}

// ciProblems are the required checks that failed, were cancelled, or are
// stuck.
func ciProblems(checks []RequiredCheck) []RequiredCheck {
	return slices.DeleteFunc(slices.Clone(checks), func(c RequiredCheck) bool {
		return c.Bucket != "fail" && c.Bucket != "cancel" && c.Bucket != "stuck"
	})
}

// firstStart is when the earliest of nodes started; zero when none says.
func firstStart(nodes []CheckNode) time.Time {
	var first time.Time
	for _, n := range nodes {
		if !n.StartedAt.IsZero() && (first.IsZero() || n.StartedAt.Before(first)) {
			first = n.StartedAt
		}
	}
	return first
}
