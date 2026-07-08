package api

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mansoor/rigger/ui/internal/crypto"
	"github.com/mansoor/rigger/ui/internal/proxyroutes"
)

// Proxy Service backup & restore.
//
// A backup is a gzip-tar ".rpx" bundle (Rigger Proxy eXport — distinct from the
// project backup's .rpb): a plaintext manifest plus the proxy
// routes + access lists (from SQLite) and, optionally, the Let's Encrypt cert
// store so the certs are PORTABLE — restoring them means the new server serves
// the existing certs immediately instead of re-requesting from LE (which would
// risk the per-registered-domain rate limit on a bulk restore). Renewals then
// happen naturally, staggered by each cert's own expiry.
//
// Secrets travel safely: the DB encrypts custom-cert private keys under the
// server's JWT-derived key, which differs on another server — so on export we
// DECRYPT with the server key and RE-WRAP under a key derived from the operator's
// passphrase; on import we reverse that with the new server's key. A passphrase is
// mandatory whenever the bundle would carry any private key or cert material.

const proxyBackupKind = "rigger-proxy-backup"
const proxyBackupVersion = 1

// proxyBackupManifest is the plaintext index at the root of the bundle.
type proxyBackupManifest struct {
	Kind        string   `json:"kind"`
	Version     int      `json:"version"`
	CreatedAt   string   `json:"created_at"`
	Encrypted   bool     `json:"encrypted"`
	CertScope   string   `json:"cert_scope"` // none | proxy | all
	Routes      int      `json:"routes"`
	AccessLists int      `json:"access_lists"`
	CertFiles   []string `json:"cert_files,omitempty"` // base names present under certs/
}

// proxyExportRoute is a route as stored, plus its custom-cert key re-wrapped under
// the passphrase (the server-key ciphertext in Route.TLSKeyEnc is json:"-" and
// never leaves the box).
type proxyExportRoute struct {
	proxyroutes.Route
	TLSKeyPortable string `json:"tls_key_portable,omitempty"`
}

type proxyBackupPayload struct {
	Routes      []proxyExportRoute       `json:"routes"`
	AccessLists []proxyroutes.AccessList `json:"access_lists"`
}

func certsDir() string {
	if d := strings.TrimSpace(os.Getenv("TRAEFIK_CERTS_DIR")); d != "" {
		return d
	}
	return "/certs"
}

