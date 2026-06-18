package api

import (
	"net/http"
	"testing"
)

func ghHeaders(event string) http.Header {
	h := http.Header{}
	if event != "" {
		h.Set("X-GitHub-Event", event)
	}
	return h
}

func TestParseGitHubPR(t *testing.T) {
	// Same-repo PR, opened.
	body := []byte(`{
		"action":"opened","number":42,
		"pull_request":{
			"head":{"ref":"feature/login","sha":"abc123","repo":{"full_name":"acme/web"}},
			"base":{"repo":{"full_name":"acme/web"}}
		}
	}`)
	ev, ok := parseGitHubPR(ghHeaders("pull_request"), body)
	if !ok {
		t.Fatal("expected a parsed event")
	}
	if ev.Action != "opened" || ev.PRNumber != 42 || ev.Branch != "feature/login" || ev.SHA != "abc123" {
		t.Errorf("unexpected event: %+v", ev)
	}
	if ev.IsFork {
		t.Error("same-repo PR should not be a fork")
	}
}

func TestParseGitHubPRFork(t *testing.T) {
	body := []byte(`{
		"action":"synchronize","number":7,
		"pull_request":{
			"head":{"ref":"patch","sha":"def","repo":{"full_name":"contributor/web"}},
			"base":{"repo":{"full_name":"acme/web"}}
		}
	}`)
	ev, ok := parseGitHubPR(ghHeaders("pull_request"), body)
	if !ok || !ev.IsFork {
		t.Errorf("fork PR not detected: ok=%v ev=%+v", ok, ev)
	}
	if ev.Action != "synchronize" {
		t.Errorf("action = %q, want synchronize", ev.Action)
	}
}

func TestParseGitHubPRIgnored(t *testing.T) {
	prBody := []byte(`{"action":"opened","number":1,"pull_request":{"head":{"ref":"x"}}}`)
	// Non-PR event header → ignored.
	if _, ok := parseGitHubPR(ghHeaders("push"), prBody); ok {
		t.Error("push event should be ignored")
	}
	// Missing event header → ignored.
	if _, ok := parseGitHubPR(ghHeaders(""), prBody); ok {
		t.Error("missing event header should be ignored")
	}
	// An action we don't act on (e.g. "labeled") → ignored.
	labeled := []byte(`{"action":"labeled","number":1,"pull_request":{"head":{"ref":"x"}}}`)
	if _, ok := parseGitHubPR(ghHeaders("pull_request"), labeled); ok {
		t.Error("labeled action should be ignored")
	}
	// Malformed JSON → ignored.
	if _, ok := parseGitHubPR(ghHeaders("pull_request"), []byte("not json")); ok {
		t.Error("malformed body should be ignored")
	}
}

func TestProviderParseGitLabUnsupported(t *testing.T) {
	if _, ok := providerParse("gitlab", http.Header{}, []byte(`{}`)); ok {
		t.Error("gitlab is not supported in v1 — should ignore")
	}
}
