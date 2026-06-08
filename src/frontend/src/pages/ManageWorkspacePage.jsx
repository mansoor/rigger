import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import HostForm from '../components/HostForm'
import RegistryForm from '../components/RegistryForm'
import BackupTargetForm from '../components/BackupTargetForm'
import {
  fetchWorkspaces, fetchProjects, renameWorkspaceTier, deleteWorkspaceTier, transferWorkspace,
  fetchWorkspaceHosts, createWorkspaceHost, updateWorkspaceHost, deleteWorkspaceHost, testWorkspaceHost,
  fetchWorkspaceRegistries, createWorkspaceRegistry, updateWorkspaceRegistry, deleteWorkspaceRegistry, testWorkspaceRegistry,
  fetchWorkspaceBackupTargets, createWorkspaceBackupTarget, updateWorkspaceBackupTarget, deleteWorkspaceBackupTarget, testWorkspaceBackupTarget,
  fetchWorkspaceSettings, updateWorkspaceSettings,
} from '../lib/api'
import { useWorkspaceStore } from '../store/workspace'

const TABS = [
  { id: 'general',        label: 'General' },
  { id: 'hosts',          label: 'Remote Hosts' },
  { id: 'registries',     label: 'Docker Registries' },
  { id: 'backup-targets', label: 'Backup Targets' },
  { id: 'danger',         label: 'Danger Zone' },
]

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9 _-]{0,31}$/

export default function ManageWorkspacePage() {
  const { workspace } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const setCurrent = useWorkspaceStore(s => s.setCurrent)

  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const { data: projects = [] } = useQuery({
    queryKey: ['projects', workspace], queryFn: () => fetchProjects(workspace), enabled: !!workspace,
  })
  const ws = workspaces.find(w => w.key === workspace)
  const others = workspaces.filter(w => w.key !== workspace)
  const [tab, setTab] = useState('general')

  return (
    <Layout>
      <div className="p-6 max-w-3xl">
        <div className="mb-6">
          <p className="text-xs font-medium uppercase tracking-wider text-content-subtle">Manage workspace</p>
          <div className="flex items-center gap-2.5 mt-0.5">
            <h1 className="text-2xl font-bold text-content-strong">{ws?.name || workspace}</h1>
            <span className="text-xs font-mono text-content-faint px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong" title="Workspace key (folder / URL / Docker prefix)">{workspace}</span>
          </div>
          <p className="text-sm text-content-muted mt-1">{projects.length} project{projects.length !== 1 ? 's' : ''}</p>
        </div>

        <div className="flex gap-1 border-b border-border mb-6">
          {TABS.map(t => (
            <button
              key={t.id} onClick={() => setTab(t.id)}
              className={`px-4 py-2.5 text-sm font-medium border-b-2 transition-colors -mb-px ${
                tab === t.id
                  ? (t.id === 'danger' ? 'border-danger text-danger-fg' : 'border-brand-500 text-brand-400')
                  : 'border-transparent text-content-subtle hover:text-content'
              }`}
            >
              {t.label}
            </button>
          ))}
        </div>

        {tab === 'general' && (
          <div className="space-y-8">
            <GeneralSection workspace={workspace} ws={ws} qc={qc} setCurrent={setCurrent} />
            <WorkspaceGeneralSettings workspace={workspace} qc={qc} />
          </div>
        )}
        {tab === 'hosts'          && <HostsSection workspace={workspace} qc={qc} />}
        {tab === 'registries'     && <RegistriesSection workspace={workspace} qc={qc} />}
        {tab === 'backup-targets' && <BackupTargetsSection workspace={workspace} qc={qc} />}
        {tab === 'danger'         && <DangerZone workspace={workspace} ws={ws} projects={projects} others={others} qc={qc} setCurrent={setCurrent} navigate={navigate} />}
      </div>
    </Layout>
  )
}

