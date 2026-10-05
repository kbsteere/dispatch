package prssection

import (
	"github.com/kbsteere/dispatch/v4/internal/data"
	"github.com/kbsteere/dispatch/v4/internal/tui/components/prrow"
)

// fetchPullRequestByURL is a variable so tests can replace the network call.
var fetchPullRequestByURL = data.FetchPullRequest

// fetchUrlSection loads the rows of a section backed by a list of PR URLs
// rather than a search.
func fetchUrlSection(urls []string) ([]prrow.Data, error) {
	prs := make([]prrow.Data, 0, len(urls))
	for _, u := range urls {
		e, err := fetchPullRequestByURL(u)
		if err != nil {
			return nil, err
		}
		pd := e.ToPullRequestData()
		prs = append(prs, prrow.Data{Primary: &pd, Enriched: e, IsEnriched: true})
	}
	return prs, nil
}
