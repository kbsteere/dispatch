package reviewing

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWithMarker(t *testing.T) {
	assert.Equal(t, "body\n\n"+Marker, WithMarker("body"))
	assert.Equal(t, Marker, WithMarker(""))
	assert.Equal(t, Marker, WithMarker("   "))
}

func TestFoldComments(t *testing.T) {
	p := ReviewPayload{
		CommitID: "c",
		Event:    "COMMENT",
		Body:     "",
		Comments: []ReviewComment{
			{
				Path: "bots/kev/main.go",
				Line: 12,
				Side: "RIGHT",
				Body: "nil map write panics\n\n" + Marker,
			},
		},
	}
	got := FoldComments(p)
	assert.Empty(t, got.Comments)
	assert.Equal(t, "**`bots/kev/main.go:12`**\nnil map write panics", got.Body)
}

func TestPREvents(t *testing.T) {
	d := PRDetail{
		Comments: []Comment{{Author: "dnegreira", CreatedAt: t0}},
		Reviews: []Review{
			{Author: "kbsteere", SubmittedAt: t0.Add(time.Minute)},
			{Author: "x", State: "PENDING"},
		},
		Threads: []Thread{
			{
				IsResolved: true,
				ResolvedBy: "powersj",
				Comments:   []Comment{{Author: "powersj", CreatedAt: t0.Add(2 * time.Minute)}},
			},
		},
	}
	events, resolved := d.Events()
	assert.Equal(t, []Event{
		{At: t0, Kind: "c", Login: "dnegreira"},
		{At: t0.Add(time.Minute), Kind: "r", Login: "kbsteere"},
		{At: t0.Add(2 * time.Minute), Kind: "t", Login: "powersj"},
	}, events)
	assert.Equal(t, []string{"powersj"}, resolved)
}

// toServer sends every request to the test server regardless of the host
// go-gh built the URL for.
type toServer struct{ target *url.URL }

func (s toServer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.URL.Scheme = s.target.Scheme
	r.URL.Host = s.target.Host
	return http.DefaultTransport.RoundTrip(r)
}

type gqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// fakeGH serves GraphQL at /graphql and REST everywhere else.
func fakeGH(
	t *testing.T,
	gql func(gqlRequest) string,
	rest http.HandlerFunc,
) (GitHub, *atomic.Int64) {
	t.Helper()
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path == "/graphql" {
			var req gqlRequest
			b, _ := io.ReadAll(r.Body)
			require.NoError(t, json.Unmarshal(b, &req))
			_, _ = w.Write([]byte(gql(req)))
			return
		}
		rest(w, r)
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	opts := api.ClientOptions{
		Host:         "github.com",
		AuthToken:    "x",
		Transport:    toServer{u},
		LogIgnoreEnv: true,
	}
	gqlc, err := api.NewGraphQLClient(opts)
	require.NoError(t, err)
	restc, err := api.NewRESTClient(opts)
	require.NoError(t, err)
	return NewGitHub(gqlc, restc), &hits
}

func noGQL(t *testing.T) func(gqlRequest) string {
	return func(gqlRequest) string { t.Fatal("unexpected GraphQL request"); return "" }
}

func noREST(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) { t.Fatalf("unexpected REST request %s", r.URL) }
}

func TestPostReviewSendsPinnedCommitAndMarker(t *testing.T) {
	var got ReviewPayload
	gh, _ := fakeGH(t, noGQL(t), func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/repos/chainguard-dev/mono/pulls/62436/reviews", r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &got))
		_, _ = w.Write(
			[]byte(
				`{"id":1,"html_url":"https://github.com/chainguard-dev/mono/pull/62436#pullrequestreview-3311"}`,
			),
		)
	})
	sha := strings.Repeat("a", 40)
	u, err := gh.PostReview(
		context.Background(),
		monoKey,
		ReviewPayload{CommitID: sha, Event: "APPROVE"},
	)
	require.NoError(t, err)
	assert.Contains(t, u, "pullrequestreview-3311")
	assert.Equal(t, sha, got.CommitID)
	assert.Equal(t, "APPROVE", got.Event)
	assert.Equal(t, Marker, got.Body, "approval has no visible text")
}