// POST /api/proxy/backup — admin. Body: { cert_scope, passphrase }. Streams the
// ".rpb" bundle as a download.
func (h *Handler) ProxyBackup(w http.ResponseWriter, r *http.Request) {
	var body struct {
		CertScope  string `json:"cert_scope"`
		Passphrase string `json:"passphrase"`
	}
	_ = readJSON(r, &body) //nolint:errcheck
	scope := strings.ToLower(strings.TrimSpace(body.CertScope))
	if scope == "" {
		scope = "none"
	}
	if scope != "none" && scope != "proxy" && scope != "all" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cert_scope must be none, proxy, or all"})
		return
	}
	pass := body.Passphrase

	store := h.proxyStore()
	routes, err := store.List()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	lists, err := store.ListAccessLists()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	// Re-key any custom-cert private keys from the server key to the passphrase key.
	hasSecret := scope != "none"
	exportRoutes := make([]proxyExportRoute, 0, len(routes))
	for _, rt := range routes {
		er := proxyExportRoute{Route: rt}
		if rt.TLSKeyEnc != "" {
			hasSecret = true
			plain, derr := crypto.Decrypt(h.cryptoKey, rt.TLSKeyEnc)
			if derr != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "couldn't read a custom-cert key for route " + rt.Name + ": " + derr.Error()})
				return
			}
			if pass == "" {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this backup contains a custom-cert private key — set a passphrase to protect it"})
				return
			}
			pk, _ := crypto.DeriveKey([]byte(pass))
			er.TLSKeyPortable, _ = crypto.Encrypt(pk, plain)
		}
		er.Route.TLSKeyEnc = "" // never travels in server-key form
		exportRoutes = append(exportRoutes, er)
	}
	if hasSecret && pass == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a passphrase is required to include certificates or private keys"})
		return
	}

	payload := proxyBackupPayload{Routes: exportRoutes, AccessLists: lists}
	payloadJSON, _ := json.MarshalIndent(payload, "", "  ")

	// Gather cert material (portable LE store).
	certFiles, cerr := gatherCertFiles(scope, routes)
	if cerr != "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": cerr})
		return
	}

	encrypted := pass != ""
	var passKey []byte
	if encrypted {
		passKey, _ = crypto.DeriveKey([]byte(pass))
	}

	manifest := proxyBackupManifest{
		Kind: proxyBackupKind, Version: proxyBackupVersion,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		Encrypted: encrypted, CertScope: scope,
		Routes: len(exportRoutes), AccessLists: len(lists),
	}

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	writeEntry := func(name string, data []byte) error {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data))}); err != nil {
			return err
		}
		_, err := tw.Write(data)
		return err
	}

	// payload (encrypted → .enc holding base64 ciphertext)
	if encrypted {
		ct, _ := crypto.Encrypt(passKey, payloadJSON)
		if err := writeEntry("proxy.json.enc", []byte(ct)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	} else {
		if err := writeEntry("proxy.json", payloadJSON); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	for base, data := range certFiles {
		name := "certs/" + base
		if encrypted {
			ct, _ := crypto.Encrypt(passKey, data)
			name += ".enc"
			data = []byte(ct)
		}
		manifest.CertFiles = append(manifest.CertFiles, base)
		if err := writeEntry(name, data); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}

	manifestJSON, _ := json.MarshalIndent(manifest, "", "  ")
	if err := writeEntry("manifest.json", manifestJSON); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	if err := tw.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	if err := gw.Close(); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	fname := "proxy-backup-" + time.Now().UTC().Format("20060102-150405") + ".rpx"
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+fname+"\"")
	_, _ = w.Write(buf.Bytes())
}

// gatherCertFiles returns the LE cert files to bundle, keyed by base name. For the
// "proxy" scope the acme stores are filtered to certs whose main/SAN serves a
// configured route host. Returns ("errMsg") only on a hard read/parse failure.
func gatherCertFiles(scope string, routes []proxyroutes.Route) (map[string][]byte, string) {
	files := map[string][]byte{}
	if scope == "none" {
		return files, ""
	}
	hosts := map[string]bool{}
	for _, rt := range routes {
		if h := strings.ToLower(strings.TrimSpace(rt.Host)); h != "" {
			hosts[h] = true
		}
	}
	for _, base := range []string{"acme.json", "acme-dns.json"} {
		raw, err := os.ReadFile(filepath.Join(certsDir(), base))
		if err != nil || len(bytes.TrimSpace(raw)) == 0 {
			continue // missing/empty store — nothing to bundle for this resolver
		}
		if scope == "all" {
			files[base] = raw
			continue
		}
		filtered, ok := filterAcmeByHosts(raw, hosts)
		if !ok {
			// Unparseable store — safest to skip rather than ship something broken.
			continue
		}
		if len(bytes.TrimSpace(filtered)) > 0 {
			files[base] = filtered
		}
	}
	return files, ""
}

// acmeResolverRaw preserves the account + certificates of one resolver while
// letting us filter/dedupe on each cert's domain.
type acmeResolverRaw struct {
	Account      json.RawMessage          `json:"Account,omitempty"`
	Certificates []map[string]interface{} `json:"Certificates"`
}

// filterAcmeByHosts keeps only certs whose main/SAN serves one of hosts. ok=false
// when the store can't be parsed.
func filterAcmeByHosts(raw []byte, hosts map[string]bool) ([]byte, bool) {
	var store map[string]acmeResolverRaw
	if json.Unmarshal(raw, &store) != nil {
		return nil, false
	}
	any := false
	for name, res := range store {
		kept := make([]map[string]interface{}, 0, len(res.Certificates))
		for _, c := range res.Certificates {
			for _, n := range certDomainNames(c) {
				matched := false
				for host := range hosts {
					if certNameMatches(n, host) {
						matched = true
						break
					}
				}
				if matched {
					kept = append(kept, c)
					break
				}
			}
		}
		res.Certificates = kept
		if len(kept) > 0 {
			any = true
		}
		store[name] = res
	}
	if !any {
		return nil, true
	}
	out, _ := json.Marshal(store)
	return out, true
}

