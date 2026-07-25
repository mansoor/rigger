import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchDatabaseInfo, fetchDatabaseSchemas, createDatabaseSchema, deleteDatabaseSchema, fetchDatabaseUsers, createDatabaseUser, adminerLoginHTML } from '../lib/api'
import { Hint, Btn, CloseBtn, CONTROL } from './ui'

// humanBytes renders a byte count compactly (e.g. 42 MB).
export function humanBytes(n) {
  if (!n || n < 1024) return `${n || 0} B`
  const u = ['KB', 'MB', 'GB', 'TB']
  let v = n / 1024, i = 0
  while (v >= 1024 && i < u.length - 1) { v /= 1024; i++ }
  return `${v >= 10 ? Math.round(v) : v.toFixed(1)} ${u[i]}`
}

// DatabasePanel — connection details + safe management for an environment's managed
// database: engine + version, internal address, external address (when published),
// masked credentials (per-value reveal for operator+), ready-to-paste connection URIs,
// and the raw connection env keys, plus a Manage subtab (schemas/databases + users).
// Rendered as one tab inside the Managed Service Console (and standalone via
// DatabaseInfoModal). Secrets use SecretValue (own reveal + copy-real); `showAll` lets
// the parent's "Reveal all" flip every value at once.

export function CopyBtn({ value }) {
  const [done, setDone] = useState(false)
  if (!value) return null
  return (
    <Btn variant="secondary" size="xs" title="Copy" onClick={async () => { try { await navigator.clipboard.writeText(value); setDone(true); setTimeout(() => setDone(false), 1200) } catch { /* clipboard unavailable */ } }}
      className="shrink-0">
      {done ? '✓' : '⧉'}
    </Btn>
  )
}

// SecretValue renders a sensitive value masked by default, with its OWN reveal (👁) toggle
// and a copy button that ALWAYS copies the real value (not the mask). When the caller isn't
// allowed to reveal (canReveal=false) the server already sent a masked placeholder, so it's
// shown read-only with no reveal/copy. `showAll` lets a parent flip every value visible at once.
export function SecretValue({ value, canReveal = false, showAll = false }) {
  const [show, setShow] = useState(false)
  const visible = show || showAll
  if (value === undefined || value === null || value === '') return <span className="text-content-faint">—</span>
  if (!canReveal) {
    // value is already a masked placeholder from the server — nothing real to reveal or copy.
    return <code className="flex-1 break-all font-mono text-content-strong select-all">{value}</code>
  }
  return (
    <>
      <code className="flex-1 break-all font-mono text-content-strong select-all">{visible ? value : '••••••••'}</code>
      <Btn variant="secondary" size="xs" onClick={() => setShow(s => !s)} title={visible ? 'Hide' : 'Reveal'}
        className="shrink-0">
        {visible ? '🙈' : '👁'}
      </Btn>
      <CopyBtn value={value} />
    </>
  )
}

export function Row({ label, value, mono = true }) {
  return (
    <div className="flex items-center gap-2 text-xs">
      <span className="w-32 shrink-0 text-content-subtle">{label}</span>
      <code className={`flex-1 break-all text-content-strong ${mono ? 'font-mono' : ''} select-all`}>{value || <span className="text-content-faint">—</span>}</code>
      <CopyBtn value={value} />
    </div>
  )
}

// ConnectBtn opens an Adminer auto-login window for the given identity. webSqlEnabled
// reflects whether the project has an Adminer web-SQL service; adminerUrl is the
// deployed web-entry URL (Adminer). Hidden/disabled with a hint otherwise.
function ConnectBtn({ openAdminer, webSqlEnabled, adminerUrl, as = 'admin', label = 'Connect to database', compact = false }) {
  if (!webSqlEnabled) {
    return compact ? null : <Hint tone="faint" className="text-[11px]">Enable <strong>Adminer</strong> in Edit Project → Services to use the web SQL console.</Hint>
  }
  if (!adminerUrl) {
    return compact ? null : <Hint tone="faint" className="text-[11px]">Deploy this environment to get a web SQL console URL.</Hint>
  }
  return (
    <button type="button" onClick={() => openAdminer(as)}
      className={`inline-flex items-center gap-1.5 rounded-lg font-semibold text-white bg-brand-600 hover:bg-brand-700 ${compact ? 'px-2 py-0.5 text-[11px]' : 'px-3 py-1.5 text-xs'}`}>
      ⛁ {label}
    </button>
  )
}

