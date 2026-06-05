package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// AppriseClient talks to the Apprise API sidecar's stateless /notify endpoint.
// Rigger stores the Apprise URL(s) per channel and posts them with each
// notification, so the sidecar needs no persistent configuration.
type AppriseClient struct {
	baseURL string
	http    *http.Client
}

func NewAppriseClient(baseURL string) *AppriseClient {
	return &AppriseClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 20 * time.Second},
	}
}

// appriseType maps our notification level to Apprise's notification type.
func appriseType(level string) string {
	switch level {
	case LevelSuccess:
		return "success"
	case LevelWarning:
		return "warning"
	case LevelFailure:
		return "failure"
	default:
		return "info"
	}
}

// Notify sends a notification to the given Apprise URLs via the sidecar.
func (a *AppriseClient) Notify(urls []string, title, body, level string) error {
	if a == nil || a.baseURL == "" {
		return fmt.Errorf("apprise service is not configured (set APPRISE_URL)")
	}
	if len(urls) == 0 {
		return fmt.Errorf("no apprise URLs provided")
	}

	payload, _ := json.Marshal(map[string]string{
		"urls":  strings.Join(urls, ","),
		"title": title,
		"body":  body,
		"type":  appriseType(level),
	})

	req, err := http.NewRequest(http.MethodPost, a.baseURL+"/notify", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("reach apprise service: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("apprise returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// parseAppriseURLs splits a stored URL blob (one per line or comma-separated)
// into individual Apprise URLs.
func parseAppriseURLs(blob string) []string {
	f := strings.FieldsFunc(blob, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r'
	})
	out := make([]string, 0, len(f))
	for _, u := range f {
		if u = strings.TrimSpace(u); u != "" {
			out = append(out, u)
		}
	}
	return out
}
