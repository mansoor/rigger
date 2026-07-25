import { useState, useRef } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import { Hint, CertBadge, Btn, IconBtn, CloseBtn, CONTROL } from '../components/ui'
import {
  fetchProxyRoutes, createProxyRoute, updateProxyRoute, deleteProxyRoute,
  testProxyRoute, fetchProxyCerts, fetchProxyPlugins, updateProxyPlugins, setProxyGeoIPDB,
  fetchProxyAccessLists, createProxyAccessList, updateProxyAccessList, deleteProxyAccessList,
  proxyBackup, proxyRestore,
} from '../lib/api'

// Proxy Service — standalone reverse-proxy manager (docs/design/proxy-service.md).
// A top-level admin page that routes public hosts/paths to arbitrary upstreams via
// Rigger's Traefik. Independent of the project/workspace model.

// ── primitives (local, matching the app's settings style) ──────────────────────
function Label({ children }) {
  return <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">{children}</label>
}
function Input({ value, onChange, placeholder, type = 'text', disabled }) {
  return (
    <input type={type} value={value ?? ''} onChange={e => onChange(e.target.value)} placeholder={placeholder} disabled={disabled}
      className={`${CONTROL} w-full disabled:opacity-50`} />
  )
}
function Select({ value, onChange, options, disabled }) {
  return (
    <select value={value} onChange={e => onChange(e.target.value)} disabled={disabled}
      className={`${CONTROL} w-full disabled:opacity-50`}>
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}
function Toggle({ checked, onChange, label }) {
  return (
    <label className="flex items-center gap-2 cursor-pointer select-none">
      <button type="button" onClick={() => onChange(!checked)}
        className={`relative w-9 h-5 rounded-full transition-colors shrink-0 ${checked ? 'bg-brand-600' : 'bg-surface-overlay'}`}>
        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-4' : ''}`} />
      </button>
      <span className="text-sm text-content">{label}</span>
    </label>
  )
}

const TLS_OPTIONS = [
  { value: 'none', label: 'None (HTTP only)' },
  { value: 'le-http', label: "Let's Encrypt (HTTP-01)" },
  { value: 'le-dns', label: "Let's Encrypt (DNS wildcard) — reuse" },
  { value: 'existing', label: 'Existing certificate — reuse' },
  { value: 'custom', label: 'Custom upload' },
]
const DEFAULT_MODES = [
  { value: 'page', label: 'Friendly “not reachable” page' },
  { value: '404', label: 'Return 404 Not Found' },
  { value: '403', label: 'Return 403 Forbidden' },
  { value: 'close', label: 'Close connection (hide existence)' },
  { value: 'redirect', label: 'Redirect to a URL' },
  { value: 'proxy', label: 'Proxy to a default site' },
]

// errMsg pulls the most useful detail out of an axios error: the server's JSON
// {error} first, then a string body, then HTTP status, then the network message.
function errMsg(e, fallback = 'Request failed') {
  const r = e?.response
  if (r?.data?.error) return r.data.error
  if (typeof r?.data === 'string' && r.data.trim()) return r.data.trim()
  if (r?.status) return `HTTP ${r.status}${r.statusText ? ' ' + r.statusText : ''}`
  if (e?.message) return e.message
  return fallback
}

function blankRoute() {
  return {
    name: '', enabled: true, type: 'proxy', host: '', path_prefix: '',
    upstreams: [{ scheme: 'http', host: '', port: '' }],
    pass_host_header: true, insecure_skip_verify: false,
    redirect_to: '', redirect_code: 301,
    tls_mode: 'none', tls_cert_ref: '', acme_email: '',
    force_https: true, hsts_seconds: 0,
    auth_mode: 'none', auth_users: [], ip_allow: '', access_list_id: 0, security_headers: false,
    geo_mode: 'off', countriesText: '',
    strip_prefix: false, waf: false, cache: false, accept_tos: false, locations: [], notes: '',
  }
}

function blankUpstream() { return { scheme: 'http', host: '', port: '' } }
function blankLocation() { return { path: '', forward_path: '', upstreams: [blankUpstream()] } }

// normLocation folds a stored location (which may be the legacy single host/port shape)
// into the editor's { path, forward_path, upstreams[] } shape.
function normLocation(l) {
  const upstreams = l.upstreams?.length
    ? l.upstreams
    : (l.host ? [{ scheme: l.scheme || 'http', host: l.host, port: l.port || '' }] : [blankUpstream()])
  return { path: l.path || '', forward_path: l.forward_path || '', upstreams }
}

function tlsBadge(m) {
  switch (m) {
    case 'le-http': case 'le-dns': return { label: "Let's Encrypt", cls: 'bg-success-subtle/50 text-success-fg' }
    case 'existing': return { label: 'Reused cert', cls: 'bg-success-subtle/50 text-success-fg' }
    case 'custom': return { label: 'Custom cert', cls: 'bg-success-subtle/50 text-success-fg' }
    default: return { label: 'HTTP', cls: 'bg-surface-overlay/50 text-content-muted' }
  }
}

function Badge({ children, cls }) {
  return <span className={`px-1.5 py-0.5 rounded text-[11px] ${cls || 'bg-surface-overlay/50 text-content-muted'}`}>{children}</span>
}

export default function ProxyServicePage() {
  const qc = useQueryClient()
  const { data: routes = [], isLoading } = useQuery({ queryKey: ['proxy-routes'], queryFn: fetchProxyRoutes })
  const { data: plugins } = useQuery({ queryKey: ['proxy-plugins'], queryFn: fetchProxyPlugins })
  const { data: accessLists = [] } = useQuery({ queryKey: ['proxy-access-lists'], queryFn: fetchProxyAccessLists })
  const [modal, setModal] = useState(null) // null | 'new' | {editing: route}
  const [deleting, setDeleting] = useState(null)

  const delMut = useMutation({
    mutationFn: (id) => deleteProxyRoute(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['proxy-routes'] }); setDeleting(null) },
  })
  const toggleMut = useMutation({
    mutationFn: (r) => updateProxyRoute(r.id, { ...r, enabled: !r.enabled, auth_users: [] }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['proxy-routes'] }),
  })

  const realRoutes = routes.filter(r => !r.is_default)
  const defaultRoute = routes.find(r => r.is_default) || null

  return (
    <Layout>
      <div className="max-w-7xl mx-auto px-6 py-8 space-y-5">
        <div>
          <h1 className="text-lg font-semibold text-content-strong">Proxy service</h1>
          <Hint className="text-sm mt-0.5">Route public hostnames to any service — on Rigger, your LAN, or a remote host.</Hint>
        </div>

        <div className="flex items-start gap-2 bg-info-subtle/40 border border-info-border/50 rounded-lg px-3 py-2 text-xs text-info-fg">
          <span>ⓘ</span>
          <span>Requests reach these routes only when Rigger’s Traefik receives them on ports 80/443 — either as your edge, or forwarded from your existing proxy.</span>
        </div>

        {isLoading ? (
          <p className="text-sm text-content-subtle py-8 text-center">Loading…</p>
        ) : (
          <div className="bg-surface border border-border rounded-xl">
            <div className="flex items-center justify-between px-4 py-3 border-b border-border">
              <div className="flex items-center gap-2">
                <span className="text-sm font-semibold text-content-strong">Routes</span>
                <span className="text-[11px] text-content-muted bg-surface-overlay/50 px-1.5 py-0.5 rounded">hostname → upstream</span>
              </div>
              <Btn size="xs" onClick={() => setModal('new')}>＋ Add route</Btn>
            </div>
            {realRoutes.length === 0 ? (
              <p className="text-sm text-content-subtle px-4 py-6 text-center">No proxy routes yet. Add a route to send a hostname to a service anywhere on your network.</p>
            ) : (
              <div className="divide-y divide-border">
                {realRoutes.map(r => (
                  <RouteRow key={r.id} r={r} plugins={plugins} accessLists={accessLists}
                    onToggle={() => toggleMut.mutate(r)}
                    onEdit={() => setModal({ editing: r })}
                    onDelete={() => setDeleting(r)} />
                ))}
              </div>
            )}
          </div>
        )}

        <AccessListsCard accessLists={accessLists} plugins={plugins} />

        <PluginsCard plugins={plugins} />

        <DefaultRouteCard route={defaultRoute} />

        <BackupRestoreCard />
      </div>

      {modal && (
        <RouteModal plugins={plugins} accessLists={accessLists}
          initial={modal === 'new' ? null : modal.editing}
          onClose={() => setModal(null)}
          onSaved={() => { qc.invalidateQueries({ queryKey: ['proxy-routes'] }); setModal(null) }} />
      )}
      {deleting && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60" onClick={() => setDeleting(null)}>
          <div className="bg-surface border border-border rounded-xl w-full max-w-sm mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Delete route “{deleting.name}”?</h3>
            <p className="text-sm text-content-muted">Traffic to {deleting.host || 'this host'} stops being proxied.</p>
            <div className="flex gap-2 justify-end">
              <Btn size="xs" variant="secondary" onClick={() => setDeleting(null)}>Cancel</Btn>
              <Btn size="xs" variant="dangerSubtle" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}>{delMut.isPending ? 'Deleting…' : 'Delete'}</Btn>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}