// DatabasePanel renders the database tab body. info is fetched here (keyed on reveal);
// onMeta lets a parent (the console header) show the engine/version badge.
export function DatabasePanel({ workspace, name, env, canReveal = false, showAll = false, canManage = false, webSqlEnabled = false, adminerUrl = '', mongoExpressEnabled = false, mongoExpressUrl = '', onMeta }) {
  const [tab, setTab] = useState('connection')
  // For operator+ (canReveal) fetch the REAL secret values up front and mask them per-value
  // client-side (SecretValue), so Copy works without first revealing — same model as the
  // service-console tabs. Viewers fetch masked placeholders.
  const { data: info, isLoading } = useQuery({
    queryKey: ['db-info', workspace, name, env, canReveal],
    queryFn: () => fetchDatabaseInfo(workspace, name, env, canReveal),
  })
  const secretKeys = new Set(info?.secret_keys || [])
  const has = info && info.engine && info.engine !== 'none'
  // Report engine/version to the parent header AFTER render (never call a parent's
  // setState during render — that triggers an infinite update loop and crashes the tree).
  useEffect(() => {
    if (onMeta && has) onMeta({ label: info.label, version: info.version })
  }, [onMeta, has, info?.label, info?.version])
  const tabCls = (t) => `px-3 py-1.5 text-xs font-medium border-b-2 transition-colors ${tab === t ? 'border-brand-500 text-content-strong' : 'border-transparent text-content-subtle hover:text-content'}`

  // Open Adminer auto-logged-in as `as` ('admin' or a username). The window is opened
  // synchronously (avoids popup blocking); the authenticated HTML (a self-submitting
  // POST form) is then written into it so the JWT stays in the request header.
  function openAdminer(as) {
    if (!adminerUrl) return
    const wnd = window.open('', '_blank')
    if (wnd) { try { wnd.document.write('<p style="font:14px system-ui;padding:2rem;color:#888">Connecting…</p>') } catch { /* */ } }
    adminerLoginHTML(workspace, name, env, as, adminerUrl)
      .then(htmlText => { if (wnd) { wnd.document.open(); wnd.document.write(htmlText); wnd.document.close() } })
      .catch(() => { if (wnd) { try { wnd.document.body.innerHTML = '<p style="font:14px system-ui;padding:2rem;color:#c00">Failed to open the SQL console.</p>' } catch { /* */ } } })
  }

  return (
    <>
      {has && (info.schemas || info.users) && (
        <div className="-mt-2 mb-4 -mx-5 px-3 border-b border-border flex items-center gap-1">
          <button className={tabCls('connection')} onClick={() => setTab('connection')}>Connection</button>
          <button className={tabCls('manage')} onClick={() => setTab('manage')}>Manage</button>
        </div>
      )}

      {isLoading ? (
        <p className="text-xs text-content-subtle">Loading…</p>
      ) : !has ? (
        <p className="text-xs text-content-subtle">No managed database configured for this environment.</p>
      ) : tab === 'manage' ? (
        <ManageTab workspace={workspace} name={name} env={env} info={info} canManage={canManage}
          openAdminer={openAdminer} webSqlEnabled={webSqlEnabled} adminerUrl={adminerUrl} />
      ) : (
        <div className="space-y-5">
          {info.note && <Hint className="leading-relaxed">{info.note}</Hint>}
          {/* One-click web console — Adminer (SQL, auto-login) or mongo-express (MongoDB). */}
          {canManage && (info.engine === 'mongodb' ? (
            <section className="flex items-center justify-between gap-3 rounded-lg border border-border-strong bg-surface-raised/40 px-3 py-2">
              <Hint>Open the mongo-express web console connected to this database.</Hint>
              {!mongoExpressEnabled
                ? <Hint tone="faint" className="text-[11px]">Enable the <strong>web console</strong> in Edit Project → Services.</Hint>
                : !mongoExpressUrl
                  ? <Hint tone="faint" className="text-[11px]">Deploy with a domain to get a web URL.</Hint>
                  : <a href={mongoExpressUrl} target="_blank" rel="noreferrer"
                      className="inline-flex items-center gap-1.5 rounded-lg font-semibold text-white bg-brand-600 hover:bg-brand-700 px-3 py-1.5 text-xs shrink-0">↗ Open mongo-express</a>}
            </section>
          ) : (
            <section className="flex items-center justify-between gap-3 rounded-lg border border-border-strong bg-surface-raised/40 px-3 py-2">
              <Hint>Open a browser SQL console connected to this database.</Hint>
              <ConnectBtn openAdminer={openAdminer} webSqlEnabled={webSqlEnabled} adminerUrl={adminerUrl} as="admin" />
            </section>
          ))}

          {/* In-network connection */}
          <section className="space-y-1.5">
            <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">In-network (from other services)</h3>
            <Row label="Host" value={info.internal_host} />
            <Row label="Port" value={String(info.port)} />
            <Row label="Database" value={info.database} />
            <Row label="Username" value={info.username} />
            <div className="flex items-center gap-2 text-xs">
              <span className="w-32 shrink-0 text-content-subtle">Password</span>
              {info.has_password ? (
                <SecretValue value={info.password !== undefined ? info.password : '••••••••'} canReveal={canReveal} showAll={showAll} />
              ) : (
                <code className="flex-1 break-all font-mono text-content-faint select-all">(none)</code>
              )}
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
              <Hint>Not published. Enable “external access” on this database in Edit Project to expose a host port for outside clients.</Hint>
            )}
          </section>

          {/* Connection URIs */}
          <section className="space-y-1.5">
            <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Connection URIs</h3>
            {(info.connections || []).map((c, i) => (
              <div key={i} className="flex items-center gap-2 text-xs">
                <span className="w-32 shrink-0 text-content-subtle">{c.label}</span>
                {c.secret ? (
                  <SecretValue value={c.value} canReveal={canReveal} showAll={showAll} />
                ) : (
                  <>
                    <code className="flex-1 break-all font-mono text-content-strong select-all">{c.value}</code>
                    <CopyBtn value={c.value} />
                  </>
                )}
              </div>
            ))}
          </section>

          {/* Raw env keys */}
          <section className="space-y-1.5">
            <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Connection env vars</h3>
            <div className="rounded-lg border border-border-strong bg-surface-raised/40 p-3 space-y-1">
              {Object.entries(info.env_keys || {}).map(([k, v]) => (
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
        </div>
      )}
    </>
  )
}

// DatabaseInfoModal — standalone modal wrapper around DatabasePanel (kept for any
// direct callers; the env card now opens the tabbed ServiceConsoleModal instead).
export default function DatabaseInfoModal({ workspace, name, env, canReveal = false, canManage = false, webSqlEnabled = false, adminerUrl = '', onClose }) {
  const [showAll, setShowAll] = useState(false)
  const [meta, setMeta] = useState(null)
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div className="w-full max-w-2xl max-h-[85vh] flex flex-col bg-surface border border-border rounded-xl shadow-xl" onClick={e => e.stopPropagation()}>
        <div className="px-5 py-3 border-b border-border flex items-center justify-between gap-3">
          <div className="flex items-center gap-2 text-sm">
            <span className="text-base">🗄</span>
            <span className="font-semibold text-content-strong">Database</span>
            {meta && <span className="px-1.5 py-0.5 rounded border border-border-strong bg-surface-raised text-xs text-content">{meta.label} {meta.version}</span>}
            <span className="text-xs text-content-subtle">· {env}</span>
          </div>
          <div className="flex items-center gap-3">
            {canReveal && (
              <Btn variant="secondary" size="xs" onClick={() => setShowAll(v => !v)}
                >
                {showAll ? 'Hide all secrets' : 'Reveal all secrets'}
              </Btn>
            )}
            <CloseBtn onClick={onClose} />
          </div>
        </div>
        <div className="flex-1 overflow-y-auto px-5 py-4">
          <DatabasePanel workspace={workspace} name={name} env={env} showAll={showAll}
            canReveal={canReveal} canManage={canManage} webSqlEnabled={webSqlEnabled} adminerUrl={adminerUrl} onMeta={setMeta} />
        </div>
      </div>
    </div>
  )
}

