// Package clouddns manages public DNS *records* (A/CNAME) via a provider API — as
// opposed to internal/acme, which only performs dns-01 TXT *challenges* for cert
// issuance. It backs "direct routing" (model #2): when a base-domain app runs on a
// public remote host, Rigger upserts an A record label.base -> that host's public IP
// so the name resolves straight to the host (a specific record that overrides the
// wildcard). Cloudflare is the only provider today; the Provider interface leaves
// room for more.
package clouddns

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Provider upserts/removes the routing records that point an app hostname at a host.
type Provider interface {
	// UpsertA makes fqdn an A record for ip (creating or updating), DNS-only (not
	// proxied), so the host is reached directly and can serve its own LE http-01.
	UpsertA(ctx context.Context, fqdn, ip string) error
	// DeleteA removes fqdn's A record if present (no error when absent).
	DeleteA(ctx context.Context, fqdn string) error
}

const defaultAPI = "https://api.cloudflare.com/client/v4"

// Cloudflare is a minimal Cloudflare DNS API client (bearer token). The token needs
// Zone:Read + DNS:Edit on the zone(s) hosting the base domain.
type Cloudflare struct {
	token  string
	api    string
	client *http.Client
}

// NewCloudflare builds a client for the given API token.
func NewCloudflare(token string) *Cloudflare {
	return &Cloudflare{
		token:  strings.TrimSpace(token),
		api:    defaultAPI,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

var _ Provider = (*Cloudflare)(nil)

// apiResp is the common Cloudflare envelope.
type apiResp struct {
	Success bool              `json:"success"`
	Errors  []cloudflareError `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

type cloudflareError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (r apiResp) err() error {
	if r.Success {
		return nil
	}
	if len(r.Errors) > 0 {
		return fmt.Errorf("cloudflare: %s (code %d)", r.Errors[0].Message, r.Errors[0].Code)
	}
	return fmt.Errorf("cloudflare: request failed")
}

// do issues a request and decodes the envelope, returning the raw Result.
func (c *Cloudflare) do(ctx context.Context, method, path string, body any) (json.RawMessage, error) {
	var rdr *bytes.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.api+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var out apiResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("cloudflare: decode %s %s: %w", method, path, err)
	}
	if err := out.err(); err != nil {
		return nil, err
	}
	return out.Result, nil
}

type zone struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// zoneFor resolves the zone hosting fqdn by the longest zone name that is a suffix of
// fqdn on a label boundary (e.g. base "app.apps.example.com" → zone "example.com").
func (c *Cloudflare) zoneFor(ctx context.Context, fqdn string) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, "/zones?per_page=50&status=active", nil)
	if err != nil {
		return "", err
	}
	var zones []zone
	if err := json.Unmarshal(raw, &zones); err != nil {
		return "", err
	}
	best := zone{}
	for _, z := range zones {
		if fqdn == z.Name || strings.HasSuffix(fqdn, "."+z.Name) {
			if len(z.Name) > len(best.Name) {
				best = z
			}
		}
	}
	if best.ID == "" {
		return "", fmt.Errorf("cloudflare: no accessible zone found for %q — check the API token's zone access", fqdn)
	}
	return best.ID, nil
}

type record struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
}

// findA returns the id of the A record for fqdn in the zone, or "" if none.
func (c *Cloudflare) findA(ctx context.Context, zoneID, fqdn string) (string, error) {
	raw, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/zones/%s/dns_records?type=A&name=%s", zoneID, fqdn), nil)
	if err != nil {
		return "", err
	}
	var recs []record
	if err := json.Unmarshal(raw, &recs); err != nil {
		return "", err
	}
	if len(recs) > 0 {
		return recs[0].ID, nil
	}
	return "", nil
}

// UpsertA creates or updates the DNS-only A record fqdn → ip.
func (c *Cloudflare) UpsertA(ctx context.Context, fqdn, ip string) error {
	if c.token == "" {
		return fmt.Errorf("cloudflare: no API token configured")
	}
	zoneID, err := c.zoneFor(ctx, fqdn)
	if err != nil {
		return err
	}
	id, err := c.findA(ctx, zoneID, fqdn)
	if err != nil {
		return err
	}
	// ttl=1 = "automatic"; proxied=false = grey-cloud (direct), so the host serves its
	// own traffic + LE http-01 rather than routing through Cloudflare's edge.
	body := record{Type: "A", Name: fqdn, Content: ip, TTL: 1, Proxied: false}
	if id == "" {
		_, err = c.do(ctx, http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", zoneID), body)
	} else {
		_, err = c.do(ctx, http.MethodPut, fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, id), body)
	}
	return err
}

// DeleteA removes fqdn's A record if it exists.
func (c *Cloudflare) DeleteA(ctx context.Context, fqdn string) error {
	if c.token == "" {
		return fmt.Errorf("cloudflare: no API token configured")
	}
	zoneID, err := c.zoneFor(ctx, fqdn)
	if err != nil {
		return err
	}
	id, err := c.findA(ctx, zoneID, fqdn)
	if err != nil || id == "" {
		return err
	}
	_, err = c.do(ctx, http.MethodDelete, fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, id), nil)
	return err
}