// BackupRestoreCard exports every route + access list (and, optionally, the
// portable Let's Encrypt cert store) as an encrypted .rpx bundle, and restores one
// on another server — so switching hosts doesn't mean rebuilding routes by hand or
// re-requesting certs (which risks LE rate limits).
const CERT_SCOPES = [
  { value: 'none', label: 'Routes and Access Lists Only — No certificates' },
  { value: 'proxy', label: 'Routes and Access Lists + Proxy Route Certificates' },
  { value: 'all', label: "Routes and Access Lists + All Let’s Encrypt Certificates" },
]

async function blobErr(e) {
  const d = e?.response?.data
  if (d instanceof Blob) { try { return JSON.parse(await d.text()).error || errMsg(e) } catch { return errMsg(e) } }
  return errMsg(e)
}

function BackupRestoreCard() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  // export
  const [certScope, setCertScope] = useState('none')
  const [expPass, setExpPass] = useState('')
  const [exporting, setExporting] = useState(false)
  const [expErr, setExpErr] = useState('')
  // import
  const fileRef = useRef(null)
  const [fileName, setFileName] = useState('')
  const [impPass, setImpPass] = useState('')
  const [replace, setReplace] = useState(false)
  const [importing, setImporting] = useState(false)
  const [impErr, setImpErr] = useState('')
  const [summary, setSummary] = useState(null)

  async function doExport() {
    setExporting(true); setExpErr('')
    try {
      await proxyBackup(certScope, expPass)
    } catch (e) {
      setExpErr(await blobErr(e))
    } finally { setExporting(false) }
  }

  async function doImport() {
    const f = fileRef.current?.files?.[0]
    if (!f) { setImpErr('Choose a .rpb backup file first.'); return }
    setImporting(true); setImpErr(''); setSummary(null)
    try {
      const s = await proxyRestore(f, impPass, replace)
      setSummary(s)
      qc.invalidateQueries({ queryKey: ['proxy-routes'] })
      qc.invalidateQueries({ queryKey: ['proxy-access-lists'] })
    } catch (e) {
      setImpErr(errMsg(e))
    } finally { setImporting(false) }
  }

  return (
    <div className="bg-surface border border-border rounded-xl">
      <button onClick={() => setOpen(o => !o)} className="w-full flex items-center justify-between px-4 py-3 text-left">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-content-strong">Backup &amp; restore</span>
          <span className="text-[11px] text-content-muted bg-surface-overlay/50 px-1.5 py-0.5 rounded">move routes + certs to another server</span>
        </div>
        <span className="text-content-subtle text-xs">{open ? '▲' : '▼'}</span>
      </button>

      {open && (
        <div className="px-4 pb-4 grid md:grid-cols-2 gap-5 border-t border-border pt-4">
          {/* Export */}
          <div className="space-y-3">
            <p className="text-xs font-semibold text-content-strong">Export a backup</p>
            <div>
              <Label>What to back up</Label>
              <Select value={certScope} onChange={setCertScope} options={CERT_SCOPES} />
              <Hint tone="faint" className="text-[11px] mt-1">
                Including certs makes them portable — the new server serves them immediately instead of re-requesting from Let’s Encrypt (which can hit rate limits on a bulk restore).
              </Hint>
            </div>
            <div>
              <Label>Passphrase {certScope !== 'none' ? '(required)' : '(optional)'}</Label>
              <Input type="password" value={expPass} onChange={setExpPass} placeholder="Protects private keys in the bundle" />
            </div>
            {expErr && <p className="text-xs text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-2.5 py-1.5">{expErr}</p>}
            <Btn size="xs" onClick={doExport} disabled={exporting}>{exporting ? 'Preparing…' : '⭳ Download backup'}</Btn>
          </div>

          {/* Import */}
          <div className="space-y-3 md:border-l md:border-border md:pl-5">
            <p className="text-xs font-semibold text-content-strong">Restore a backup</p>
            <div>
              <Label>Backup file (.rpx)</Label>
              <input ref={fileRef} type="file" accept=".rpx,.rpb,.tar.gz,.gz" onChange={e => setFileName(e.target.files?.[0]?.name || '')}
                className="block w-full text-xs text-content file:mr-3 file:py-1.5 file:px-3 file:rounded-lg file:border-0 file:text-xs file:font-semibold file:bg-surface-overlay file:text-content hover:file:bg-surface-raised" />
              {fileName && <p className="text-[11px] text-content-faint mt-1 truncate">{fileName}</p>}
            </div>
            <div>
              <Label>Passphrase</Label>
              <Input type="password" value={impPass} onChange={setImpPass} placeholder="If the backup is encrypted" />
            </div>
            <Toggle checked={replace} onChange={setReplace} label="Overwrite routes/lists with the same name" />
            {impErr && <p className="text-xs text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-2.5 py-1.5">{impErr}</p>}
            <Btn size="xs" onClick={doImport} disabled={importing}>{importing ? 'Restoring…' : '⭱ Restore'}</Btn>

            {summary && (
              <div className="text-xs space-y-2 border-t border-border pt-3">
                <p className="text-success-fg">
                  ✓ Routes: {summary.routes_created} added{summary.routes_replaced ? `, ${summary.routes_replaced} replaced` : ''}{summary.routes_skipped ? `, ${summary.routes_skipped} skipped` : ''}
                  {' · '}Access-lists: {summary.access_lists_created} added{summary.access_lists_replaced ? `, ${summary.access_lists_replaced} replaced` : ''}{summary.access_lists_skipped ? `, ${summary.access_lists_skipped} skipped` : ''}
                </p>
                {(summary.warnings || []).length > 0 && (
                  <ul className="text-warning-fg list-disc pl-4">{summary.warnings.map((w, i) => <li key={i}>{w}</li>)}</ul>
                )}
                {summary.cert_instructions && (
                  <div>
                    <p className="text-content-muted mb-1">Certificates staged ({(summary.certs_staged || []).join(', ')}). To install them into Traefik and reload:</p>
                    <pre className="whitespace-pre-wrap break-words bg-surface-raised border border-border rounded-lg p-2.5 text-[11px] text-content overflow-x-auto">{summary.cert_instructions}</pre>
                  </div>
                )}
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function RouteRow({ r, plugins, accessLists = [], onToggle, onEdit, onDelete }) {
  const dot = !r.enabled ? 'bg-content-faint' : 'bg-success-fg'
  const tls = tlsBadge(r.tls_mode)
  const acl = r.access_list_id ? accessLists.find(a => a.id === r.access_list_id) : null
  const upstream = r.type === 'redirect' ? r.redirect_to
    : (r.upstreams || []).map(u => `${u.scheme}://${u.host}${u.port ? ':' + u.port : ''}`).join(', ')
  return (
    <div className="flex items-center gap-3 px-4 py-3">
      <span className={`w-2 h-2 rounded-full shrink-0 ${dot}`} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2 flex-wrap">
          <span className="text-sm font-semibold text-content-strong">{r.name}</span>
          {r.type === 'redirect' && <Badge>Redirect</Badge>}
          {r.tls_mode !== 'none' && <Badge cls={tls.cls}>{tls.label}</Badge>}
          {r.tls_mode !== 'none' && r.cert && <CertBadge cert={r.cert} />}
          {acl
            ? <Badge cls="bg-brand-600/15 text-accent-text">🔒 {acl.name}</Badge>
            : <>
                {r.auth_mode === 'basic' && <Badge cls="bg-brand-600/15 text-accent-text">Basic auth</Badge>}
                {r.ip_allow && <Badge>IP allow</Badge>}
                {r.geo_mode === 'allow' && <Badge cls="bg-brand-600/15 text-accent-text">🌐 allow</Badge>}
                {r.geo_mode === 'block' && <Badge cls="bg-danger-subtle/50 text-danger-fg">🌐 deny</Badge>}
              </>}
          {r.hsts_seconds > 0 && <Badge>HSTS</Badge>}
          {r.waf && plugins?.waf_enabled && <Badge cls="bg-brand-600/15 text-accent-text">WAF</Badge>}
          {r.cache && plugins?.cache_enabled && <Badge cls="bg-warning-subtle/50 text-warning-fg">Cache</Badge>}
        </div>
        <div className="text-xs text-content-subtle mt-0.5 font-mono truncate">
          {r.host || '(any host)'}{r.path_prefix ? r.path_prefix : ''} → {upstream || '—'}
        </div>
      </div>
      <div className="flex items-center gap-1.5 shrink-0">
        <Toggle checked={r.enabled} onChange={onToggle} label="" />
        <Btn size="xs" variant="ghost" onClick={onEdit}>Edit</Btn>
        <Btn size="xs" variant="ghost" onClick={onDelete}>✕</Btn>
      </div>
    </div>
  )
}

function PluginsCard({ plugins }) {
  const qc = useQueryClient()
  const [geoToken, setGeoToken] = useState('')
  const [geoMsg, setGeoMsg] = useState(null)
  const mut = useMutation({
    mutationFn: (body) => updateProxyPlugins(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['proxy-plugins'] }); qc.invalidateQueries({ queryKey: ['proxy-routes'] }) },
  })
  const geoMut = useMutation({
    mutationFn: () => setProxyGeoIPDB(geoToken.trim()),
    onSuccess: () => { setGeoMsg({ ok: true }); setGeoToken(''); qc.invalidateQueries({ queryKey: ['proxy-plugins'] }) },
    onError: (e) => setGeoMsg({ error: e.response?.data?.error || 'Download failed' }),
  })
  if (!plugins) return null
  const db = plugins.geoip_db || {}
  return (
    <div className="bg-surface border border-border rounded-xl p-4">
      <div className="flex items-center gap-2 mb-1">
        <span className="text-sm font-semibold text-content-strong">Plugins</span>
        <span className="text-[11px] text-content-muted bg-surface-overlay/50 px-1.5 py-0.5 rounded">advanced</span>
      </div>
      <Hint className="mb-1">Optional WAF, asset cache, and GeoIP country blocking — attachable per route / access list / hosted env once enabled. Managed from here; no docker-compose edit.</Hint>
      <p className="text-[11px] text-warning-fg mb-3">⚠ Enabling or disabling a plugin restarts the reverse proxy — a few seconds of downtime for ALL routed apps. Per-route attach afterwards is instant.{mut.isPending && ' · applying…'}</p>
      <div className="flex gap-6 flex-wrap items-center">
        <Toggle checked={!!plugins.waf_enabled} onChange={v => mut.mutate({ waf_enabled: v })} label="Web application firewall (Coraza)" />
        {plugins.cache_supported
          ? <Toggle checked={!!plugins.cache_enabled} onChange={v => mut.mutate({ cache_enabled: v })} label="Cache assets (Souin)" />
          : <span className="inline-flex items-center gap-1.5 text-sm text-content-faint" title="Souin is incompatible with Traefik's Yaegi plugin interpreter">🚫 Cache assets <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-overlay/50 text-content-muted">unavailable</span></span>}
        <Toggle checked={!!plugins.geoip_enabled} onChange={v => mut.mutate({ geoip_enabled: v })} label="GeoIP blocking (geoblock)" />
      </div>
      <Hint tone="faint" className="text-[11px] mt-2">Pinned: Coraza {plugins.waf_version} · geoblock {plugins.geoip_version} — fetched from their module source when the proxy starts. WAF + GeoIP are verified working.{!plugins.cache_supported && <> <span className="text-warning-fg">Asset cache (Souin) is disabled — it panics under Traefik&apos;s plugin interpreter (Yaegi), which would drop every routed app. For caching, put a CDN (e.g. Cloudflare) in front, or run a dedicated cache sidecar (Varnish / Nginx).</span></>}</Hint>

      {plugins.geoip_enabled && (
        <div className="mt-3 border-t border-border pt-3">
          <p className="text-xs font-semibold text-content-strong mb-1">GeoIP database (IP2Location LITE)</p>
          <Hint tone="faint" className="text-[11px] mb-2">
            {db.present
              ? <>Installed — {(db.size / 1e6).toFixed(1)} MB, updated {new Date(db.mod_time).toLocaleDateString()}.</>
              : <>Not downloaded yet — GeoIP rules won&apos;t match until a database is installed.</>}
            {' '}Get a free token at ip2location.com (LITE, DB1, IPv6 BIN); refresh monthly.
          </Hint>
          <div className="flex items-center gap-2">
            <input type="password" value={geoToken} onChange={e => { setGeoToken(e.target.value); setGeoMsg(null) }}
              placeholder={plugins.geoip_token_set ? 'token saved — paste to replace, or just refresh' : 'IP2Location LITE download token'}
              className={`${CONTROL} flex-1`} />
            <Btn variant="primary" size="md" onClick={() => { setGeoMsg(null); geoMut.mutate() }}
              disabled={geoMut.isPending || (!geoToken.trim() && !plugins.geoip_token_set)}
              className="shrink-0">
              {geoMut.isPending ? 'Downloading…' : db.present ? 'Refresh' : 'Download'}
            </Btn>
          </div>
          {geoMsg?.ok && <p className="text-[11px] text-accent-text mt-1">Database updated.</p>}
          {geoMsg?.error && <p className="text-[11px] text-danger-fg mt-1">{geoMsg.error}</p>}
          <Hint tone="faint" className="text-[11px] mt-1">Behind another proxy (NPM / Cloudflare)? GeoIP needs the real client IP — set Traefik forwardedHeaders trust for your proxy.</Hint>
        </div>
      )}
    </div>
  )
}

function DefaultRouteCard({ route }) {
  const qc = useQueryClient()
  const [mode, setMode] = useState(route?.default_mode || 'page')
  const [redirectTo, setRedirectTo] = useState(route?.redirect_to || '')
  const [saved, setSaved] = useState(false)
  const mut = useMutation({
    mutationFn: () => {
      const body = { ...(route || blankRoute()), name: 'Default route', is_default: true, enabled: true, default_mode: mode, type: mode === 'redirect' ? 'redirect' : 'proxy', redirect_to: redirectTo, host: '', upstreams: [], auth_users: [] }
      return route ? updateProxyRoute(route.id, body) : createProxyRoute(body)
    },
    onSuccess: () => { setSaved(true); qc.invalidateQueries({ queryKey: ['proxy-routes'] }); setTimeout(() => setSaved(false), 3000) },
  })
  return (
    <div className="bg-surface border border-border rounded-xl p-4">
      <div className="flex items-center gap-2 mb-1">
        <span className="text-sm font-semibold text-content-strong">Default route</span>
        <span className="text-[11px] text-content-muted bg-surface-overlay/50 px-1.5 py-0.5 rounded">catch-all · lowest priority</span>
      </div>
      <Hint className="mb-3">What a request gets when its host matches no project URL and no proxy route. Project URLs and configured routes always take priority.</Hint>
      <div className="grid grid-cols-2 gap-3 items-end">
        <div>
          <Label>When unmatched</Label>
          <Select value={mode} onChange={setMode} options={DEFAULT_MODES} />
        </div>
        {mode === 'redirect' && (
          <div><Label>Redirect to URL</Label><Input value={redirectTo} onChange={setRedirectTo} placeholder="https://example.com" /></div>
        )}
      </div>
      <div className="flex items-center gap-3 mt-3">
        <Btn size="xs" onClick={() => mut.mutate()} disabled={mut.isPending}>{mut.isPending ? 'Saving…' : 'Save default'}</Btn>
        {saved && <span className="text-xs text-success-fg">✓ Saved</span>}
      </div>
    </div>
  )
}

function TabBtn({ active, onClick, children }) {
  return (
    <button type="button" onClick={onClick}
      className={`px-3 py-2 text-xs font-semibold border-b-2 -mb-px transition-colors whitespace-nowrap ${active ? 'border-brand-500 text-content-strong' : 'border-transparent text-content-muted hover:text-content'}`}>
      {children}
    </button>
  )
}

function blankAccessList() { return { name: '', pass_auth: true, users: [], rules: [], geo_mode: 'off', countries: [] } }

// AccessListsCard lists the reusable access lists (NPM-style) and hosts their editor.
// Sits between the route list and the Plugins card.
function AccessListsCard({ accessLists, plugins }) {
  const qc = useQueryClient()
  const [modal, setModal] = useState(null) // null | 'new' | {editing}
  const [deleting, setDeleting] = useState(null)
  const invalidate = () => { qc.invalidateQueries({ queryKey: ['proxy-access-lists'] }); qc.invalidateQueries({ queryKey: ['proxy-routes'] }) }
  const delMut = useMutation({
    mutationFn: (id) => deleteProxyAccessList(id),
    onSuccess: () => { invalidate(); setDeleting(null) },
  })
  return (
    <div className="bg-surface border border-border rounded-xl">
      <div className="flex items-center justify-between px-4 py-3 border-b border-border">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-content-strong">Access lists</span>
          <span className="text-[11px] text-content-muted bg-surface-overlay/50 px-1.5 py-0.5 rounded">reusable auth + IP rules</span>
        </div>
        <Btn size="xs" onClick={() => setModal('new')}>＋ Add access list</Btn>
      </div>
      {accessLists.length === 0 ? (
        <p className="text-sm text-content-subtle px-4 py-6 text-center">No access lists yet. Create one to reuse the same basic-auth users and IP rules across multiple routes.</p>
      ) : (
        <div className="divide-y divide-border">
          {accessLists.map(a => {
            const allow = (a.rules || []).filter(r => r.action === 'allow').length
            const deny = (a.rules || []).filter(r => r.action === 'deny').length
            return (
              <div key={a.id} className="flex items-center gap-3 px-4 py-3">
                <div className="min-w-0 flex-1">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="text-sm font-semibold text-content-strong">{a.name}</span>
                    {(a.users || []).length > 0 && <Badge cls="bg-brand-600/15 text-accent-text">{a.users.length} user{a.users.length > 1 ? 's' : ''}</Badge>}
                    {allow > 0 && <Badge>{allow} allow</Badge>}
                    {deny > 0 && <Badge cls="bg-danger-subtle/50 text-danger-fg">{deny} deny</Badge>}
                    {a.geo_mode === 'allow' && <Badge cls="bg-brand-600/15 text-accent-text">🌐 allow {(a.countries || []).length}</Badge>}
                    {a.geo_mode === 'block' && <Badge cls="bg-danger-subtle/50 text-danger-fg">🌐 block {(a.countries || []).length}</Badge>}
                    {!a.pass_auth && <Badge>strips auth header</Badge>}
                  </div>
                </div>
                <div className="flex items-center gap-1.5 shrink-0">
                  <Btn size="xs" variant="ghost" onClick={() => setModal({ editing: a })}>Edit</Btn>
                  <Btn size="xs" variant="ghost" onClick={() => setDeleting(a)}>✕</Btn>
                </div>
              </div>
            )
          })}
        </div>
      )}
      {modal && <AccessListModal initial={modal === 'new' ? null : modal.editing} plugins={plugins} onClose={() => setModal(null)} onSaved={() => { invalidate(); setModal(null) }} />}
      {deleting && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60" onClick={() => setDeleting(null)}>
          <div className="bg-surface border border-border rounded-xl w-full max-w-sm mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Delete access list “{deleting.name}”?</h3>
            <p className="text-sm text-content-muted">Routes using it become publicly accessible (no auth/IP restriction) until reconfigured.</p>
            <div className="flex gap-2 justify-end">
              <Btn size="xs" variant="secondary" onClick={() => setDeleting(null)}>Cancel</Btn>
              <Btn size="xs" variant="dangerSubtle" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}>{delMut.isPending ? 'Deleting…' : 'Delete'}</Btn>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function AccessListModal({ initial, plugins, onClose, onSaved }) {
  const isEdit = !!initial
  const [f, setF] = useState(() => initial
    ? { ...blankAccessList(), ...initial, users: (initial.users || []).map(u => ({ user: u.user, password: '' })), rules: (initial.rules || []).map(r => ({ action: r.action || 'allow', address: r.address || '' })), geo_mode: initial.geo_mode || 'off', countriesText: (initial.countries || []).join(', ') }
    : { ...blankAccessList(), countriesText: '' })
  const [err, setErr] = useState('')
  const set = (k, v) => { if (err) setErr(''); setF(s => ({ ...s, [k]: v })) }
  const save = useMutation({
    mutationFn: () => {
      const countries = (f.countriesText || '').split(/[\s,]+/).map(c => c.trim().toUpperCase()).filter(c => c.length === 2)
      const body = {
        name: f.name, pass_auth: !!f.pass_auth,
        users: f.users.filter(u => u.user).map(u => ({ user: u.user, password: u.password || '' })),
        rules: f.rules.filter(r => r.address).map(r => ({ action: r.action === 'deny' ? 'deny' : 'allow', address: r.address })),
        geo_mode: countries.length ? f.geo_mode : 'off',
        countries,
      }
      return isEdit ? updateProxyAccessList(initial.id, body) : createProxyAccessList(body)
    },
    onSuccess: onSaved,
    onError: (e) => setErr(errMsg(e, 'Save failed')),
  })
  function trySave() { setErr(''); if (!f.name.trim()) { setErr('Name is required'); return } save.mutate() }
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-4 overflow-y-auto" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-xl my-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-3 border-b border-border">
          <span className="font-semibold text-content-strong text-sm">{isEdit ? `Edit access list — ${initial.name}` : 'Add access list'}</span>
          <CloseBtn onClick={onClose} />
        </div>
        <div className="px-5 py-4 space-y-4">
          <div><Label>Name</Label><Input value={f.name} onChange={v => set('name', v)} placeholder="Office + admins" /></div>

          <div className="border-t border-border pt-3">
            <Label>Authorized users (basic auth)</Label>
            <div className="space-y-2 mt-1">
              {f.users.map((u, i) => (
                <div key={i} className="flex items-center gap-2">
                  <input value={u.user} onChange={e => set('users', f.users.map((x, j) => j === i ? { ...x, user: e.target.value } : x))} placeholder="username"
                    className={`${CONTROL} flex-1`} />
                  <input type="password" value={u.password} onChange={e => set('users', f.users.map((x, j) => j === i ? { ...x, password: e.target.value } : x))} placeholder={isEdit ? '(unchanged)' : 'password'}
                    className={`${CONTROL} flex-1`} />
                  <IconBtn variant="dangerGhost" size="xs" onClick={() => set('users', f.users.filter((_, j) => j !== i))} >🗑</IconBtn>
                </div>
              ))}
              <button onClick={() => set('users', [...f.users, { user: '', password: '' }])} className="text-xs text-accent-text hover:text-accent-text-hover">＋ Add user</button>
            </div>
            <div className="mt-2"><Toggle checked={!!f.pass_auth} onChange={v => set('pass_auth', v)} label="Forward the Authorization header to the upstream" /></div>
          </div>

          <div className="border-t border-border pt-3">
            <Label>IP rules</Label>
            <Hint tone="faint" className="text-[11px] mt-1 mb-2">Traefik enforces an allow-list: if any Allow rules exist, only those ranges are permitted (everything else denied). Deny rules document exclusions; a deny-only list can’t be enforced at the proxy.</Hint>
            <div className="space-y-2">
              {f.rules.map((r, i) => (
                <div key={i} className="flex items-center gap-2">
                  <select value={r.action} onChange={e => set('rules', f.rules.map((x, j) => j === i ? { ...x, action: e.target.value } : x))} className={`${CONTROL} w-full`}>
                    <option value="allow">Allow</option><option value="deny">Deny</option>
                  </select>
                  <input value={r.address} onChange={e => set('rules', f.rules.map((x, j) => j === i ? { ...x, address: e.target.value } : x))} placeholder="192.168.0.0/16 or 203.0.113.4"
                    className={`${CONTROL} flex-1`} />
                  <IconBtn variant="dangerGhost" size="xs" onClick={() => set('rules', f.rules.filter((_, j) => j !== i))} >🗑</IconBtn>
                </div>
              ))}
              <button onClick={() => set('rules', [...f.rules, { action: 'allow', address: '' }])} className="text-xs text-accent-text hover:text-accent-text-hover">＋ Add IP rule</button>
            </div>
          </div>

          <div className="border-t border-border pt-3">
            <Label>GeoIP country policy</Label>
            <div className="grid grid-cols-2 gap-3 mt-1">
              <Select value={f.geo_mode || 'off'} onChange={v => set('geo_mode', v)} options={[
                { value: 'off', label: 'Off' },
                { value: 'allow', label: 'Allow only these countries' },
                { value: 'block', label: 'Block these countries' },
              ]} />
              <Input value={f.countriesText} onChange={v => set('countriesText', v)} placeholder="US, DE, GB" disabled={f.geo_mode === 'off'} />
            </div>
            <Hint tone="faint" className="text-[11px] mt-1">
              Two-letter <a href="https://en.wikipedia.org/wiki/ISO_3166-1_alpha-2" target="_blank" rel="noreferrer" className="text-accent-text underline">ISO country codes</a>, comma-separated.
              {!plugins?.geoip_enabled && ' Enable the GeoIP plugin on the Proxy Service page for this to take effect.'}
            </Hint>
          </div>

          {err && (
            <details open className="text-sm bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">
              <summary className="cursor-pointer text-danger-fg font-medium select-none">Couldn’t save — details</summary>
              <pre className="mt-2 whitespace-pre-wrap break-words text-xs text-danger-fg/90 font-mono">{err}</pre>
            </details>
          )}
        </div>
        <div className="flex items-center justify-end gap-2 px-5 py-3 border-t border-border">
          <Btn size="xs" variant="secondary" onClick={onClose}>Cancel</Btn>
          <Btn size="xs" onClick={trySave} disabled={save.isPending}>{save.isPending ? 'Saving…' : 'Save access list'}</Btn>
        </div>
      </div>
    </div>
  )
}

// TestToast is the compact, auto-dismissing result shown next to "Test upstream".
function TestToast({ test, onClose }) {
  if (test.loading) return <span className="text-xs text-content-muted">Testing…</span>
  const rs = test.results || []
  const good = rs.filter(r => r.ok).length
  const allOk = !test.error && rs.length > 0 && good === rs.length
  let msg, tone
  if (test.error) { msg = `✕ ${test.error}`; tone = 'danger' }
  else if (rs.length === 0) { msg = 'No upstreams to test'; tone = 'muted' }
  else if (allOk) { msg = rs.length === 1 ? `✓ ${rs[0].target} reachable (${rs[0].latency_ms}ms)` : `✓ all ${good} upstreams reachable`; tone = 'success' }
  else { const b = rs.find(r => !r.ok); msg = `✕ ${rs.length - good}/${rs.length} failed — ${b.target}: ${b.error}`; tone = 'danger' }
  const cls = tone === 'success' ? 'bg-success-subtle/40 border-success-border/50 text-success-fg'
    : tone === 'danger' ? 'bg-danger-subtle/40 border-danger-border/50 text-danger-fg'
    : 'bg-surface-overlay/40 border-border text-content-muted'
  return (
    <div className={`flex items-center gap-1.5 min-w-0 text-xs px-2 py-1 rounded-md border ${cls}`}>
      <span className="truncate" title={msg}>{msg}</span>
      <button onClick={onClose} className="shrink-0 opacity-60 hover:opacity-100">✕</button>
    </div>
  )
}

function RouteModal({ initial, plugins, accessLists = [], onClose, onSaved }) {
  const isEdit = !!initial
  const [f, setF] = useState(() => {
    if (!initial) return blankRoute()
    return {
      ...blankRoute(), ...initial,
      upstreams: initial.upstreams?.length ? initial.upstreams : [{ scheme: 'http', host: '', port: '' }],
      locations: (initial.locations || []).map(normLocation),
      auth_users: (initial.auth_users || []).map(u => ({ user: u.user, password: '' })),
      geo_mode: initial.geo_mode || 'off',
      countriesText: (initial.countries || []).join(', '),
    }
  })
  // Access mode (Public / Basic auth / Access list) is a single radio over the existing
  // auth_mode + access_list_id fields; ipEnabled gates the IP allow-list box (ip_allow is a
  // plain string, so an explicit toggle lets it show "on but empty"). Mirrors the env editor.
  const [accessMode, setAccessMode] = useState(
    Number(initial?.access_list_id) > 0 ? 'list' : (initial?.auth_mode === 'basic' ? 'basic' : 'public'))
  const [ipEnabled, setIpEnabled] = useState(!!(initial?.ip_allow || '').trim())
  const [tab, setTab] = useState('basics')
  const [err, setErr] = useState('')
  const [test, setTest] = useState(null)
  const { data: certData } = useQuery({ queryKey: ['proxy-certs'], queryFn: fetchProxyCerts })
  const certs = certData?.certs || []
  const inheritedEmail = certData?.acme_email || '(not set in Settings)'
  // Any edit to the form clears a stale save error (user asked: message goes away
  // as soon as they start making changes).
  const set = (k, v) => { if (err) setErr(''); setF(s => ({ ...s, [k]: v })) }
  const chooseAccess = (m) => {
    if (err) setErr('')
    setAccessMode(m)
    if (m === 'public') setF(s => ({ ...s, auth_mode: 'none', access_list_id: 0 }))
    else if (m === 'basic') setF(s => ({ ...s, auth_mode: 'basic', access_list_id: 0 }))
    else setF(s => ({ ...s, auth_mode: 'none' })) // list: keep access_list_id; the dropdown sets it
  }
  const setUp = (i, k, v) => { if (err) setErr(''); setF(s => ({ ...s, upstreams: s.upstreams.map((u, j) => j === i ? { ...u, [k]: v } : u) })) }
  // location upstream helpers (li = location index, ui = upstream index)
  const setLoc = (li, k, v) => set('locations', f.locations.map((l, j) => j === li ? { ...l, [k]: v } : l))
  const setLocUp = (li, ui, k, v) => set('locations', f.locations.map((l, j) => j === li ? { ...l, upstreams: l.upstreams.map((u, m) => m === ui ? { ...u, [k]: v } : u) } : l))

  const needsToS = (f.tls_mode === 'le-http' || f.tls_mode === 'le-dns') && !f.accept_tos

  const save = useMutation({
    mutationFn: () => {
      const countries = (f.countriesText || '').split(',').map(s => s.trim().toUpperCase()).filter(Boolean)
      const body = {
        ...f,
        access_list_id: Number(f.access_list_id) || 0,
        redirect_code: Number(f.redirect_code) || 301,
        hsts_seconds: f.hsts_seconds > 0 ? Number(f.hsts_seconds) : 0,
        ip_allow: ipEnabled ? f.ip_allow : '',
        geo_mode: countries.length ? f.geo_mode : 'off',
        countries,
        upstreams: f.upstreams.filter(u => u.host).map(u => ({ scheme: u.scheme, host: u.host, port: Number(u.port) || 0 })),
        auth_users: f.auth_mode === 'basic' ? f.auth_users.filter(u => u.user) : [],
        locations: (f.locations || []).map(l => ({
          path: l.path,
          forward_path: l.forward_path || '',
          upstreams: (l.upstreams || []).filter(u => u.host).map(u => ({ scheme: u.scheme || 'http', host: u.host, port: Number(u.port) || 0 })),
        })).filter(l => l.path && l.upstreams.length),
      }
      return isEdit ? updateProxyRoute(initial.id, body) : createProxyRoute(body)
    },
    onSuccess: onSaved,
    onError: (e) => setErr(errMsg(e, 'Save failed')),
  })

  function trySave() {
    setErr('')
    if (needsToS) {
      setTab('certs')
      setErr("Accept the Let's Encrypt Terms of Service (Certs & SSL tab) to use a Let's Encrypt certificate.")
      return
    }
    save.mutate()
  }

  // Test result behaves like a toaster: clears itself after ~12s. The ref lets a new
  // test cancel the prior timer so an old result can't wipe a fresh one early.
  const testTimer = useRef(null)
  function showTest(state) {
    if (testTimer.current) clearTimeout(testTimer.current)
    setTest(state)
    if (state && !state.loading) testTimer.current = setTimeout(() => setTest(null), 12000)
  }
  async function runTest() {
    showTest({ loading: true })
    try {
      const res = await testProxyRoute(isEdit ? initial.id : null, { upstreams: f.upstreams.filter(u => u.host).map(u => ({ scheme: u.scheme, host: u.host, port: Number(u.port) || 0 })) })
      showTest({ results: res.results || [] })
    } catch (e) { showTest({ error: errMsg(e, 'Test failed') }) }
  }

  const isRedirect = f.type === 'redirect'
  const tlsOn = f.tls_mode !== 'none'

  const tabs = [
    { id: 'basics', label: 'Basics & Security' },
    ...(!isRedirect ? [{ id: 'locations', label: 'Locations' }] : []),
    { id: 'certs', label: 'Certs & SSL' },
    ...(!isRedirect ? [{ id: 'advanced', label: 'Advanced' }] : []),
  ]
  const activeTab = tabs.some(t => t.id === tab) ? tab : 'basics'

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-4 overflow-y-auto" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-xl my-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-3 border-b border-border">
          <span className="font-semibold text-content-strong text-sm">{isEdit ? `Edit route — ${initial.name}` : 'Add route'}</span>
          <CloseBtn onClick={onClose} />
        </div>
        <div className="flex gap-1 px-5 border-b border-border">
          {tabs.map(t => <TabBtn key={t.id} active={activeTab === t.id} onClick={() => setTab(t.id)}>{t.label}</TabBtn>)}
        </div>
        <div className="px-5 py-4 space-y-4 min-h-[18rem]">
          {/* ── Basics & Security ─────────────────────────────────────────── */}
          {activeTab === 'basics' && (<>
          <div className="grid grid-cols-2 gap-3 items-end">
            <div><Label>Name</Label><Input value={f.name} onChange={v => set('name', v)} placeholder="Jellyfin" /></div>
            <div>
              <Label>Type</Label>
              <Select value={f.type} onChange={v => set('type', v)} options={[{ value: 'proxy', label: 'Proxy' }, { value: 'redirect', label: 'Redirect' }]} />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Domain(s)</Label>
              <Input value={f.host} onChange={v => set('host', v)} placeholder="media.example.com, www.example.com" />
              <Hint tone="faint" className="text-[11px] mt-1">One or more, separated by space or comma.</Hint>
            </div>
            <div><Label>Path prefix</Label><Input value={f.path_prefix} onChange={v => set('path_prefix', v)} placeholder="/ (optional)" /></div>
          </div>

          {isRedirect ? (
            <div className="grid grid-cols-2 gap-3 items-end">
              <div><Label>Redirect to URL</Label><Input value={f.redirect_to} onChange={v => set('redirect_to', v)} placeholder="https://new.example.com" /></div>
              <div><Label>Code</Label><Select value={String(f.redirect_code)} onChange={v => set('redirect_code', v)} options={[{ value: '301', label: '301 permanent' }, { value: '302', label: '302 temporary' }]} /></div>
            </div>
          ) : (
            <div>
              <Label>Upstreams</Label>
              <div className="space-y-2">
                {f.upstreams.map((u, i) => (
                  <div key={i} className="flex items-center gap-2">
                    <select value={u.scheme} onChange={e => setUp(i, 'scheme', e.target.value)} className={`${CONTROL} w-full`}>
                      <option value="http">http</option><option value="https">https</option>
                    </select>
                    <input value={u.host} onChange={e => setUp(i, 'host', e.target.value)} placeholder="192.168.1.50"
                      className={`${CONTROL} flex-1`} />
                    <span className="text-content-muted">:</span>
                    <input value={u.port} onChange={e => setUp(i, 'port', e.target.value)} placeholder="8096"
                      className={`${CONTROL} w-20`} />
                    <IconBtn variant="dangerGhost" size="xs" onClick={() => set('upstreams', f.upstreams.filter((_, j) => j !== i))} title="Remove"
                      >🗑</IconBtn>
                  </div>
                ))}
                <button onClick={() => set('upstreams', [...f.upstreams, { scheme: 'http', host: '', port: '' }])}
                  className="text-xs text-accent-text hover:text-accent-text-hover">＋ Add upstream</button>
              </div>
            </div>
          )}

          {/* Access & hardening */}
          {!isRedirect && (
            <div className="border-t border-border pt-3 space-y-3">
              {/* Access mode — Public / Basic auth / Access list (mutually exclusive). */}
              <div className="flex flex-wrap items-center gap-x-5 gap-y-2">
                {[
                  { v: 'public', label: 'Public' },
                  { v: 'basic', label: 'Basic Auth — HTTP password' },
                  { v: 'list', label: 'Access list' },
                ].map(o => (
                  <label key={o.v} className="flex items-center gap-2 text-sm text-content-muted cursor-pointer">
                    <input type="radio" name="route-access" value={o.v} checked={accessMode === o.v}
                      onChange={() => chooseAccess(o.v)} className="w-3.5 h-3.5 accent-brand-500" />
                    {o.label}
                  </label>
                ))}
              </div>

              {/* Access list — GLOBAL Proxy Service lists only (workspace lists stay isolated). */}
              {accessMode === 'list' && (() => {
                const al = accessLists.find(a => a.id === Number(f.access_list_id))
                return (
                  <div>
                    <Label>Access list</Label>
                    <Select value={String(f.access_list_id || 0)} onChange={v => set('access_list_id', Number(v))}
                      options={[{ value: '0', label: accessLists.length ? 'Select a list…' : 'No lists available' }, ...accessLists.map(a => ({ value: String(a.id), label: a.name }))]} />
                    {accessLists.length === 0
                      ? <Hint tone="faint" className="text-[11px] mt-1">No global access lists yet — create them in the Access lists section below.</Hint>
                      : al ? (() => {
                          const allow = (al.rules || []).filter(r => r.action === 'allow').length
                          const deny = (al.rules || []).filter(r => r.action === 'deny').length
                          const geo = al.geo_mode && al.geo_mode !== 'off' ? `, GeoIP ${al.geo_mode} ${(al.countries || []).length}` : ''
                          return <Hint className="text-[11px] mt-1">{(al.users || []).length} user{(al.users || []).length === 1 ? '' : 's'}, {allow} allow{deny ? `, ${deny} deny` : ''}{geo}{al.pass_auth ? '' : ' · strips auth header'}. Edit it in the Access lists section below.</Hint>
                        })()
                      : <p className="text-xs text-danger-fg mt-1">Selected access list no longer exists — pick another.</p>}
                  </div>
                )
              })()}

              {/* Basic auth — username/password list. */}
              {accessMode === 'basic' && (
                <div className="space-y-2">
                  {f.auth_users.map((u, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <input value={u.user} onChange={e => set('auth_users', f.auth_users.map((x, j) => j === i ? { ...x, user: e.target.value } : x))} placeholder="username"
                        className={`${CONTROL} flex-1`} />
                      <input type="password" value={u.password} onChange={e => set('auth_users', f.auth_users.map((x, j) => j === i ? { ...x, password: e.target.value } : x))} placeholder={isEdit ? '(unchanged)' : 'password'}
                        className={`${CONTROL} flex-1`} />
                      <IconBtn variant="dangerGhost" size="xs" onClick={() => set('auth_users', f.auth_users.filter((_, j) => j !== i))} >🗑</IconBtn>
                    </div>
                  ))}
                  <button onClick={() => set('auth_users', [...f.auth_users, { user: '', password: '' }])} className="text-xs text-accent-text hover:text-accent-text-hover">＋ Add user</button>
                </div>
              )}

              {/* Inline IP allow-list + GeoIP — for Public & Basic auth (a chosen list carries its own). */}
              {accessMode !== 'list' && (
                <>
                  <div className="flex items-start justify-between gap-3">
                    <label className="flex items-center gap-2 text-sm text-content-muted cursor-pointer shrink-0 pt-2">
                      <input type="checkbox" checked={ipEnabled}
                        onChange={e => { setIpEnabled(e.target.checked); if (!e.target.checked) set('ip_allow', '') }}
                        className="w-3.5 h-3.5 accent-brand-500" />
                      Enable IP rules
                    </label>
                    {ipEnabled && (
                      <div className="flex-1 min-w-0 max-w-md">
                        <div className="flex items-center gap-2">
                          <span className="text-xs text-content-subtle shrink-0">IP / CIDR:</span>
                          <Input value={f.ip_allow} onChange={v => set('ip_allow', v)} placeholder="192.168.0.0/16, 203.0.113.7" />
                        </div>
                        <Hint tone="faint" className="text-[11px] mt-1">Allow-list — only these IPs / CIDRs may reach the route (comma-separated). Traefik has no native deny-list.</Hint>
                      </div>
                    )}
                  </div>

                  <div className="flex items-start justify-between gap-3">
                    <label className="flex items-center gap-2 text-sm text-content-muted cursor-pointer shrink-0 pt-2">
                      <input type="checkbox" checked={f.geo_mode === 'allow' || f.geo_mode === 'block'}
                        onChange={e => set('geo_mode', e.target.checked ? 'allow' : 'off')}
                        disabled={!plugins?.geoip_enabled}
                        className="w-3.5 h-3.5 accent-brand-500 disabled:opacity-50" />
                      Enable GeoIP country policy
                    </label>
                    {!plugins?.geoip_enabled
                      ? <Hint tone="faint" className="flex-1 text-[11px] pt-2">Enable the GeoIP plugin (Plugins card) to use this.</Hint>
                      : (f.geo_mode === 'allow' || f.geo_mode === 'block') && (
                        <div className="flex-1 min-w-0 max-w-md">
                          <div className="flex items-center gap-3 flex-wrap">
                            <div className="flex items-center gap-3 shrink-0">
                              {[{ v: 'allow', l: 'Allow' }, { v: 'block', l: 'Deny' }].map(o => (
                                <label key={o.v} className="flex items-center gap-1.5 text-xs text-content-muted cursor-pointer">
                                  <input type="radio" name="route-geo" checked={f.geo_mode === o.v}
                                    onChange={() => set('geo_mode', o.v)} className="w-3 h-3 accent-brand-500" />
                                  {o.l}
                                </label>
                              ))}
                            </div>
                            <div className="flex items-center gap-2 flex-1 min-w-0">
                              <span className="text-xs text-content-subtle shrink-0">Country codes:</span>
                              <Input value={f.countriesText} onChange={v => set('countriesText', v)} placeholder="US, DE, GB" />
                            </div>
                          </div>
                          <Hint tone="faint" className="text-[11px] mt-1">ISO 3166-1 alpha-2 codes. <span className="text-content-subtle">Allow</span> = only these; <span className="text-content-subtle">Deny</span> = block these (allow the rest).</Hint>
                        </div>
                      )}
                  </div>
                </>
              )}

              <Toggle checked={!!f.security_headers} onChange={v => set('security_headers', v)} label="Block common exploits (security headers)" />
            </div>
          )}
          </>)}

          {/* ── Locations (custom locations, NPM parity) ──────────────────── */}
          {activeTab === 'locations' && !isRedirect && (
            <div>
              <Hint tone="faint" className="text-[11px] mb-3">Forward sub-paths on this host to different services. Each inherits this route's TLS, auth and headers. Add more than one upstream to load-balance that path.</Hint>
              <div className="space-y-3">
                {(f.locations || []).map((l, i) => (
                  <div key={i} className="border border-border rounded-lg p-3 space-y-2 bg-surface-raised/30">
                    <div className="flex items-center gap-2">
                      <input value={l.path} onChange={e => setLoc(i, 'path', e.target.value)} placeholder="/path"
                        className={`${CONTROL} w-28`} />
                      <input value={l.forward_path || ''} onChange={e => setLoc(i, 'forward_path', e.target.value)} placeholder="forward to /sub (optional)"
                        className={`${CONTROL} flex-1`} />
                      <IconBtn variant="dangerGhost" size="xs" onClick={() => set('locations', f.locations.filter((_, j) => j !== i))} title="Remove location" >🗑</IconBtn>
                    </div>
                    <div className="space-y-1.5 pl-1">
                      {(l.upstreams || []).map((u, k) => (
                        <div key={k} className="flex items-center gap-2">
                          <select value={u.scheme || 'http'} onChange={e => setLocUp(i, k, 'scheme', e.target.value)} className={`${CONTROL} w-full`}>
                            <option value="http">http</option><option value="https">https</option>
                          </select>
                          <input value={u.host} onChange={e => setLocUp(i, k, 'host', e.target.value)} placeholder="10.0.0.5"
                            className={`${CONTROL} flex-1 min-w-[90px]`} />
                          <span className="text-content-muted">:</span>
                          <input value={u.port} onChange={e => setLocUp(i, k, 'port', e.target.value)} placeholder="80"
                            className={`${CONTROL} w-16`} />
                          <IconBtn variant="dangerGhost" size="xs" onClick={() => setLoc(i, 'upstreams', l.upstreams.filter((_, m) => m !== k))} title="Remove upstream"
                             disabled={l.upstreams.length <= 1}>🗑</IconBtn>
                        </div>
                      ))}
                      <button onClick={() => setLoc(i, 'upstreams', [...(l.upstreams || []), blankUpstream()])} className="text-xs text-accent-text hover:text-accent-text-hover">＋ Add upstream</button>
                    </div>
                  </div>
                ))}
                <button onClick={() => set('locations', [...(f.locations || []), blankLocation()])}
                  className="text-xs text-accent-text hover:text-accent-text-hover">＋ Add location</button>
                {(!f.locations || f.locations.length === 0) && <Hint>No custom locations — all traffic goes to the upstreams on the Basics tab.</Hint>}
              </div>
            </div>
          )}

          {/* ── Certs & SSL ───────────────────────────────────────────────── */}
          {activeTab === 'certs' && (
          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <div><Label>Certificate</Label><Select value={f.tls_mode} onChange={v => set('tls_mode', v)} options={TLS_OPTIONS} /></div>
              {f.tls_mode === 'existing' && (
                <div><Label>Choose certificate</Label>
                  <Select value={f.tls_cert_ref} onChange={v => set('tls_cert_ref', v)} options={[{ value: '', label: '(any matching stored cert)' }, ...certs.map(c => ({ value: c.ref, label: c.label }))]} />
                </div>
              )}
              {f.tls_mode === 'custom' && (
                <div className="col-span-2 grid grid-cols-1 gap-2">
                  <textarea value={f.tls_cert_pem || ''} onChange={e => set('tls_cert_pem', e.target.value)} placeholder="-----BEGIN CERTIFICATE-----" rows={3}
                    className={`${CONTROL} w-full font-mono`} />
                  <textarea value={f.tls_key || ''} onChange={e => set('tls_key', e.target.value)} placeholder={isEdit ? 'private key (leave blank to keep)' : '-----BEGIN PRIVATE KEY-----'} rows={3}
                    className={`${CONTROL} w-full font-mono`} />
                </div>
              )}
            </div>
            {tlsOn && (
              <div className="flex gap-6">
                <Toggle checked={!!f.force_https} onChange={v => set('force_https', v)} label="Force HTTPS" />
                <Toggle checked={f.hsts_seconds > 0} onChange={v => set('hsts_seconds', v ? 31536000 : 0)} label="HSTS" />
              </div>
            )}
            {(f.tls_mode === 'le-http' || f.tls_mode === 'le-dns') && (
              <div className="space-y-2 rounded-lg bg-surface-raised/40 border border-border-strong/50 px-3 py-2">
                <div className="text-xs text-content-subtle">
                  ACME email: <span className="text-content font-mono">{inheritedEmail}</span> <span className="text-content-faint">· inherited from Rigger settings</span>
                </div>
                <div>
                  <Label>Override (optional)</Label>
                  <Input value={f.acme_email} onChange={v => set('acme_email', v)} placeholder="leave blank to inherit" />
                  <Hint tone="faint" className="text-[11px] mt-1">An override issues the cert under this email via DNS-01 (needs a Cloudflare DNS token).</Hint>
                </div>
                <label className="flex items-start gap-2 cursor-pointer text-xs text-content pt-1">
                  <input type="checkbox" checked={!!f.accept_tos} onChange={e => set('accept_tos', e.target.checked)} className="mt-0.5" />
                  <span>I agree to the <a href="https://letsencrypt.org/repository/" target="_blank" rel="noreferrer" className="text-accent-text underline">Let's Encrypt Terms of Service</a>.</span>
                </label>
              </div>
            )}
            <Hint tone="faint" className="text-[11px]">✓ Reuse serves a stored cert matching the host (nothing re-issued). HTTP/2 and WebSocket are automatic.</Hint>
          </div>
          )}

          {/* ── Advanced ──────────────────────────────────────────────────── */}
          {activeTab === 'advanced' && !isRedirect && (
            <div className="space-y-4">
              <div className="flex flex-col gap-2">
                <Toggle checked={!!f.pass_host_header} onChange={v => set('pass_host_header', v)} label="Pass host header" />
                {f.path_prefix && <Toggle checked={!!f.strip_prefix} onChange={v => set('strip_prefix', v)} label="Strip path prefix" />}
                <Toggle checked={!!f.insecure_skip_verify} onChange={v => set('insecure_skip_verify', v)} label="Allow self-signed upstream" />
              </div>
              <div className="border-t border-border pt-3">
                <div className="flex items-center gap-2 mb-2">
                  <span className="text-xs font-semibold text-content-muted uppercase tracking-wider">Plugins</span>
                  <span title="WAF and asset cache are Traefik plugins. Enable them instance-wide on the Proxy Service page first; then attach them per route here."
                    className="text-content-faint cursor-help text-xs">ⓘ</span>
                </div>
                <div className="space-y-1.5">
                  {plugins?.waf_enabled
                    ? <Toggle checked={!!f.waf} onChange={v => set('waf', v)} label="Web application firewall (WAF)" />
                    : <Hint tone="faint">WAF — enable the plugin on the Proxy Service page first to use it here.</Hint>}
                  {plugins?.cache_enabled
                    ? <Toggle checked={!!f.cache} onChange={v => set('cache', v)} label="Cache assets" />
                    : <Hint tone="faint">{plugins?.cache_supported === false
                        ? 'Cache assets — unavailable (Souin is incompatible with Traefik’s plugin interpreter). Use a CDN or cache sidecar instead.'
                        : 'Cache assets — enable the plugin on the Proxy Service page first to use it here.'}</Hint>}
                </div>
              </div>
            </div>
          )}

          {err && (
            <details open className="text-sm bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">
              <summary className="cursor-pointer text-danger-fg font-medium select-none">Couldn’t save route — details</summary>
              <pre className="mt-2 whitespace-pre-wrap break-words text-xs text-danger-fg/90 font-mono">{err}</pre>
            </details>
          )}
        </div>
        <div className="flex items-center justify-between gap-3 px-5 py-3 border-t border-border">
          <div className="flex items-center gap-2 min-w-0 flex-1">
            {!isRedirect && <Btn size="xs" variant="ghost" onClick={runTest} disabled={test?.loading}>{test?.loading ? 'Testing…' : '⚡ Test upstream'}</Btn>}
            {test && <TestToast test={test} onClose={() => showTest(null)} />}
          </div>
          <div className="flex gap-2 shrink-0">
            <Btn size="xs" variant="secondary" onClick={onClose}>Cancel</Btn>
            <Btn size="xs" onClick={trySave} disabled={save.isPending}>{save.isPending ? 'Saving…' : 'Save route'}</Btn>
          </div>
        </div>
      </div>
    </div>
  )
}
