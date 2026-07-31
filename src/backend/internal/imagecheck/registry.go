package imagecheck

// Talking to whichever registry an image actually comes from.
//
// This used to be Docker Hub and nothing else: the token came from
// auth.docker.io and the manifest from registry-1.docker.io, with the image
// reference used verbatim as a Hub repository path. So "ghcr.io/foo/bar" was
// requested as registry-1.docker.io/v2/ghcr.io/foo/bar/manifests/… , which 404s.
// The service then reported "could not reach registry" — indistinguishable from a
// network problem, when in fact the registry was never contacted.
//
// That silently excluded GHCR, quay.io, lscr.io (LinuxServer, common in
// self-hosted stacks), registry.k8s.io and any private registry.
//
// The registry v2 spec makes this generic. An unauthenticated request to /v2/
// answers 401 with a WWW-Authenticate header naming the token realm and service;
// fetch a token from there with a pull scope, and the rest of the API is
// identical everywhere. Hub is then just one registry that happens to be the
// default when a reference names no host.
//
// Anonymous only. A registry that needs credentials answers 401 to the token
// request too, and the image reports "could not reach registry" — accurate, if
// not yet actionable. Wiring in the stored registry credentials is a separate
// change.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// registryTimeout bounds each registry call. The check runs hourly in the
// background over every image in the fleet, so one unresponsive registry must not
// hold the sweep open.
const registryTimeout = 15 * time.Second

var registryClient = &http.Client{Timeout: registryTimeout}

// dockerHubAPI is where a reference with no registry host resolves to. The
// hostname in a reference ("docker.io") is not the API endpoint.
const dockerHubAPI = "registry-1.docker.io"

// splitRef separates an image reference into its registry host and repository
// path, following Docker's own rule: the first path component is a registry host
// only if it looks like one — it contains a dot or a port, or is exactly
// "localhost". Otherwise it is part of the repository on Docker Hub.
//
//	mysql                     → registry-1.docker.io, library/mysql
//	minio/minio               → registry-1.docker.io, minio/minio
//	ghcr.io/mansoor/rigger    → ghcr.io,              mansoor/rigger
//	localhost:5001/mcl_sdm    → localhost:5001,       mcl_sdm
func splitRef(image string) (host, repo string) {
	first, rest, hasSlash := strings.Cut(image, "/")
	if hasSlash && (strings.ContainsAny(first, ".:") || first == "localhost") {
		host, repo = first, rest
		// docker.io is the reference hostname; the registry API lives elsewhere.
		if host == "docker.io" || host == "index.docker.io" {
			host = dockerHubAPI
		}
	} else {
		host, repo = dockerHubAPI, image
	}
	// Hub's official images live under library/, which references omit.
	if host == dockerHubAPI && !strings.Contains(repo, "/") {
		repo = "library/" + repo
	}
	return host, repo
}

// scheme picks http for a local registry and https for everything else. Rigger's
// own managed registry is commonly plain HTTP on localhost, and requiring TLS
// there would mean it could never be checked; anything reachable over a network
// must use TLS.
func scheme(host string) string {
	if host == "localhost" || strings.HasPrefix(host, "localhost:") ||
		strings.HasPrefix(host, "127.0.0.1") {
		return "http"
	}
	return "https"
}

// registryToken obtains a pull token for repo on host.
//
// Discovery rather than a hardcoded endpoint: GET /v2/ answers 401 with a
// WWW-Authenticate header naming the realm and service to ask. A registry that
// allows anonymous reads without a token answers 200, and "" is then the correct
// token — the caller sends no Authorization header at all.
func registryToken(host, repo string) (token string, ok bool) {
	resp, err := registryClient.Get(scheme(host) + "://" + host + "/v2/") //nolint:noctx
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		return "", true // open registry — no token needed
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return "", false
	}

	realm, service := parseAuthChallenge(resp.Header.Get("WWW-Authenticate"))
	if realm == "" {
		return "", false
	}

	url := realm + "?scope=" + "repository:" + repo + ":pull"
	if service != "" {
		url += "&service=" + service
	}
	tr, err := registryClient.Get(url) //nolint:noctx
	if err != nil {
		return "", false
	}
	defer tr.Body.Close()
	if tr.StatusCode != http.StatusOK {
		// Typically a private repository: anonymous pull isn't permitted.
		return "", false
	}
	// Registries differ on the field name — Hub returns "token", GHCR also
	// populates "access_token" — so accept either.
	var v struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	json.NewDecoder(tr.Body).Decode(&v) //nolint:errcheck
	if v.Token != "" {
		return v.Token, true
	}
	return v.AccessToken, v.AccessToken != ""
}

// parseAuthChallenge pulls realm and service out of a Bearer challenge, e.g.
//
//	Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="..."
//
// Only the parameters we need are read; anything else is ignored so a registry
// adding its own doesn't break parsing.
func parseAuthChallenge(header string) (realm, service string) {
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", ""
	}
	for _, part := range splitChallenge(header[len(prefix):]) {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch strings.ToLower(k) {
		case "realm":
			realm = v
		case "service":
			service = v
		}
	}
	return realm, service
}

