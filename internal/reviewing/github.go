package reviewing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cli/go-gh/v2/pkg/api"
)

// Marker is a hidden HTML comment carried by everything dispatch posts, so
// its own threads can be recognised.
const Marker = "<!-- dispatch -->"

const requiredContextsTTL = 15 * time.Minute

type RateLimit struct {
	Cost      int
	Remaining int
	ResetAt   time.Time
}

type SearchHit struct {
	Key       PRKey
	NodeID    string
	UpdatedAt string
}

type Comment struct {
	ID        string
	Author    string
	Body      string
	CreatedAt time.Time
}

type Review struct {
	Author      string
	State       string
	CommitOID   string
	Body        string
	SubmittedAt time.Time
}

type Thread struct {
	ID         string
	IsResolved bool
	IsOutdated bool
	ResolvedBy string
	Path       string
	Line       int
	Comments   []Comment
}

// PRDetail is everything a review pass reads about a PR from GitHub.
type PRDetail struct {
	Key       PRKey
	NodeID    string
	State     string
	Author    string
	Title     string
	Body      string
	IsDraft   bool
	HeadOID   string
	BaseRef   string
	BaseOID   string
	Mergeable string
	UpdatedAt string
	// CreatedAt is when the PR was opened.
	CreatedAt time.Time
	// ClosedAt is when the PR was merged or closed; zero while open.
	ClosedAt time.Time
	Files    []string
	Comments []Comment
	Reviews  []Review
	Threads  []Thread
	Checks   []CheckNode
}

// Events lists the PR's activity and who resolved each resolved thread.
func (d PRDetail) Events() ([]Event, []string) {
	var events []Event
	var resolved []string
	for _, c := range d.Comments {
		events = append(events, Event{At: c.CreatedAt, Kind: "c", Login: c.Author})
	}
	for _, r := range d.Reviews {
		if !r.SubmittedAt.IsZero() {
			events = append(events, Event{At: r.SubmittedAt, Kind: "r", Login: r.Author})
		}
	}
	for _, t := range d.Threads {
		if t.IsResolved {
			resolved = append(resolved, t.ResolvedBy)
		}
		for _, c := range t.Comments {
			events = append(events, Event{At: c.CreatedAt, Kind: "t", Login: c.Author})
		}
	}
	return events, resolved
}

type ReviewComment struct {
	Path      string `json:"path"`
	Line      int    `json:"line"`
	StartLine int    `json:"start_line,omitempty"`
	Side      string `json:"side"`
	Body      string `json:"body"`
	// SubjectType "file" marks a finding on a line GitHub cannot take an
	// inline comment on: it is posted as a thread on the whole file, and
	// Line is kept only to say where in the file it is.
	SubjectType string `json:"subject_type,omitempty"`
}

type ReviewPayload struct {
	CommitID string          `json:"commit_id"`
	Event    string          `json:"event"`
	Body     string          `json:"body"`
	Comments []ReviewComment `json:"comments"`
}

// GitHub is the GitHub access the engine needs.
type GitHub interface {
	Viewer(ctx context.Context) (string, error)
	Search(ctx context.Context, query string, maxPages int) ([]SearchHit, RateLimit, error)
	Details(ctx context.Context, nodeIDs []string) ([]PRDetail, RateLimit, error)
	Resolve(ctx context.Context, k PRKey) (string, error)
	OpenPRs(ctx context.Context, keys []PRKey) ([]PRKey, error)
	RequiredContexts(ctx context.Context, k PRKey, branch string) (RequiredContexts, error)
	PostReview(ctx context.Context, k PRKey, p ReviewPayload) (string, error)
	ReplyThread(ctx context.Context, threadID, body string) (string, error)
	ResolveThread(ctx context.Context, threadID string) error
	ReviewThreads(ctx context.Context, k PRKey, reviewURL string) ([]ThreadRef, error)
	PostFileComment(ctx context.Context, k PRKey, commitID, path, body string) (string, error)
	CheckReport(ctx context.Context, k PRKey, checkRunID int64) (CheckReport, error)
	BaseChecks(ctx context.Context, k PRKey, branch string) (map[string]string, error)
}

// WithMarker appends the hidden dispatch marker to a body.
func WithMarker(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return Marker
	}
	return body + "\n\n" + Marker
}

