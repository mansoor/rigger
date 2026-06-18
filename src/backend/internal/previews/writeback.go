package previews

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Write-back posts the preview URL/status back to the PR (design §4.1, Phase 4).
// v1 targets GitHub's REST API; it is best-effort and never blocks the lifecycle.

const previewCommentMarker = "<!-- rigger-preview -->" // hidden tag to find+update our comment

// ParseGitHubRepo extracts "owner/repo" from a GitHub remote URL (https or ssh),
// returning ok=false for non-GitHub or unparseable URLs (write-back is skipped).
func ParseGitHubRepo(gitURL string) (string, bool) {
	u := strings.TrimSpace(gitURL)
	if u == "" {
		return "", false
	}
	u = strings.TrimSuffix(u, ".git")
	switch {
	case strings.HasPrefix(u, "git@github.com:"):
		u = strings.TrimPrefix(u, "git@github.com:")
	case strings.Contains(u, "github.com/"):
		u = u[strings.Index(u, "github.com/")+len("github.com/"):]
	default:
		return "", false // not github.com (self-hosted Gitea/GitLab not supported in v1)
	}
	parts := strings.Split(strings.Trim(u, "/"), "/")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

// GitHubCommitState maps a preview status to a GitHub commit-status state.
func GitHubCommitState(status string) string {
	switch status {
	case StatusRunning:
		return "success"
	case StatusFailed:
		return "failure"
	case StatusTornDown:
		return "success" // closed/merged — nothing pending
	default: // creating | updating
		return "pending"
	}
}

// WriteBackClient posts to a GitHub repo on behalf of the project (token-authed).
type WriteBackClient struct {
	token   string
	repo    string // owner/repo
	apiBase string
	http    *http.Client
}

// NewGitHubWriteBack builds a client for api.github.com.
func NewGitHubWriteBack(token, repo string) *WriteBackClient {
	return &WriteBackClient{token: token, repo: repo, apiBase: "https://api.github.com", http: &http.Client{Timeout: 15 * time.Second}}
}

func (c *WriteBackClient) req(method, path string, body any) (*http.Response, error) {
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, c.apiBase+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.http.Do(req)
}

// PostCommitStatus sets a commit status on the PR head sha with a Details link to
// the preview URL.
func (c *WriteBackClient) PostCommitStatus(sha, state, targetURL, description string) error {
	if sha == "" {
		return nil
	}
	resp, err := c.req("POST", fmt.Sprintf("/repos/%s/statuses/%s", c.repo, sha), map[string]string{
		"state": state, "target_url": targetURL, "description": description, "context": "rigger/preview",
	})
	if err != nil {
		return err
	}
	return drainOK(resp)
}

// UpsertPRComment posts (or updates) a single Rigger-owned comment on the PR,
// identified by a hidden marker so repeated deploys don't spam new comments.
func (c *WriteBackClient) UpsertPRComment(prNumber int, body string) error {
	marked := body + "\n\n" + previewCommentMarker
	// Find an existing marked comment.
	resp, err := c.req("GET", fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100", c.repo, prNumber), nil)
	if err != nil {
		return err
	}
	var comments []struct {
		ID   int64  `json:"id"`
		Body string `json:"body"`
	}
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	json.Unmarshal(data, &comments) //nolint:errcheck
	for _, cm := range comments {
		if strings.Contains(cm.Body, previewCommentMarker) {
			up, err := c.req("PATCH", fmt.Sprintf("/repos/%s/issues/comments/%d", c.repo, cm.ID), map[string]string{"body": marked})
			if err != nil {
				return err
			}
			return drainOK(up)
		}
	}
	cr, err := c.req("POST", fmt.Sprintf("/repos/%s/issues/%d/comments", c.repo, prNumber), map[string]string{"body": marked})
	if err != nil {
		return err
	}
	return drainOK(cr)
}

func drainOK(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github API %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}