func TestPostReviewMarksInlineComments(t *testing.T) {
	var got ReviewPayload
	gh, _ := fakeGH(t, noGQL(t), func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &got))
		_, _ = w.Write([]byte(`{"html_url":"u"}`))
	})
	_, err := gh.PostReview(context.Background(), monoKey, ReviewPayload{
		CommitID: strings.Repeat("a", 40), Event: "COMMENT",
		Comments: []ReviewComment{{Path: "a.go", Line: 3, Body: "x"}},
	})
	require.NoError(t, err)
	require.Len(t, got.Comments, 1)
	assert.Equal(t, "RIGHT", got.Comments[0].Side)
	assert.Equal(t, "x\n\n"+Marker, got.Comments[0].Body)
}

func TestPostReviewFoldsOn422(t *testing.T) {
	calls := 0
	var last ReviewPayload
	gh, _ := fakeGH(t, noGQL(t), func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &last))
		if len(last.Comments) > 0 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"message":"Unprocessable Entity","errors":["Line could not be resolved"],"status":"422"}`,
				),
			)
			return
		}
		_, _ = w.Write([]byte(`{"html_url":"u"}`))
	})
	_, err := gh.PostReview(context.Background(), monoKey, ReviewPayload{
		CommitID: strings.Repeat("a", 40), Event: "COMMENT",
		Comments: []ReviewComment{{Path: "a.go", Line: 999, Body: "x"}},
	})
	require.NoError(t, err)
	assert.Equal(t, 2, calls)
	assert.Empty(t, last.Comments)
	assert.Equal(t, "**`a.go:999`**\nx\n\n"+Marker, last.Body)
}

func TestPostReviewOtherErrorsFail(t *testing.T) {
	gh, _ := fakeGH(t, noGQL(t), func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	})
	_, err := gh.PostReview(
		context.Background(),
		monoKey,
		ReviewPayload{CommitID: strings.Repeat("a", 40), Event: "APPROVE"},
	)
	assert.Error(t, err)
}

const searchPage1 = `{"data":{"rateLimit":{"cost":1,"remaining":4023,"resetAt":"2026-10-05T16:11:29Z"},"search":{"pageInfo":{"hasNextPage":true,"endCursor":"Y3Vyc29yOjEwMA=="},"nodes":[
 {"id":"PR_kwDOA1","number":1234,"updatedAt":"2026-10-05T15:00:00Z","repository":{"name":"stereo","owner":{"login":"chainguard-dev"}}},
 {"id":"PR_kwDOA2","number":7,"updatedAt":"2026-10-05T15:01:00Z","repository":{"name":"..","owner":{"login":"chainguard-dev"}}},
 {}
]}}}`

func TestSearch(t *testing.T) {
	var queries []string
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		queries = append(queries, r.Variables["q"].(string))
		return searchPage1
	}, noREST(t))
	hits, rl, err := gh.Search(
		context.Background(),
		"is:open draft:false repo:chainguard-dev/stereo author:dnegreira",
		1,
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]string{"is:pr is:open draft:false repo:chainguard-dev/stereo author:dnegreira"},
		queries,
		"maxPages stops pagination",
	)
	assert.Equal(
		t,
		[]SearchHit{
			{
				Key:       PRKey{"chainguard-dev", "stereo", 1234},
				NodeID:    "PR_kwDOA1",
				UpdatedAt: "2026-10-05T15:00:00Z",
			},
		},
		hits,
		"invalid repo names and non-PR nodes are dropped",
	)
	assert.Equal(
		t,
		RateLimit{
			Cost:      1,
			Remaining: 4023,
			ResetAt:   time.Date(2026, 10, 5, 16, 11, 29, 0, time.UTC),
		},
		rl,
	)
}

const detailResp = `{"data":{"rateLimit":{"cost":1,"remaining":4000,"resetAt":"2026-10-05T16:11:29Z"},"node":{
 "id":"PR_kwDOA1","number":1234,"state":"OPEN","isDraft":false,"title":"kev: bump","body":"Fixes CVE-2026-1234","createdAt":"2026-10-01T09:41:00Z","updatedAt":"2026-10-05T15:00:00Z","mergeable":"MERGEABLE",
 "author":null,
 "headRefOid":"1111111111111111111111111111111111111111","baseRefName":"main","baseRefOid":"2222222222222222222222222222222222222222",
 "repository":{"name":"stereo","owner":{"login":"chainguard-dev"}},
 "files":{"nodes":[{"path":"kev.yaml"}]},
 "comments":{"nodes":[{"id":"IC_1","body":"ptal","createdAt":"2026-10-05T14:00:00Z","author":{"login":"dnegreira"}}]},
 "reviews":{"nodes":[{"state":"CHANGES_REQUESTED","body":"","submittedAt":"2026-10-05T14:30:00Z","author":{"login":"powersj"},"commit":{"oid":"1111111111111111111111111111111111111111"}}]},
 "reviewThreads":{"nodes":[{"id":"PRRT_1","isResolved":true,"isOutdated":false,"path":"kev.yaml","line":4,"resolvedBy":{"login":"powersj"},
   "comments":{"nodes":[{"id":"PRRC_1","body":"epoch not bumped","createdAt":"2026-10-05T14:31:00Z","author":{"login":"powersj"}}]}}]},
 "commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{"nodes":[
   {"__typename":"CheckRun","name":"build","status":"COMPLETED","conclusion":"SUCCESS","isRequired":true,"checkSuite":{"databaseId":105042049023,"app":{"databaseId":15368},"workflowRun":{"workflow":{"databaseId":264707181}}}},
   {"__typename":"StatusContext","context":"ci/legacy","state":"PENDING","isRequired":false}
 ]}}}}]}
}}}`

func TestDetails(t *testing.T) {
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		require.Equal(t, "PR_kwDOA1", r.Variables["id"])
		require.Contains(t, r.Query, "isRequired(pullRequestId: $id)")
		return detailResp
	}, noREST(t))
	ds, rl, err := gh.Details(context.Background(), []string{"PR_kwDOA1"})
	require.NoError(t, err)
	assert.Equal(t, 1, rl.Cost)
	require.Len(t, ds, 1)
	d := ds[0]
	assert.Equal(t, PRKey{"chainguard-dev", "stereo", 1234}, d.Key)
	assert.Equal(t, "ghost", d.Author)
	assert.Equal(t, "1111111111111111111111111111111111111111", d.HeadOID)
	assert.Equal(t, "main", d.BaseRef)
	assert.Equal(t, time.Date(2026, 10, 1, 9, 41, 0, 0, time.UTC), d.CreatedAt)
	assert.Equal(t, []string{"kev.yaml"}, d.Files)
	require.Len(t, d.Threads, 1)
	assert.Equal(t, Thread{
		ID:         "PRRT_1",
		IsResolved: true,
		ResolvedBy: "powersj",
		Path:       "kev.yaml",
		Line:       4,
		Comments: []Comment{
			{
				ID:        "PRRC_1",
				Author:    "powersj",
				Body:      "epoch not bumped",
				CreatedAt: time.Date(2026, 10, 5, 14, 31, 0, 0, time.UTC),
			},
		},
	}, d.Threads[0])
	assert.Equal(
		t,
		[]Review{
			{
				Author:      "powersj",
				State:       "CHANGES_REQUESTED",
				CommitOID:   "1111111111111111111111111111111111111111",
				SubmittedAt: time.Date(2026, 10, 5, 14, 30, 0, 0, time.UTC),
			},
		},
		d.Reviews,
	)
	assert.Equal(t, []CheckNode{
		{
			Typename:   "CheckRun",
			Name:       "build",
			Status:     "COMPLETED",
			Conclusion: "SUCCESS",
			IsRequired: true,
			AppID:      15368,
			SuiteID:    105042049023,
			WorkflowID: 264707181,
		},
		{Typename: "StatusContext", Name: "ci/legacy", State: "PENDING"},
	}, d.Checks)
}

func TestDetailsPaginatesFiles(t *testing.T) {
	// A PR with more than 100 changed files: a finding on file 101 must be
	// anchorable, so every page is fetched.
	firstPage := strings.Replace(
		detailResp,
		`"files":{"nodes":[{"path":"kev.yaml"}]}`,
		`"files":{"pageInfo":{"hasNextPage":true,"endCursor":"Y3Vyc29yOjEwMA=="},"nodes":[{"path":"kev.yaml"}]}`,
		1,
	)
	var cursors []any
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		if strings.Contains(r.Query, "files(first: 100, after: $after)") {
			cursors = append(cursors, r.Variables["after"])
			if r.Variables["after"] == "Y3Vyc29yOjEwMA==" {
				return `{"data":{"rateLimit":{"cost":1},"node":{"files":{"pageInfo":{"hasNextPage":true,"endCursor":"Y3Vyc29yOjIwMA=="},"nodes":[{"path":"zz/late.go"}]}}}}`
			}
			return `{"data":{"rateLimit":{"cost":1},"node":{"files":{"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[{"path":"zz/later.go"}]}}}}`
		}
		return firstPage
	}, noREST(t))
	ds, rl, err := gh.Details(context.Background(), []string{"PR_kwDOA1"})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	assert.Equal(t, []string{"kev.yaml", "zz/late.go", "zz/later.go"}, ds[0].Files)
	assert.Equal(t, []any{"Y3Vyc29yOjEwMA==", "Y3Vyc29yOjIwMA=="}, cursors)
	assert.Equal(t, 3, rl.Cost, "every page's cost is counted")
}