// FoldComments moves inline comments into the review body, for when GitHub
// rejects their line anchors.
func FoldComments(p ReviewPayload) ReviewPayload {
	var parts []string
	if b := strings.TrimSpace(strings.ReplaceAll(p.Body, Marker, "")); b != "" {
		parts = append(parts, b)
	}
	for _, c := range p.Comments {
		parts = append(
			parts,
			fmt.Sprintf(
				"**`%s:%d`**\n%s",
				c.Path,
				c.Line,
				strings.TrimSpace(strings.ReplaceAll(c.Body, Marker, "")),
			),
		)
	}
	p.Body = strings.Join(parts, "\n\n")
	// GitHub rejects a null comment list; send an empty one.
	p.Comments = []ReviewComment{}
	return p
}

type cachedContexts struct {
	at  time.Time
	req RequiredContexts
}

type ghClient struct {
	gql  *api.GraphQLClient
	rest *api.RESTClient
	now  func() time.Time

	mu       sync.Mutex
	reqCache map[string]cachedContexts
}

// NewGitHub wraps go-gh clients.
func NewGitHub(gql *api.GraphQLClient, rest *api.RESTClient) GitHub {
	return &ghClient{gql: gql, rest: rest, now: time.Now, reqCache: map[string]cachedContexts{}}
}

// DefaultGitHub uses gh's configured authentication. Construction errors
// are returned from every call.
func DefaultGitHub() GitHub {
	gql, err := api.DefaultGraphQLClient()
	if err != nil {
		return failingGitHub{err}
	}
	rest, err := api.DefaultRESTClient()
	if err != nil {
		return failingGitHub{err}
	}
	return NewGitHub(gql, rest)
}