// certDomainNames extracts the main + SAN names from a raw acme cert entry.
func certDomainNames(c map[string]interface{}) []string {
	dom, _ := c["domain"].(map[string]interface{})
	if dom == nil {
		return nil
	}
	var names []string
	if m, _ := dom["main"].(string); m != "" {
		names = append(names, strings.ToLower(m))
	}
	if sans, _ := dom["sans"].([]interface{}); sans != nil {
		for _, s := range sans {
			if str, _ := s.(string); str != "" {
				names = append(names, strings.ToLower(str))
			}
		}
	}
	return names
}

// proxyRestoreSummary is returned to the UI after an import.
type proxyRestoreSummary struct {
	RoutesCreated      int      `json:"routes_created"`
	RoutesReplaced     int      `json:"routes_replaced"`
	RoutesSkipped      int      `json:"routes_skipped"`
	AccessCreated      int      `json:"access_lists_created"`
	AccessReplaced     int      `json:"access_lists_replaced"`
	AccessSkipped      int      `json:"access_lists_skipped"`
	CertsStaged        []string `json:"certs_staged,omitempty"`
	CertInstructions   string   `json:"cert_instructions,omitempty"`
	Warnings           []string `json:"warnings,omitempty"`
}

// POST /api/proxy/restore — admin. multipart: file, passphrase, replace_existing.
func (h *Handler) ProxyRestore(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "couldn't read upload: " + err.Error()})
		return
	}
	file, _, ferr := r.FormFile("file")
	if ferr != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "a backup file is required"})
		return
	}
	defer file.Close()
	pass := r.FormValue("passphrase")
	replace := r.FormValue("replace_existing") == "true" || r.FormValue("replace_existing") == "1"

	raw, err := io.ReadAll(io.LimitReader(file, 128<<20))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "couldn't read upload: " + err.Error()})
		return
	}

	entries, err := untarGz(raw)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "not a valid .rpx bundle: " + err.Error()})
		return
	}

	var manifest proxyBackupManifest
	if mb, ok := entries["manifest.json"]; ok {
		_ = json.Unmarshal(mb, &manifest) //nolint:errcheck
	}
	if manifest.Kind != proxyBackupKind {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this file isn't a Rigger proxy backup"})
		return
	}
	if manifest.Encrypted && pass == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "this backup is encrypted — enter the passphrase used when it was created"})
		return
	}
	var passKey []byte
	if pass != "" {
		passKey, _ = crypto.DeriveKey([]byte(pass))
	}

	// Load the payload (encrypted or plain).
	var payloadBytes []byte
	if enc, ok := entries["proxy.json.enc"]; ok {
		pb, derr := crypto.Decrypt(passKey, string(enc))
		if derr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "wrong passphrase, or the backup is corrupt"})
			return
		}
		payloadBytes = pb
	} else if pj, ok := entries["proxy.json"]; ok {
		payloadBytes = pj
	} else {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bundle is missing its proxy data"})
		return
	}
	var payload proxyBackupPayload
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "couldn't parse proxy data: " + err.Error()})
		return
	}

	store := h.proxyStore()
	summary := proxyRestoreSummary{}

	// Access lists first — build old-ID → new-ID map so routes can re-point.
	existingLists, _ := store.ListAccessLists()
	listByKey := map[string]proxyroutes.AccessList{}
	for _, a := range existingLists {
		listByKey[a.Workspace+"\x00"+a.Name] = a
	}
	idMap := map[int64]int64{}
	now := time.Now().Unix()
	for _, a := range payload.AccessLists {
		oldID := a.ID
		key := a.Workspace + "\x00" + a.Name
		if cur, ok := listByKey[key]; ok {
			if replace {
				a.ID = cur.ID
				a.UpdatedAt = now
				if err := store.UpdateAccessList(a); err != nil {
					summary.Warnings = append(summary.Warnings, "access list "+a.Name+": "+err.Error())
					continue
				}
				summary.AccessReplaced++
				idMap[oldID] = cur.ID
			} else {
				summary.AccessSkipped++
				idMap[oldID] = cur.ID // still re-point routes at the existing one
			}
			continue
		}
		a.ID = 0
		if a.CreatedAt == 0 {
			a.CreatedAt = now
		}
		a.UpdatedAt = now
		newID, err := store.CreateAccessList(a)
		if err != nil {
			summary.Warnings = append(summary.Warnings, "access list "+a.Name+": "+err.Error())
			continue
		}
		summary.AccessCreated++
		idMap[oldID] = newID
	}

	// Routes.
	existingRoutes, _ := store.List()
	routeByName := map[string]proxyroutes.Route{}
	var existingDefault *proxyroutes.Route
	for i, rt := range existingRoutes {
		routeByName[rt.Name] = rt
		if rt.IsDefault {
			existingDefault = &existingRoutes[i]
		}
	}
	for _, er := range payload.Routes {
		rt := er.Route
		// The catch-all default is a singleton — reconcile it against the existing
		// default (whatever its name) so a restore never creates a second one.
		if rt.IsDefault && existingDefault != nil {
			if !replace {
				summary.RoutesSkipped++
				continue
			}
			rt.ID = existingDefault.ID
			rt.UpdatedAt = now
			if err := store.Update(rt); err != nil {
				summary.Warnings = append(summary.Warnings, "default route: "+err.Error())
			} else {
				summary.RoutesReplaced++
			}
			continue
		}
		// Re-point the access-list reference to the freshly-imported list.
		if rt.AccessListID > 0 {
			if nid, ok := idMap[rt.AccessListID]; ok {
				rt.AccessListID = nid
			} else {
				rt.AccessListID = 0 // referenced list wasn't in the bundle
			}
		}
		// Re-wrap the custom-cert key from the passphrase to this server's key.
		rt.TLSKeyEnc = ""
		if er.TLSKeyPortable != "" {
			if passKey == nil {
				summary.Warnings = append(summary.Warnings, "route "+rt.Name+": has a custom-cert key but no passphrase was given — key skipped")
			} else if plain, derr := crypto.Decrypt(passKey, er.TLSKeyPortable); derr != nil {
				summary.Warnings = append(summary.Warnings, "route "+rt.Name+": couldn't decrypt its custom-cert key")
			} else {
				rt.TLSKeyEnc, _ = crypto.Encrypt(h.cryptoKey, plain)
			}
		}
		if cur, ok := routeByName[rt.Name]; ok {
			if !replace {
				summary.RoutesSkipped++
				continue
			}
			rt.ID = cur.ID
			rt.UpdatedAt = now
			if err := store.Update(rt); err != nil {
				summary.Warnings = append(summary.Warnings, "route "+rt.Name+": "+err.Error())
				continue
			}
			summary.RoutesReplaced++
			continue
		}
		rt.ID = 0
		if rt.CreatedAt == 0 {
			rt.CreatedAt = now
		}
		rt.UpdatedAt = now
		if _, err := store.Create(rt); err != nil {
			summary.Warnings = append(summary.Warnings, "route "+rt.Name+": "+err.Error())
			continue
		}
		summary.RoutesCreated++
	}

	// Re-render the file-provider config for the imported routes.
	if err := h.renderProxy(); err != nil {
		summary.Warnings = append(summary.Warnings, "re-render: "+err.Error())
	}

	// Stage any bundled LE cert store, merged with what's already here, and hand
	// back copy-paste install instructions (we don't fight Traefik's live file).
	if staged, instr := h.stageRestoredCerts(entries, manifest, passKey); len(staged) > 0 {
		summary.CertsStaged = staged
		summary.CertInstructions = instr
	}

	writeJSON(w, http.StatusOK, summary)
}