// WorkspaceGeneralSettings — workspace-scoped general settings (ACME email, base
// domain). Like the global General tab, these are stored values used for SSL/URL
// config; full per-workspace Traefik wiring lands with later automation.
function WorkspaceGeneralSettings({ workspace, qc }) {
  const settingsKey = ['ws-settings', workspace]
  const { data: saved } = useQuery({
    queryKey: settingsKey, queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace,
  })
  const [acme, setAcme] = useState('')
  const [domain, setDomain] = useState('')
  const [seeded, setSeeded] = useState(false)
  if (!seeded && saved) { setAcme(saved.acme_email || ''); setDomain(saved.domain || ''); setSeeded(true) }

  const mut = useMutation({
    mutationFn: () => updateWorkspaceSettings(workspace, { acme_email: acme.trim(), domain: domain.trim() }),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKey }),
  })
  const dirty = saved && (acme.trim() !== (saved.acme_email || '') || domain.trim() !== (saved.domain || ''))

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-3">SSL &amp; domain</h2>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">ACME email</label>
          <input value={acme} onChange={e => setAcme(e.target.value)} type="email" placeholder="ops@example.com"
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500" />
          <p className="text-xs text-content-subtle mt-1">Let's Encrypt registration email for this workspace's certificates.</p>
        </div>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Default domain</label>
          <input value={domain} onChange={e => setDomain(e.target.value)} placeholder="apps.example.com"
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500" />
          <p className="text-xs text-content-subtle mt-1">Base domain new environments in this workspace default to.</p>
        </div>
        <p className="text-xs text-content-faint">Stored now; full per-workspace SSL automation arrives with a later phase (today Traefik reads the global <code className="font-mono">ACME_EMAIL</code>).</p>
        <div className="flex items-center gap-3">
          <button onClick={() => mut.mutate()} disabled={!dirty || mut.isPending}
            className="bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white text-sm font-semibold px-4 py-2 rounded-lg transition-colors">
            {mut.isPending ? 'Saving…' : 'Save'}
          </button>
          {mut.isSuccess && !dirty && <span className="text-xs text-success-fg">✓ Saved</span>}
        </div>
      </div>
    </section>
  )
}

function GeneralSection({ workspace, ws, qc, setCurrent }) {
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  // Seed once the workspace meta loads.
  const [seeded, setSeeded] = useState(false)
  if (!seeded && ws) { setName(ws.name || workspace); setSeeded(true) }

  const mut = useMutation({
    mutationFn: () => renameWorkspaceTier(workspace, name.trim()),
    onSuccess: () => { setErr(''); qc.invalidateQueries({ queryKey: ['workspaces'] }) },
    onError: (e) => setErr(e.response?.data?.error || 'Rename failed'),
  })
  const ok = NAME_RE.test(name.trim())
  const dirty = ws && name.trim() !== (ws.name || workspace)

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-3">General</h2>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Display name</label>
          <input
            value={name} onChange={e => setName(e.target.value)} maxLength={32}
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500"
          />
          <p className="text-xs text-content-subtle mt-1">1–32 chars, letters/digits/space/dash/underscore. The key <code className="font-mono">{workspace}</code> is fixed.</p>
        </div>
        {err && <p className="text-sm text-danger-fg">{err}</p>}
        <div className="flex items-center gap-3">
          <button
            onClick={() => mut.mutate()} disabled={!ok || !dirty || mut.isPending}
            className="bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white text-sm font-semibold px-4 py-2 rounded-lg transition-colors"
          >
            {mut.isPending ? 'Saving…' : 'Save'}
          </button>
          {mut.isSuccess && !dirty && <span className="text-xs text-success-fg">✓ Saved</span>}
        </div>
      </div>
    </section>
  )
}