type rateLimitJSON struct {
	Cost      int       `json:"cost"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
}

func (r rateLimitJSON) toRateLimit() RateLimit {
	return RateLimit(r)
}

type loginJSON struct {
	Login string `json:"login"`
}

func loginOf(l *loginJSON) string {
	if l == nil || l.Login == "" {
		return "ghost"
	}
	return l.Login
}

type repoJSON struct {
	Name  string    `json:"name"`
	Owner loginJSON `json:"owner"`
}

const viewerQuery = `query { viewer { login } }`

func (g *ghClient) Viewer(ctx context.Context) (string, error) {
	var resp struct {
		Viewer loginJSON `json:"viewer"`
	}
	if err := g.gql.DoWithContext(ctx, viewerQuery, nil, &resp); err != nil {
		return "", err
	}
	return resp.Viewer.Login, nil
}

const searchQuery = `query($q: String!, $after: String) {
  rateLimit { cost remaining resetAt }
  search(type: ISSUE, first: 100, after: $after, query: $q) {
    pageInfo { hasNextPage endCursor }
    nodes { ... on PullRequest { id number updatedAt repository { name owner { login } } } }
  }
}`

func (g *ghClient) Search(
	ctx context.Context,
	query string,
	maxPages int,
) ([]SearchHit, RateLimit, error) {
	var hits []SearchHit
	var rl RateLimit
	var after *string
	for page := 0; page < maxPages; page++ {
		var resp struct {
			RateLimit rateLimitJSON `json:"rateLimit"`
			Search    struct {
				PageInfo struct {
					HasNextPage bool   `json:"hasNextPage"`
					EndCursor   string `json:"endCursor"`
				} `json:"pageInfo"`
				Nodes []struct {
					ID         string   `json:"id"`
					Number     int      `json:"number"`
					UpdatedAt  string   `json:"updatedAt"`
					Repository repoJSON `json:"repository"`
				} `json:"nodes"`
			} `json:"search"`
		}
		vars := map[string]any{"q": "is:pr " + query, "after": after}
		if err := g.gql.DoWithContext(ctx, searchQuery, vars, &resp); err != nil {
			return hits, rl, err
		}
		cost := rl.Cost + resp.RateLimit.Cost
		rl = resp.RateLimit.toRateLimit()
		rl.Cost = cost
		for _, n := range resp.Search.Nodes {
			if n.ID == "" || n.Number <= 0 || !ValidName(n.Repository.Owner.Login) ||
				!ValidName(n.Repository.Name) {
				continue
			}
			hits = append(hits, SearchHit{
				Key: PRKey{
					Owner:  n.Repository.Owner.Login,
					Repo:   n.Repository.Name,
					Number: n.Number,
				},
				NodeID:    n.ID,
				UpdatedAt: n.UpdatedAt,
			})
		}
		if !resp.Search.PageInfo.HasNextPage {
			break
		}
		cursor := resp.Search.PageInfo.EndCursor
		after = &cursor
	}
	return hits, rl, nil
}

const detailQuery = `query($id: ID!) {
  rateLimit { cost remaining resetAt }
  node(id: $id) { ... on PullRequest {
    id number state isDraft title body createdAt updatedAt closedAt mergeable
    author { login }
    headRefOid baseRefName baseRefOid
    repository { name owner { login } }
    files(first: 100) { pageInfo { hasNextPage endCursor } nodes { path } }
    comments(last: 50) { nodes { id body createdAt author { login } } }
    reviews(last: 50) { nodes { state body submittedAt author { login } commit { oid } } }
    reviewThreads(last: 100) { nodes {
      id isResolved isOutdated path line resolvedBy { login }
      comments(first: 20) { nodes { id body createdAt author { login } } }
    } }
    commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100) {
      pageInfo { hasNextPage endCursor } nodes { ` + contextFields + ` }
    } } } } }
  } }
}`

type commentJSON struct {
	ID        string     `json:"id"`
	Body      string     `json:"body"`
	CreatedAt time.Time  `json:"createdAt"`
	Author    *loginJSON `json:"author"`
}

func (c commentJSON) toComment() Comment {
	return Comment{ID: c.ID, Author: loginOf(c.Author), Body: c.Body, CreatedAt: c.CreatedAt}
}

type detailJSON struct {
	ID         string        `json:"id"`
	Number     int           `json:"number"`
	State      string        `json:"state"`
	IsDraft    bool          `json:"isDraft"`
	Title      string        `json:"title"`
	Body       string        `json:"body"`
	CreatedAt  time.Time     `json:"createdAt"`
	UpdatedAt  string        `json:"updatedAt"`
	ClosedAt   time.Time     `json:"closedAt"`
	Mergeable  string        `json:"mergeable"`
	Author     *loginJSON    `json:"author"`
	HeadRefOid string        `json:"headRefOid"`
	BaseRef    string        `json:"baseRefName"`
	BaseRefOid string        `json:"baseRefOid"`
	Repository repoJSON      `json:"repository"`
	Files      filesPageJSON `json:"files"`
	Comments   struct {
		Nodes []commentJSON `json:"nodes"`
	} `json:"comments"`
	Reviews struct {
		Nodes []struct {
			State       string     `json:"state"`
			Body        string     `json:"body"`
			SubmittedAt *time.Time `json:"submittedAt"`
			Author      *loginJSON `json:"author"`
			Commit      *struct {
				Oid string `json:"oid"`
			} `json:"commit"`
		} `json:"nodes"`
	} `json:"reviews"`
	ReviewThreads struct {
		Nodes []struct {
			ID         string     `json:"id"`
			IsResolved bool       `json:"isResolved"`
			IsOutdated bool       `json:"isOutdated"`
			Path       string     `json:"path"`
			Line       int        `json:"line"`
			ResolvedBy *loginJSON `json:"resolvedBy"`
			Comments   struct {
				Nodes []commentJSON `json:"nodes"`
			} `json:"comments"`
		} `json:"nodes"`
	} `json:"reviewThreads"`
	Commits commitsJSON `json:"commits"`
}

func (n detailJSON) toDetail() PRDetail {
	d := PRDetail{
		Key: PRKey{
			Owner:  n.Repository.Owner.Login,
			Repo:   n.Repository.Name,
			Number: n.Number,
		},
		NodeID:    n.ID,
		State:     n.State,
		Author:    loginOf(n.Author),
		Title:     n.Title,
		Body:      n.Body,
		IsDraft:   n.IsDraft,
		HeadOID:   n.HeadRefOid,
		BaseRef:   n.BaseRef,
		BaseOID:   n.BaseRefOid,
		Mergeable: n.Mergeable,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
		ClosedAt:  n.ClosedAt,
	}
	for _, f := range n.Files.Nodes {
		d.Files = append(d.Files, f.Path)
	}
	for _, c := range n.Comments.Nodes {
		d.Comments = append(d.Comments, c.toComment())
	}
	for _, r := range n.Reviews.Nodes {
		rv := Review{Author: loginOf(r.Author), State: r.State, Body: r.Body}
		if r.SubmittedAt != nil {
			rv.SubmittedAt = *r.SubmittedAt
		}
		if r.Commit != nil {
			rv.CommitOID = r.Commit.Oid
		}
		d.Reviews = append(d.Reviews, rv)
	}
	for _, t := range n.ReviewThreads.Nodes {
		th := Thread{
			ID:         t.ID,
			IsResolved: t.IsResolved,
			IsOutdated: t.IsOutdated,
			Path:       t.Path,
			Line:       t.Line,
		}
		if t.ResolvedBy != nil {
			th.ResolvedBy = t.ResolvedBy.Login
		}
		for _, c := range t.Comments.Nodes {
			th.Comments = append(th.Comments, c.toComment())
		}
		d.Threads = append(d.Threads, th)
	}
	for _, x := range n.Commits.contexts().Nodes {
		d.Checks = append(d.Checks, x.toCheckNode())
	}
	return d
}

// detailConcurrency bounds how many PRs Details fetches at once.
const detailConcurrency = 8

// Details fetches each PR separately, because isRequired needs the PR's id,
// with up to detailConcurrency requests in flight. Results keep the order of
// nodeIDs; PRs that no longer exist or have invalid names are skipped.
func (g *ghClient) Details(ctx context.Context, nodeIDs []string) ([]PRDetail, RateLimit, error) {
	type result struct {
		d   *PRDetail
		rl  RateLimit
		err error
	}
	results := make([]result, len(nodeIDs))
	sem := make(chan struct{}, detailConcurrency)
	var wg sync.WaitGroup
	for i, id := range nodeIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			d, rl, err := g.detail(ctx, id)
			results[i] = result{d, rl, err}
		}()
	}
	wg.Wait()

	var out []PRDetail
	var rl RateLimit
	cost := 0
	var firstErr error
	for _, r := range results {
		cost += r.rl.Cost
		// The lowest remaining quota is the most recent reading.
		if !r.rl.ResetAt.IsZero() && (rl.ResetAt.IsZero() || r.rl.Remaining < rl.Remaining) {
			rl = r.rl
		}
		if r.err != nil && firstErr == nil {
			firstErr = r.err
		}
		if r.d != nil {
			out = append(out, *r.d)
		}
	}
	rl.Cost = cost
	return out, rl, firstErr
}

// detail fetches one PR, including every page of its changed files.
func (g *ghClient) detail(ctx context.Context, id string) (*PRDetail, RateLimit, error) {
	var resp struct {
		RateLimit rateLimitJSON `json:"rateLimit"`
		Node      *detailJSON   `json:"node"`
	}
	if err := g.gql.DoWithContext(ctx, detailQuery, map[string]any{"id": id}, &resp); err != nil {
		return nil, RateLimit{}, err
	}
	rl := resp.RateLimit.toRateLimit()
	if resp.Node == nil || resp.Node.ID == "" {
		return nil, rl, nil
	}
	d := resp.Node.toDetail()
	if !ValidName(d.Key.Owner) || !ValidName(d.Key.Repo) || d.Key.Number <= 0 {
		return nil, rl, nil
	}
	more, cost, err := g.remainingFiles(ctx, id, resp.Node.Files.PageInfo)
	rl.Cost += cost
	if err != nil {
		return nil, rl, err
	}
	d.Files = append(d.Files, more...)
	checks, cost, err := g.remainingChecks(ctx, id, resp.Node.Commits.contexts().PageInfo)
	rl.Cost += cost
	if err != nil {
		return nil, rl, err
	}
	d.Checks = append(d.Checks, checks...)
	return &d, rl, nil
}

type pageInfoJSON struct {
	HasNextPage bool   `json:"hasNextPage"`
	EndCursor   string `json:"endCursor"`
}

type filesPageJSON struct {
	PageInfo pageInfoJSON `json:"pageInfo"`
	Nodes    []struct {
		Path string `json:"path"`
	} `json:"nodes"`
}

// maxFilePages bounds file pagination; GitHub lists at most 3000 files.
const maxFilePages = 30

const filesPageQuery = `query($id: ID!, $after: String) {
  rateLimit { cost }
  node(id: $id) { ... on PullRequest { files(first: 100, after: $after) { pageInfo { hasNextPage endCursor } nodes { path } } } }
}`

// remainingFiles fetches the changed files beyond the first page, so a
// finding on any changed file can be anchored.
func (g *ghClient) remainingFiles(
	ctx context.Context,
	id string,
	page pageInfoJSON,
) ([]string, int, error) {
	var files []string
	cost := 0
	for i := 0; page.HasNextPage && i < maxFilePages; i++ {
		var resp struct {
			RateLimit struct {
				Cost int `json:"cost"`
			} `json:"rateLimit"`
			Node struct {
				Files filesPageJSON `json:"files"`
			} `json:"node"`
		}
		vars := map[string]any{"id": id, "after": page.EndCursor}
		if err := g.gql.DoWithContext(ctx, filesPageQuery, vars, &resp); err != nil {
			return files, cost, err
		}
		cost += resp.RateLimit.Cost
		for _, n := range resp.Node.Files.Nodes {
			files = append(files, n.Path)
		}
		page = resp.Node.Files.PageInfo
	}
	return files, cost, nil
}

const resolvePRQuery = `query($o: String!, $r: String!, $n: Int!) { repository(owner: $o, name: $r) { pullRequest(number: $n) { id } } }`

func (g *ghClient) Resolve(ctx context.Context, k PRKey) (string, error) {
	var resp struct {
		Repository struct {
			PullRequest *struct {
				ID string `json:"id"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := g.gql.DoWithContext(
		ctx,
		resolvePRQuery,
		map[string]any{"o": k.Owner, "r": k.Repo, "n": k.Number},
		&resp,
	); err != nil {
		return "", err
	}
	if resp.Repository.PullRequest == nil {
		return "", fmt.Errorf("%s not found", k)
	}
	return resp.Repository.PullRequest.ID, nil
}

