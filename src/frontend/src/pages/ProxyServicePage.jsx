import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import {
  fetchProxyRoutes, createProxyRoute, updateProxyRoute, deleteProxyRoute,
  testProxyRoute, fetchProxyCerts, fetchProxyPlugins, updateProxyPlugins,
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
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle text-sm focus:outline-none focus:border-brand-500 disabled:opacity-50" />
  )
}
function Select({ value, onChange, options, disabled }) {
  return (
    <select value={value} onChange={e => onChange(e.target.value)} disabled={disabled}
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 disabled:opacity-50">
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
function Btn({ onClick, disabled, variant = 'primary', children, type = 'button' }) {
  const variants = {
    primary: 'bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50',
    secondary: 'bg-surface-overlay text-content disabled:opacity-50',
    danger: 'bg-danger-subtle/60 hover:bg-danger/20 text-danger-fg disabled:opacity-50',
    ghost: 'text-content-muted hover:text-content-strong hover:bg-surface-raised',
  }
  return <button type={type} onClick={onClick} disabled={disabled} className={`font-semibold rounded-lg px-3 py-1.5 text-xs transition-colors ${variants[variant]}`}>{children}</button>
}

const TLS_OPTIONS = [
  { value: 'le-http', label: "Let's Encrypt (HTTP-01)" },
  { value: 'le-dns', label: "Let's Encrypt (DNS wildcard) — reuse" },
  { value: 'existing', label: 'Existing certificate — reuse' },
  { value: 'custom', label: 'Custom upload' },
  { value: 'none', label: 'None (HTTP only)' },
]
const DEFAULT_MODES = [
  { value: 'page', label: 'Friendly “not reachable” page' },
  { value: '404', label: 'Return 404 Not Found' },
  { value: '403', label: 'Return 403 Forbidden' },
  { value: 'close', label: 'Close connection (hide existence)' },
  { value: 'redirect', label: 'Redirect to a URL' },
  { value: 'proxy', label: 'Proxy to a default site' },
]

function blankRoute() {
  return {
    name: '', type: 'proxy', host: '', path_prefix: '',
    upstreams: [{ scheme: 'http', host: '', port: '' }],
    pass_host_header: true, insecure_skip_verify: false,
    redirect_to: '', redirect_code: 301,
    tls_mode: 'le-http', tls_cert_ref: '', acme_email: '',
    force_https: true, hsts_seconds: 0,
    auth_mode: 'none', auth_users: [], ip_allow: '', security_headers: false,
    strip_prefix: false, waf: false, cache: false, notes: '',
  }
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
      <div className="max-w-4xl mx-auto px-4 py-6 space-y-5">
        <div className="flex items-start justify-between gap-3">
          <div>
            <h1 className="text-lg font-semibold text-content-strong">Proxy service</h1>
            <p className="text-sm text-content-subtle mt-0.5">Route public hostnames to any service — on Rigger, your LAN, or a remote host.</p>
          </div>
          <Btn onClick={() => setModal('new')}>＋ Add route</Btn>
        </div>

        <div className="flex items-start gap-2 bg-info-subtle/40 border border-info-border/50 rounded-lg px-3 py-2 text-xs text-info-fg">
          <span>ⓘ</span>
          <span>Requests reach these routes only when Rigger’s Traefik receives them on ports 80/443 — either as your edge, or forwarded from your existing proxy.</span>
        </div>

        <PluginsCard plugins={plugins} />

        {isLoading ? (
          <p className="text-sm text-content-subtle py-8 text-center">Loading…</p>
        ) : realRoutes.length === 0 ? (
          <div className="bg-surface border border-border rounded-xl p-10 text-center">
            <p className="text-content font-medium mb-1">No proxy routes yet</p>
            <p className="text-sm text-content-subtle mb-4">Add a route to send a hostname to a service anywhere on your network.</p>
            <Btn onClick={() => setModal('new')}>＋ Add first route</Btn>
          </div>
        ) : (
          <div className="bg-surface border border-border rounded-xl divide-y divide-border">
            {realRoutes.map(r => (
              <RouteRow key={r.id} r={r} plugins={plugins}
                onToggle={() => toggleMut.mutate(r)}
                onEdit={() => setModal({ editing: r })}
                onDelete={() => setDeleting(r)} />
            ))}
          </div>
        )}

        <DefaultRouteCard route={defaultRoute} />
      </div>

      {modal && (
        <RouteModal plugins={plugins}
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
              <Btn variant="secondary" onClick={() => setDeleting(null)}>Cancel</Btn>
              <Btn variant="danger" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}>{delMut.isPending ? 'Deleting…' : 'Delete'}</Btn>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}

function RouteRow({ r, plugins, onToggle, onEdit, onDelete }) {
  const dot = !r.enabled ? 'bg-content-faint' : 'bg-success-fg'
  const tls = tlsBadge(r.tls_mode)
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
          {r.auth_mode === 'basic' && <Badge cls="bg-brand-600/15 text-brand-300">Basic auth</Badge>}
          {r.hsts_seconds > 0 && <Badge>HSTS</Badge>}
          {r.ip_allow && <Badge>IP allow</Badge>}
          {r.waf && plugins?.waf_enabled && <Badge cls="bg-brand-600/15 text-brand-300">WAF</Badge>}
          {r.cache && plugins?.cache_enabled && <Badge cls="bg-warning-subtle/50 text-warning-fg">Cache</Badge>}
        </div>
        <div className="text-xs text-content-subtle mt-0.5 font-mono truncate">
          {r.host || '(any host)'}{r.path_prefix ? r.path_prefix : ''} → {upstream || '—'}
        </div>
      </div>
      <div className="flex items-center gap-1.5 shrink-0">
        <Toggle checked={r.enabled} onChange={onToggle} label="" />
        <Btn variant="ghost" onClick={onEdit}>Edit</Btn>
        <Btn variant="ghost" onClick={onDelete}>✕</Btn>
      </div>
    </div>
  )
}

function PluginsCard({ plugins }) {
  const qc = useQueryClient()
  const mut = useMutation({
    mutationFn: (body) => updateProxyPlugins(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['proxy-plugins'] }); qc.invalidateQueries({ queryKey: ['proxy-routes'] }) },
  })
  if (!plugins) return null
  return (
    <div className="bg-surface border border-border rounded-xl p-4">
      <div className="flex items-center gap-2 mb-1">
        <span className="text-sm font-semibold text-content-strong">Plugins</span>
        <span className="text-[11px] text-content-muted bg-surface-overlay/50 px-1.5 py-0.5 rounded">advanced</span>
        <span title="Enabling installs the plugin into Traefik (a one-time declaration in docker-compose + rebuild) and may restart the proxy once. Per-route toggling afterwards is instant."
          className="text-content-faint cursor-help text-xs">ⓘ</span>
      </div>
      <p className="text-xs text-content-subtle mb-3">Optional WAF and asset cache, attachable per route once enabled. Requires the matching Traefik plugin to be declared in docker-compose (see proxy-service docs).</p>
      <div className="flex gap-6">
        <Toggle checked={!!plugins.waf_enabled} onChange={v => mut.mutate({ waf_enabled: v })} label="Web application firewall (Coraza)" />
        <Toggle checked={!!plugins.cache_enabled} onChange={v => mut.mutate({ cache_enabled: v })} label="Cache assets (Souin)" />
      </div>
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
      <p className="text-xs text-content-subtle mb-3">What a request gets when its host matches no project URL and no proxy route. Project URLs and configured routes always take priority.</p>
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
        <Btn onClick={() => mut.mutate()} disabled={mut.isPending}>{mut.isPending ? 'Saving…' : 'Save default'}</Btn>
        {saved && <span className="text-xs text-success-fg">✓ Saved</span>}
      </div>
    </div>
  )
}

