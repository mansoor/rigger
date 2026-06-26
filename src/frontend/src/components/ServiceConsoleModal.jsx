import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchServiceConsole, fetchStorageBuckets, createStorageBucket } from '../lib/api'
import { DatabasePanel, CopyBtn, SecretValue } from './DatabaseInfoModal'

// ServiceConsoleModal — per-env Managed Service Console (P4). A tabbed view over every
// managed service enabled for the environment: the database (rich Connection/Manage
// panel, reused from DatabasePanel) plus the non-DB sidecars (Redis, object storage,
// Mailpit) with connection info, a one-click open-UI link where one exists, and — for
// MinIO — bucket list/create. One shared secret-reveal toggle (operator+) unmasks
// credentials across all tabs.

const KIND_ICON = { database: '🗄', redis: '⚡', s3: '🪣', storage_local: '📁', mailpit: '✉️' }

// uiURLFor builds a sidecar's web-UI URL from the env's apex URL by prefixing the
// service subdomain (storage./mail.). Mirrors composegen's subdomain routing. Returns
// '' when the env has no public URL (localhost / no domain) — the caller shows a hint.
function uiURLFor(apexUrl, subdomain) {
  if (!apexUrl || !subdomain) return ''
  return apexUrl.replace(/^(https?:\/\/)/, `$1${subdomain}.`)
}

export default function ServiceConsoleModal({ workspace, name, env, hasManagedDB = false,
  canReveal = false, canManage = false, webSqlEnabled = false, adminerUrl = '', apexUrl = '', onClose }) {
  const [reveal, setReveal] = useState(false)
  const [dbMeta, setDbMeta] = useState(null)
  // For operator+ (canReveal) we fetch the REAL secret values up front and mask them
  // client-side per value (so Copy works without first clicking reveal). Viewers fetch
  // masked. The header toggle (`reveal`) then just flips every value's display at once.
  const { data, isLoading } = useQuery({
    queryKey: ['svc-console', workspace, name, env, canReveal],
    queryFn: () => fetchServiceConsole(workspace, name, env, canReveal),
  })
  const services = data?.services || []

  // Tabs: database first (own endpoints), then each non-DB service from the console.
  const tabs = []
  if (hasManagedDB) tabs.push({ key: 'database', kind: 'database', label: 'Database' })
  services.forEach(s => tabs.push({ key: s.kind, kind: s.kind, label: s.label, svc: s }))

  const [active, setActive] = useState(hasManagedDB ? 'database' : null)
  // Settle the active tab once data arrives (no DB + services load late).
  const activeKey = active && tabs.some(t => t.key === active) ? active : (tabs[0]?.key || null)
  const activeTab = tabs.find(t => t.key === activeKey)

  const tabCls = (on) => `flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg whitespace-nowrap transition-colors ${on ? 'bg-brand-600 text-white' : 'bg-surface-raised text-content-subtle hover:text-content hover:bg-surface-overlay'}`

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div className="w-full max-w-2xl max-h-[85vh] flex flex-col bg-surface border border-border rounded-xl shadow-xl" onClick={e => e.stopPropagation()}>
        <div className="px-5 py-3 border-b border-border flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 text-sm">
            <span className="text-base">🧩</span>
            <span className="font-semibold text-content-strong">Managed services</span>
            {activeTab?.kind === 'database' && dbMeta && (
              <span className="px-1.5 py-0.5 rounded border border-border-strong bg-surface-raised text-xs text-content">{dbMeta.label} {dbMeta.version}</span>
            )}
            <span className="text-xs text-content-subtle">· {env}</span>
          </div>
          <div className="flex items-center gap-3">
            {canReveal && (
              <button type="button" onClick={() => setReveal(v => !v)}
                className="px-2 py-0.5 rounded bg-surface-raised hover:bg-surface-overlay text-content-subtle hover:text-content text-[11px]">
                {reveal ? 'Hide all secrets' : 'Reveal all secrets'}
              </button>
            )}
            <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-lg leading-none">✕</button>
          </div>
        </div>

        {/* Service tabs */}
        <div className="px-5 pt-3 pb-2 border-b border-border flex items-center gap-1.5 overflow-x-auto">
          {tabs.length === 0 ? (
            <span className="text-xs text-content-subtle py-1">{isLoading ? 'Loading…' : 'No managed services for this environment.'}</span>
          ) : tabs.map(t => (
            <button key={t.key} className={tabCls(t.key === activeKey)} onClick={() => setActive(t.key)}>
              <span>{KIND_ICON[t.kind] || '•'}</span>{t.label}
            </button>
          ))}
        </div>

        {/* Fixed body height so the modal stays the same size across tabs (content
            scrolls within) instead of resizing to each tab's content. */}
        <div className="h-[58vh] overflow-y-auto px-5 py-4">
          {activeTab?.kind === 'database' ? (
            <DatabasePanel workspace={workspace} name={name} env={env} showAll={reveal}
              canReveal={canReveal} canManage={canManage} webSqlEnabled={webSqlEnabled} adminerUrl={adminerUrl} onMeta={setDbMeta} />
          ) : activeTab?.svc ? (
            <ServicePanel workspace={workspace} name={name} env={env} svc={activeTab.svc}
              apexUrl={apexUrl} canManage={canManage} canReveal={canReveal} showAll={reveal} />
          ) : (
            <p className="text-xs text-content-subtle">{isLoading ? 'Loading…' : 'Nothing to show.'}</p>
          )}
        </div>
      </div>
    </div>
  )
}