// splitChallenge splits on commas that are not inside quotes — a scope value
// legitimately contains commas ("repository:a:pull,push").
func splitChallenge(s string) []string {
	var out []string
	var cur strings.Builder
	inQuotes := false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '"':
			inQuotes = !inQuotes
			cur.WriteByte(c)
		case c == ',' && !inQuotes:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// registryGet performs an authenticated GET against a registry API path.
func registryGet(host, repo, path string, accept []string) (*http.Response, bool) {
	token, ok := registryToken(host, repo)
	if !ok {
		return nil, false
	}
	return registryGetWithToken(host, fmt.Sprintf("/v2/%s/%s", repo, path), token, accept)
}

// registryGetWithToken fetches one absolute path on a registry with a token the
// caller already holds — so paging a tag list doesn't re-authenticate per page.
func registryGetWithToken(host, path, token string, accept []string) (*http.Response, bool) {
	req, err := http.NewRequest("GET", scheme(host)+"://"+host+path, nil) //nolint:noctx
	if err != nil {
		return nil, false
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if len(accept) > 0 {
		req.Header.Set("Accept", strings.Join(accept, ", "))
	}
	resp, err := registryClient.Do(req)
	if err != nil {
		return nil, false
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, false
	}
	return resp, true
}

// manifestAccept asks for manifest-list / OCI-index types first so a multi-arch
// image returns the SAME top-level digest that docker records locally in
// RepoDigests; otherwise a strict registry could hand back a single-arch digest
// that never matches, and every check would read as an available update.
var manifestAccept = []string{
	"application/vnd.docker.distribution.manifest.list.v2+json",
	"application/vnd.oci.image.index.v1+json",
	"application/vnd.docker.distribution.manifest.v2+json",
	"application/vnd.oci.image.manifest.v1+json",
}

// remoteDigest fetches the manifest digest for image:tag from whichever registry
// the reference names.
func remoteDigest(image, tag string) string {
	host, repo := splitRef(image)
	resp, ok := registryGet(host, repo, "manifests/"+tag, manifestAccept)
	if !ok {
		return ""
	}
	defer resp.Body.Close()
	return resp.Header.Get("Docker-Content-Digest")
}

// maxTagPages bounds how far a paginated tag list is followed. A repository with
// more history than this is one where the newest tags are already in hand;
// without a bound, a pathological repo could page indefinitely on a background
// sweep that touches every image in the fleet.
const maxTagPages = 20

// remoteTags lists a repository's tags, following pagination.
//
// Pagination is not optional here. GHCR caps a page at 100 and returns the
// OLDEST tags first, so a single unpaged request to home-assistant answers with
// 2021 releases and nothing since — and "no newer stable version" then reads as
// a confident no rather than a truncated list. Docker Hub returns far more per
// page, which is why this was never noticed while Hub was the only registry.
//
// Returns nil when the registry can't be reached or doesn't allow anonymous
// listing.
func remoteTags(image string) []string {
	host, repo := splitRef(image)
	token, ok := registryToken(host, repo)
	if !ok {
		return nil
	}

	// n=1000 asks for everything at once. Registries are free to cap it lower —
	// GHCR does — which is what the paging below is for.
	path := fmt.Sprintf("/v2/%s/tags/list?n=1000", repo)
	var all []string
	for page := 0; page < maxTagPages && path != ""; page++ {
		resp, ok := registryGetWithToken(host, path, token, nil)
		if !ok {
			break // partial list beats none: the tags already read are still valid
		}
		body, _ := io.ReadAll(resp.Body)
		next := nextPageLink(resp.Header.Get("Link"))
		resp.Body.Close()

		var result struct{ Tags []string }
		json.Unmarshal(body, &result) //nolint:errcheck
		all = append(all, result.Tags...)
		path = next
	}
	return all
}

// nextPageLink reads the next-page path out of a Link header:
//
//	</v2/user/image/tags/list?n=100&last=2021.7.1>; rel="next"
//
// Returns "" when there is no next page. The URL is a path on the same host, so
// it is used as-is.
func nextPageLink(header string) string {
	if header == "" {
		return ""
	}
	for _, part := range strings.Split(header, ",") {
		seg := strings.Split(part, ";")
		if len(seg) < 2 {
			continue
		}
		isNext := false
		for _, attr := range seg[1:] {
			if strings.Contains(strings.ToLower(attr), `rel="next"`) ||
				strings.Contains(strings.ToLower(attr), "rel=next") {
				isNext = true
			}
		}
		if !isNext {
			continue
		}
		url := strings.TrimSpace(seg[0])
		url = strings.TrimPrefix(url, "<")
		url = strings.TrimSuffix(url, ">")
		// Only same-host relative paths are followed; an absolute URL would mean
		// redirecting the token to another host.
		if strings.HasPrefix(url, "/") {
			return url
		}
	}
	return ""
}
