package data

import (
	"fmt"
	"io"
	"net/http"

	gh "github.com/cli/go-gh/v2/pkg/api"
)

// maxDiffBytes caps how much of a PR diff is read into memory since GitHub
// can return very large payloads for big PRs.
const maxDiffBytes = 1 << 20

// FetchPRDiff fetches the raw unified diff for a PR using the REST API's
// diff media type.
func FetchPRDiff(repoNameWithOwner string, prNumber int) (string, error) {
	client, err := gh.NewRESTClient(gh.ClientOptions{
		Headers: map[string]string{"Accept": "application/vnd.github.diff"},
	})
	if err != nil {
		return "", err
	}

	resp, err := client.Request(
		http.MethodGet,
		fmt.Sprintf("repos/%s/pulls/%d", repoNameWithOwner, prNumber),
		nil,
	)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	diff, err := io.ReadAll(io.LimitReader(resp.Body, maxDiffBytes))
	if err != nil {
		return "", err
	}

	return string(diff), nil
}