func TestDetailsFetchesConcurrentlyInOrder(t *testing.T) {
	// A watch's first poll reads every open PR; doing that one request at a
	// time took about a minute for 45 PRs.
	var mu sync.Mutex
	inFlight, peak := 0, 0
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		mu.Lock()
		inFlight++
		peak = max(peak, inFlight)
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		inFlight--
		mu.Unlock()
		id := r.Variables["id"].(string)
		n := strings.TrimPrefix(id, "PR_")
		return strings.NewReplacer(`"id":"PR_kwDOA1"`, `"id":"`+id+`"`, `"number":1234`, `"number":`+n).
			Replace(detailResp)
	}, noREST(t))

	var ids []string
	for i := 1; i <= 16; i++ {
		ids = append(ids, fmt.Sprintf("PR_%d", i))
	}
	start := time.Now()
	ds, rl, err := gh.Details(context.Background(), ids)
	require.NoError(t, err)
	require.Len(t, ds, 16)
	for i, d := range ds {
		assert.Equal(t, i+1, d.Key.Number, "results keep the requested order")
	}
	assert.Equal(t, 16, rl.Cost, "every request's cost is counted")
	assert.Greater(t, peak, 1, "requests overlap")
	assert.LessOrEqual(t, peak, detailConcurrency, "concurrency is bounded")
	assert.Less(t, time.Since(start), 16*30*time.Millisecond)
}