// stageRestoredCerts decrypts the bundle's LE cert files, merges each with the
// current on-disk store (union by cert domain, keeping any existing account), and
// writes the result to <dataDir>/proxy-cert-restore/. Returns the staged base
// names and a copy-paste snippet to install them into Traefik.
func (h *Handler) stageRestoredCerts(entries map[string][]byte, manifest proxyBackupManifest, passKey []byte) ([]string, string) {
	stageDir := filepath.Join(h.dataDir, "proxy-cert-restore")
	var staged []string
	for _, base := range []string{"acme.json", "acme-dns.json"} {
		data, ok := entries["certs/"+base]
		enc := false
		if !ok {
			data, ok = entries["certs/"+base+".enc"]
			enc = true
		}
		if !ok {
			continue
		}
		if enc {
			if passKey == nil {
				continue
			}
			pb, err := crypto.Decrypt(passKey, string(data))
			if err != nil {
				continue
			}
			data = pb
		}
		merged := mergeAcme(readFileOrEmpty(filepath.Join(certsDir(), base)), data)
		if err := os.MkdirAll(stageDir, 0o700); err != nil {
			continue
		}
		if err := os.WriteFile(filepath.Join(stageDir, base), merged, 0o600); err != nil {
			continue
		}
		staged = append(staged, base)
	}
	if len(staged) == 0 {
		return nil, ""
	}
	// Volume names are the compose project prefix ("rigger") + the volume name.
	var b strings.Builder
	b.WriteString("Restored certificate store staged in the rigger-data volume at /data/proxy-cert-restore/.\n")
	b.WriteString("Install it into Traefik and reload (run on the Rigger host):\n\n")
	b.WriteString("docker run --rm \\\n  -v rigger_traefik-certs:/certs \\\n  -v rigger_rigger-data:/data alpine sh -c '")
	parts := make([]string, 0, len(staged))
	for _, base := range staged {
		parts = append(parts, fmt.Sprintf("cp /data/proxy-cert-restore/%s /certs/%s", base, base))
	}
	b.WriteString(strings.Join(parts, " && "))
	b.WriteString(" && chmod 600 /certs/acme*.json'\n")
	b.WriteString("docker restart rigger-traefik\n")
	return staged, b.String()
}

