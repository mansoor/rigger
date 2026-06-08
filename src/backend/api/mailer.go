package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/notify"
)

// System (transactional) email — invite + verification links. Separate from the
// alert "notification channels": this is how Rigger emails users directly.
// Settings live in app_settings under sys_smtp_*; the password is encrypted.

func (h *Handler) appSetting(key string) string {
	var v string
	h.db.QueryRow(`SELECT value FROM app_settings WHERE key=?`, key).Scan(&v) //nolint:errcheck
	return v
}

func (h *Handler) setAppSetting(key, val string) {
	h.db.Exec(`INSERT INTO app_settings (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(key) DO UPDATE SET value=excluded.value, updated_at=CURRENT_TIMESTAMP`, key, val) //nolint:errcheck
}

// systemSMTP returns the configured transactional-email SMTP config. ok is false
// when not configured (no host/from) — callers then surface the link instead.
func (h *Handler) systemSMTP() (notify.EmailConfig, bool) {
	host := h.appSetting("sys_smtp_host")
	from := h.appSetting("sys_smtp_from")
	if host == "" || from == "" {
		return notify.EmailConfig{}, false
	}
	port, _ := strconv.Atoi(h.appSetting("sys_smtp_port"))
	if port == 0 {
		port = 587
	}
	pass := ""
	if enc := h.appSetting("sys_smtp_pass_enc"); enc != "" {
		if dec, err := crypto.Decrypt(h.cryptoKey, enc); err == nil {
			pass = string(dec)
		}
	}
	return notify.EmailConfig{
		Host: host, Port: port,
		Username: h.appSetting("sys_smtp_user"), Password: pass,
		From: from, UseTLS: h.appSetting("sys_smtp_tls") != "false",
	}, true
}

// baseURL returns the externally-reachable base for building links: the configured
// app_base_url, else derived from the request.
func (h *Handler) baseURL(r *http.Request) string {
	if u := strings.TrimRight(h.appSetting("app_base_url"), "/"); u != "" {
		return u
	}
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

// sendUserLink emails a single recipient an action link. Returns sent=false (no
// error) when SMTP isn't configured, so the caller can surface the link instead.
func (h *Handler) sendUserLink(to, subject, heading, action, link string) (sent bool, err error) {
	cfg, ok := h.systemSMTP()
	if !ok {
		return false, nil
	}
	cfg.To = to
	body := fmt.Sprintf("%s\n\n%s:\n%s\n\nIf you weren't expecting this, you can ignore this email.", heading, action, link)
	if err := notify.SendTransactional(cfg, subject, body); err != nil {
		return false, err
	}
	return true, nil
}

// GET /api/settings/system-email — admin. Never returns the password.
func (h *Handler) GetSystemEmail(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"host":         h.appSetting("sys_smtp_host"),
		"port":         h.appSetting("sys_smtp_port"),
		"username":     h.appSetting("sys_smtp_user"),
		"from":         h.appSetting("sys_smtp_from"),
		"tls":          h.appSetting("sys_smtp_tls") != "false",
		"base_url":     h.appSetting("app_base_url"),
		"has_password": h.appSetting("sys_smtp_pass_enc") != "",
	})
}

// PUT /api/settings/system-email — admin. Empty password keeps the existing one.
func (h *Handler) PutSystemEmail(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Host     string `json:"host"`
		Port     string `json:"port"`
		Username string `json:"username"`
		Password string `json:"password"`
		From     string `json:"from"`
		TLS      *bool  `json:"tls"`
		BaseURL  string `json:"base_url"`
	}
	if err := readJSON(r, &b); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	h.setAppSetting("sys_smtp_host", strings.TrimSpace(b.Host))
	h.setAppSetting("sys_smtp_port", strings.TrimSpace(b.Port))
	h.setAppSetting("sys_smtp_user", strings.TrimSpace(b.Username))
	h.setAppSetting("sys_smtp_from", strings.TrimSpace(b.From))
	h.setAppSetting("app_base_url", strings.TrimSpace(b.BaseURL))
	if b.TLS != nil {
		h.setAppSetting("sys_smtp_tls", strconv.FormatBool(*b.TLS))
	}
	if b.Password != "" {
		if enc, err := crypto.Encrypt(h.cryptoKey, []byte(b.Password)); err == nil {
			h.setAppSetting("sys_smtp_pass_enc", enc)
		}
	}
	h.GetSystemEmail(w, r)
}