func TestDetailsSkipsInvalidRepo(t *testing.T) {
	gh, _ := fakeGH(t, func(gqlRequest) string {
		return strings.Replace(detailResp, `"name":"stereo"`, `"name":".."`, 1)
	}, noREST(t))
	ds, _, err := gh.Details(context.Background(), []string{"PR_kwDOA1"})
	require.NoError(t, err)
	assert.Empty(t, ds)
}

func TestRequiredContexts(t *testing.T) {
	gh, hits := fakeGH(t, noGQL(t), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/chainguard-dev/mono/rules/branches/main":
			_, _ = w.Write(
				[]byte(
					`[{"type":"pull_request"},{"type":"required_status_checks","parameters":{"required_status_checks":[
				{"context":"check-go-complete","integration_id":15368},{"context":"lint"}]}}]`,
				),
			)
		case "/repos/chainguard-dev/mono/branches/main/protection/required_status_checks":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Branch not protected"}`))
		default:
			t.Fatalf("unexpected %s", r.URL.Path)
		}
	})
	req, err := gh.RequiredContexts(context.Background(), monoKey, "main")
	require.NoError(t, err)
	assert.Equal(t, RequiredContexts{"check-go-complete": {15368}, "lint": nil}, req)
	first := hits.Load()

	again, err := gh.RequiredContexts(context.Background(), monoKey, "main")
	require.NoError(t, err)
	assert.Equal(t, req, again)
	assert.Equal(t, first, hits.Load(), "second call is served from cache")
}