// PwCell renders a managed user's password masked, with reveal + copy.
function PwCell({ password }) {
  const [show, setShow] = useState(false)
  if (!password) return <span className="text-content-faint">—</span>
  return (
    <span className="inline-flex items-center gap-1">
      <code className="font-mono text-content select-all">{show ? password : '••••••••'}</code>
      <button type="button" onClick={() => setShow(s => !s)} title={show ? 'Hide' : 'Reveal'}
        className="px-1 rounded bg-surface-raised hover:bg-surface-overlay text-content-subtle text-[10px]">{show ? '🙈' : '👁'}</button>
      <CopyBtn value={password} />
    </span>
  )
}

const dbIdent = /^[A-Za-z_][A-Za-z0-9_]{0,62}$/

// ManageTab — schemas/databases with table count + size, each row joined to the
// Rigger-created user that owns it (user, password, one-click Adminer link). Create
// a schema (optionally with a dedicated user that gets granted on it). operator+.
function ManageTab({ workspace, name, env, info, canManage, openAdminer, webSqlEnabled, adminerUrl }) {
  const qc = useQueryClient()
  const [newName, setNewName] = useState('')
  const [withUser, setWithUser] = useState(true)
  const [newUser, setNewUser] = useState('')
  const [err, setErr] = useState('')
  const [created, setCreated] = useState(null) // { name, password } shown once
  const sKey = ['db-schemas', workspace, name, env]
  const uKey = ['db-users', workspace, name, env, canManage]
  const { data, isLoading, error } = useQuery({ queryKey: sKey, queryFn: () => fetchDatabaseSchemas(workspace, name, env) })
  const { data: usersData } = useQuery({ queryKey: uKey, queryFn: () => fetchDatabaseUsers(workspace, name, env, canManage) })
  const unit = data?.unit || 'schema'
  const users = usersData?.users || []
  const userBySchema = {}
  users.forEach(u => { if (u.schema && !userBySchema[u.schema]) userBySchema[u.schema] = u })

  const createMut = useMutation({
    mutationFn: async () => {
      const schema = newName.trim()
      await createDatabaseSchema(workspace, name, env, schema)
      if (withUser && newUser.trim()) {
        const r = await createDatabaseUser(workspace, name, env, { name: newUser.trim(), schema })
        return r
      }
      return null
    },
    onSuccess: (r) => {
      setNewName(''); setNewUser(''); setErr('')
      setCreated(r && r.name ? { name: r.name, password: r.password } : null)
      qc.invalidateQueries({ queryKey: sKey }); qc.invalidateQueries({ queryKey: uKey })
    },
    onError: (e) => setErr(e?.response?.data?.error || 'Create failed'),
  })
  const schemaValid = dbIdent.test(newName.trim())
  const userValid = !withUser || dbIdent.test(newUser.trim())

  // Delete a schema/database — requires typing the exact name to confirm.
  const [delName, setDelName] = useState('')   // schema pending delete
  const [delTyped, setDelTyped] = useState('') // what the user typed
  const [delErr, setDelErr] = useState('')
  const deleteMut = useMutation({
    mutationFn: () => deleteDatabaseSchema(workspace, name, env, delName),
    onSuccess: () => { setDelName(''); setDelTyped(''); setDelErr(''); qc.invalidateQueries({ queryKey: sKey }); qc.invalidateQueries({ queryKey: uKey }) },
    onError: (e) => setDelErr(e?.response?.data?.error || 'Delete failed'),
  })

  return (
    <div className="space-y-4">
      {canManage && (
        <div className="flex items-center justify-between gap-3 rounded-lg border border-border-strong bg-surface-raised/40 px-3 py-2">
          <Hint>Browser SQL console, auto-logged-in as the database admin.</Hint>
          <ConnectBtn openAdminer={openAdminer} webSqlEnabled={webSqlEnabled} adminerUrl={adminerUrl} as="admin" label="Connect as admin" />
        </div>
      )}

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
          <div className="rounded-lg border border-border-strong overflow-x-auto">
            <table className="w-full text-xs">
              <thead className="bg-surface-raised/60 text-content-subtle">
                <tr>
                  <th className="text-left px-3 py-1.5 font-medium">Name</th>
                  <th className="text-right px-3 py-1.5 font-medium">Tables</th>
                  <th className="text-right px-3 py-1.5 font-medium">Size</th>
                  <th className="text-left px-3 py-1.5 font-medium">User</th>
                  {canManage && <th className="text-left px-3 py-1.5 font-medium">Password</th>}
                  {webSqlEnabled && <th className="text-right px-3 py-1.5 font-medium">SQL</th>}
                  {canManage && <th className="px-3 py-1.5"></th>}
                </tr>
              </thead>
              <tbody>
                {data.schemas.map(s => {
                  const u = userBySchema[s.name]
                  return (
                    <tr key={s.name} className="border-t border-border">
                      <td className="px-3 py-1.5 font-mono text-content-strong">{s.name}</td>
                      <td className="px-3 py-1.5 text-right text-content">{s.tables}</td>
                      <td className="px-3 py-1.5 text-right text-content-subtle">{humanBytes(s.bytes)}</td>
                      <td className="px-3 py-1.5 font-mono text-content">{u ? u.username : <span className="text-content-faint">—</span>}</td>
                      {canManage && <td className="px-3 py-1.5">{u ? <PwCell password={u.password} /> : <span className="text-content-faint">—</span>}</td>}
                      {webSqlEnabled && (
                        <td className="px-3 py-1.5 text-right">
                          {u && adminerUrl
                            ? <ConnectBtn openAdminer={openAdminer} webSqlEnabled={webSqlEnabled} adminerUrl={adminerUrl} as={u.username} label="Adminer" compact />
                            : <span className="text-content-faint">—</span>}
                        </td>
                      )}
                      {canManage && (
                        <td className="px-3 py-1.5 text-right">
                          {s.name === info.database || (info.engine === 'postgres' && s.name === 'public')
                            ? <span className="text-content-faint text-[10px]" title="The primary application database can't be deleted">—</span>
                            : <Btn variant="dangerSubtle" size="xs" title={`Delete ${s.name}`} onClick={() => { setDelName(s.name); setDelTyped(''); setDelErr('') }}
                                >🗑</Btn>}
                        </td>
                      )}
                    </tr>
                  )
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {delName && (
        <section className="rounded-lg border border-danger-border bg-danger-subtle/30 p-3 space-y-2">
          <p className="text-xs text-danger-fg">
            Permanently delete <code className="font-mono">{delName}</code> and everything in it — this cannot be undone.
            Type <code className="font-mono">{delName}</code> to confirm.
          </p>
          <div className="flex items-center gap-2">
            <input value={delTyped} onChange={e => { setDelTyped(e.target.value); setDelErr('') }} placeholder={delName} autoFocus
              className="flex-1 px-3 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-sm text-content-strong font-mono focus:outline-none focus:border-danger" />
            <Btn variant="danger" size="xs" disabled={delTyped !== delName || deleteMut.isPending} onClick={() => deleteMut.mutate()}
              >
              {deleteMut.isPending ? 'Deleting…' : 'Delete'}
            </Btn>
            <Btn variant="secondary" size="xs" onClick={() => { setDelName(''); setDelTyped(''); setDelErr('') }}
              >Cancel</Btn>
          </div>
          {delErr && <p className="text-xs text-danger-fg">{delErr}</p>}
        </section>
      )}

      {canManage && (
        <section className="space-y-2 pt-1 border-t border-border">
          <h3 className="text-[11px] font-semibold uppercase tracking-wider text-content-muted">Create {unit}</h3>
          <div className="flex items-center gap-2">
            <input value={newName} onChange={e => { setNewName(e.target.value); setErr(''); if (withUser && !newUser) setNewUser(e.target.value ? `${e.target.value}_user` : '') }}
              placeholder={unit === 'database' ? 'new_database' : 'new_schema'}
              className={`${CONTROL} flex-1 font-mono`} />
            <Btn variant="primary" size="xs" disabled={!schemaValid || !userValid || createMut.isPending} onClick={() => createMut.mutate()}
              >
              {createMut.isPending ? 'Creating…' : '+ Create'}
            </Btn>
          </div>
          <label className="flex items-center gap-2 text-xs text-content-muted cursor-pointer">
            <input type="checkbox" checked={withUser} onChange={e => setWithUser(e.target.checked)} className="w-3.5 h-3.5 accent-brand-500" />
            Create a dedicated user and grant it on this {unit}
          </label>
          {withUser && (
            <input value={newUser} onChange={e => { setNewUser(e.target.value); setErr('') }}
              placeholder="username"
              className={`${CONTROL} w-full font-mono`} />
          )}
          {created && (
            <div className="rounded-lg border border-success-border/50 bg-success-subtle/30 p-2 text-xs space-y-1">
              <p className="text-success-fg">User <code className="font-mono">{created.name}</code> created. Password (shown once):</p>
              <div className="flex items-center gap-2"><code className="font-mono text-content-strong select-all break-all">{created.password}</code><CopyBtn value={created.password} /></div>
            </div>
          )}
          {err && <p className="text-xs text-danger-fg">{err}</p>}
          <Hint tone="faint" className="text-[11px]">Names: letters, digits and underscores (start with a letter or underscore). A blank password is auto-generated.</Hint>
        </section>
      )}
    </div>
  )
}