// OpenPRs returns which of keys name an open pull request, asking for all of
// them in one query. Keys that name no PR, or a missing repository, are left
// out.
func (g *ghClient) OpenPRs(ctx context.Context, keys []PRKey) ([]PRKey, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	var params, fields strings.Builder
	vars := make(map[string]any, 3*len(keys))
	for i, k := range keys {
		fmt.Fprintf(&params, "$o%d: String!, $r%d: String!, $n%d: Int!, ", i, i, i)
		fmt.Fprintf(
			&fields,
			"p%d: repository(owner: $o%d, name: $r%d) { pullRequest(number: $n%d) { state } } ",
			i, i, i, i,
		)
		vars[fmt.Sprintf("o%d", i)] = k.Owner
		vars[fmt.Sprintf("r%d", i)] = k.Repo
		vars[fmt.Sprintf("n%d", i)] = k.Number
	}
	query := fmt.Sprintf(
		"query(%s) { %s}",
		strings.TrimSuffix(params.String(), ", "),
		fields.String(),
	)
	var resp map[string]*struct {
		PullRequest *struct {
			State string `json:"state"`
		} `json:"pullRequest"`
	}
	if err := g.gql.DoWithContext(ctx, query, vars, &resp); err != nil && !onlyNotFound(err) {
		return nil, err
	}
	var open []PRKey
	for i, k := range keys {
		if r := resp[fmt.Sprintf("p%d", i)]; r != nil && r.PullRequest != nil &&
			r.PullRequest.State == "OPEN" {
			open = append(open, k)
		}
	}
	return open, nil
}