func readFileOrEmpty(path string) []byte {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	return b
}

// mergeAcme unions the certificates of incoming into current (by cert domain
// main), preferring the current account key when present. Either side may be
// empty/unparseable; on any parse failure it falls back to the incoming bytes
// (the common fresh-install case where current is empty).
func mergeAcme(current, incoming []byte) []byte {
	var cur, in map[string]acmeResolverRaw
	if json.Unmarshal(incoming, &in) != nil {
		return incoming
	}
	if len(bytes.TrimSpace(current)) == 0 || json.Unmarshal(current, &cur) != nil {
		return incoming
	}
	for name, inRes := range in {
		curRes, ok := cur[name]
		if !ok {
			cur[name] = inRes
			continue
		}
		if len(bytes.TrimSpace(curRes.Account)) == 0 {
			curRes.Account = inRes.Account
		}
		have := map[string]bool{}
		for _, c := range curRes.Certificates {
			for _, n := range certDomainNames(c) {
				have[n] = true
			}
		}
		for _, c := range inRes.Certificates {
			names := certDomainNames(c)
			dup := false
			for _, n := range names {
				if have[n] {
					dup = true
					break
				}
			}
			if !dup {
				curRes.Certificates = append(curRes.Certificates, c)
			}
		}
		cur[name] = curRes
	}
	out, err := json.Marshal(cur)
	if err != nil {
		return incoming
	}
	return out
}

// untarGz reads a gzip-tar blob into a map of path → contents.
func untarGz(blob []byte) (map[string][]byte, error) {
	gr, err := gzip.NewReader(bytes.NewReader(blob))
	if err != nil {
		return nil, err
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	out := map[string][]byte{}
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		if hdr.FileInfo().IsDir() {
			continue
		}
		data, err := io.ReadAll(io.LimitReader(tr, 128<<20))
		if err != nil {
			return nil, err
		}
		out[filepath.ToSlash(hdr.Name)] = data
	}
	return out, nil
}
