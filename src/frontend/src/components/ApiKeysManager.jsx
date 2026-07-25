import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  fetchApiKeys, fetchApiKeyScopes, createApiKey, setApiKeyEnabled, deleteApiKey,
  fetchWorkspaceApiKeys, fetchWorkspaceApiKeyScopes, createWorkspaceApiKey,
  setWorkspaceApiKeyEnabled, deleteWorkspaceApiKey,
  fetchWorkspaces, fetchProjects,
} from '../lib/api'
import { Hint, Btn } from './ui'

// ApiKeysManager renders the API-key list + create flow. Used in two places:
//   • Admin → API Keys      (workspace = null) — global keys, any-workspace project scope.
//   • Manage Workspace → API Keys (workspace = key) — keys CONFINED to that workspace;
//     the create form locks scope to this workspace's projects (no workspace picker).
// The backend enforces the confinement; the UI just hides what doesn't apply.
export default function ApiKeysManager({ workspace = null }) {
  const ws = workspace || null
  const qc = useQueryClient()
  const keysKey = ws ? ['ws-api-keys', ws] : ['api-keys']

  const { data: keys = [], isLoading } = useQuery({
    queryKey: keysKey,
    queryFn: () => (ws ? fetchWorkspaceApiKeys(ws) : fetchApiKeys()),
  })
  const { data: groups = [] } = useQuery({
    queryKey: ws ? ['ws-api-key-scopes', ws] : ['api-key-scopes'],
    queryFn: () => (ws ? fetchWorkspaceApiKeyScopes(ws) : fetchApiKeyScopes()),
  })

  const [modal, setModal]       = useState(false)
  const [revealed, setRevealed] = useState(null)
  const [deleting, setDeleting] = useState(null)

  const toggleMut = useMutation({
    mutationFn: ({ id, enabled }) => (ws ? setWorkspaceApiKeyEnabled(ws, id, enabled) : setApiKeyEnabled(id, enabled)),
    onSuccess: () => qc.invalidateQueries({ queryKey: keysKey }),
  })
  const delMut = useMutation({
    mutationFn: (id) => (ws ? deleteWorkspaceApiKey(ws, id) : deleteApiKey(id)),
    onSuccess: () => { qc.invalidateQueries({ queryKey: keysKey }); setDeleting(null) },
  })

  const fmtExpiry = (e) => !e ? 'never' : new Date(e * 1000).toLocaleDateString()

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-content-strong">API Keys</h2>
          <Hint className="text-sm mt-0.5">
            {ws
              ? <>Programmatic access to <strong>this workspace’s</strong> projects via the <code className="font-mono text-xs">/api/v1</code> REST API.</>
              : <>Programmatic access to the <code className="font-mono text-xs">/api/v1</code> REST API — scoped, project-restricted, rate-limited.</>}
            {' '}<a href="/api/v1/docs" target="_blank" rel="noreferrer" className="text-brand-400 hover:text-brand-300">View API docs ↗</a>
          </Hint>
        </div>
        <Btn variant="primary" size="sm" onClick={() => setModal(true)} >＋ New API key</Btn>
      </div>

      {keys.length === 0 ? (
        <div className="bg-surface border border-border rounded-xl p-8 text-center">
          <div className="text-3xl mb-2">🔑</div>
          <p className="text-sm font-medium text-content-strong">No API keys</p>
          <Hint className="text-sm">Create a key to call the Rigger REST API from scripts or CI.</Hint>
        </div>
      ) : (
        <div className="space-y-2">
          {keys.map(k => (
            <div key={k.id} className={`flex items-center gap-4 p-4 bg-surface border border-border rounded-xl ${k.enabled ? '' : 'opacity-60'}`}>
              <div className="flex-shrink-0 w-8 h-8 rounded-lg bg-surface-raised flex items-center justify-center text-sm">🔑</div>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2 flex-wrap">
                  <p className="text-sm font-semibold text-content-strong">{k.name}</p>
                  <span className="font-mono text-[11px] text-content-faint">{k.key_prefix}</span>
                  {!ws && k.workspace && <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint">ws · {k.workspace}</span>}
                  {!k.enabled && <span className="text-[10px] px-1.5 py-0.5 rounded bg-danger-subtle/40 text-danger-fg border border-danger-border/40">disabled</span>}
                  {k.expires_at > 0 && k.expires_at * 1000 < Date.now() && <span className="text-[10px] px-1.5 py-0.5 rounded bg-danger-subtle/40 text-danger-fg">expired</span>}
                </div>
                <p className="text-xs text-content-subtle mt-0.5">
                  {k.project_access === 'specific' ? `${(k.projects || []).length} project(s)` : (ws ? 'all projects in this workspace' : 'all projects')}
                  {' · '}{(k.scopes || []).length} scope(s)
                  {' · '}{k.rate_limit > 0 ? `${k.rate_limit}/min` : 'unlimited'}
                  {' · expires '}{fmtExpiry(k.expires_at)}
                  {k.last_used_at > 0 ? ` · last used ${new Date(k.last_used_at * 1000).toLocaleDateString()}` : ' · never used'}
                </p>
              </div>
              <div className="flex items-center gap-2">
                <Btn variant="secondary" size="xs" onClick={() => toggleMut.mutate({ id: k.id, enabled: !k.enabled })} >{k.enabled ? 'Disable' : 'Enable'}</Btn>
                <Btn variant="dangerSubtle" size="xs" onClick={() => setDeleting(k)} >Delete</Btn>
              </div>
            </div>
          ))}
        </div>
      )}

      {modal && (
        <CreateApiKeyModal
          workspace={ws} groups={groups}
          onClose={() => setModal(false)}
          onCreated={(k) => { setModal(false); setRevealed(k); qc.invalidateQueries({ queryKey: keysKey }) }}
        />
      )}
      {revealed && <RevealApiKeyModal apiKey={revealed} onClose={() => setRevealed(null)} />}
      {deleting && (
        <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={() => setDeleting(null)}>
          <div className="bg-surface border border-border rounded-xl w-full max-w-md mx-4 p-6" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong mb-2">Delete API key</h3>
            <p className="text-sm text-content-subtle mb-5">Delete <strong className="text-content">{deleting.name}</strong>? Any client using it will immediately get 401. This can’t be undone.</p>
            <div className="flex justify-end gap-2">
              <Btn variant="ghost" size="sm" onClick={() => setDeleting(null)} >Cancel</Btn>
              <Btn variant="danger" size="sm" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending} >{delMut.isPending ? 'Deleting…' : 'Delete'}</Btn>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

function CreateApiKeyModal({ workspace, groups, onClose, onCreated }) {
  const ws = workspace || null
  const [name, setName]           = useState('')
  const [scopes, setScopes]       = useState(() => new Set())
  const [expanded, setExpanded]   = useState(() => new Set())
  const [access, setAccess]       = useState('all')
  const [projects, setProjects]   = useState([])
  const [rateLimit, setRateLimit] = useState(60)
  const [expiresDays, setExpires] = useState(0)
  const [pickWs, setPickWs]       = useState(ws || '')
  const [err, setErr]             = useState('')

  // Global mode needs the workspace list; ws mode locks to its own.
  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces, enabled: !ws })
  const projectsWs = ws || pickWs
  const { data: pickProjects = [] } = useQuery({
    queryKey: ['projects', projectsWs], queryFn: () => fetchProjects(projectsWs), enabled: !!projectsWs && access === 'specific',
  })

  const createMut = useMutation({
    mutationFn: (body) => (ws ? createWorkspaceApiKey(ws, body) : createApiKey(body)),
    onSuccess: (k) => onCreated(k),
    onError: (e) => setErr(e.response?.data?.error || 'Failed to create key'),
  })

  const toggleOp = (id) => setScopes(s => { const n = new Set(s); n.has(id) ? n.delete(id) : n.add(id); return n })
  const toggleGroup = (g) => {
    const ids = g.ops.map(o => o.id)
    const allOn = ids.every(id => scopes.has(id))
    setScopes(s => { const n = new Set(s); ids.forEach(id => allOn ? n.delete(id) : n.add(id)); return n })
  }
  const toggleExpand = (id) => setExpanded(e => { const n = new Set(e); n.has(id) ? n.delete(id) : n.add(id); return n })
  const addProjectRef = (w, p) => setProjects(ps => ps.some(x => x.workspace === w && x.project === p) ? ps : [...ps, { workspace: w, project: p }])

  function submit() {
    setErr('')
    if (!name.trim()) { setErr('Name is required'); return }
    if (scopes.size === 0) { setErr('Select at least one scope'); return }
    if (access === 'specific' && projects.length === 0) { setErr('Add at least one project, or use all-projects access'); return }
    createMut.mutate({
      name: name.trim(), scopes: [...scopes], project_access: access,
      projects: access === 'specific' ? projects : [],
      rate_limit: Number(rateLimit) || 0, expires_in_days: Number(expiresDays) || 0,
    })
  }

  const inputCls = 'w-full px-2 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-5">
          <h3 className="font-semibold text-content-strong">New API key{ws ? ` · ${ws}` : ''}</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>

        <div className="space-y-4">
          <div>
            <label className="block text-xs font-medium text-content-muted mb-1">Name</label>
            <input value={name} onChange={e => setName(e.target.value)} placeholder="CI deploy bot" className={inputCls} autoFocus />
          </div>

          <div>
            <label className="block text-xs font-medium text-content-muted mb-1">Permissions</label>
            <div className="space-y-1.5">
              {groups.map(g => {
                const ids = g.ops.map(o => o.id)
                const on = ids.filter(id => scopes.has(id)).length
                const state = on === 0 ? 'off' : on === ids.length ? 'all' : 'some'
                const isOpen = expanded.has(g.id)
                return (
                  <div key={g.id} className="border border-border-strong rounded-lg">
                    <div className="flex items-center gap-2 px-3 py-2">
                      <input type="checkbox" className="accent-brand-500 w-4 h-4"
                        checked={state === 'all'} ref={el => { if (el) el.indeterminate = state === 'some' }}
                        onChange={() => toggleGroup(g)} />
                      <button type="button" onClick={() => toggleExpand(g.id)} className="flex-1 text-left">
                        <span className="text-sm text-content-strong font-medium">{g.label}</span>
                        <span className="text-xs text-content-subtle ml-2">{g.desc}</span>
                      </button>
                      <span className="text-[11px] text-content-faint">{on}/{ids.length}</span>
                      <button type="button" onClick={() => toggleExpand(g.id)} className={`text-content-subtle text-xs transition-transform ${isOpen ? 'rotate-90' : ''}`}>▸</button>
                    </div>
                    {isOpen && (
                      <div className="border-t border-border-strong px-3 py-2 space-y-1.5">
                        {g.ops.map(o => (
                          <label key={o.id} className="flex items-start gap-2 cursor-pointer">
                            <input type="checkbox" className="accent-brand-500 w-3.5 h-3.5 mt-0.5 shrink-0" checked={scopes.has(o.id)} onChange={() => toggleOp(o.id)} />
                            <span className="min-w-0">
                              <span className="text-sm text-content">{o.label}</span>
                              <span className="block font-mono text-[10px] text-content-faint break-all">{o.method} {o.path.replace('/api/v1', '')}</span>
                            </span>
                          </label>
                        ))}
                      </div>
                    )}
                  </div>
                )
              })}
            </div>
          </div>

          <div>
            <label className="block text-xs font-medium text-content-muted mb-1">Project access</label>
            <div className="flex gap-3 mb-2">
              <label className="flex items-center gap-1.5 text-sm cursor-pointer"><input type="radio" checked={access === 'all'} onChange={() => setAccess('all')} className="accent-brand-500" /> {ws ? 'All projects in this workspace' : 'All projects'}</label>
              <label className="flex items-center gap-1.5 text-sm cursor-pointer"><input type="radio" checked={access === 'specific'} onChange={() => setAccess('specific')} className="accent-brand-500" /> Specific projects</label>
            </div>
            {access === 'specific' && (
              <div className="space-y-2 border border-border-strong rounded-lg p-3">
                <div className="flex gap-2">
                  {!ws && (
                    <select value={pickWs} onChange={e => setPickWs(e.target.value)} className={inputCls}>
                      <option value="">— workspace —</option>
                      {workspaces.map(w => <option key={w.key} value={w.key}>{w.name || w.key}</option>)}
                    </select>
                  )}
                  <select disabled={!projectsWs} onChange={e => { if (e.target.value) addProjectRef(projectsWs, e.target.value); e.target.value = '' }} className={inputCls}>
                    <option value="">＋ add project…</option>
                    {pickProjects.map(p => (
                      <option key={p.name} value={p.name}>
                        {(p.config?.project?.name || p.name)} ({projectsWs}/{p.name})
                      </option>
                    ))}
                  </select>
                </div>
                {projects.length > 0 && (
                  <div className="flex flex-wrap gap-1.5">
                    {projects.map((p, i) => (
                      <span key={i} className="inline-flex items-center gap-1 text-xs bg-surface-raised border border-border-strong rounded px-2 py-0.5">
                        <span className="font-mono">{ws ? p.project : `${p.workspace}/${p.project}`}</span>
                        <button type="button" onClick={() => setProjects(ps => ps.filter((_, j) => j !== i))} className="text-content-faint hover:text-danger-fg">×</button>
                      </span>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>

          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs font-medium text-content-muted mb-1">Rate limit (req/min/project)</label>
              <input type="number" min="0" value={rateLimit} onChange={e => setRateLimit(e.target.value)} className={inputCls} />
              <Hint tone="faint" className="text-[11px]">0 = unlimited</Hint>
            </div>
            <div>
              <label className="block text-xs font-medium text-content-muted mb-1">Expires in (days)</label>
              <input type="number" min="0" value={expiresDays} onChange={e => setExpires(e.target.value)} className={inputCls} />
              <Hint tone="faint" className="text-[11px]">0 = never</Hint>
            </div>
          </div>

          {err && <p className="text-danger-fg text-sm">{err}</p>}
        </div>

        <div className="flex justify-end gap-2 mt-6">
          <Btn variant="ghost" size="sm" onClick={onClose} >Cancel</Btn>
          <Btn variant="primary" size="sm" onClick={submit} disabled={createMut.isPending} >{createMut.isPending ? 'Creating…' : 'Create key'}</Btn>
        </div>
      </div>
    </div>
  )
}

function RevealApiKeyModal({ apiKey, onClose }) {
  const [copied, setCopied] = useState(false)
  const copy = () => navigator.clipboard?.writeText(apiKey.token).then(() => { setCopied(true); setTimeout(() => setCopied(false), 2000) })
  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
        <h3 className="font-semibold text-content-strong mb-2">API key created</h3>
        <p className="text-sm text-content-subtle mb-3">Copy this key now — it will <strong className="text-content">not be shown again</strong>. Send it as <code className="font-mono text-xs">Authorization: Bearer &lt;key&gt;</code>.</p>
        <div className="flex items-center gap-2 bg-surface-raised border border-border-strong rounded-lg p-3">
          <code className="flex-1 font-mono text-xs text-content-strong break-all">{apiKey.token}</code>
          <Btn variant="primary" size="xs" onClick={copy} className="shrink-0">{copied ? '✓ Copied' : 'Copy'}</Btn>
        </div>
        <div className="flex justify-end mt-5"><Btn variant="primary" size="sm" onClick={onClose} >Done</Btn></div>
      </div>
    </div>
  )
}