func TestRequiredContextsClassicProtection(t *testing.T) {
	gh, _ := fakeGH(t, noGQL(t), func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/chainguard-dev/stereo/rules/branches/main":
			_, _ = w.Write([]byte(`[]`))
		case "/repos/chainguard-dev/stereo/branches/main/protection/required_status_checks":
			_, _ = w.Write(
				[]byte(
					`{"strict":false,"contexts":["build"],"checks":[{"context":"build","app_id":-1}]}`,
				),
			)
		}
	})
	req, err := gh.RequiredContexts(
		context.Background(),
		PRKey{"chainguard-dev", "stereo", 1},
		"main",
	)
	require.NoError(t, err)
	assert.Equal(t, RequiredContexts{"build": nil}, req)
}

func TestReplyAndResolveThread(t *testing.T) {
	var reqs []gqlRequest
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		reqs = append(reqs, r)
		if strings.Contains(r.Query, "addPullRequestReviewThreadReply") {
			return `{"data":{"addPullRequestReviewThreadReply":{"comment":{"id":"PRRC_9"}}}}`
		}
		return `{"data":{"resolveReviewThread":{"thread":{"isResolved":true}}}}`
	}, noREST(t))
	id, err := gh.ReplyThread(
		context.Background(),
		"PRRT_1",
		"The loop still never exits when ctx is cancelled: `run` ignores `ctx.Done()`.",
	)
	require.NoError(t, err)
	assert.Equal(t, "PRRC_9", id)
	require.NoError(t, gh.ResolveThread(context.Background(), "PRRT_1"))
	require.Len(t, reqs, 2)
	assert.Equal(t, "PRRT_1", reqs[0].Variables["id"])
	assert.True(t, strings.HasSuffix(reqs[0].Variables["b"].(string), Marker))
	assert.Equal(t, "PRRT_1", reqs[1].Variables["id"])
}

func TestReviewThreads(t *testing.T) {
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		return `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
			{"id":"PRRT_a","path":"bots/kev/main.go","line":12,"comments":{"nodes":[{"pullRequestReview":{"databaseId":3311}}]}},
			{"id":"PRRT_b","comments":{"nodes":[{"pullRequestReview":{"databaseId":42}}]}},
			{"id":"PRRT_c","path":"bots/kev/sync.go","line":3,"comments":{"nodes":[{"pullRequestReview":{"databaseId":3311}}]}}
		]}}}}}`
	}, noREST(t))
	ids, err := gh.ReviewThreads(
		context.Background(),
		monoKey,
		"https://github.com/chainguard-dev/mono/pull/62436#pullrequestreview-3311",
	)
	require.NoError(t, err)
	assert.Equal(
		t,
		[]ThreadRef{
			{ID: "PRRT_a", Path: "bots/kev/main.go", Line: 12},
			{ID: "PRRT_c", Path: "bots/kev/sync.go", Line: 3},
		},
		ids,
	)

	_, err = gh.ReviewThreads(
		context.Background(),
		monoKey,
		"https://github.com/chainguard-dev/mono/pull/62436",
	)
	assert.Error(t, err)
}

func TestViewerAndResolve(t *testing.T) {
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		if strings.Contains(r.Query, "viewer") {
			return `{"data":{"viewer":{"login":"kbsteere"}}}`
		}
		return `{"data":{"repository":{"pullRequest":{"id":"PR_kwDO88"}}}}`
	}, noREST(t))
	v, err := gh.Viewer(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "kbsteere", v)
	id, err := gh.Resolve(context.Background(), PRKey{"chainguard-dev", "omnibump", 88})
	require.NoError(t, err)
	assert.Equal(t, "PR_kwDO88", id)
}

