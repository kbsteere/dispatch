package reviewing

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
)

const (
	// maxCISummaryBytes, maxCIAnnotations, maxCILogBytes, and maxCILogLines
	// bound what one failing check contributes to a review.
	maxCISummaryBytes = 8 << 10
	maxCIAnnotations  = 30
	maxCILogBytes     = 8 << 20
	maxCILogLines     = 150
)

// CheckReport is what GitHub reports about one failing check run. All of it
// was written by CI and is untrusted.
type CheckReport struct {
	Title       string
	Summary     string
	Annotations []string
	// LogTail is the end of the job log, for GitHub Actions runs.
	LogTail string
}

// CheckReport fetches a check run's output, annotations, and, for GitHub
// Actions, the tail of its job log.
func (g *ghClient) CheckReport(ctx context.Context, k PRKey, id int64) (CheckReport, error) {
	base := fmt.Sprintf("repos/%s/%s", k.Owner, k.Repo)
	var run struct {
		App struct {
			Slug string `json:"slug"`
		} `json:"app"`
		Output struct {
			Title   string `json:"title"`
			Summary string `json:"summary"`
		} `json:"output"`
	}
	if err := g.rest.DoWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("%s/check-runs/%d", base, id),
		nil,
		&run,
	); err != nil {
		return CheckReport{}, err
	}
	r := CheckReport{
		Title:   run.Output.Title,
		Summary: clipBytes(run.Output.Summary, maxCISummaryBytes),
	}

	var anns []struct {
		Path      string `json:"path"`
		StartLine int    `json:"start_line"`
		Level     string `json:"annotation_level"`
		Message   string `json:"message"`
	}
	path := fmt.Sprintf("%s/check-runs/%d/annotations?per_page=%d", base, id, maxCIAnnotations)
	if g.rest.DoWithContext(ctx, http.MethodGet, path, nil, &anns) == nil {
		for _, a := range anns {
			r.Annotations = append(
				r.Annotations,
				fmt.Sprintf(
					"%s:%d %s: %s",
					a.Path,
					a.StartLine,
					a.Level,
					clipBytes(a.Message, 1<<10),
				),
			)
		}
	}

	if run.App.Slug == "github-actions" {
		// A check run's id is its Actions job id.
		resp, err := g.rest.RequestWithContext(ctx, http.MethodGet,
			fmt.Sprintf("%s/actions/jobs/%d/logs", base, id), nil)
		if err == nil {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, maxCILogBytes))
			resp.Body.Close()
			r.LogTail = tailLines(b, maxCILogLines)
		}
	}
	return r, nil
}

// String renders the report for the reviewer.
func (r CheckReport) String() string {
	var b strings.Builder
	if r.Title != "" {
		fmt.Fprintf(&b, "title: %s\n", r.Title)
	}
	if r.Summary != "" {
		fmt.Fprintf(&b, "\nsummary:\n%s\n", r.Summary)
	}
	if len(r.Annotations) > 0 {
		b.WriteString("\nannotations:\n")
		for _, a := range r.Annotations {
			fmt.Fprintf(&b, "- %s\n", a)
		}
	}
	if r.LogTail != "" {
		fmt.Fprintf(&b, "\nlast %d lines of the job log:\n%s\n", maxCILogLines, r.LogTail)
	}
	if b.Len() == 0 {
		return "GitHub reported no output for this check.\n"
	}
	return b.String()
}

func clipBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n[clipped]"
}

// tailLines returns the last n lines of b.
func tailLines(b []byte, n int) string {
	b = bytes.TrimRight(b, "\n")
	lines := bytes.Split(b, []byte("\n"))
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return string(bytes.Join(lines, []byte("\n")))
}

// baseChecksQuery reads the checks on a branch's latest commit.
const baseChecksQuery = `query($owner: String!, $name: String!, $ref: String!, $after: String) {
  rateLimit { cost }
  repository(owner: $owner, name: $name) { ref(qualifiedName: $ref) { target { ... on Commit {
    statusCheckRollup { contexts(first: 100, after: $after) {
      pageInfo { hasNextPage endCursor }
      nodes { __typename
        ... on CheckRun { name status conclusion startedAt }
        ... on StatusContext { context state createdAt } }
    } }
  } } } }
}`

// BaseChecks returns how each check fared on branch's latest commit, by
// name: pass, fail, cancel, pending, stuck, or skipping.
func (g *ghClient) BaseChecks(
	ctx context.Context,
	k PRKey,
	branch string,
) (map[string]string, error) {
	out := map[string]string{}
	var after any
	for range maxCheckPages {
		var resp struct {
			Repository struct {
				Ref *struct {
					Target struct {
						StatusCheckRollup *struct {
							Contexts contextsPageJSON `json:"contexts"`
						} `json:"statusCheckRollup"`
					} `json:"target"`
				} `json:"ref"`
			} `json:"repository"`
		}
		vars := map[string]any{
			"owner": k.Owner, "name": k.Repo, "ref": "refs/heads/" + branch, "after": after,
		}
		if err := g.gql.DoWithContext(ctx, baseChecksQuery, vars, &resp); err != nil {
			return out, err
		}
		ref := resp.Repository.Ref
		if ref == nil || ref.Target.StatusCheckRollup == nil {
			return out, nil
		}
		page := ref.Target.StatusCheckRollup.Contexts
		for _, x := range page.Nodes {
			n := x.toCheckNode()
			b := bucket(n, g.now())
			if cur, ok := out[n.Name]; !ok || bucketRank[b] > bucketRank[cur] {
				out[n.Name] = b
			}
		}
		if !page.PageInfo.HasNextPage {
			break
		}
		after = page.PageInfo.EndCursor
	}
	return out, nil
}
