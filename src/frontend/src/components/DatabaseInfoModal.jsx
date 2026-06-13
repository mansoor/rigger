import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchDatabaseInfo, fetchDatabaseSchemas, createDatabaseSchema } from '../lib/api'

// humanBytes renders a byte count compactly (e.g. 42 MB).
function humanBytes(n) {
  if (!n || n < 1024) return `${n || 0} B`
  const u = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024, i = 0
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v >= 10 ? Math.round(v) : v.toFixed(1)} ${u[i]}`
}

// DatabaseInfoModal — connection details for an environment's managed database:
// engine + version, internal address, external address (when published), masked
// credentials (reveal for operator+), ready-to-paste connection URIs, and the raw
// connection env keys. Read-only; mirrors the ContainerInfoModal layout. The
// password is fetched (and shown) only when reveal is on AND the user can reveal.

function CopyBtn({ value }) {
  const [done, setDone] = useState(false)
  if (!value) return null
  return (
    <button type="button" title="Copy"
      onClick={async () => { try { await navigator.clipboard.writeText(value); setDone(true); setTimeout(() => setDone(false), 1200) } catch { /* clipboard unavailable */ } }}
      className="shrink-0 px-1.5 py-0.5 rounded bg-surface-raised hover:bg-surface-overlay text-content-subtle hover:text-content text-[10px]">
      {done ? '✓' : '⧉'}
    </button>
  )
}

function Row({ label, value, mono = true }) {
  return (
    <div className="flex items-center gap-2 text-xs">
      <span className="w-32 shrink-0 text-content-subtle">{label}</span>
      <code className={`flex-1 break-all text-content-strong ${mono ? 'font-mono' : ''} select-all`}>{value || <span className="text-content-faint">—</span>}</code>
      <CopyBtn value={value} />
    </div>
  )
}

export default function DatabaseInfoModal({ workspace, name, env, canReveal = false, canManage = false, onClose }) {
  const [reveal, setReveal] = useState(false)
  const [tab, setTab] = useState('connection')
  const { data: info, isLoading } = useQuery({
    queryKey: ['db-info', workspace, name, env, reveal],
    queryFn: () => fetchDatabaseInfo(workspace, name, env, reveal),
  })
  const has = info && info.engine && info.engine !== 'none'
  const tabCls = (t) => `px-3 py-1.5 text-xs font-medium border-b-2 transition-colors ${tab === t ? 'border-brand-500 text-content-strong' : 'border-transparent text-content-subtle hover:text-content'}`

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div className="w-full max-w-2xl max-h-[85vh] flex flex-col bg-surface border border-border rounded-xl shadow-xl" onClick={e => e.stopPropagation()}>
        <div className="px-5 py-3 border-b border-border flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 text-sm">
            <span className="text-base">🗄</span>
            <span className="font-semibold text-content-strong">Database</span>
            {has && <span className="px-1.5 py-0.5 rounded border border-border-strong bg-surface-raised text-xs text-content">{info.label} {info.version}</span>}
            <span className="text-xs text-content-subtle">· {env}</span>
          </div>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-lg leading-none">✕</button>
        </div>

        {has && (
          <div className="px-3 border-b border-border flex items-center gap-1">
            <button className={tabCls('connection')} onClick={() => setTab('connection')}>Connection</button>
            <button className={tabCls('manage')} onClick={() => setTab('manage')}>Manage</button>
          </div>
        )}

        <div className="flex-1 overflow-y-auto px-5 py-4 space-y-5">
          {isLoading ? (
            <p className="text-xs text-content-subtle">Loading…</p>
          ) : !has ? (
            <p className="text-xs text-content-subtle">No managed database configured for this environment.</p>
          ) : tab === 'manage' ? (
            <ManageTab workspace={workspace} name={name} env={env} info={info} canManage={canManage} />
          ) : (
            <>
              {/* In-network connection */}
              <section className="space-y-1.5">
                <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">In-network (from other services)</h3>
                <Row label="Host" value={info.internal_host} />
                <Row label="Port" value={String(info.port)} />
                <Row label="Database" value={info.database} />
                <Row label="Username" value={info.username} />
                <div className="flex items-center gap-2 text-xs">
                  <span className="w-32 shrink-0 text-content-subtle">Password</span>
                  <code className="flex-1 break-all font-mono text-content-strong select-all">
                    {info.revealed ? (info.password || <span className="text-content-faint">(none)</span>) : '••••••••'}
                  </code>
                  {canReveal && info.has_password && (
                    <button type="button" onClick={() => setReveal(v => !v)}
                      className="shrink-0 px-1.5 py-0.5 rounded bg-surface-raised hover:bg-surface-overlay text-content-subtle hover:text-content text-[10px]">
                      {info.revealed ? 'Hide' : 'Reveal'}
                    </button>
                  )}
                  {info.revealed && <CopyBtn value={info.password} />}
                </div>
              </section>

              {/* External access */}
              <section className="space-y-1.5">
                <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">External access</h3>
                {info.external ? (
                  <>
                    <Row label="Host" value={info.external_host} />
                    <Row label="Port" value={String(info.external_port)} />
                  </>
                ) : (
                  <p className="text-xs text-content-subtle">Not published. Enable “external access” on this database in Edit Project to expose a host port for outside clients.</p>
                )}
              </section>

              {/* Connection URIs */}
              <section className="space-y-1.5">
                <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Connection URIs</h3>
                {(info.connections || []).map((c, i) => (
                  <div key={i} className="flex items-center gap-2 text-xs">
                    <span className="w-32 shrink-0 text-content-subtle">{c.label}</span>
                    <code className="flex-1 break-all font-mono text-content-strong select-all">{c.value}</code>
                    <CopyBtn value={c.value} />
                  </div>
                ))}
                {!info.revealed && (
                  <p className="text-[11px] text-content-faint">Password masked — reveal it above to copy a ready-to-use URI.</p>
                )}
              </section>

              {/* Raw env keys */}
              <section className="space-y-1.5">
                <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Connection env vars</h3>
                <div className="rounded-lg border border-border-strong bg-surface-raised/40 p-3 space-y-1">
                  {Object.entries(info.env_keys || {}).map(([k, v]) => (
                    <div key={k} className="flex items-center gap-2 text-[11px] font-mono">
                      <span className="text-content-subtle">{k}</span>
                      <span className="text-content-faint">=</span>
                      <span className="flex-1 break-all text-content select-all">{v}</span>
                    </div>
                  ))}
                </div>
              </section>
            </>
          )}
        </div>
      </div>
    </div>
  )
}

// ManageTab — the safe management surface (Phase 6): list schemas/databases with
// table count + size, and create a new one (operator+). No ad-hoc SQL.
function ManageTab({ workspace, name, env, info, canManage }) {
  const qc = useQueryClient()
  const [newName, setNewName] = useState('')
  const [err, setErr] = useState('')
  const key = ['db-schemas', workspace, name, env]
  const { data, isLoading, error } = useQuery({ queryKey: key, queryFn: () => fetchDatabaseSchemas(workspace, name, env) })
  const unit = data?.unit || 'schema'
  const createMut = useMutation({
    mutationFn: () => createDatabaseSchema(workspace, name, env, newName.trim()),
    onSuccess: () => { setNewName(''); setErr(''); qc.invalidateQueries({ queryKey: key }) },
    onError: (e) => setErr(e?.response?.data?.error || 'Create failed'),
  })
  const valid = /^[A-Za-z_][A-Za-z0-9_]{0,62}$/.test(newName.trim())

  return (
    <div className="space-y-4">
      <section className="space-y-1.5">
        <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">
          {unit === 'database' ? 'Databases' : 'Schemas'}
        </h3>
        {isLoading ? (
          <p className="text-xs text-content-subtle">Loading…</p>
        ) : error ? (
          <p className="text-xs text-danger-fg">{error?.response?.data?.error || 'Could not list — is the database running?'}</p>
        ) : (data?.schemas || []).length === 0 ? (
          <p className="text-xs text-content-subtle">None yet.</p>
        ) : (
          <div className="rounded-lg border border-border-strong overflow-hidden">
            <table className="w-full text-xs">
              <thead className="bg-surface-raised/60 text-content-subtle">
                <tr><th className="text-left px-3 py-1.5 font-medium">Name</th><th className="text-right px-3 py-1.5 font-medium">Tables</th><th className="text-right px-3 py-1.5 font-medium">Size</th></tr>
              </thead>
              <tbody>
                {data.schemas.map(s => (
                  <tr key={s.name} className="border-t border-border">
                    <td className="px-3 py-1.5 font-mono text-content-strong">{s.name}</td>
                    <td className="px-3 py-1.5 text-right text-content">{s.tables}</td>
                    <td className="px-3 py-1.5 text-right text-content-subtle">{humanBytes(s.bytes)}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {canManage && (
        <section className="space-y-2 pt-1 border-t border-border">
          <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Create {unit}</h3>
          <div className="flex items-center gap-2">
            <input value={newName} onChange={e => { setNewName(e.target.value); setErr('') }}
              placeholder={unit === 'database' ? 'new_database' : 'new_schema'}
              className="flex-1 px-3 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500" />
            <button type="button" disabled={!valid || createMut.isPending}
              onClick={() => createMut.mutate()}
              className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50">
              {createMut.isPending ? 'Creating…' : '+ Create'}
            </button>
          </div>
          {err && <p className="text-xs text-danger-fg">{err}</p>}
          <p className="text-[11px] text-content-faint">Letters, digits and underscores (must start with a letter or underscore).</p>
        </section>
      )}
    </div>
  )
}