func TestPostReviewFoldSendsAnEmptyCommentList(t *testing.T) {
	// GitHub answered mono#58394's fallback with "For 'properties/comments',
	// nil is not an array."
	var bodies []string
	gh, _ := fakeGH(t, noGQL(t), func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if strings.Contains(string(b), `"comments":null`) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"message":"Invalid request.\n\nFor 'properties/comments', nil is not an array."}`,
				),
			)
			return
		}
		if len(bodies) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write(
				[]byte(
					`{"message":"Unprocessable Entity","errors":["Line could not be resolved"]}`,
				),
			)
			return
		}
		_, _ = w.Write([]byte(`{"html_url":"u"}`))
	})
	_, err := gh.PostReview(t.Context(), monoKey, ReviewPayload{
		CommitID: strings.Repeat("a", 40), Event: "COMMENT",
		Comments: []ReviewComment{{Path: "a.go", Line: 999, Body: "x"}},
	})
	require.NoError(t, err)
	require.Len(t, bodies, 2)
	assert.Contains(t, bodies[1], `"comments":[]`)
}

func TestPostReviewReportsBothErrors(t *testing.T) {
	calls := 0
	gh, _ := fakeGH(t, noGQL(t), func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnprocessableEntity)
		if calls == 1 {
			_, _ = w.Write([]byte(`{"message":"Line could not be resolved"}`))
			return
		}
		_, _ = w.Write([]byte(`{"message":"Review body too long"}`))
	})
	_, err := gh.PostReview(t.Context(), monoKey, ReviewPayload{
		CommitID: strings.Repeat("a", 40), Event: "COMMENT",
		Comments: []ReviewComment{{Path: "a.go", Line: 999, Body: "x"}},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Line could not be resolved", "the first reason is kept")
	assert.Contains(t, err.Error(), "Review body too long")
}

func TestDetailsPaginatesChecks(t *testing.T) {
	// mono#66370 reports 182 checks; the required codeowners-gate and
	// acid-sandbox (presubmit) were on the second page, so reading only the
	// first left the PR pending forever.
	firstPage := strings.Replace(
		detailResp,
		`"statusCheckRollup":{"contexts":{"nodes":[`,
		`"statusCheckRollup":{"contexts":{"pageInfo":{"hasNextPage":true,"endCursor":"Y3Vyc29yOjEwMA=="},"nodes":[`,
		1,
	)
	var cursors []any
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		if strings.Contains(r.Query, "contexts(first: 100, after: $after)") {
			cursors = append(cursors, r.Variables["after"])
			return `{"data":{"rateLimit":{"cost":1},"node":{"commits":{"nodes":[{"commit":{"statusCheckRollup":{"contexts":{` +
				`"pageInfo":{"hasNextPage":false,"endCursor":""},"nodes":[` +
				`{"__typename":"CheckRun","databaseId":112621999001,"name":"codeowners-gate","status":"COMPLETED","conclusion":"SUCCESS","startedAt":"2026-10-07T03:47:00Z","isRequired":true,"checkSuite":{"app":{"databaseId":5149240}}},` +
				`{"__typename":"CheckRun","databaseId":112621999002,"name":"acid-sandbox (presubmit)","status":"COMPLETED","conclusion":"SUCCESS","startedAt":"2026-10-07T03:48:00Z","isRequired":true,"checkSuite":{"app":{"databaseId":210473}}}` +
				`]}}}}]}}}}`
		}
		return firstPage
	}, noREST(t))
	ds, rl, err := gh.Details(t.Context(), []string{"PR_kwDOA1"})
	require.NoError(t, err)
	require.Len(t, ds, 1)
	assert.Equal(t, []any{"Y3Vyc29yOjEwMA=="}, cursors)
	assert.Equal(t, 2, rl.Cost, "the extra page's cost is counted")
	names := make([]string, 0, len(ds[0].Checks))
	for _, c := range ds[0].Checks {
		names = append(names, c.Name)
	}
	assert.Equal(
		t,
		[]string{"build", "ci/legacy", "codeowners-gate", "acid-sandbox (presubmit)"},
		names,
	)
	last := ds[0].Checks[3]
	assert.Equal(t, int64(112621999002), last.ID)
	assert.Equal(t, time.Date(2026, 10, 7, 3, 48, 0, 0, time.UTC), last.StartedAt)

	req := RequiredContexts{
		"codeowners-gate":          {5149240},
		"acid-sandbox (presubmit)": {210473},
		"build":                    nil,
	}
	assert.Equal(t, "SUCCESS", RequiredState(ds[0].Checks, req, last.StartedAt.Add(time.Hour)))
}

func TestOpenPRs(t *testing.T) {
	keys := []PRKey{
		{"chainguard-dev", "mono", 66370},
		{"chainguard-dev", "stereo", 66370},
		{"chainguard-dev", "omnibump", 66370},
	}
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		assert.Equal(t, "mono", r.Variables["r0"])
		assert.Equal(t, "omnibump", r.Variables["r2"])
		assert.EqualValues(t, 66370, r.Variables["n1"])
		assert.NotContains(t, r.Query, "mono", "names travel as variables, not query text")
		// GitHub answers what it found and reports the missing PR as an error.
		return `{"data":{
		  "p0":{"pullRequest":{"state":"OPEN"}},
		  "p1":{"pullRequest":{"state":"MERGED"}},
		  "p2":{"pullRequest":null}},
		 "errors":[{"type":"NOT_FOUND","path":["p2","pullRequest"],
		  "message":"Could not resolve to a PullRequest with the number of 66370."}]}`
	}, noREST(t))

	open, err := gh.OpenPRs(context.Background(), keys)
	require.NoError(t, err, "a missing PR is not a failure")
	assert.Equal(t, []PRKey{{"chainguard-dev", "mono", 66370}}, open, "merged PRs are left out")
}

func TestOpenPRsOtherErrors(t *testing.T) {
	gh, _ := fakeGH(t, func(gqlRequest) string {
		return `{"data":null,"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`
	}, noREST(t))
	_, err := gh.OpenPRs(context.Background(), []PRKey{{"chainguard-dev", "mono", 1}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "rate limit")
}

func TestPostFileComment(t *testing.T) {
	var got map[string]any
	gh, _ := fakeGH(t, func(r gqlRequest) string {
		require.Contains(t, r.Query, "reviewThreads")
		// The PR's threads: an older one by someone else, and the new file
		// thread from review 9001.
		return `{"data":{"repository":{"pullRequest":{"reviewThreads":{"nodes":[
		  {"id":"PRRT_old","path":"pkg/memusage/heartbeat.go","line":115,
		   "comments":{"nodes":[{"pullRequestReview":{"databaseId":17}}]}},
		  {"id":"PRRT_file","path":"pkg/memusage/heartbeat.go","line":null,
		   "comments":{"nodes":[{"pullRequestReview":{"databaseId":9001}}]}}]}}}}}`
	}, func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "/repos/chainguard-dev/mono/pulls/62436/comments", r.URL.Path)
		b, _ := io.ReadAll(r.Body)
		require.NoError(t, json.Unmarshal(b, &got))
		_, _ = w.Write([]byte(`{"id":5,"node_id":"PRRC_5","pull_request_review_id":9001}`))
	})
	sha := strings.Repeat("a", 40)
	id, err := gh.PostFileComment(context.Background(), monoKey, sha,
		"pkg/memusage/heartbeat.go", "Line 304: non-blocking: no test covers the guard.")
	require.NoError(t, err)
	assert.Equal(t, "PRRT_file", id)
	assert.Equal(t, map[string]any{
		"commit_id":    sha,
		"path":         "pkg/memusage/heartbeat.go",
		"subject_type": "file",
		"body":         "Line 304: non-blocking: no test covers the guard.\n\n" + Marker,
	}, got, "a file thread sends no line")
}