// onlyNotFound reports a GraphQL error whose every item is NOT_FOUND, which
// GitHub returns, alongside the data it did find, for a missing repository
// or PR number.
func onlyNotFound(err error) bool {
	var gqlErr *api.GraphQLError
	if !errors.As(err, &gqlErr) || len(gqlErr.Errors) == 0 {
		return false
	}
	for _, e := range gqlErr.Errors {
		if e.Type != "NOT_FOUND" {
			return false
		}
	}
	return true
}

func isStatus(err error, codes ...int) bool {
	var httpErr *api.HTTPError
	if !errors.As(err, &httpErr) {
		return false
	}
	for _, c := range codes {
		if httpErr.StatusCode == c {
			return true
		}
	}
	return false
}

// RequiredContexts unions the required status checks from rulesets and
// classic branch protection. Either source may be unreadable.
func (g *ghClient) RequiredContexts(
	ctx context.Context,
	k PRKey,
	branch string,
) (RequiredContexts, error) {
	cacheKey := k.NameWithOwner() + ":" + branch
	g.mu.Lock()
	if c, ok := g.reqCache[cacheKey]; ok && g.now().Sub(c.at) < requiredContextsTTL {
		g.mu.Unlock()
		return c.req, nil
	}
	g.mu.Unlock()

	req := RequiredContexts{}
	add := func(name string, app *int) {
		if name == "" {
			return
		}
		if app == nil || *app == -1 {
			req[name] = nil
			return
		}
		if cur, ok := req[name]; ok && cur == nil {
			return
		}
		req[name] = append(req[name], *app)
	}

	b := url.PathEscape(branch)
	var rules []struct {
		Type       string `json:"type"`
		Parameters struct {
			RequiredStatusChecks []struct {
				Context       string `json:"context"`
				IntegrationID *int   `json:"integration_id"`
			} `json:"required_status_checks"`
		} `json:"parameters"`
	}
	err := g.rest.DoWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf("repos/%s/%s/rules/branches/%s?per_page=100", k.Owner, k.Repo, b),
		nil,
		&rules,
	)
	if err != nil && !isStatus(err, http.StatusForbidden, http.StatusNotFound) {
		return nil, err
	}
	for _, r := range rules {
		if r.Type != "required_status_checks" {
			continue
		}
		for _, c := range r.Parameters.RequiredStatusChecks {
			add(c.Context, c.IntegrationID)
		}
	}

	var prot struct {
		Contexts []string `json:"contexts"`
		Checks   []struct {
			Context string `json:"context"`
			AppID   *int   `json:"app_id"`
		} `json:"checks"`
	}
	err = g.rest.DoWithContext(
		ctx,
		http.MethodGet,
		fmt.Sprintf(
			"repos/%s/%s/branches/%s/protection/required_status_checks",
			k.Owner,
			k.Repo,
			b,
		),
		nil,
		&prot,
	)
	if err != nil && !isStatus(err, http.StatusForbidden, http.StatusNotFound) {
		return nil, err
	}
	if len(prot.Checks) > 0 {
		for _, c := range prot.Checks {
			add(c.Context, c.AppID)
		}
	} else {
		for _, c := range prot.Contexts {
			add(c, nil)
		}
	}

	g.mu.Lock()
	g.reqCache[cacheKey] = cachedContexts{at: g.now(), req: req}
	g.mu.Unlock()
	return req, nil
}