function RouteModal({ initial, plugins, onClose, onSaved }) {
  const isEdit = !!initial
  const [f, setF] = useState(() => {
    if (!initial) return blankRoute()
    return { ...blankRoute(), ...initial, upstreams: initial.upstreams?.length ? initial.upstreams : [{ scheme: 'http', host: '', port: '' }], auth_users: (initial.auth_users || []).map(u => ({ user: u.user, password: '' })) }
  })
  const [err, setErr] = useState('')
  const [test, setTest] = useState(null)
  const { data: certs = [] } = useQuery({ queryKey: ['proxy-certs'], queryFn: fetchProxyCerts })
  const set = (k, v) => setF(s => ({ ...s, [k]: v }))
  const setUp = (i, k, v) => setF(s => ({ ...s, upstreams: s.upstreams.map((u, j) => j === i ? { ...u, [k]: v } : u) }))

  const save = useMutation({
    mutationFn: () => {
      const body = {
        ...f,
        redirect_code: Number(f.redirect_code) || 301,
        hsts_seconds: f.hsts_seconds > 0 ? Number(f.hsts_seconds) : 0,
        upstreams: f.upstreams.filter(u => u.host).map(u => ({ scheme: u.scheme, host: u.host, port: Number(u.port) || 0 })),
        auth_users: f.auth_mode === 'basic' ? f.auth_users.filter(u => u.user) : [],
      }
      return isEdit ? updateProxyRoute(initial.id, body) : createProxyRoute(body)
    },
    onSuccess: onSaved,
    onError: (e) => setErr(e?.response?.data?.error || 'Save failed'),
  })

  async function runTest() {
    setTest({ loading: true })
    try {
      const res = await testProxyRoute(isEdit ? initial.id : null, { upstreams: f.upstreams.filter(u => u.host).map(u => ({ scheme: u.scheme, host: u.host, port: Number(u.port) || 0 })) })
      setTest({ results: res.results || [] })
    } catch (e) { setTest({ error: e?.response?.data?.error || 'Test failed' }) }
  }

  const isRedirect = f.type === 'redirect'
  const tlsOn = f.tls_mode !== 'none'

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-4 overflow-y-auto" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-xl my-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-3 border-b border-border">
          <span className="font-semibold text-content-strong text-sm">{isEdit ? `Edit route — ${initial.name}` : 'Add route'}</span>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-lg leading-none">✕</button>
        </div>
        <div className="px-5 py-4 space-y-4">
          <div className="grid grid-cols-2 gap-3 items-end">
            <div><Label>Name</Label><Input value={f.name} onChange={v => set('name', v)} placeholder="Jellyfin" /></div>
            <div>
              <Label>Type</Label>
              <Select value={f.type} onChange={v => set('type', v)} options={[{ value: 'proxy', label: 'Proxy' }, { value: 'redirect', label: 'Redirect' }]} />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div><Label>Domain (host)</Label><Input value={f.host} onChange={v => set('host', v)} placeholder="media.example.com" /></div>
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
                    <select value={u.scheme} onChange={e => setUp(i, 'scheme', e.target.value)} className="px-2 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm">
                      <option value="http">http</option><option value="https">https</option>
                    </select>
                    <input value={u.host} onChange={e => setUp(i, 'host', e.target.value)} placeholder="192.168.1.50"
                      className="flex-1 px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500" />
                    <span className="text-content-muted">:</span>
                    <input value={u.port} onChange={e => setUp(i, 'port', e.target.value)} placeholder="8096"
                      className="w-20 px-2 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500" />
                    <button onClick={() => set('upstreams', f.upstreams.filter((_, j) => j !== i))} title="Remove"
                      className="text-content-faint hover:text-danger-fg px-1.5">🗑</button>
                  </div>
                ))}
                <button onClick={() => set('upstreams', [...f.upstreams, { scheme: 'http', host: '', port: '' }])}
                  className="text-xs text-brand-400 hover:text-brand-300">＋ Add upstream</button>
              </div>
            </div>
          )}

          {/* TLS */}
          <div className="border-t border-border pt-3 space-y-3">
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
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-xs font-mono focus:outline-none focus:border-brand-500" />
                  <textarea value={f.tls_key || ''} onChange={e => set('tls_key', e.target.value)} placeholder={isEdit ? 'private key (leave blank to keep)' : '-----BEGIN PRIVATE KEY-----'} rows={3}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-xs font-mono focus:outline-none focus:border-brand-500" />
                </div>
              )}
            </div>
            {tlsOn && (
              <div className="flex gap-6">
                <Toggle checked={!!f.force_https} onChange={v => set('force_https', v)} label="Force HTTPS" />
                <Toggle checked={f.hsts_seconds > 0} onChange={v => set('hsts_seconds', v ? 31536000 : 0)} label="HSTS" />
              </div>
            )}
            <p className="text-[11px] text-content-faint">✓ Reuse serves a stored cert matching the host (nothing re-issued). HTTP/2 and WebSocket are automatic. ACME email is inherited from Rigger settings.</p>
          </div>

          {/* Access */}
          {!isRedirect && (
            <div className="border-t border-border pt-3 space-y-3">
              <div className="grid grid-cols-2 gap-3">
                <div><Label>Authentication</Label><Select value={f.auth_mode} onChange={v => set('auth_mode', v)} options={[{ value: 'none', label: 'None' }, { value: 'basic', label: 'Basic auth' }]} /></div>
                <div><Label>IP allowlist (CIDR)</Label><Input value={f.ip_allow} onChange={v => set('ip_allow', v)} placeholder="192.168.0.0/16" /></div>
              </div>
              {f.auth_mode === 'basic' && (
                <div className="space-y-2">
                  {f.auth_users.map((u, i) => (
                    <div key={i} className="flex items-center gap-2">
                      <input value={u.user} onChange={e => set('auth_users', f.auth_users.map((x, j) => j === i ? { ...x, user: e.target.value } : x))} placeholder="username"
                        className="flex-1 px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm" />
                      <input type="password" value={u.password} onChange={e => set('auth_users', f.auth_users.map((x, j) => j === i ? { ...x, password: e.target.value } : x))} placeholder={isEdit ? '(unchanged)' : 'password'}
                        className="flex-1 px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm" />
                      <button onClick={() => set('auth_users', f.auth_users.filter((_, j) => j !== i))} className="text-content-faint hover:text-danger-fg px-1.5">🗑</button>
                    </div>
                  ))}
                  <button onClick={() => set('auth_users', [...f.auth_users, { user: '', password: '' }])} className="text-xs text-brand-400 hover:text-brand-300">＋ Add user</button>
                </div>
              )}
              <Toggle checked={!!f.security_headers} onChange={v => set('security_headers', v)} label="Security headers (block common exploits, lite)" />
              {(plugins?.waf_enabled || plugins?.cache_enabled) && (
                <div className="flex gap-6">
                  {plugins?.waf_enabled && <Toggle checked={!!f.waf} onChange={v => set('waf', v)} label="WAF" />}
                  {plugins?.cache_enabled && <Toggle checked={!!f.cache} onChange={v => set('cache', v)} label="Cache assets" />}
                </div>
              )}
            </div>
          )}

          {/* Advanced */}
          {!isRedirect && (
            <details className="border-t border-border pt-3">
              <summary className="text-xs font-semibold text-content-muted cursor-pointer">Advanced</summary>
              <div className="flex flex-col gap-2 mt-2">
                <Toggle checked={!!f.pass_host_header} onChange={v => set('pass_host_header', v)} label="Pass host header" />
                {f.path_prefix && <Toggle checked={!!f.strip_prefix} onChange={v => set('strip_prefix', v)} label="Strip path prefix" />}
                <Toggle checked={!!f.insecure_skip_verify} onChange={v => set('insecure_skip_verify', v)} label="Allow self-signed upstream" />
              </div>
            </details>
          )}

          {test?.results && (
            <div className="text-xs space-y-1">
              {test.results.map((t, i) => (
                <div key={i} className={t.ok ? 'text-success-fg' : 'text-danger-fg'}>{t.ok ? `✓ ${t.target} (${t.latency_ms}ms)` : `✕ ${t.target} — ${t.error}`}</div>
              ))}
            </div>
          )}
          {test?.error && <p className="text-xs text-danger-fg">{test.error}</p>}
          {err && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{err}</p>}
        </div>
        <div className="flex items-center justify-between px-5 py-3 border-t border-border">
          {!isRedirect ? <Btn variant="ghost" onClick={runTest} disabled={test?.loading}>{test?.loading ? 'Testing…' : '⚡ Test upstream'}</Btn> : <span />}
          <div className="flex gap-2">
            <Btn variant="secondary" onClick={onClose}>Cancel</Btn>
            <Btn onClick={() => { setErr(''); save.mutate() }} disabled={save.isPending}>{save.isPending ? 'Saving…' : 'Save route'}</Btn>
          </div>
        </div>
      </div>
    </div>
  )
}
