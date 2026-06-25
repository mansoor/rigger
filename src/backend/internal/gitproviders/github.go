package gitproviders

// GitHub App support: the one-click "App Manifest" flow (create + install the app,
// store its credentials) and short-lived installation-token minting used to clone
// private repos. The app's private key is stored encrypted (secret_enc); the
// non-secret ids live in meta. Installation access tokens are minted per clone and
// never persisted. Works for github.com and GitHub Enterprise Server (host set).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// GitHubAppMeta is the non-secret GitHub App metadata stored in Provider.Meta (json).
// The app private key (the secret) is stored separately in secret_enc.
type GitHubAppMeta struct {
	AppID          string `json:"app_id"`
	Slug           string `json:"slug"`
	ClientID       string `json:"client_id"`
	InstallationID string `json:"installation_id"`
	HTMLURL        string `json:"html_url"` // the app's page (for the Install link base)
}

// ParseGitHubMeta decodes a Provider's Meta json (empty → zero value).
func ParseGitHubMeta(meta string) GitHubAppMeta {
	var m GitHubAppMeta
	if strings.TrimSpace(meta) != "" {
		_ = json.Unmarshal([]byte(meta), &m)
	}
	return m
}

func (m GitHubAppMeta) JSON() string { b, _ := json.Marshal(m); return string(b) }

// apiBase returns the GitHub REST API base for a host ("" / "github.com" → public).
func apiBase(host string) string {
	h := strings.TrimSpace(host)
	if h == "" || h == "github.com" {
		return "https://api.github.com"
	}
	return "https://" + h + "/api/v3"
}

// webBase returns the GitHub web base (for app-create / install URLs).
func webBase(host string) string {
	h := strings.TrimSpace(host)
	if h == "" || h == "github.com" {
		return "https://github.com"
	}
	return "https://" + h
}

// InstallURL is where the user installs the created app onto an account/org.
func (m GitHubAppMeta) InstallURL(host string) string {
	if m.Slug == "" {
		return ""
	}
	return webBase(host) + "/apps/" + m.Slug + "/installations/new"
}

// appJWT builds a short-lived RS256 JWT signed with the app private key, used to
// authenticate as the App to the GitHub API (per GitHub's app-auth docs).
func appJWT(appID, privPEM string) (string, error) {
	key, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(privPEM))
	if err != nil {
		return "", fmt.Errorf("parse app private key: %w", err)
	}
	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.RegisteredClaims{
		Issuer:    appID,
		IssuedAt:  jwt.NewNumericDate(now.Add(-30 * time.Second)), // clock-skew slack
		ExpiresAt: jwt.NewNumericDate(now.Add(9 * time.Minute)),   // GitHub max is 10m
	})
	return tok.SignedString(key)
}

// mintInstallationToken exchanges the app JWT for a 1-hour installation access
// token (POST /app/installations/{id}/access_tokens). The token is short-lived and
// returned for one-time use — never stored.
func mintInstallationToken(host, appID, privPEM, installationID string) (string, error) {
	if appID == "" || installationID == "" || strings.TrimSpace(privPEM) == "" {
		return "", fmt.Errorf("GitHub App is missing its app id, installation, or private key — finish installing it")
	}
	jwtStr, err := appJWT(appID, privPEM)
	if err != nil {
		return "", err
	}
	url := apiBase(host) + "/app/installations/" + installationID + "/access_tokens"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, nil)
	req.Header.Set("Authorization", "Bearer "+jwtStr)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request installation token: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", fmt.Errorf("GitHub installation-token request failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &out); err != nil || out.Token == "" {
		return "", fmt.Errorf("GitHub returned no installation token")
	}
	return out.Token, nil
}

// Manifest builds the GitHub App manifest Rigger POSTs to /settings/apps/new. The
// app requests read access to repo contents/metadata plus PR + status write (so the
// same app can later drive previews/auto-deploy — phase 2); for now only the clone
// token is used. redirectURL receives the temporary code; setupURL receives the
// installation_id after install.
func Manifest(name, host, redirectURL, setupURL, webhookURL string) string {
	m := map[string]any{
		"name":         name,
		"url":          webBase(host),
		"redirect_url": redirectURL,
		"setup_url":    setupURL,
		"public":       false,
		"default_permissions": map[string]string{
			"contents":      "read",
			"metadata":      "read",
			"pull_requests": "write",
			"statuses":      "write",
			"checks":        "write",
		},
		"default_events": []string{"push", "pull_request"},
	}
	// GitHub's manifest flow ALWAYS creates a webhook and requires a non-blank url
	// ("Hook url cannot be blank"). We always supply one (Rigger's own origin) but
	// mark it inactive — we don't consume webhooks yet (auto-deploy is phase 2), and
	// the host may not be publicly reachable, so no deliveries are attempted.
	if webhookURL != "" {
		m["hook_attributes"] = map[string]any{"url": webhookURL, "active": false}
	}
	b, _ := json.Marshal(m)
	return string(b)
}

// AppCreateURL is where the browser POSTs the manifest to create the app.
func AppCreateURL(host, state string) string {
	return webBase(host) + "/settings/apps/new?state=" + state
}

// conversionResult is GitHub's response to the manifest code conversion.
type conversionResult struct {
	ID            int64  `json:"id"`
	Slug          string `json:"slug"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret"`
	WebhookSecret string `json:"webhook_secret"`
	PEM           string `json:"pem"`
	HTMLURL       string `json:"html_url"`
}

// ConvertManifestCode exchanges the temporary manifest code for the new app's
// credentials (POST /app-manifests/{code}/conversions). Returns the provider fields
// to persist: the private key (→ secret) and the non-secret meta.
func ConvertManifestCode(host, code string) (privPEM string, meta GitHubAppMeta, err error) {
	url := apiBase(host) + "/app-manifests/" + code + "/conversions"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(nil))
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", meta, fmt.Errorf("convert manifest: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != http.StatusCreated {
		return "", meta, fmt.Errorf("GitHub manifest conversion failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var c conversionResult
	if err := json.Unmarshal(body, &c); err != nil {
		return "", meta, fmt.Errorf("parse manifest conversion: %w", err)
	}
	meta = GitHubAppMeta{
		AppID: fmt.Sprintf("%d", c.ID), Slug: c.Slug, ClientID: c.ClientID, HTMLURL: c.HTMLURL,
	}
	return c.PEM, meta, nil
}