function HostsSection({ workspace, qc }) {
  const hostsKey = ['ws-hosts', workspace]
  const { data: hosts = [], isLoading } = useQuery({
    queryKey: hostsKey, queryFn: () => fetchWorkspaceHosts(workspace), enabled: !!workspace,
  })
  const [modal, setModal]       = useState(null) // null | 'new' | { editing: host }
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({}) // id -> { loading, ok, msg, error }

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateWorkspaceHost(workspace, id, body) : createWorkspaceHost(workspace, body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: hostsKey }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteWorkspaceHost(workspace, id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: hostsKey }); setDeleting(null) },
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      const res = await testWorkspaceHost(workspace, id)
      if (res.status === 'ok') setTestStatus(s => ({ ...s, [id]: { ok: true, msg: res.message } }))
      else setTestStatus(s => ({ ...s, [id]: { error: res.error || 'Connection failed' } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Connection failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 8000)
  }

  const isOwned = (h) => h.owner_scope === `ws:${workspace}`

  return (
    <section>
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Remote hosts</h2>
          <p className="text-xs text-content-subtle mt-0.5">Hosts this workspace can deploy to: its own plus any shared by an administrator.</p>
        </div>
        <button onClick={() => setModal('new')}
          className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg border border-border-strong text-content hover:bg-surface-raised transition-colors">
          ＋ Add host
        </button>
      </div>

      <div className="bg-surface border border-border rounded-xl">
        {isLoading ? (
          <p className="p-5 text-sm text-content-subtle">Loading…</p>
        ) : hosts.length === 0 ? (
          <p className="p-5 text-sm text-content-subtle">No hosts available. Add one for this workspace, or ask an admin to share a global host with it.</p>
        ) : (
          <div className="divide-y divide-border">
            {hosts.map(host => {
              const owned = isOwned(host)
              const ts = testStatus[host.id]
              return (
                <div key={host.id} className="flex items-center gap-3 p-4">
                  <div className="flex-shrink-0 w-8 h-8 rounded-lg bg-surface-raised flex items-center justify-center text-sm">🖥️</div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-semibold text-content-strong">{host.name}</p>
                      {owned
                        ? <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40">this workspace</span>
                        : <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint" title="Shared by an administrator — managed in Settings">shared</span>}
                    </div>
                    <p className="text-xs text-content-subtle mt-0.5">{host.ssh_user}@{host.address}:{host.ssh_port}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    {ts?.loading && <span className="text-xs text-content-subtle">Testing…</span>}
                    {ts?.ok && <span className="text-xs text-success-fg max-w-[180px] truncate" title={ts.msg}>✓ {ts.msg}</span>}
                    {ts?.error && <span className="text-xs text-danger-fg max-w-[180px] truncate" title={ts.error}>{ts.error}</span>}
                    <button onClick={() => handleTest(host.id)} disabled={ts?.loading}
                      className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised disabled:opacity-50">Test</button>
                    {owned ? (
                      <>
                        <button onClick={() => setModal({ editing: host })}
                          className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised">Edit</button>
                        <button onClick={() => setDeleting(host)}
                          className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-danger-fg hover:bg-danger/20">Delete</button>
                      </>
                    ) : (
                      <span className="text-[11px] text-content-faint px-2">read-only</span>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={() => setModal(null)}>
          <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add host' : `Edit “${modal.editing.name}”`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <HostForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={(body) => saveMut.mutateAsync({ id: modal?.editing?.id, body })}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {deleting && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={() => setDeleting(null)}>
          <div className="bg-surface border border-border rounded-2xl w-full max-w-sm p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Delete “{deleting.name}”?</h3>
            <p className="text-sm text-content-muted">Environments bound to this host will need to be repointed. This cannot be undone.</p>
            <div className="flex gap-2 justify-end">
              <button onClick={() => setDeleting(null)} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
              <button onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                className="px-4 py-2 text-sm font-semibold rounded-lg bg-red-800 hover:bg-red-700 disabled:opacity-40 text-white">
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

function RegistriesSection({ workspace, qc }) {
  const regsKey = ['ws-registries', workspace]
  const { data: regs = [], isLoading } = useQuery({
    queryKey: regsKey, queryFn: () => fetchWorkspaceRegistries(workspace), enabled: !!workspace,
  })
  const [modal, setModal]       = useState(null) // null | 'new' | { editing }
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({}) // id -> { loading, ok, error }

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateWorkspaceRegistry(workspace, id, body) : createWorkspaceRegistry(workspace, body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: regsKey }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteWorkspaceRegistry(workspace, id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: regsKey }); setDeleting(null) },
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testWorkspaceRegistry(workspace, id)
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Login failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 6000)
  }

  const isOwned = (r) => r.owner_scope === `ws:${workspace}`

  return (
    <section>
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Docker registries</h2>
          <p className="text-xs text-content-subtle mt-0.5">Registries this workspace's projects can pull/push images from: its own plus any shared by an administrator.</p>
        </div>
        <button onClick={() => setModal('new')}
          className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg border border-border-strong text-content hover:bg-surface-raised transition-colors">
          ＋ Add registry
        </button>
      </div>

      <div className="bg-surface border border-border rounded-xl">
        {isLoading ? (
          <p className="p-5 text-sm text-content-subtle">Loading…</p>
        ) : regs.length === 0 ? (
          <p className="p-5 text-sm text-content-subtle">No registries available. Add one for this workspace, or ask an admin to share a global registry with it.</p>
        ) : (
          <div className="divide-y divide-border">
            {regs.map(r => {
              const owned = isOwned(r)
              const ts = testStatus[r.id]
              return (
                <div key={r.id} className="flex items-center gap-3 p-4">
                  <div className="flex-shrink-0 w-8 h-8 rounded-lg bg-surface-raised flex items-center justify-center text-sm">📦</div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-semibold text-content-strong">{r.name}</p>
                      {owned
                        ? <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40">this workspace</span>
                        : <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint" title="Shared by an administrator — managed in Settings">shared</span>}
                    </div>
                    <p className="text-xs text-content-subtle mt-0.5">{r.url} · {r.username}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    {ts?.loading && <span className="text-xs text-content-subtle">Testing…</span>}
                    {ts?.ok && <span className="text-xs text-success-fg">✓ Connected</span>}
                    {ts?.error && <span className="text-xs text-danger-fg max-w-[180px] truncate" title={ts.error}>{ts.error}</span>}
                    <button onClick={() => handleTest(r.id)} disabled={ts?.loading}
                      className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised disabled:opacity-50">Test</button>
                    {owned ? (
                      <>
                        <button onClick={() => setModal({ editing: r })}
                          className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised">Edit</button>
                        <button onClick={() => setDeleting(r)}
                          className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-danger-fg hover:bg-danger/20">Delete</button>
                      </>
                    ) : (
                      <span className="text-[11px] text-content-faint px-2">read-only</span>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={() => setModal(null)}>
          <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add registry' : `Edit “${modal.editing.name}”`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <RegistryForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={(body) => saveMut.mutateAsync({ id: modal?.editing?.id, body })}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {deleting && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={() => setDeleting(null)}>
          <div className="bg-surface border border-border rounded-2xl w-full max-w-sm p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Delete “{deleting.name}”?</h3>
            <p className="text-sm text-content-muted">Projects referencing this registry will need a different one. This cannot be undone.</p>
            <div className="flex gap-2 justify-end">
              <button onClick={() => setDeleting(null)} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
              <button onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                className="px-4 py-2 text-sm font-semibold rounded-lg bg-red-800 hover:bg-red-700 disabled:opacity-40 text-white">
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

function BackupTargetsSection({ workspace, qc }) {
  const targetsKey = ['ws-backup-targets', workspace]
  const { data: targets = [], isLoading } = useQuery({
    queryKey: targetsKey, queryFn: () => fetchWorkspaceBackupTargets(workspace), enabled: !!workspace,
  })
  const [modal, setModal]       = useState(null)
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({})

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateWorkspaceBackupTarget(workspace, id, body) : createWorkspaceBackupTarget(workspace, body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: targetsKey }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteWorkspaceBackupTarget(workspace, id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: targetsKey }); setDeleting(null) },
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testWorkspaceBackupTarget(workspace, id)
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Connection failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 6000)
  }

  const isOwned = (t) => t.owner_scope === `ws:${workspace}`
  const detail = (t) => t.type === 's3'
    ? `${t.config?.endpoint || 's3'} / ${t.config?.bucket || '—'}`
    : `${t.config?.username || ''}@${t.config?.host || '—'}:${t.config?.port || 22}`

  return (
    <section>
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Backup targets</h2>
          <p className="text-xs text-content-subtle mt-0.5">Off-site destinations this workspace's environments can back up to: its own plus any shared by an administrator.</p>
        </div>
        <button onClick={() => setModal('new')}
          className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg border border-border-strong text-content hover:bg-surface-raised transition-colors">
          ＋ Add target
        </button>
      </div>

      <div className="bg-surface border border-border rounded-xl">
        {isLoading ? (
          <p className="p-5 text-sm text-content-subtle">Loading…</p>
        ) : targets.length === 0 ? (
          <p className="p-5 text-sm text-content-subtle">No backup targets available. Add one for this workspace, or ask an admin to share a global target with it.</p>
        ) : (
          <div className="divide-y divide-border">
            {targets.map(t => {
              const owned = isOwned(t)
              const ts = testStatus[t.id]
              return (
                <div key={t.id} className="flex items-center gap-3 p-4">
                  <span className={`flex-shrink-0 inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider
                    ${t.type === 's3' ? 'bg-warning-subtle/60 text-warning-fg' : 'bg-cyan-100/70 text-cyan-700 dark:bg-cyan-900/60 dark:text-cyan-300'}`}>
                    {t.type}
                  </span>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-semibold text-content-strong">{t.name}</p>
                      {owned
                        ? <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40">this workspace</span>
                        : <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint" title="Shared by an administrator — managed in Settings">shared</span>}
                    </div>
                    <p className="text-xs text-content-subtle mt-0.5 truncate">{detail(t)}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    {ts?.loading && <span className="text-xs text-content-subtle">Testing…</span>}
                    {ts?.ok && <span className="text-xs text-success-fg">✓ Connected</span>}
                    {ts?.error && <span className="text-xs text-danger-fg max-w-[180px] truncate" title={ts.error}>{ts.error}</span>}
                    <button onClick={() => handleTest(t.id)} disabled={ts?.loading}
                      className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised disabled:opacity-50">Test</button>
                    {owned ? (
                      <>
                        <button onClick={() => setModal({ editing: t })}
                          className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised">Edit</button>
                        <button onClick={() => setDeleting(t)}
                          className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-danger-fg hover:bg-danger/20">Delete</button>
                      </>
                    ) : (
                      <span className="text-[11px] text-content-faint px-2">read-only</span>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={() => setModal(null)}>
          <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add backup target' : `Edit “${modal.editing.name}”`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <BackupTargetForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={(body) => saveMut.mutateAsync({ id: modal?.editing?.id, body })}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {deleting && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={() => setDeleting(null)}>
          <div className="bg-surface border border-border rounded-2xl w-full max-w-sm p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Delete “{deleting.name}”?</h3>
            <p className="text-sm text-content-muted">Backup schedules pointing at this target will fall back to local. This cannot be undone.</p>
            <div className="flex gap-2 justify-end">
              <button onClick={() => setDeleting(null)} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
              <button onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                className="px-4 py-2 text-sm font-semibold rounded-lg bg-red-800 hover:bg-red-700 disabled:opacity-40 text-white">
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

function DangerZone({ workspace, ws, projects, others, qc, setCurrent, navigate }) {
  const [mode, setMode] = useState(null) // null | 'transfer' | 'delete'
  return (
    <section>
      <h2 className="text-sm font-semibold text-danger-fg mb-3">Danger zone</h2>
      <div className="bg-surface border border-danger-border/50 rounded-xl divide-y divide-border">
        <div className="p-5 flex items-center justify-between gap-4">
          <div>
            <p className="text-sm font-medium text-content-strong">Transfer projects to another workspace</p>
            <p className="text-xs text-content-subtle mt-0.5">Move all (or selected) projects to another workspace, then remove this one. Containers keep running (their resource prefix is unchanged).</p>
          </div>
          <button onClick={() => setMode('transfer')} disabled={projects.length === 0 || others.length === 0}
            className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg border border-border-strong text-content hover:bg-surface-raised disabled:opacity-40 transition-colors">
            Transfer…
          </button>
        </div>
        <div className="p-5 flex items-center justify-between gap-4">
          <div>
            <p className="text-sm font-medium text-content-strong">Delete this workspace</p>
            <p className="text-xs text-content-subtle mt-0.5">Stops and removes every project, environment, container, network and volume in this workspace, then deletes it. Cannot be undone.</p>
          </div>
          <button onClick={() => setMode('delete')}
            className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg bg-red-800 hover:bg-red-700 text-white transition-colors">
            Delete…
          </button>
        </div>
      </div>

      {mode === 'transfer' && (
        <TransferModal workspace={workspace} projects={projects} others={others}
          onClose={() => setMode(null)}
          onDone={(target) => { qc.invalidateQueries({ queryKey: ['workspaces'] }); setCurrent(target); navigate('/') }} />
      )}
      {mode === 'delete' && (
        <DeleteModal workspace={workspace} ws={ws} projects={projects}
          onClose={() => setMode(null)}
          onDone={() => { qc.invalidateQueries({ queryKey: ['workspaces'] }); setCurrent(''); navigate('/') }} />
      )}
    </section>
  )
}

function TransferModal({ workspace, projects, others, onClose, onDone }) {
  const [target, setTarget] = useState(others[0]?.key || '')
  const [sel, setSel] = useState(null) // null = all
  const [err, setErr] = useState('')
  const chosen = sel ?? projects.map(p => p.name)
  const mut = useMutation({
    mutationFn: () => transferWorkspace(workspace, target, chosen.length === projects.length ? [] : chosen),
    onSuccess: () => onDone(target),
    onError: (e) => setErr(e.response?.data?.error || 'Transfer failed'),
  })
  function toggle(k) {
    const cur = sel ?? projects.map(p => p.name)
    setSel(cur.includes(k) ? cur.filter(x => x !== k) : [...cur, k])
  }
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-md p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <h3 className="font-semibold text-content-strong">Transfer projects</h3>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Target workspace</label>
          <select value={target} onChange={e => setTarget(e.target.value)}
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500">
            {others.map(o => <option key={o.key} value={o.key}>{o.name} ({o.key})</option>)}
          </select>
        </div>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Projects</label>
          <div className="max-h-48 overflow-y-auto space-y-1 border border-border-strong rounded-lg p-2">
            {projects.map(p => (
              <label key={p.name} className="flex items-center gap-2 text-sm text-content cursor-pointer">
                <input type="checkbox" checked={chosen.includes(p.name)} onChange={() => toggle(p.name)} className="accent-brand-500" />
                <span>{p.config?.project?.name || p.name}</span>
                <span className="text-[10px] font-mono text-content-faint ml-auto">{p.config?.project?.resource_prefix || p.name}</span>
              </label>
            ))}
          </div>
          <p className="text-xs text-content-subtle mt-1">Keys are kept; on a name clash in the target the moved project's key is suffixed. Resource prefixes (and running containers) are unchanged.</p>
        </div>
        {err && <p className="text-sm text-danger-fg">{err}</p>}
        <div className="flex gap-2 justify-end">
          <button onClick={onClose} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
          <button onClick={() => mut.mutate()} disabled={!target || chosen.length === 0 || mut.isPending}
            className="px-4 py-2 text-sm font-semibold rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white">
            {mut.isPending ? 'Transferring…' : `Transfer ${chosen.length}`}
          </button>
        </div>
      </div>
    </div>
  )
}

function DeleteModal({ workspace, ws, projects, onClose, onDone }) {
  const [confirm, setConfirm] = useState('')
  const [err, setErr] = useState('')
  const mut = useMutation({
    mutationFn: () => deleteWorkspaceTier(workspace),
    onSuccess: onDone,
    onError: (e) => setErr(e.response?.data?.error || 'Delete failed'),
  })
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-danger-border/60 rounded-2xl w-full max-w-md p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <h3 className="font-semibold text-content-strong">Delete workspace “{ws?.name || workspace}”?</h3>
        <p className="text-sm text-content-muted">
          This stops and removes <span className="text-content-strong font-medium">{projects.length} project{projects.length !== 1 ? 's' : ''}</span> and all their
          environments, containers, networks and volumes — then deletes the workspace. <span className="text-danger-fg font-medium">This cannot be undone.</span>
        </p>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Type <code className="font-mono text-content">{workspace}</code> to confirm</label>
          <input value={confirm} onChange={e => setConfirm(e.target.value)}
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-danger" />
        </div>
        {err && <p className="text-sm text-danger-fg">{err}</p>}
        <div className="flex gap-2 justify-end">
          <button onClick={onClose} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
          <button onClick={() => mut.mutate()} disabled={confirm !== workspace || mut.isPending}
            className="px-4 py-2 text-sm font-semibold rounded-lg bg-red-800 hover:bg-red-700 disabled:opacity-40 text-white">
            {mut.isPending ? 'Deleting…' : 'Delete workspace'}
          </button>
        </div>
      </div>
    </div>
  )
}