// ServicePanel renders a non-DB managed service: an optional note, an open-UI button
// (built from the env apex + the service subdomain), the connection rows, the raw env
// keys, and — for S3+MinIO — a bucket manager.
function ServicePanel({ workspace, name, env, svc, apexUrl, canManage, canReveal = false, showAll = false }) {
  const uiUrl = uiURLFor(apexUrl, svc.subdomain)
  const secretKeys = new Set(svc.secret_keys || [])
  return (
    <div className="space-y-5">
      {svc.note && <p className="text-xs text-content-subtle leading-relaxed">{svc.note}</p>}

      {svc.subdomain && (
        <section className="flex items-center justify-between gap-3 rounded-lg border border-border-strong bg-surface-raised/40 px-3 py-2">
          <div className="text-xs text-content-subtle">
            {svc.kind === 'mailpit' ? 'Open the Mailpit web inbox.' : 'Open the storage console.'}
          </div>
          {uiUrl ? (
            <a href={uiUrl} target="_blank" rel="noreferrer"
              className="inline-flex items-center gap-1.5 rounded-lg font-semibold text-white bg-brand-600 hover:bg-brand-700 px-3 py-1.5 text-xs">
              ↗ Open {svc.kind === 'mailpit' ? 'inbox' : 'console'}
            </a>
          ) : (
            <span className="text-[11px] text-content-faint">Deploy with a domain to get a web URL.</span>
          )}
        </section>
      )}

      <section className="space-y-1.5">
        <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Connection</h3>
        {(svc.rows || []).map((r, i) => (
          <div key={i} className="flex items-center gap-2 text-xs">
            <span className="w-40 shrink-0 text-content-subtle">{r.label}</span>
            {r.secret ? (
              <SecretValue value={r.value} canReveal={canReveal} showAll={showAll} />
            ) : (
              <>
                <code className="flex-1 break-all font-mono text-content-strong select-all">{r.value || <span className="text-content-faint">—</span>}</code>
                <CopyBtn value={r.value} />
              </>
            )}
          </div>
        ))}
      </section>

      {svc.buckets && <BucketManager workspace={workspace} name={name} env={env} canManage={canManage} />}

      {svc.env_keys && Object.keys(svc.env_keys).length > 0 && (
        <section className="space-y-1.5">
          <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Connection env vars</h3>
          <div className="rounded-lg border border-border-strong bg-surface-raised/40 p-3 space-y-1">
            {Object.entries(svc.env_keys).map(([k, v]) => (
              <div key={k} className="flex items-center gap-2 text-[11px] font-mono">
                <span className="text-content-subtle">{k}</span>
                <span className="text-content-faint">=</span>
                {secretKeys.has(k) ? (
                  <SecretValue value={v} canReveal={canReveal} showAll={showAll} />
                ) : (
                  <span className="flex-1 break-all text-content select-all">{v}</span>
                )}
              </div>
            ))}
          </div>
        </section>
      )}
    </div>
  )
}

const bucketValid = /^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$/

// BucketManager lists MinIO buckets and (operator+) creates new ones. The list runs a
// transient mc container server-side, so it may take a moment / error when MinIO isn't
// up yet — surfaced inline rather than blocking the rest of the tab.
function BucketManager({ workspace, name, env, canManage }) {
  const qc = useQueryClient()
  const [newName, setNewName] = useState('')
  const [err, setErr] = useState('')
  const bKey = ['s3-buckets', workspace, name, env]
  const { data, isLoading, error } = useQuery({ queryKey: bKey, queryFn: () => fetchStorageBuckets(workspace, name, env), retry: false })
  const buckets = data?.buckets || []

  const createMut = useMutation({
    mutationFn: () => createStorageBucket(workspace, name, env, newName.trim().toLowerCase()),
    onSuccess: () => { setNewName(''); setErr(''); qc.invalidateQueries({ queryKey: bKey }) },
    onError: (e) => setErr(e?.response?.data?.error || 'Create failed'),
  })
  const valid = bucketValid.test(newName.trim().toLowerCase())

  return (
    <section className="space-y-2">
      <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Buckets</h3>
      {isLoading ? (
        <p className="text-xs text-content-subtle">Loading…</p>
      ) : error ? (
        <p className="text-xs text-danger-fg">{error?.response?.data?.error || 'Could not list — is MinIO running?'}</p>
      ) : buckets.length === 0 ? (
        <p className="text-xs text-content-subtle">No buckets yet.</p>
      ) : (
        <div className="rounded-lg border border-border-strong bg-surface-raised/40 p-3 space-y-1">
          {buckets.map(b => (
            <div key={b} className="flex items-center gap-2 text-xs font-mono text-content-strong">
              <span>🪣</span><span className="flex-1 break-all select-all">{b}</span><CopyBtn value={b} />
            </div>
          ))}
        </div>
      )}
      {canManage && (
        <div className="flex items-center gap-2 pt-1">
          <input value={newName} onChange={e => { setNewName(e.target.value); setErr('') }}
            placeholder="new-bucket-name"
            className="flex-1 px-3 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500" />
          <button type="button" disabled={!valid || createMut.isPending} onClick={() => createMut.mutate()}
            className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50">
            {createMut.isPending ? 'Creating…' : '+ Create'}
          </button>
        </div>
      )}
      {canManage && <p className="text-[11px] text-content-faint">Names: 3–63 chars, lowercase letters, digits, dots and hyphens (S3 naming).</p>}
      {err && <p className="text-xs text-danger-fg">{err}</p>}
    </section>
  )
}
