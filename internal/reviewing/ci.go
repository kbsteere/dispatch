package reviewing

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// maxCIProblems bounds how many failing or stuck checks one review is shown.
const maxCIProblems = 6

// ciFileRe keeps check names safe as file names.
var ciFileRe = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// CIProblem is a required check that has not passed, as shown to the
// reviewer.
type CIProblem struct {
	Name string
	// Kind is failing or stuck.
	Kind string
	// OnBase is how the check fared on the base branch, or empty if unknown.
	OnBase string
	// Report is the session path of the check's output, for failing checks.
	Report string
}

// loadCIProblems finds the required checks on d's head that failed or are
// stuck, writes each failing check's output into dir/ci for the reviewer,
// and reads how every check fared on the base branch.
func (e *Engine) loadCIProblems(
	ctx context.Context,
	d PRDetail,
	dir string,
) ([]CIProblem, map[string]string) {
	req, err := e.d.GitHub.RequiredContexts(ctx, d.Key, d.BaseRef)
	if err != nil {
		return nil, nil
	}
	problems := ciProblems(RequiredChecks(d.Checks, req, e.d.Now()))
	if len(problems) == 0 {
		return nil, nil
	}
	base, _ := e.d.GitHub.BaseChecks(ctx, d.Key, d.BaseRef)
	out := make([]CIProblem, 0, min(len(problems), maxCIProblems))
	for _, p := range problems[:min(len(problems), maxCIProblems)] {
		cp := CIProblem{
			Name:   p.Name,
			Kind:   "failing",
			OnBase: base[p.Name],
		}
		if p.Bucket == "stuck" {
			cp.Kind = "stuck"
		} else if p.Node.Typename == "CheckRun" && p.Node.ID > 0 {
			if r, err := e.d.GitHub.CheckReport(ctx, d.Key, p.Node.ID); err == nil {
				cp.Report = writeCIReport(dir, p.Name, r)
			}
		}
		out = append(out, cp)
	}
	return out, base
}

// writeCIReport writes r for the reviewer and returns its path, or "" if it
// could not be written.
func writeCIReport(dir, name string, r CheckReport) string {
	ciDir := filepath.Join(dir, "ci")
	if os.MkdirAll(ciDir, 0o700) != nil {
		return ""
	}
	file := strings.Trim(ciFileRe.ReplaceAllString(name, "-"), "-.")
	if len(file) > 80 {
		file = file[:80]
	}
	p := filepath.Join(ciDir, file+".txt")
	if os.WriteFile(p, []byte(r.String()), 0o600) != nil {
		return ""
	}
	return p
}

// ciExcuse decides whether required checks that have not passed may be set
// aside for an approval: none may still be running within the limit, and
// each that has not passed is stuck, or the reviewer judged it unrelated to
// the change and it is not passing on the base branch. It returns the one
// line posted with the approval saying why.
func ciExcuse(
	checks []RequiredCheck,
	assessed []CIAssessment,
	base map[string]string,
) (bool, string) {
	problems := ciProblems(checks)
	if len(problems) == 0 {
		return false, ""
	}
	if slices.ContainsFunc(checks, func(c RequiredCheck) bool { return c.Bucket == "pending" }) {
		return false, ""
	}
	lines := make([]string, 0, len(problems))
	for _, p := range problems {
		if p.Bucket == "stuck" {
			if p.Node.StartedAt.IsZero() {
				lines = append(
					lines,
					fmt.Sprintf("`%s` has not reported for over two hours.", p.Name),
				)
			} else {
				lines = append(
					lines,
					fmt.Sprintf("`%s` has been running for over two hours.", p.Name),
				)
			}
			continue
		}
		a, ok := findAssessment(assessed, p.Name)
		if !ok || a.Related || base[p.Name] == "pass" {
			return false, ""
		}
		lines = append(lines, unrelatedLine(p.Name, a.Reason))
	}
	return true, strings.Join(lines, " ")
}

func findAssessment(assessed []CIAssessment, name string) (CIAssessment, bool) {
	i := slices.IndexFunc(assessed, func(a CIAssessment) bool {
		return strings.EqualFold(strings.TrimSpace(a.Check), name)
	})
	if i < 0 {
		return CIAssessment{}, false
	}
	return assessed[i], true
}

// unrelatedLine says why a failing check was set aside. The reviewer's reason
// is used only when it is one plain sentence that passes the tone check.
func unrelatedLine(name, reason string) string {
	reason = strings.Join(strings.Fields(SanitizeTerminal(reason)), " ")
	if reason == "" || len(reason) > 200 || len(LintBody(reason)) > 0 {
		return fmt.Sprintf("`%s` is failing for a reason unrelated to this change.", name)
	}
	reason = strings.TrimSuffix(reason, ".")
	return fmt.Sprintf("`%s` is failing for a reason unrelated to this change: %s.", name, reason)
}