func (g *ghClient) PostReview(ctx context.Context, k PRKey, p ReviewPayload) (string, error) {
	p.Body = WithMarker(p.Body)
	comments := make([]ReviewComment, len(p.Comments))
	for i, c := range p.Comments {
		c.Side = "RIGHT"
		c.Body = WithMarker(c.Body)
		comments[i] = c
	}
	p.Comments = comments
	path := fmt.Sprintf("repos/%s/%s/pulls/%d/reviews", k.Owner, k.Repo, k.Number)
	var resp struct {
		HTMLURL string `json:"html_url"`
	}
	err := g.postJSON(ctx, path, p, &resp)
	if err != nil && len(p.Comments) > 0 && isStatus(err, http.StatusUnprocessableEntity) {
		folded := FoldComments(p)
		folded.Body = WithMarker(folded.Body)
		if ferr := g.postJSON(ctx, path, folded, &resp); ferr != nil {
			err = fmt.Errorf("%w; posting with comments in the body also failed: %w", err, ferr)
		} else {
			err = nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("posting review on %s: %w", k, err)
	}
	return resp.HTMLURL, nil
}

func (g *ghClient) postJSON(ctx context.Context, path string, body, out any) error {
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	return g.rest.DoWithContext(ctx, http.MethodPost, path, bytes.NewReader(b), out)
}

const replyMutation = `mutation($id: ID!, $b: String!) { addPullRequestReviewThreadReply(input: {pullRequestReviewThreadId: $id, body: $b}) { comment { id } } }`

func (g *ghClient) ReplyThread(ctx context.Context, threadID, body string) (string, error) {
	var resp struct {
		Reply struct {
			Comment struct {
				ID string `json:"id"`
			} `json:"comment"`
		} `json:"addPullRequestReviewThreadReply"`
	}
	if err := g.gql.DoWithContext(
		ctx,
		replyMutation,
		map[string]any{"id": threadID, "b": WithMarker(body)},
		&resp,
	); err != nil {
		return "", err
	}
	return resp.Reply.Comment.ID, nil
}

const resolveMutation = `mutation($id: ID!) { resolveReviewThread(input: {threadId: $id}) { thread { isResolved } } }`

func (g *ghClient) ResolveThread(ctx context.Context, threadID string) error {
	var resp json.RawMessage
	return g.gql.DoWithContext(ctx, resolveMutation, map[string]any{"id": threadID}, &resp)
}

var reviewIDRe = regexp.MustCompile(`#pullrequestreview-(\d+)$`)

const reviewThreadsQuery = `query($o: String!, $r: String!, $n: Int!) { repository(owner: $o, name: $r) { pullRequest(number: $n) {
  reviewThreads(last: 100) { nodes { id path line comments(first: 1) { nodes { pullRequestReview { databaseId } } } } }
} } }`

// ThreadRef locates a review thread.
type ThreadRef struct {
	ID   string
	Path string
	Line int
}

// ReviewThreads returns the threads opened by the review at reviewURL.
func (g *ghClient) ReviewThreads(
	ctx context.Context,
	k PRKey,
	reviewURL string,
) ([]ThreadRef, error) {
	m := reviewIDRe.FindStringSubmatch(reviewURL)
	if m == nil {
		return nil, fmt.Errorf("no review id in %q", reviewURL)
	}
	reviewID, _ := strconv.Atoi(m[1])
	return g.threadsOfReview(ctx, k, reviewID)
}

// threadsOfReview returns the threads review reviewID opened on k.
func (g *ghClient) threadsOfReview(
	ctx context.Context,
	k PRKey,
	reviewID int,
) ([]ThreadRef, error) {
	var resp struct {
		Repository struct {
			PullRequest struct {
				ReviewThreads struct {
					Nodes []struct {
						ID       string `json:"id"`
						Path     string `json:"path"`
						Line     int    `json:"line"`
						Comments struct {
							Nodes []struct {
								PullRequestReview *struct {
									DatabaseID int `json:"databaseId"`
								} `json:"pullRequestReview"`
							} `json:"nodes"`
						} `json:"comments"`
					} `json:"nodes"`
				} `json:"reviewThreads"`
			} `json:"pullRequest"`
		} `json:"repository"`
	}
	if err := g.gql.DoWithContext(
		ctx,
		reviewThreadsQuery,
		map[string]any{"o": k.Owner, "r": k.Repo, "n": k.Number},
		&resp,
	); err != nil {
		return nil, err
	}
	var refs []ThreadRef
	for _, t := range resp.Repository.PullRequest.ReviewThreads.Nodes {
		if len(t.Comments.Nodes) == 0 || t.Comments.Nodes[0].PullRequestReview == nil {
			continue
		}
		if t.Comments.Nodes[0].PullRequestReview.DatabaseID == reviewID {
			refs = append(refs, ThreadRef{ID: t.ID, Path: t.Path, Line: t.Line})
		}
	}
	return refs, nil
}

// failingGitHub reports a client construction error from every call.
type failingGitHub struct{ err error }

func (f failingGitHub) Viewer(context.Context) (string, error) { return "", f.err }
func (f failingGitHub) Search(context.Context, string, int) ([]SearchHit, RateLimit, error) {
	return nil, RateLimit{}, f.err
}

func (f failingGitHub) Details(context.Context, []string) ([]PRDetail, RateLimit, error) {
	return nil, RateLimit{}, f.err
}
func (f failingGitHub) Resolve(context.Context, PRKey) (string, error) { return "", f.err }
func (f failingGitHub) RequiredContexts(context.Context, PRKey, string) (RequiredContexts, error) {
	return nil, f.err
}

func (f failingGitHub) PostReview(context.Context, PRKey, ReviewPayload) (string, error) {
	return "", f.err
}

func (f failingGitHub) ReplyThread(
	context.Context,
	string,
	string,
) (string, error) {
	return "", f.err
}
func (f failingGitHub) ResolveThread(context.Context, string) error { return f.err }
func (f failingGitHub) ReviewThreads(context.Context, PRKey, string) ([]ThreadRef, error) {
	return nil, f.err
}

type commitsJSON struct {
	Nodes []struct {
		Commit struct {
			StatusCheckRollup *struct {
				Contexts contextsPageJSON `json:"contexts"`
			} `json:"statusCheckRollup"`
		} `json:"commit"`
	} `json:"nodes"`
}

// contexts is the head commit's check page, if it has one.
func (c commitsJSON) contexts() contextsPageJSON {
	for _, n := range c.Nodes {
		if n.Commit.StatusCheckRollup != nil {
			return n.Commit.StatusCheckRollup.Contexts
		}
	}
	return contextsPageJSON{}
}

type contextsPageJSON struct {
	PageInfo pageInfoJSON  `json:"pageInfo"`
	Nodes    []contextJSON `json:"nodes"`
}

type contextJSON struct {
	Typename   string    `json:"__typename"`
	Name       string    `json:"name"`
	DatabaseID int64     `json:"databaseId"`
	StartedAt  time.Time `json:"startedAt"`
	CreatedAt  time.Time `json:"createdAt"`
	Context    string    `json:"context"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion"`
	State      string    `json:"state"`
	IsRequired bool      `json:"isRequired"`
	CheckSuite *struct {
		DatabaseID int64 `json:"databaseId"`
		App        *struct {
			DatabaseID int `json:"databaseId"`
		} `json:"app"`
		WorkflowRun *struct {
			Workflow *struct {
				DatabaseID int64 `json:"databaseId"`
			} `json:"workflow"`
		} `json:"workflowRun"`
	} `json:"checkSuite"`
}

func (x contextJSON) toCheckNode() CheckNode {
	node := CheckNode{
		Typename:   x.Typename,
		Name:       x.Name,
		Status:     x.Status,
		Conclusion: x.Conclusion,
		State:      x.State,
		IsRequired: x.IsRequired,
		ID:         x.DatabaseID,
		StartedAt:  x.StartedAt,
	}
	if x.Typename == "StatusContext" {
		node.Name = x.Context
		node.StartedAt = x.CreatedAt
	}
	if x.CheckSuite != nil {
		node.SuiteID = x.CheckSuite.DatabaseID
		if x.CheckSuite.App != nil {
			node.AppID = x.CheckSuite.App.DatabaseID
		}
		if r := x.CheckSuite.WorkflowRun; r != nil && r.Workflow != nil {
			node.WorkflowID = r.Workflow.DatabaseID
		}
	}
	return node
}

// contextFields are the check fields fetched on every page.
const contextFields = `__typename
      ... on CheckRun { databaseId name status conclusion startedAt isRequired(pullRequestId: $id) checkSuite { databaseId app { databaseId } workflowRun { workflow { databaseId } } } }
      ... on StatusContext { context state createdAt isRequired(pullRequestId: $id) }`

const checksPageQuery = `query($id: ID!, $after: String) {
  rateLimit { cost }
  node(id: $id) { ... on PullRequest {
    commits(last: 1) { nodes { commit { statusCheckRollup { contexts(first: 100, after: $after) {
      pageInfo { hasNextPage endCursor } nodes { ` + contextFields + ` }
    } } } } }
  } }
}`

// maxCheckPages bounds check pagination. A large repository can report
// well over 100 checks on one commit, and a required one may be on any page.
const maxCheckPages = 10

// remainingChecks fetches the head commit's checks beyond the first page.
func (g *ghClient) remainingChecks(
	ctx context.Context,
	id string,
	page pageInfoJSON,
) ([]CheckNode, int, error) {
	var nodes []CheckNode
	cost := 0
	for i := 0; page.HasNextPage && i < maxCheckPages; i++ {
		var resp struct {
			RateLimit struct {
				Cost int `json:"cost"`
			} `json:"rateLimit"`
			Node struct {
				Commits commitsJSON `json:"commits"`
			} `json:"node"`
		}
		vars := map[string]any{"id": id, "after": page.EndCursor}
		if err := g.gql.DoWithContext(ctx, checksPageQuery, vars, &resp); err != nil {
			return nodes, cost, err
		}
		cost += resp.RateLimit.Cost
		ctxs := resp.Node.Commits.contexts()
		for _, x := range ctxs.Nodes {
			nodes = append(nodes, x.toCheckNode())
		}
		page = ctxs.PageInfo
	}
	return nodes, cost, nil
}

func (f failingGitHub) CheckReport(context.Context, PRKey, int64) (CheckReport, error) {
	return CheckReport{}, f.err
}

func (f failingGitHub) BaseChecks(context.Context, PRKey, string) (map[string]string, error) {
	return nil, f.err
}

func (f failingGitHub) OpenPRs(context.Context, []PRKey) ([]PRKey, error) {
	return nil, f.err
}

// PostFileComment opens a review thread on the whole of path at commitID,
// for a finding on a line GitHub cannot take an inline comment on, and
// returns the thread's id.
func (g *ghClient) PostFileComment(
	ctx context.Context,
	k PRKey,
	commitID, path, body string,
) (string, error) {
	var resp struct {
		ReviewID int `json:"pull_request_review_id"`
	}
	err := g.postJSON(
		ctx,
		fmt.Sprintf("repos/%s/%s/pulls/%d/comments", k.Owner, k.Repo, k.Number),
		map[string]string{
			"commit_id":    commitID,
			"path":         path,
			"subject_type": "file",
			"body":         WithMarker(body),
		},
		&resp,
	)
	if err != nil {
		return "", fmt.Errorf("posting file comment on %s %s: %w", k, path, err)
	}
	refs, err := g.threadsOfReview(ctx, k, resp.ReviewID)
	if err != nil {
		return "", err
	}
	for _, r := range refs {
		if r.Path == path {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("no thread found for the file comment on %s %s", k, path)
}

func (f failingGitHub) PostFileComment(
	context.Context,
	PRKey,
	string,
	string,
	string,
) (string, error) {
	return "", f.err
}
