import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import HostForm from '../components/HostForm'
import HostCapabilityBadges from '../components/HostBadges'
import RegistryForm from '../components/RegistryForm'
import BackupTargetForm from '../components/BackupTargetForm'
import ChannelForm from '../components/ChannelForm'
import AccessRequestsInbox from '../components/AccessRequestsInbox'
import ApiKeysManager from '../components/ApiKeysManager'
import VerticalTabs from '../components/VerticalTabs'
import RoleHelp from '../components/RoleHelp'
import { globalRoleOptions, wsRoleOptions } from '../lib/roles'
import {
  fetchBackupTargets, createBackupTarget, updateBackupTarget, deleteBackupTarget, testBackupTarget,
  fetchRegistries, createRegistry, updateRegistry, deleteRegistry, testRegistry, markRegistrySystem,
  fetchManagedRegistry, managedRegistryAction,
  fetchHosts, createHost, updateHost, deleteHost, testHost, scanHost, importHost, fetchHostStats,
  fetchVersion, checkUpdates, applyUpdate, rollbackUpdate,
  fetchGeneralSettings, updateGeneralSettings, detectHostIP,
  fetchAlertRules, createAlertRule, updateAlertRule, deleteAlertRule, fetchAlertMeta,
  fetchProjects, fetchWorkspaces,
  fetchNotificationChannels, createNotificationChannel, updateNotificationChannel,
  deleteNotificationChannel, testNotificationChannel,
  fetchUsers, inviteUser, updateUser, deleteUser, resendInvite,
  fetchSystemEmail, updateSystemEmail,
} from '../lib/api'
import { useWorkspaceStore } from '../store/workspace'
import { useAuthStore } from '../store/auth'
import { useTheme } from '../theme/ThemeProvider'
import {
  THEMES, FONT_SANS_OPTIONS, FONT_MONO_OPTIONS, DENSITY_OPTIONS,
  LOG_FONT_SIZE_MIN, LOG_FONT_SIZE_MAX,
} from '../theme/themes'
import AppearanceDefaultEditor from '../components/AppearanceDefaultEditor'
import ConfirmDefaultEditor from '../components/ConfirmDefaultEditor'

// ── Shared primitives ─────────────────────────────────────────────────────────

function Label({ children, required }) {
  return (
    <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
      {children}{required && <span className="text-danger-fg ml-0.5">*</span>}
    </label>
  )
}

function Input({ value, onChange, placeholder, type = 'text', disabled, ...rest }) {
  return (
    <input
      type={type} value={value ?? ''} onChange={e => onChange(e.target.value)}
      placeholder={placeholder} disabled={disabled}
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle text-sm focus:outline-none focus:border-brand-500 transition-colors disabled:opacity-50"
      {...rest}
    />
  )
}

function Select({ value, onChange, options, disabled }) {
  return (
    <select
      value={value} onChange={e => onChange(e.target.value)} disabled={disabled}
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 disabled:opacity-50"
    >
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

function Toggle({ checked, onChange, label }) {
  return (
    <label className="flex items-center gap-2 cursor-pointer select-none">
      <button
        type="button" onClick={() => onChange(!checked)}
        className={`relative w-9 h-5 rounded-full transition-colors ${checked ? 'bg-brand-600' : 'bg-surface-overlay'}`}
      >
        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-4' : ''}`} />
      </button>
      <span className="text-sm text-content">{label}</span>
    </label>
  )
}

function Btn({ onClick, disabled, variant = 'primary', children, type = 'button', size = 'md' }) {
  const base = 'font-semibold rounded-lg transition-colors focus:outline-none'
  const sizes = { sm: 'px-3 py-1.5 text-xs', md: 'px-4 py-2 text-sm' }
  const variants = {
    primary:   'bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50',
    secondary: 'bg-surface-overlay hover:bg-surface-overlay text-content disabled:opacity-50',
    danger:    'bg-danger-subtle/60 hover:bg-danger/20 text-danger-fg disabled:opacity-50',
    ghost:     'text-content-muted hover:text-content-strong hover:bg-surface-raised disabled:opacity-50',
  }
  return (
    <button type={type} onClick={onClick} disabled={disabled}
      className={`${base} ${sizes[size]} ${variants[variant]}`}>
      {children}
    </button>
  )
}

function EmptyState({ icon, title, description, action }) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="text-4xl mb-3 opacity-40">{icon}</div>
      <p className="text-content font-medium mb-1">{title}</p>
      <p className="text-sm text-content-subtle mb-4">{description}</p>
      {action}
    </div>
  )
}

function ConfirmDeleteModal({ name, onConfirm, onClose, loading }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-sm mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <h3 className="font-semibold text-content-strong">Delete {name}?</h3>
        <p className="text-sm text-content-muted">This cannot be undone.</p>
        <div className="flex gap-2 justify-end pt-2">
          <Btn variant="secondary" onClick={onClose}>Cancel</Btn>
          <Btn variant="danger" onClick={onConfirm} disabled={loading}>
            {loading ? 'Deleting…' : 'Delete'}
          </Btn>
        </div>
      </div>
    </div>
  )
}

// ── Backup Targets (Phase 3: scoping) ─────────────────────────────────────────

function BackupTargetsTab() {
  const qc = useQueryClient()
  const { data: targets = [], isLoading } = useQuery({ queryKey: ['backup-targets'], queryFn: fetchBackupTargets })
  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const [modal, setModal] = useState(null) // null | 'new' | { editing: target }
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({}) // id -> { loading, ok, error }

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testBackupTarget(id)
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Connection failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 6000)
  }

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateBackupTarget(id, body) : createBackupTarget(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['backup-targets'] }); setModal(null) },
  })

  const delMut = useMutation({
    mutationFn: (id) => deleteBackupTarget(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['backup-targets'] }); setDeleting(null) },
  })

  function handleSave(body) {
    const id = modal?.editing?.id
    return saveMut.mutateAsync({ id, body })
  }

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-content-strong">Backup Targets</h2>
          <p className="text-sm text-content-subtle mt-0.5">S3-compatible object storage and SFTP destinations for workspace backups.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add target</Btn>
      </div>

      {targets.length === 0 ? (
        <EmptyState
          icon="🗄"
          title="No backup targets configured"
          description="Add an S3 bucket or SFTP server to enable off-site backups."
          action={<Btn onClick={() => setModal('new')}>＋ Add first target</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {targets.map(t => (
            <div key={t.id} className="flex items-center gap-4 p-4 bg-surface border border-border rounded-xl">
              <div className="flex-shrink-0">
                <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider
                  ${t.type === 's3' ? 'bg-warning-subtle/60 text-warning-fg' : 'bg-cyan-100/70 text-cyan-700 dark:bg-cyan-900/60 dark:text-cyan-300'}`}>
                  {t.type}
                </span>
              </div>
              <div className="flex-1 min-w-0">
                <div className="flex items-center gap-2">
                  <p className="text-sm font-semibold text-content-strong">{t.name}</p>
                  {t.owner_scope === 'global' ? (
                    <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint" title={`Offered to: ${grantSummary(t)}`}>shared · {grantSummary(t)}</span>
                  ) : (
                    <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40" title="Private to a workspace">workspace · {t.owner_scope.replace(/^ws:/, '')}</span>
                  )}
                </div>
                <p className="text-xs text-content-subtle mt-0.5 truncate">
                  {t.type === 's3'
                    ? `${t.config?.endpoint || 's3'} / ${t.config?.bucket || '—'}`
                    : `${t.config?.username || ''}@${t.config?.host || '—'}:${t.config?.port || 22}`
                  }
                </p>
              </div>
              <div className="flex items-center gap-2">
                {testStatus[t.id]?.loading && <span className="text-xs text-content-subtle">Testing…</span>}
                {testStatus[t.id]?.ok && <span className="text-xs text-success-fg">✓ Connected</span>}
                {testStatus[t.id]?.error && <span className="text-xs text-danger-fg max-w-[180px] truncate" title={testStatus[t.id].error}>{testStatus[t.id].error}</span>}
                <Btn variant="ghost" size="sm" onClick={() => handleTest(t.id)} disabled={testStatus[t.id]?.loading}>Test</Btn>
                <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: t })}>Edit</Btn>
                <Btn variant="danger" size="sm" onClick={() => setDeleting(t)}>Delete</Btn>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Add / Edit modal */}
      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-surface border border-border rounded-xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add backup target' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <BackupTargetForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
              showGrants={modal === 'new' || modal.editing?.owner_scope === 'global'}
              workspaces={workspaces}
            />
          </div>
        </div>
      )}

      {/* Delete confirm */}
      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

// ── Rigger-managed registry (image-distribution Phase 2) ──────────────────────
// One-click registry:2 sidecar Rigger runs on the host. When an apps base domain
// is configured it's fronted by Traefik over HTTPS at registry.{base} (pullable by
// every Swarm node); otherwise it's a local HTTP registry (single-node only).
function ManagedRegistryCard({ onChanged }) {
  const qc = useQueryClient()
  const { data: st, isLoading } = useQuery({ queryKey: ['managed-registry'], queryFn: fetchManagedRegistry })
  const [msg, setMsg] = useState(null) // { ok, text }

  const act = useMutation({
    mutationFn: (action) => managedRegistryAction(action),
    onSuccess: (_d, action) => {
      qc.invalidateQueries({ queryKey: ['managed-registry'] })
      qc.invalidateQueries({ queryKey: ['registries'] }) // entry created/marked system
      onChanged?.()
      setMsg({ ok: true, text: action === 'gc' ? 'Garbage collection complete' : action === 'down' ? 'Registry stopped' : 'Registry running' })
      setTimeout(() => setMsg(null), 5000)
    },
    onError: (e) => setMsg({ ok: false, text: e?.response?.data?.error || 'Action failed' }),
  })

  if (isLoading) return null
  const running = st?.running
  const noBaseDomain = !st?.base_domain

  return (
    <div className="mb-6 rounded-xl border border-border bg-surface-raised/40 p-4">
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="flex items-center gap-2">
            <span className="text-sm">🗄️</span>
            <h3 className="text-sm font-semibold text-content-strong">Rigger-managed registry</h3>
            {running
              ? <span className="text-[10px] px-1.5 py-0.5 rounded bg-emerald-100/70 text-emerald-700 border border-emerald-200 dark:bg-emerald-950/60 dark:text-emerald-300 dark:border-emerald-800/40">running</span>
              : <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint">stopped</span>}
          </div>
          <p className="text-xs text-content-subtle mt-1">
            {running
              ? <>Serving at <code className="text-content-muted">{st.url}</code>{st.https ? ' over HTTPS' : ' (local HTTP)'}{st.system ? ' · system registry' : ''}{st.disk_usage ? ` · ${st.disk_usage}` : ''}.</>
              : <>Run a registry:2 container on this host with one click. Build pushes here; deploys (and Swarm nodes) pull from it.</>}
          </p>
          {noBaseDomain ? (
            <p className="text-xs text-amber-600 dark:text-amber-400 mt-1">No apps base domain set — the registry will be local HTTP (<code>localhost:5000</code>), usable only for single-node compose deploys. Set a base domain (Settings → General) for an HTTPS <code>registry.&#123;base&#125;</code> a Swarm can pull from.</p>
          ) : (
            <p className="text-xs text-content-faint mt-1">Will be fronted by Traefik over HTTPS at <code>registry.{st.base_domain}</code>{st.https ? '' : ' once running'} — pullable by every Swarm node.</p>
          )}
        </div>
        <div className="flex flex-col items-end gap-2 shrink-0">
          {!running ? (
            <Btn onClick={() => act.mutate('up')} disabled={act.isPending}>{act.isPending ? 'Starting…' : st?.exists ? 'Start registry' : 'Run managed registry'}</Btn>
          ) : (
            <div className="flex items-center gap-2">
              <Btn variant="ghost" size="sm" onClick={() => act.mutate('gc')} disabled={act.isPending} title="Reclaim space from deleted/overwritten tags">Garbage-collect</Btn>
              <Btn variant="danger" size="sm" onClick={() => act.mutate('down')} disabled={act.isPending}>Stop</Btn>
            </div>
          )}
        </div>
      </div>
      {msg && <p className={`text-xs mt-2 ${msg.ok ? 'text-success-fg' : 'text-danger-fg'}`}>{msg.ok ? '✓ ' : '✗ '}{msg.text}</p>}
    </div>
  )
}

// ── Docker Registries (Phase 3: scoping) ──────────────────────────────────────

function RegistriesTab() {
  const qc = useQueryClient()
  const { data: regs = [], isLoading } = useQuery({ queryKey: ['registries'], queryFn: fetchRegistries })
  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const [modal, setModal]   = useState(null)
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({}) // id -> { loading, ok, error }

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateRegistry(id, body) : createRegistry(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['registries'] }); setModal(null) },
  })

  const delMut = useMutation({
    mutationFn: (id) => deleteRegistry(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['registries'] }); setDeleting(null) },
  })

  const sysMut = useMutation({
    mutationFn: ({ id, system }) => markRegistrySystem(id, system),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['registries'] }),
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testRegistry(id)
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Login failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 5000)
  }

  function handleSave(body) {
    const id = modal?.editing?.id
    return saveMut.mutateAsync({ id, body })
  }

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-content-strong">Docker Registries</h2>
          <p className="text-sm text-content-subtle mt-0.5">Pre-authenticated registries available when creating new workspaces. Mark one <span className="text-content-muted font-medium">system</span> to use it wherever a project sets no registry — required to deploy built images to a Swarm or remote host.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add registry</Btn>
      </div>

      <ManagedRegistryCard />

      {regs.length === 0 ? (
        <EmptyState
          icon="📦"
          title="No registries configured"
          description="Add a Docker registry to pull private images when creating workspaces."
          action={<Btn onClick={() => setModal('new')}>＋ Add first registry</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {regs.map(r => {
            const ts = testStatus[r.id]
            return (
              <div key={r.id} className="flex items-center gap-4 p-4 bg-surface border border-border rounded-xl">
                <div className="flex-shrink-0 w-8 h-8 rounded-lg bg-surface-raised flex items-center justify-center text-sm">
                  📦
                </div>
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-semibold text-content-strong">{r.name}</p>
                    {r.owner_scope === 'global' ? (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint" title={`Offered to: ${grantSummary(r)}`}>shared · {grantSummary(r)}</span>
                    ) : (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40" title="Private to a workspace">workspace · {r.owner_scope.replace(/^ws:/, '')}</span>
                    )}
                    {r.system && (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-emerald-100/70 text-emerald-700 border border-emerald-200 dark:bg-emerald-950/60 dark:text-emerald-300 dark:border-emerald-800/40" title="Used wherever a project sets no registry">★ system</span>
                    )}
                  </div>
                  <p className="text-xs text-content-subtle mt-0.5">{r.url} · {r.username}</p>
                </div>
                <div className="flex items-center gap-2">
                  {ts?.loading && <span className="text-xs text-content-subtle">Testing…</span>}
                  {ts?.ok && <span className="text-xs text-success-fg">✓ Connected</span>}
                  {ts?.error && <span className="text-xs text-danger-fg max-w-[180px] truncate" title={ts.error}>{ts.error}</span>}
                  <Btn variant="ghost" size="sm" onClick={() => handleTest(r.id)} disabled={ts?.loading}>Test</Btn>
                  {r.owner_scope === 'global' && (
                    <Btn variant="ghost" size="sm" onClick={() => sysMut.mutate({ id: r.id, system: !r.system })} disabled={sysMut.isPending}
                      title={r.system ? 'Stop using this as the system registry' : 'Use wherever a project sets no registry'}>
                      {r.system ? 'Unset system' : 'Set system'}
                    </Btn>
                  )}
                  <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: r })}>Edit</Btn>
                  <Btn variant="danger" size="sm" onClick={() => setDeleting(r)}>Delete</Btn>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-surface border border-border rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add registry' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <RegistryForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
              showGrants={modal === 'new' || modal.editing?.owner_scope === 'global'}
              workspaces={workspaces}
            />
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

// ── Hosts (Phase 7: Multi-Host Support; Phase 3: scoping) ─────────────────────

// grantSummary describes who a global host is offered to.
function grantSummary(host) {
  if (host.owner_scope !== 'global') return null
  const g = host.grants || []
  if (g.length === 0 || g.includes('*')) return 'All workspaces'
  return `${g.length} workspace${g.length !== 1 ? 's' : ''}`
}

function HostsTab() {
  const qc = useQueryClient()
  const { data: hosts = [], isLoading } = useQuery({ queryKey: ['hosts'], queryFn: fetchHosts })
  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const [modal, setModal]       = useState(null)
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({}) // id -> { loading, ok, msg, error }
  const [scanning, setScanning] = useState(null)    // host being scanned (modal)
  const [health, setHealth]     = useState(null)    // host whose health is shown (modal)

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateHost(id, body) : createHost(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['hosts'] }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteHost(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['hosts'] }); setDeleting(null) },
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      const res = await testHost(id)
      if (res.status === 'ok') setTestStatus(s => ({ ...s, [id]: { ok: true, msg: res.message } }))
      else setTestStatus(s => ({ ...s, [id]: { error: res.error || 'Connection failed' } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Connection failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 8000)
  }

  function handleSave(body) {
    return saveMut.mutateAsync({ id: modal?.editing?.id, body })
  }

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-content-strong">Remote Hosts</h2>
          <p className="text-sm text-content-subtle mt-0.5">Manage Docker workloads on other servers over SSH. Hosts need only Docker + SSH.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add host</Btn>
      </div>

      {hosts.length === 0 ? (
        <EmptyState
          icon="🖥️"
          title="No remote hosts"
          description="Register a server to deploy and operate workspaces on it from this control plane."
          action={<Btn onClick={() => setModal('new')}>＋ Add first host</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {hosts.map(host => {
            const ts = testStatus[host.id]
            return (
              <div key={host.id} className="flex items-center gap-4 p-4 bg-surface border border-border rounded-xl">
                <div className="flex-shrink-0 w-8 h-8 rounded-lg bg-surface-raised flex items-center justify-center text-sm">🖥️</div>
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-semibold text-content-strong">{host.name}</p>
                    {host.owner_scope === 'global' ? (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint" title={`Offered to: ${grantSummary(host)}`}>shared · {grantSummary(host)}</span>
                    ) : (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40" title="Private to a workspace">workspace · {host.owner_scope.replace(/^ws:/, '')}</span>
                    )}
                    <HostCapabilityBadges host={host} showRole />
                  </div>
                  <p className="text-xs text-content-subtle mt-0.5">{host.ssh_user}@{host.address}:{host.ssh_port}</p>
                </div>
                <div className="flex items-center gap-2">
                  {ts?.loading && <span className="text-xs text-content-subtle">Testing…</span>}
                  {ts?.ok && <span className="text-xs text-success-fg max-w-[200px] truncate" title={ts.msg}>✓ {ts.msg}</span>}
                  {ts?.error && <span className="text-xs text-danger-fg max-w-[200px] truncate" title={ts.error}>{ts.error}</span>}
                  <Btn variant="ghost" size="sm" onClick={() => handleTest(host.id)} disabled={ts?.loading}>Test</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setHealth(host)}>Health</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setScanning(host)}>Scan</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: host })}>Edit</Btn>
                  <Btn variant="danger" size="sm" onClick={() => setDeleting(host)}>Delete</Btn>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-surface border border-border rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add host' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <HostForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
              showGrants={modal === 'new' || modal.editing?.owner_scope === 'global'}
              showBuildOnly
              workspaces={workspaces}
            />
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}

      {scanning && (
        <ScanHostModal
          host={scanning}
          onClose={() => setScanning(null)}
          onImported={() => { qc.invalidateQueries({ queryKey: ['workspaces'] }) }}
        />
      )}

      {health && (
        <HostStatsModal host={health} onClose={() => setHealth(null)} />
      )}
    </div>
  )
}

// HostStatsModal fetches and displays a remote host's Docker + system health.
function HostStatsModal({ host, onClose }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['host-stats', host.id],
    queryFn: () => fetchHostStats(host.id),
    refetchInterval: 5000,
  })
  const failed = error || data?.status === 'error'
  const d = data?.docker || {}
  const hs = data?.host || {}

  const fmtUptime = (s) => {
    if (!s) return '—'
    const days = Math.floor(s / 86400), hrs = Math.floor((s % 86400) / 3600)
    return days > 0 ? `${days}d ${hrs}h` : `${hrs}h ${Math.floor((s % 3600) / 60)}m`
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
      <div className="bg-surface border border-border rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-5">
          <h3 className="font-semibold text-content-strong">Health · {host.name}</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>

        {isLoading && <div className="py-8 text-center text-content-subtle text-sm">Loading…</div>}
        {!isLoading && failed && (
          <div className="py-3 px-4 bg-red-500/10 border border-danger/30 rounded-lg text-sm text-danger-fg">
            {data?.error || error?.response?.data?.error || 'Failed to reach host'}
          </div>
        )}

        {!isLoading && !failed && (
          <div className="space-y-4">
            <div>
              <p className="text-xs font-semibold text-content-muted uppercase tracking-wider mb-2">Docker</p>
              {d.error ? (
                <p className="text-sm text-danger-fg">{d.error}</p>
              ) : (
                <div className="grid grid-cols-2 gap-3">
                  <StatCell label="Server" value={d.server_version || '—'} />
                  <StatCell label="Storage driver" value={d.storage_driver || '—'} />
                  <StatCell label="Containers" value={`${d.containers_running || 0} up · ${d.containers_stopped || 0} stopped`} />
                  <StatCell label="Images" value={d.images_total ?? 0} />
                  <StatCell label="Volumes" value={d.volumes_total ?? 0} />
                  <StatCell label="Networks" value={d.networks_total ?? 0} />
                </div>
              )}
            </div>
            <div>
              <p className="text-xs font-semibold text-content-muted uppercase tracking-wider mb-2">System</p>
              <div className="grid grid-cols-2 gap-3">
                <StatCell label="OS" value={hs.os || '—'} />
                <StatCell label="Arch · CPUs" value={`${hs.arch || '—'} · ${hs.cpus || 0}`} />
                <StatCell label="Memory" value={`${(hs.mem_used_pct || 0).toFixed(0)}% of ${(hs.mem_total_mb / 1024 || 0).toFixed(1)} GB`} />
                <StatCell label="Disk" value={`${(hs.disk_used_pct || 0).toFixed(0)}% of ${(hs.disk_total_gb || 0).toFixed(0)} GB`} />
                <StatCell label="Uptime" value={fmtUptime(hs.uptime_seconds)} />
              </div>
            </div>
            <div className="flex justify-end pt-1"><Btn variant="ghost" onClick={onClose}>Close</Btn></div>
          </div>
        )}
      </div>
    </div>
  )
}

function StatCell({ label, value }) {
  return (
    <div className="bg-canvas/50 border border-border rounded-lg px-3 py-2">
      <p className="text-[11px] text-content-subtle uppercase tracking-wider">{label}</p>
      <p className="text-sm text-content-strong mt-0.5 truncate" title={String(value)}>{value}</p>
    </div>
  )
}

// ScanHostModal lists workspaces discovered on a remote host and imports the
// selected ones (caches config.json locally + associates the workspace).
function ScanHostModal({ host, onClose, onImported }) {
  const [loading, setLoading]   = useState(true)
  const [error, setError]       = useState(null)
  const [rows, setRows]         = useState([])      // discovered workspaces
  const [sel, setSel]           = useState({})      // name -> bool
  const [importing, setImporting] = useState(false)
  const [done, setDone]         = useState(null)    // { imported, errors }

  useState(() => {
    scanHost(host.id)
      .then(res => {
        if (res.status !== 'ok') { setError(res.error || 'Scan failed'); return }
        const ws = res.workspaces || []
        setRows(ws)
        const init = {}
        ws.forEach(w => { if (!w.imported) init[w.name] = true })
        setSel(init)
      })
      .catch(err => setError(err.response?.data?.error || 'Scan failed'))
      .finally(() => setLoading(false))
  })

  const chosen = rows.filter(w => sel[w.name]).map(w => w.name)

  async function doImport() {
    setImporting(true)
    try {
      const res = await importHost(host.id, chosen)
      setDone(res)
      onImported?.()
    } catch (err) {
      setError(err.response?.data?.error || 'Import failed')
    }
    setImporting(false)
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
      <div className="bg-surface border border-border rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-5">
          <h3 className="font-semibold text-content-strong">Scan "{host.name}"</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>

        {loading && <div className="py-8 text-center text-content-subtle text-sm">Scanning host…</div>}
        {error && <div className="py-3 px-4 mb-4 bg-red-500/10 border border-danger/30 rounded-lg text-sm text-danger-fg">{error}</div>}

        {!loading && !error && (
          done ? (
            <div className="text-sm text-content space-y-2">
              <p className="text-success-fg">✓ Imported {done.imported?.length || 0} workspace(s).</p>
              {done.errors && Object.keys(done.errors).length > 0 && (
                <ul className="text-danger-fg text-xs space-y-1">
                  {Object.entries(done.errors).map(([n, e]) => <li key={n}>{n}: {e}</li>)}
                </ul>
              )}
              <div className="flex justify-end pt-2"><Btn onClick={onClose}>Done</Btn></div>
            </div>
          ) : rows.length === 0 ? (
            <div className="py-8 text-center text-content-subtle text-sm">No workspaces found in the remote workspaces directory.</div>
          ) : (
            <>
              <p className="text-xs text-content-subtle mb-3">Select workspaces to import. Already-imported workspaces are disabled.</p>
              <div className="space-y-1.5 max-h-72 overflow-y-auto">
                {rows.map(w => (
                  <label key={w.name} className={`flex items-center gap-3 p-3 rounded-lg border ${w.imported ? 'border-border bg-surface/50 opacity-60' : 'border-border bg-surface cursor-pointer hover:border-border-strong'}`}>
                    <input
                      type="checkbox"
                      disabled={w.imported}
                      checked={!!sel[w.name]}
                      onChange={e => setSel(s => ({ ...s, [w.name]: e.target.checked }))}
                    />
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-content-strong">{w.name}{w.imported && <span className="ml-2 text-xs text-content-subtle">(imported)</span>}</p>
                      <p className="text-xs text-content-subtle mt-0.5">{w.project} · {w.type} · {w.envs?.join(', ') || 'no envs'}</p>
                    </div>
                  </label>
                ))}
              </div>
              <div className="flex justify-end gap-2 pt-4">
                <Btn variant="ghost" onClick={onClose}>Cancel</Btn>
                <Btn onClick={doImport} disabled={importing || chosen.length === 0}>
                  {importing ? 'Importing…' : `Import ${chosen.length || ''}`}
                </Btn>
              </div>
            </>
          )
        )}
      </div>
    </div>
  )
}

// ── General Settings Tab ──────────────────────────────────────────────────────

const IPV4_RE = /^\d{1,3}(\.\d{1,3}){3}$/
// looksDockerInternal flags addresses that are almost certainly a Docker network,
// not the real host LAN IP: the Docker Desktop VM subnet (192.168.65.0/24) and the
// default docker0 bridge (172.17.0.0/16). Detection from inside a container hits
// these on Docker Desktop / non-Linux hosts, so we warn rather than trust them.
function looksDockerInternal(ip) {
  return /^192\.168\.65\./.test(ip) || /^172\.1[78]\./.test(ip)
}

// ── Domains & TLS Tab ─────────────────────────────────────────────────────────
// All app-domain / SSL / auto-URL settings, split out of General so each surface stays
// focused. Saves the same global app_settings keys — the PUT updates only the keys it
// sends, so DomainsTab and GeneralTab can each own a subset.
function DomainsTab() {
  const qc = useQueryClient()
  const { data: cfg = {}, isLoading } = useQuery({
    queryKey: ['general-settings'],
    queryFn: fetchGeneralSettings,
  })
  const [acmeEmail, setAcmeEmail] = useState('')
  const [riggerDomain, setRiggerDomain] = useState('')
  const [appHost, setAppHost] = useState('')
  const [appsBaseDomain, setAppsBaseDomain] = useState('')
  const [autoUrlMode, setAutoUrlMode] = useState('localhost')
  const [dnsProvider, setDnsProvider] = useState('')
  const [dnsToken, setDnsToken] = useState('')

  const saveMut = useMutation({
    mutationFn: () => updateGeneralSettings({
      acme_email: acmeEmail,
      rigger_domain: riggerDomain,
      app_host: appHost.trim(),
      apps_base_domain: appsBaseDomain.trim(),
      auto_url_mode: autoUrlMode,
      apps_dns_provider: dnsProvider,
      apps_dns_token: dnsToken,
    }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['general-settings'] }),
  })

  // Ask the backend to detect the Docker host's IP (host-networked lookup).
  const [detectErr, setDetectErr] = useState('')
  const detectMut = useMutation({
    mutationFn: detectHostIP,
    onSuccess: (d) => {
      if (d?.ip) {
        setAppHost(d.ip)
        setDetectErr(looksDockerInternal(d.ip)
          ? `Detected ${d.ip}, but that looks like a Docker-internal address (common on Docker Desktop / non-Linux hosts). Enter your host's real LAN/public IP manually.`
          : '')
      } else {
        setDetectErr(d?.error || 'Could not detect the host IP')
      }
    },
    onError: (e) => setDetectErr(e?.response?.data?.error || 'Detection failed'),
  })

  const [synced, setSynced] = useState(false)
  if (!isLoading && !synced && cfg.acme_email !== undefined) {
    setAcmeEmail(cfg.acme_email || '')
    setRiggerDomain(cfg.rigger_domain || '')
    setAppHost(cfg.app_host || '')
    setAppsBaseDomain(cfg.apps_base_domain || '')
    setAutoUrlMode(cfg.auto_url_mode || 'localhost')
    setDnsProvider(cfg.apps_dns_provider || '')
    setDnsToken(cfg.apps_dns_token || '')
    setSynced(true)
  }

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div className="space-y-8 max-w-2xl">
      {/* SSL / Let's Encrypt */}
      <div>
        <h2 className="text-base font-semibold text-content-strong mb-1">SSL Certificates — Let's Encrypt</h2>
        <p className="text-sm text-content-subtle mb-4">
          Traefik automatically issues and renews certificates via Let's Encrypt. Set your email
          below — it is sent to Let's Encrypt for cert expiry notifications and account recovery.
        </p>
        <div className="space-y-4 p-4 bg-surface border border-border rounded-xl">
          <div>
            <Label required>ACME email address</Label>
            <Input value={acmeEmail} onChange={setAcmeEmail} placeholder="admin@example.com" type="email" />
            <p className="text-xs text-content-subtle mt-1">
              Must match the <code className="font-mono text-xs">ACME_EMAIL</code> value in{' '}
              <code className="font-mono text-xs">src/.env</code>. Traefik reads it from there;
              this field stores it for reference and future automation.
            </p>
          </div>
          <div className="px-4 py-3 bg-warning-subtle/40 border border-warning-border/50 rounded-lg">
            <p className="text-xs text-warning-fg font-semibold mb-1">Requirements for SSL to work</p>
            <ul className="text-xs text-warning-fg space-y-0.5 list-disc pl-4">
              <li>Port 80 must be publicly reachable (for the HTTP-01 ACME challenge)</li>
              <li>Each domain must have a DNS A record pointing to this server</li>
              <li>Let's Encrypt rate limits: max 5 certs per domain per week</li>
            </ul>
          </div>
        </div>
      </div>

      {/* Rigger domain */}
      <div>
        <h2 className="text-base font-semibold text-content-strong mb-1">Rigger UI Domain</h2>
        <p className="text-sm text-content-subtle mb-4">
          Optionally expose the Rigger UI itself through Traefik with an SSL cert at the
          domain below. Leave blank to keep reaching Rigger on its port only.
        </p>
        <div className="p-4 bg-surface border border-border rounded-xl">
          <Label>Rigger UI domain</Label>
          <Input value={riggerDomain} onChange={setRiggerDomain} placeholder="rigger.example.com" />
          <p className="text-xs text-content-subtle mt-1">
            Leave blank to access Rigger UI on port{' '}
            <code className="font-mono text-xs">RIGGER_PORT</code> only.
          </p>
        </div>
      </div>

      {/* App host / IP — the local Docker host's address */}
      <div>
        <h2 className="text-base font-semibold text-content-strong mb-1">App host / IP</h2>
        <p className="text-sm text-content-subtle mb-4">
          The address users reach this host's apps at (the Docker host's IP or hostname). Drives
          both the <strong>Open app</strong> port links AND the auto-URL (sslip/nip) hostnames for
          <strong> locally-deployed</strong> environments. Seeded from the host IP at install; the
          server runs in a container so it can't re-detect this itself.{' '}
          <strong>Remote-host envs always use their own host's address</strong> — this only applies
          to the local host.
        </p>
        <div className="p-4 bg-surface border border-border rounded-xl">
          <Label>Host address</Label>
          <div className="flex gap-2">
            <Input value={appHost} onChange={v => { setAppHost(v); setDetectErr('') }}
              placeholder="192.168.1.50 or host.example.com" />
            <button type="button" onClick={() => detectMut.mutate()} disabled={detectMut.isPending}
              className="shrink-0 px-3 py-2 text-xs font-medium bg-surface-raised border border-border rounded-lg text-content hover:bg-surface-hover disabled:opacity-50"
              title="Query the Docker host for its real outbound IP">
              {detectMut.isPending ? 'Detecting…' : 'Detect'}
            </button>
            {typeof window !== 'undefined' && window.location?.hostname &&
             window.location.hostname !== appHost.trim() && (
              <button type="button" onClick={() => { setAppHost(window.location.hostname); setDetectErr('') }}
                className="shrink-0 px-3 py-2 text-xs font-medium bg-surface-raised border border-border rounded-lg text-content hover:bg-surface-hover"
                title="Use the address your browser reached Rigger at">
                Use {window.location.hostname}
              </button>
            )}
          </div>
          {detectErr && <p className="text-xs text-warning-fg mt-1">{detectErr}</p>}
          <p className="text-xs text-content-subtle mt-1">
            Usually set automatically from the host IP at install time. To change it:{' '}
            <strong>Detect</strong> asks the Docker host for its outbound IP (works on a native Linux
            host; on <strong>Docker Desktop</strong> it returns the internal VM IP, not your machine's
            LAN IP — enter it by hand there). <strong>Use {'{hostname}'}</strong> takes the address
            your browser reached Rigger at — correct when you browse to Rigger by IP. Needs an IP for
            sslip/nip auto-URLs. Blank ⇒ browser hostname for port links, <code className="font-mono text-xs">*.localhost</code> for auto-URLs.
          </p>
        </div>
      </div>

      {/* Application domains — the Render-style auto-URL base */}
      <div>
        <h2 className="text-base font-semibold text-content-strong mb-1">Application domains</h2>
        <p className="text-sm text-content-subtle mb-4">
          The base domain every deployed app gets a URL under —
          <code className="font-mono text-xs"> {'{workspace}-{app}-{env}'}.{appsBaseDomain || 'onrigger.com'}</code>.
          Workspaces can override this in <strong>Manage Workspace → SSL &amp; domain</strong>; an env can set
          its own custom domain. When no base domain is set, apps fall back to the auto-URL below so
          they're still reachable across machines.
        </p>
        <div className="space-y-4 p-4 bg-surface border border-border rounded-xl">
          <div>
            <Label>Apps base domain</Label>
            <Input value={appsBaseDomain} onChange={setAppsBaseDomain} placeholder="onrigger.com" />
            <p className="text-xs text-content-subtle mt-1">
              Point a wildcard DNS record <code className="font-mono text-xs">*.{appsBaseDomain || 'onrigger.com'}</code> at
              this server. Leave blank to use the auto-URL fallback instead.
            </p>
          </div>
          {appsBaseDomain.trim() && (
            <div>
              <Label>Wildcard cert (DNS-01)</Label>
              <select value={dnsProvider} onChange={e => setDnsProvider(e.target.value)}
                className="w-full px-3 py-2 bg-surface-raised border border-border rounded-lg text-sm text-content">
                <option value="">Per-host certs (HTTP-01 — needs public port 80)</option>
                <option value="cloudflare">Cloudflare — one wildcard cert for *.{appsBaseDomain.trim()}</option>
              </select>
              {dnsProvider === 'cloudflare' ? (
                <div className="mt-3 space-y-2">
                  <p className="text-xs text-content-subtle">
                    Traefik issues a single <code className="font-mono text-xs">*.{appsBaseDomain.trim()}</code> cert via DNS-01
                    (no port-80 challenge, no per-app rate limits).
                  </p>
                  <div>
                    <Label>Cloudflare API token</Label>
                    <Input type="password" value={dnsToken} onChange={setDnsToken}
                      placeholder="paste a Zone:DNS:Edit + Zone:Read token" />
                    <p className="text-xs text-content-subtle mt-1">
                      Create at Cloudflare → My Profile → API Tokens with <strong>Zone:DNS:Edit</strong> + <strong>Zone:Read</strong>,
                      scoped to <code className="font-mono text-xs">{appsBaseDomain.trim()}</code>. Stored encrypted-at-rest and
                      never shown again; saving applies it and briefly restarts the proxy. Leave the
                      masked value to keep the current token.
                    </p>
                  </div>
                </div>
              ) : (
                <p className="text-xs text-content-subtle mt-1">
                  Per-host: Traefik gets a separate Let&apos;s Encrypt cert for each{' '}
                  <code className="font-mono text-xs">{'{label}'}.{appsBaseDomain.trim()}</code> on first request via the
                  HTTP-01 challenge — needs <strong>port 80 publicly reachable</strong> and is subject to Let&apos;s Encrypt
                  rate limits. No API token required.
                </p>
              )}
            </div>
          )}
          {appsBaseDomain.trim() ? (
            <p className="text-xs text-content-subtle">
              A base domain is set, so every app routes under{' '}
              <code className="font-mono text-xs">*.{appsBaseDomain.trim()}</code> and the auto-URL
              (sslip/nip) fallback isn&apos;t used. Clear the base domain above to switch back to it.
            </p>
          ) : (
          <div>
            <Label>Auto-URL fallback (when no base domain)</Label>
            <select value={autoUrlMode} onChange={e => setAutoUrlMode(e.target.value)}
              className="w-full px-3 py-2 bg-surface-raised border border-border rounded-lg text-sm text-content">
              <option value="localhost">localhost (host-only — not reachable from other machines)</option>
              <option value="sslip">sslip.io (recommended — {'{label}'}.&lt;ip&gt;.sslip.io)</option>
              <option value="nip">nip.io</option>
              <option value="traefikme">traefik.me</option>
              <option value="off">off</option>
            </select>
            {autoUrlMode !== 'localhost' && autoUrlMode !== 'off' && (
              <p className="text-xs text-content-subtle mt-2">
                The host embedded in the magic-DNS name comes from <strong>App host / IP</strong> above
                (for local envs) or each env's own remote host — e.g.{' '}
                <code className="font-mono text-xs">myws-myapp-dev.{(appHost.trim() || '10.10.10.111')}.{autoUrlMode === 'nip' ? 'nip.io' : autoUrlMode === 'traefikme' ? 'traefik.me' : 'sslip.io'}</code>.
                {!appHost.trim() && <span className="text-warning-fg"> Set App host above for this to work across machines.</span>}
              </p>
            )}
            {autoUrlMode !== 'localhost' && autoUrlMode !== 'off' && appHost.trim() &&
             !IPV4_RE.test(appHost.trim()) && (
              <p className="text-xs text-warning-fg mt-1">
                <code className="font-mono text-xs">{appHost.trim()}</code> isn't an IP address — sslip/nip/traefik.me
                only echo back an <em>embedded IP</em>, so a hostname won't resolve. Click <strong>Detect</strong> above
                to fetch the host's IP, or switch to a base domain.
              </p>
            )}
          </div>
          )}
        </div>
      </div>

      {/* Save */}
      <div className="flex items-center gap-3">
        <Btn onClick={() => saveMut.mutate()} disabled={saveMut.isPending}>
          {saveMut.isPending ? 'Saving…' : 'Save settings'}
        </Btn>
        {saveMut.isSuccess && <span className="text-xs text-success-fg">✓ Saved</span>}
      </div>
    </div>
  )
}

function GeneralTab() {
  const qc = useQueryClient()
  const { data: cfg = {}, isLoading } = useQuery({
    queryKey: ['general-settings'],
    queryFn: fetchGeneralSettings,
  })
  const [keyMin, setKeyMin] = useState(3)
  const [keyMax, setKeyMax] = useState(4)
  // Password policy (auth Group A)
  const [pwMin, setPwMin] = useState(8)
  const [pwUpper, setPwUpper] = useState(false)
  const [pwLower, setPwLower] = useState(false)
  const [pwNumber, setPwNumber] = useState(false)
  const [pwSymbol, setPwSymbol] = useState(false)
  const [pwMaxAge, setPwMaxAge] = useState(0)

  const saveMut = useMutation({
    mutationFn: () => updateGeneralSettings({
      key_min_length: String(keyMin),
      key_max_length: String(Math.max(keyMin, keyMax)),
      pw_min_length: String(pwMin),
      pw_require_upper: pwUpper ? 'true' : 'false',
      pw_require_lower: pwLower ? 'true' : 'false',
      pw_require_number: pwNumber ? 'true' : 'false',
      pw_require_symbol: pwSymbol ? 'true' : 'false',
      pw_max_age_days: String(pwMaxAge),
    }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['general-settings'] }),
  })

  // Sync from loaded data when it arrives
  const [synced, setSynced] = useState(false)
  if (!isLoading && !synced && cfg.key_min_length !== undefined) {
    setKeyMin(Number(cfg.key_min_length) || 3)
    setKeyMax(Number(cfg.key_max_length) || 4)
    setPwMin(Number(cfg.pw_min_length) || 8)
    setPwUpper(cfg.pw_require_upper === 'true')
    setPwLower(cfg.pw_require_lower === 'true')
    setPwNumber(cfg.pw_require_number === 'true')
    setPwSymbol(cfg.pw_require_symbol === 'true')
    setPwMaxAge(Number(cfg.pw_max_age_days) || 0)
    setSynced(true)
  }

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div className="space-y-8 max-w-2xl">
      {/* Naming — key length */}
      <div>
        <h2 className="text-base font-semibold text-content-strong mb-1">Naming — resource keys</h2>
        <p className="text-sm text-content-subtle mb-4">
          Workspaces and projects are identified by a short lowercase <strong>key</strong> used for
          folders, URLs and Docker artifact names. Keys are auto-derived from the display name; these
          bounds control their length. Changing them affects only <em>new</em> keys — existing keys are immutable.
        </p>
        <div className="p-4 bg-surface border border-border rounded-xl grid grid-cols-2 gap-4 max-w-sm">
          <div>
            <Label>Min length</Label>
            <Input type="number" value={String(keyMin)} onChange={v => setKeyMin(Math.min(12, Math.max(1, Number(v) || 1)))} />
          </div>
          <div>
            <Label>Max length</Label>
            <Input type="number" value={String(keyMax)} onChange={v => setKeyMax(Math.min(12, Math.max(1, Number(v) || 1)))} />
          </div>
          <p className="col-span-2 text-xs text-content-subtle">
            1–12 characters. Collisions append a digit/letter within the max budget (e.g. <code className="font-mono">web → web2</code>).
            Larger setups may prefer 5–7 to reduce clashes.
          </p>
        </div>
      </div>

      {/* Security — password policy */}
      <div>
        <h2 className="text-base font-semibold text-content-strong mb-1">Security — password policy</h2>
        <p className="text-sm text-content-subtle mb-4">
          Rules enforced whenever a password is set — first-run setup, invite completion, admin
          reset, self-service change, and the forgot-password flow. Applies to <em>new</em> passwords;
          existing ones aren't re-checked until next change (or rotation, below).
        </p>
        <div className="space-y-4 p-4 bg-surface border border-border rounded-xl max-w-lg">
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label>Minimum length</Label>
              <Input type="number" value={String(pwMin)} onChange={v => setPwMin(Math.min(128, Math.max(6, Number(v) || 6)))} />
              <p className="text-xs text-content-subtle mt-1">At least 6.</p>
            </div>
            <div>
              <Label>Rotation (max age, days)</Label>
              <Input type="number" value={String(pwMaxAge)} onChange={v => setPwMaxAge(Math.max(0, Number(v) || 0))} />
              <p className="text-xs text-content-subtle mt-1">0 = never expire. Users are forced to change an expired password at next sign-in.</p>
            </div>
          </div>
          <div className="space-y-2">
            <Toggle checked={pwUpper}  onChange={setPwUpper}  label="Require an uppercase letter" />
            <Toggle checked={pwLower}  onChange={setPwLower}  label="Require a lowercase letter" />
            <Toggle checked={pwNumber} onChange={setPwNumber} label="Require a number" />
            <Toggle checked={pwSymbol} onChange={setPwSymbol} label="Require a symbol" />
          </div>
        </div>
      </div>

      {/* Save */}
      <div className="flex items-center gap-3">
        <Btn onClick={() => saveMut.mutate()} disabled={saveMut.isPending}>
          {saveMut.isPending ? 'Saving…' : 'Save settings'}
        </Btn>
        {saveMut.isSuccess && (
          <span className="text-xs text-success-fg">✓ Saved</span>
        )}
      </div>
    </div>
  )
}

// ── Alert Rules (Phase 6a) ──────────────────────────────────────────────────────

const SEVERITY_BADGE = {
  critical: 'bg-red-500/15 text-danger-fg border-danger-border/50',
  warning:  'bg-amber-500/15 text-warning-fg border-warning-border/50',
  info:     'bg-blue-500/15 text-info-fg border-info-border/50',
}

function RuleForm({ initial, meta, workspaces, channels = [], onSave, onCancel, saving }) {
  const conditions = meta?.conditions || []
  const isEdit = !!initial?.id

  const [name, setName]                 = useState(initial?.name || '')
  const [conditionType, setConditionType] = useState(initial?.condition_type || conditions[0]?.value || 'container_down')
  const [threshold, setThreshold]       = useState(initial?.threshold ?? 80)
  const [severity, setSeverity]         = useState(initial?.severity || 'warning')
  const [workspace, setWorkspace]       = useState(initial?.workspace || '')
  const [env, setEnv]                   = useState(initial?.env || '')
  const [cooldown, setCooldown]         = useState(initial?.cooldown_minutes ?? 15)
  const [enabled, setEnabled]           = useState(initial?.enabled ?? true)
  const [notifyIds, setNotifyIds]       = useState(initial?.notify_channel_ids || [])
  const [error, setError]               = useState('')

  function toggleChannel(id) {
    setNotifyIds(ids => ids.includes(id) ? ids.filter(x => x !== id) : [...ids, id])
  }

  const cond      = conditions.find(c => c.value === conditionType) || {}
  const isHost    = cond.scope === 'host'
  const isNumeric = !!cond.numeric

  // Alert targets key by the project's resource prefix ({workspace}_{project}).
  const wsPrefix = (w) => w.resource_prefix || `${w.workspace}_${w.name}`
  const wsObj = workspaces.find(w => wsPrefix(w) === workspace)
  const envOptions = [{ value: '', label: 'All environments' },
    ...((wsObj?.envs || []).map(e => ({ value: e, label: e })))]
  const wsOptions = [{ value: '', label: 'All projects' },
    ...workspaces.map(w => ({ value: wsPrefix(w), label: w.name }))]

  function changeWorkspace(v) {
    setWorkspace(v)
    if (!v) setEnv('') // "all workspaces" can't target a specific env
  }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim()) { setError('Name is required'); return }
    if (isNumeric && Number(threshold) <= 0) { setError('Threshold must be greater than 0'); return }
    const body = {
      name: name.trim(),
      condition_type: conditionType,
      threshold: isNumeric ? Number(threshold) : 0,
      workspace_key: initial?.workspace_key || '', // preserve a rule's workspace scope on edit
      workspace: isHost ? '' : workspace,
      env: (isHost || !workspace) ? '' : env,
      severity,
      cooldown_minutes: Number(cooldown) || 15,
      enabled,
      notify_channel_ids: notifyIds,
    }
    try { await onSave(body) }
    catch (err) { setError(err.response?.data?.error || 'Failed to save') }
  }

  return (
    <form onSubmit={submit} className="space-y-5">
      <div>
        <Label required>Rule name</Label>
        <Input value={name} onChange={setName} placeholder="Production stack down" />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div>
          <Label required>Condition</Label>
          <Select value={conditionType} onChange={setConditionType}
            options={conditions.map(c => ({ value: c.value, label: c.label }))} />
        </div>
        <div>
          <Label required>Severity</Label>
          <Select value={severity} onChange={setSeverity}
            options={(meta?.severities || ['info', 'warning', 'critical']).map(s => ({ value: s, label: s[0].toUpperCase() + s.slice(1) }))} />
        </div>
      </div>

      {isNumeric && (
        <div>
          <Label required>Threshold {cond.unit ? `(${cond.unit})` : ''}</Label>
          <Input value={threshold} onChange={v => setThreshold(v)} type="number" placeholder="80" />
        </div>
      )}

      {/* Targeting — hidden for host-scoped conditions (e.g. disk) */}
      {isHost ? (
        <div className="px-4 py-3 bg-surface-raised/40 border border-border-strong/60 rounded-lg">
          <p className="text-xs text-content-muted">This condition is evaluated against the <span className="text-content font-medium">host</span> and applies globally.</p>
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-4">
          <div>
            <Label>Workspace</Label>
            <Select value={workspace} onChange={changeWorkspace} options={wsOptions} />
          </div>
          <div>
            <Label>Environment</Label>
            <Select value={env} onChange={setEnv} options={envOptions} disabled={!workspace} />
            {!workspace && <p className="text-xs text-content-faint mt-1">Applies to all environments.</p>}
          </div>
        </div>
      )}

      <div className="grid grid-cols-2 gap-4 items-end">
        <div>
          <Label>Cooldown (minutes)</Label>
          <Input value={cooldown} onChange={v => setCooldown(v)} type="number" placeholder="15" />
          <p className="text-xs text-content-faint mt-1">Minimum gap before re-firing for the same target.</p>
        </div>
        <div className="pb-2">
          <Toggle checked={enabled} onChange={setEnabled} label={enabled ? 'Enabled' : 'Disabled'} />
        </div>
      </div>

      {/* Notify channels */}
      <div>
        <Label>Notify channels</Label>
        {channels.length === 0 ? (
          <p className="text-xs text-content-faint mt-1">
            No channels yet — add one on the <span className="text-content-muted">Notifications</span> tab to deliver this alert. The alert still shows in the inbox without a channel.
          </p>
        ) : (
          <div className="flex flex-wrap gap-2 mt-1">
            {channels.map(ch => {
              const on = notifyIds.includes(ch.id)
              return (
                <button
                  key={ch.id} type="button" onClick={() => toggleChannel(ch.id)}
                  className={`px-2.5 py-1 rounded-lg text-xs font-medium border transition-colors ${
                    on ? 'bg-brand-600/20 border-brand-500 text-brand-300'
                       : 'bg-surface-raised border-border-strong text-content-muted hover:text-content'
                  }`}
                >
                  {on ? '✓ ' : ''}{ch.name}
                  <span className="ml-1 text-content-subtle">{ch.type}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" onClick={onCancel}>Cancel</Btn>
        <Btn type="submit" disabled={saving}>{saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add rule'}</Btn>
      </div>
    </form>
  )
}

function RulesTab() {
  const qc = useQueryClient()
  const { data: rules = [], isLoading } = useQuery({ queryKey: ['alert-rules'], queryFn: fetchAlertRules })
  const { data: meta }       = useQuery({ queryKey: ['alert-meta'], queryFn: fetchAlertMeta })
  const currentWs = useWorkspaceStore(s => s.current)
  const { data: workspaces = [] } = useQuery({
    queryKey: ['projects', currentWs], queryFn: () => fetchProjects(currentWs), enabled: !!currentWs,
  })
  const { data: channels = [] } = useQuery({ queryKey: ['notification-channels'], queryFn: fetchNotificationChannels })
  const [modal, setModal]       = useState(null) // null | 'new' | { editing: rule }
  const [deleting, setDeleting] = useState(null)

  const condLabel = (v) => meta?.conditions?.find(c => c.value === v)?.label || v
  const condUnit  = (v) => meta?.conditions?.find(c => c.value === v)?.unit || ''

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateAlertRule(id, body) : createAlertRule(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['alert-rules'] }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteAlertRule(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['alert-rules'] }); setDeleting(null) },
  })
  const toggleMut = useMutation({
    mutationFn: (rule) => updateAlertRule(rule.id, { ...rule, enabled: !rule.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alert-rules'] }),
  })

  function handleSave(body) {
    return saveMut.mutateAsync({ id: modal?.editing?.id, body })
  }

  function targetLabel(r) {
    if (!r.workspace) return 'All workspaces'
    return r.env ? `${r.workspace} / ${r.env}` : `${r.workspace} (all envs)`
  }

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-content-strong">Alert Rules</h2>
          <p className="text-sm text-content-subtle mt-0.5">Conditions evaluated every 60s. Matches open an alert in the inbox; clearing auto-resolves it.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add rule</Btn>
      </div>

      {rules.length === 0 ? (
        <EmptyState
          icon="🔔"
          title="No alert rules yet"
          description="Create a rule to be notified when a container goes down, disk fills up, a backup fails, and more."
          action={<Btn onClick={() => setModal('new')}>＋ Add first rule</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {rules.map(r => (
            <div key={r.id} className="flex items-center gap-4 p-4 bg-surface border border-border rounded-xl">
              <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider border ${SEVERITY_BADGE[r.severity] || SEVERITY_BADGE.warning}`}>
                {r.severity}
              </span>
              <div className="flex-1 min-w-0">
                <p className="text-sm font-semibold text-content-strong truncate">{r.name}</p>
                <p className="text-xs text-content-subtle mt-0.5 truncate">
                  {condLabel(r.condition_type)}
                  {r.threshold > 0 ? ` ${r.threshold}${condUnit(r.condition_type)}` : ''} · {targetLabel(r)}
                  {r.notify_channel_ids?.length > 0 && <span className="text-content-muted"> · 🔔 {r.notify_channel_ids.length}</span>}
                </p>
              </div>
              <Toggle checked={r.enabled} onChange={() => toggleMut.mutate(r)} label="" />
              <div className="flex items-center gap-2">
                <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: r })}>Edit</Btn>
                <Btn variant="danger" size="sm" onClick={() => setDeleting(r)}>Delete</Btn>
              </div>
            </div>
          ))}
        </div>
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-surface border border-border rounded-xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add alert rule' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <RuleForm
              initial={modal === 'new' ? null : modal.editing}
              meta={meta}
              workspaces={workspaces}
              channels={channels}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

// ── Notification Channels (Phase 6b; Phase 3: scoping) ────────────────────────

function NotificationsTab() {
  const qc = useQueryClient()
  const { data: channels = [], isLoading } = useQuery({ queryKey: ['notification-channels'], queryFn: fetchNotificationChannels })
  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const [modal, setModal]       = useState(null)
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({})

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateNotificationChannel(id, body) : createNotificationChannel(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['notification-channels'] }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteNotificationChannel(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['notification-channels'] }); setDeleting(null) },
  })
  const toggleMut = useMutation({
    mutationFn: (ch) => updateNotificationChannel(ch.id, { ...ch, enabled: !ch.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notification-channels'] }),
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testNotificationChannel(id)
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Test failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 6000)
  }

  function handleSave(body) {
    return saveMut.mutateAsync({ id: modal?.editing?.id, body })
  }

  function summary(ch) {
    const c = typeof ch.config === 'string' ? safeParse(ch.config) : (ch.config || {})
    if (ch.type === 'email') return `${c.from || '—'} → ${c.to || '—'}`
    const urls = (c.urls || '').split(/[\n,]/).map(s => s.trim()).filter(Boolean)
    return urls.length ? `${urls.length} Apprise URL${urls.length > 1 ? 's' : ''}: ${urls[0].split('://')[0]}…` : 'no URLs'
  }

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-content-strong">Notification Channels</h2>
          <p className="text-sm text-content-subtle mt-0.5">Where alerts are delivered. Assign channels to rules on the Alert Rules tab.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add channel</Btn>
      </div>

      {channels.length === 0 ? (
        <EmptyState
          icon="📣"
          title="No notification channels"
          description="Add a channel to deliver alerts to Slack, Discord, email, and more."
          action={<Btn onClick={() => setModal('new')}>＋ Add first channel</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {channels.map(ch => {
            const ts = testStatus[ch.id]
            return (
              <div key={ch.id} className="flex items-center gap-4 p-4 bg-surface border border-border rounded-xl">
                <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider
                  ${ch.type === 'email' ? 'bg-cyan-100/70 text-cyan-700 dark:bg-cyan-900/60 dark:text-cyan-300' : 'bg-purple-100/70 text-purple-700 dark:bg-purple-900/60 dark:text-purple-300'}`}>
                  {ch.type}
                </span>
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2">
                    <p className="text-sm font-semibold text-content-strong truncate">{ch.name}</p>
                    {ch.owner_scope === 'global' ? (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint shrink-0" title={`Offered to: ${grantSummary(ch)}`}>shared · {grantSummary(ch)}</span>
                    ) : (
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40 shrink-0" title="Private to a workspace">workspace · {ch.owner_scope.replace(/^ws:/, '')}</span>
                    )}
                  </div>
                  <p className="text-xs text-content-subtle mt-0.5 truncate">{summary(ch)}</p>
                </div>
                {ts?.loading && <span className="text-xs text-content-subtle">Sending…</span>}
                {ts?.ok && <span className="text-xs text-success-fg">✓ Sent</span>}
                {ts?.error && <span className="text-xs text-danger-fg max-w-[200px] truncate" title={ts.error}>{ts.error}</span>}
                <Toggle checked={ch.enabled} onChange={() => toggleMut.mutate(ch)} label="" />
                <div className="flex items-center gap-2">
                  <Btn variant="ghost" size="sm" onClick={() => handleTest(ch.id)} disabled={ts?.loading}>Test</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: ch })}>Edit</Btn>
                  <Btn variant="danger" size="sm" onClick={() => setDeleting(ch)}>Delete</Btn>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-surface border border-border rounded-xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add notification channel' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <ChannelForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
              showGrants={modal === 'new' || modal.editing?.owner_scope === 'global'}
              workspaces={workspaces}
            />
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

function safeParse(s) { try { return JSON.parse(s) } catch { return {} } }

// ── Appearance ──────────────────────────────────────────────────────────────

// Tiny preview swatch per theme (literal colors — JS can't read CSS vars of an
// inactive theme; these mirror src/styles/themes.css).
const THEME_SWATCH = {
  system: { bg: 'linear-gradient(105deg, #0f172a 0 50%, #f8fafc 50% 100%)', fg: '#94a3b8', border: '#334155' },
  dark:   { bg: '#0b1220', fg: '#f3f4f6', border: '#1f2937' },
  light:  { bg: '#f8fafc', fg: '#0f172a', border: '#cbd5e1' },
}

function SettingsSection({ title, description, children }) {
  return (
    <section className="bg-surface border border-border rounded-xl p-5">
      <h2 className="text-sm font-bold text-content-strong">{title}</h2>
      {description && <p className="text-xs text-content-subtle mt-0.5 mb-4">{description}</p>}
      <div className={description ? '' : 'mt-4'}>{children}</div>
    </section>
  )
}

// PreferencesTab (Admin) — the instance-wide DEFAULT appearance (theme + typography).
// Workspaces override it; each user overrides it for themselves (Profile → Appearance).
function PreferencesTab() {
  const qc = useQueryClient()
  const { data: general } = useQuery({ queryKey: ['general-settings'], queryFn: fetchGeneralSettings })
  const apMut = useMutation({
    mutationFn: (blob) => updateGeneralSettings({ appearance_prefs: blob ? JSON.stringify(blob) : '' }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['general-settings'] }),
  })
  const cfMut = useMutation({
    mutationFn: (c) => updateGeneralSettings({ confirm_destructive: c.confirm, confirm_destructive_allow_override: c.allow }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['general-settings'] }); qc.invalidateQueries({ queryKey: ['confirm-settings'] }) },
  })
  let apValue, cfValue // undefined while loading → null/object when loaded
  if (general !== undefined) {
    try { apValue = general?.appearance_prefs ? JSON.parse(general.appearance_prefs) : null } catch { apValue = null }
    cfValue = { confirm: general?.confirm_destructive || '', allow: general?.confirm_destructive_allow_override || '' }
  }
  return (
    <div className="space-y-5 max-w-2xl">
      <SettingsSection
        title="Default appearance"
        description="The instance-wide default theme & typography. Workspaces can override it (Manage Workspace → Preferences), and each user can override it for themselves (Profile → Appearance)."
      >
        <AppearanceDefaultEditor
          value={apValue}
          onSave={(blob) => apMut.mutate(blob)}
          saving={apMut.isPending}
          savedOk={apMut.isSuccess}
          inheritLabel="No default (built-in)"
        />
      </SettingsSection>

      <SettingsSection
        title="Confirmations"
        description="Whether destructive actions (Inactivate, Down, Delete, …) prompt for confirmation. The instance-wide default; workspaces and users can narrow it unless you lock it."
      >
        <ConfirmDefaultEditor
          value={cfValue}
          onSave={(c) => cfMut.mutate(c)}
          saving={cfMut.isPending}
          savedOk={cfMut.isSuccess}
          allowInherit={false}
        />
      </SettingsSection>
    </div>
  )
}

export function AppearanceTab() {
  const { prefs, setPrefs, resolvedTheme, resetPrefs } = useTheme()
  const opt = (list) => list.map(o => ({ value: o.id, label: o.label }))

  return (
    <div className="space-y-5 max-w-2xl">
      <SettingsSection
        title="Color theme"
        description="Pick a scheme, or follow your operating system automatically."
      >
        <div className="grid grid-cols-3 gap-3">
          {THEMES.map(t => {
            const sw = THEME_SWATCH[t.id] || THEME_SWATCH.dark
            const active = prefs.theme === t.id
            return (
              <button
                key={t.id}
                onClick={() => setPrefs({ theme: t.id })}
                className={`text-left rounded-xl border p-3 transition-colors ${
                  active ? 'border-brand-500 ring-1 ring-brand-500/40' : 'border-border hover:border-border-strong'
                }`}
              >
                <div
                  className="h-12 rounded-lg mb-2 flex items-center justify-center"
                  style={{ background: sw.bg, border: `1px solid ${sw.border}` }}
                >
                  <span className="text-xs font-semibold" style={{ color: sw.fg }}>Aa</span>
                </div>
                <div className="text-sm font-medium text-content-strong">{t.label}</div>
                <div className="text-[11px] text-content-subtle">{t.hint}</div>
              </button>
            )
          })}
        </div>
        {prefs.theme === 'system' && (
          <p className="text-xs text-content-subtle mt-3">
            Following your system — currently showing <span className="text-content font-medium">{resolvedTheme}</span>.
          </p>
        )}
      </SettingsSection>

      <SettingsSection
        title="Typography"
        description="Fonts and overall density of the interface."
      >
        <div className="grid sm:grid-cols-3 gap-4">
          <div>
            <Label>Interface font</Label>
            <Select value={prefs.fontSans} onChange={v => setPrefs({ fontSans: v })} options={opt(FONT_SANS_OPTIONS)} />
          </div>
          <div>
            <Label>Monospace font</Label>
            <Select value={prefs.fontMono} onChange={v => setPrefs({ fontMono: v })} options={opt(FONT_MONO_OPTIONS)} />
          </div>
          <div>
            <Label>Density</Label>
            <Select value={prefs.density} onChange={v => setPrefs({ density: v })} options={opt(DENSITY_OPTIONS)} />
          </div>
        </div>
      </SettingsSection>

      <div>
        <Btn variant="secondary" onClick={resetPrefs}>Reset appearance to defaults</Btn>
      </div>
    </div>
  )
}

// LogsTerminalTab — per-user preferences for live log views + the container
// terminal. Split out of Appearance (which was getting crowded) into its own
// Account-settings tab: font size, line spacing, and the wrap / line-number
// defaults. All persist to the same user appearance prefs as the log toolbar's
// own toggles, so the two stay in sync.
export function LogsTerminalTab() {
  const { prefs, setPrefs } = useTheme()
  return (
    <div className="space-y-5 max-w-2xl">
      <SettingsSection
        title="Display"
        description="Applies to live log views and the container terminal."
      >
        <div className="grid sm:grid-cols-2 gap-5 items-start">
          <div>
            <Label>Font size — {prefs.logFontSize}px</Label>
            <input
              type="range" min={LOG_FONT_SIZE_MIN} max={LOG_FONT_SIZE_MAX} step={1}
              value={prefs.logFontSize}
              onChange={e => setPrefs({ logFontSize: Number(e.target.value) })}
              className="w-full accent-brand-500 mt-1"
            />
            <div className="mt-3">
              <Label>Line spacing</Label>
              <Select
                value={String(prefs.logLineHeight)}
                onChange={v => setPrefs({ logLineHeight: Number(v) })}
                options={[
                  { value: '1.3', label: 'Compact' },
                  { value: '1.5', label: 'Normal' },
                  { value: '1.7', label: 'Relaxed' },
                ]}
              />
            </div>
          </div>
          <div>
            <Label>Preview</Label>
            <pre
              className="bg-canvas border border-border rounded-lg p-3 overflow-hidden text-content"
              style={{
                fontFamily: prefs.fontMono === 'system' ? 'ui-monospace, monospace' : `'${prefs.fontMono}', monospace`,
                fontSize: `${prefs.logFontSize}px`,
                lineHeight: prefs.logLineHeight,
              }}
            >{`$ docker compose up -d
[+] Running 3/3
 ✔ Container db     Started
 ✔ Container cache  Started
 ✔ Container web    Started`}</pre>
          </div>
        </div>
      </SettingsSection>

      <SettingsSection
        title="Defaults"
        description="How log views open by default. You can still toggle these per-view from the log toolbar — your last choice is remembered here."
      >
        <div className="space-y-3">
          <Toggle
            checked={prefs.logWrap}
            onChange={v => setPrefs({ logWrap: v })}
            label="Wrap long lines"
          />
          <Toggle
            checked={prefs.logRowNumbers}
            onChange={v => setPrefs({ logRowNumbers: v })}
            label="Show line numbers"
          />
        </div>
      </SettingsSection>
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

// ── Users (Phase 5 RBAC) ──────────────────────────────────────────────────────

// Global roles (Users tab). Workspace/project access is granted separately via
// workspace membership — see the Members tab on each workspace.
const ROLE_OPTIONS = globalRoleOptions
const ROLE_BADGE = {
  superadmin: 'bg-red-100/70 text-red-700 border-red-200 dark:bg-red-950/60 dark:text-red-300 dark:border-red-800/40',
  user:       'bg-slate-100/70 text-slate-700 border-slate-200 dark:bg-slate-800/60 dark:text-slate-300 dark:border-slate-700/40',
}

// Workspace-tier roles offered when granting access at invite time.
const INVITE_WS_ROLES = wsRoleOptions

function InviteForm({ onSave, onCancel, saving }) {
  const [email, setEmail]       = useState('')
  const [username, setUsername] = useState('')
  const [role, setRole]         = useState('user')
  const [workspace, setWorkspace] = useState('')
  const [project, setProject]     = useState('')
  const [wsRole, setWsRole]       = useState('viewer')
  const [error, setError]       = useState('')

  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const { data: projects = [] } = useQuery({
    queryKey: ['projects', workspace], queryFn: () => fetchProjects(workspace), enabled: !!workspace,
  })

  const grantsAccess = role === 'user' // super-admins already see everything

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!email.trim()) { setError('Email is required'); return }
    const body = { email: email.trim(), username: username.trim(), role }
    if (grantsAccess && workspace) {
      body.workspace = workspace
      body.ws_role = wsRole
      if (project) body.project = project
    }
    try { await onSave(body) }
    catch (err) { setError(err.response?.data?.error || 'Failed to invite') }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div>
        <Label required>Email</Label>
        <Input value={email} onChange={setEmail} type="email" placeholder="jane@example.com" />
        <p className="text-xs text-content-faint mt-1">An invite link is sent here; the user sets their own password.</p>
      </div>
      <div>
        <Label>Display name <span className="font-normal normal-case">(optional)</span></Label>
        <Input value={username} onChange={setUsername} placeholder="defaults to the part before @" />
      </div>
      <div className="space-y-2">
        <Label required>Global role</Label>
        <Select value={role} onChange={setRole} options={ROLE_OPTIONS} />
        <RoleHelp scope="global" />
      </div>

      {grantsAccess && (
        <div className="rounded-xl border border-border bg-surface/40 p-3 space-y-3">
          <p className="text-xs font-medium uppercase tracking-wider text-content-subtle">Grant access <span className="font-normal normal-case">(optional)</span></p>
          <div>
            <Label>Workspace</Label>
            <Select value={workspace} onChange={(v) => { setWorkspace(v); setProject('') }}
              options={[{ value: '', label: '— no access yet —' }, ...workspaces.map(w => ({ value: w.key, label: w.name || w.key }))]} />
          </div>
          {workspace && (
            <>
              <div>
                <Label>Limit to one project <span className="font-normal normal-case">(optional)</span></Label>
                <Select value={project} onChange={setProject}
                  options={[{ value: '', label: 'Whole workspace' }, ...projects.map(p => ({ value: p.name, label: p.config?.project?.name || p.name }))]} />
              </div>
              <div className="space-y-2">
                <Label required>Role in this {project ? 'project' : 'workspace'}</Label>
                <Select value={wsRole} onChange={setWsRole} options={INVITE_WS_ROLES} />
                <RoleHelp scope="workspace" />
              </div>
            </>
          )}
          {!workspace && <p className="text-xs text-content-faint">Leave empty to invite without access — they'll see a "request access" message until you add them to a workspace.</p>}
        </div>
      )}

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" onClick={onCancel}>Cancel</Btn>
        <Btn type="submit" disabled={saving}>{saving ? 'Inviting…' : 'Send invite'}</Btn>
      </div>
    </form>
  )
}

function EditUserForm({ initial, onSave, onCancel, saving }) {
  const [role, setRole]         = useState(initial?.role || 'user')
  const [password, setPassword] = useState('')
  const [error, setError]       = useState('')

  async function submit(e) {
    e.preventDefault()
    setError('')
    try { await onSave({ role, password }) }
    catch (err) { setError(err.response?.data?.error || 'Failed to save') }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div>
        <Label>Email</Label>
        <Input value={initial?.email || ''} onChange={() => {}} disabled />
      </div>
      <div>
        <Label required>Role</Label>
        <Select value={role} onChange={setRole} options={ROLE_OPTIONS} />
      </div>
      <div>
        <Label>Reset password <span className="font-normal normal-case">(optional)</span></Label>
        <Input value={password} onChange={setPassword} type="password" placeholder="(leave blank to keep)" />
      </div>
      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" onClick={onCancel}>Cancel</Btn>
        <Btn type="submit" disabled={saving}>{saving ? 'Saving…' : 'Save changes'}</Btn>
      </div>
    </form>
  )
}

function UsersTab() {
  const qc = useQueryClient()
  const me = useAuthStore(s => s.user)
  const { data: users = [], isLoading } = useQuery({ queryKey: ['users'], queryFn: fetchUsers })
  const [modal, setModal]       = useState(null)   // 'new' | { editing }
  const [deleting, setDeleting] = useState(null)
  const [link, setLink]         = useState(null)    // { email, url } — surfaced invite link (no SMTP)
  const invalidate = () => qc.invalidateQueries({ queryKey: ['users'] })

  const inviteMut = useMutation({
    mutationFn: (body) => inviteUser(body),
    onSuccess: (data) => { invalidate(); setModal(null); if (data?.invite_link) setLink({ email: data.user?.email, url: data.invite_link }) },
  })
  const editMut = useMutation({
    mutationFn: ({ id, body }) => updateUser(id, body),
    onSuccess: () => { invalidate(); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteUser(id),
    onSuccess: () => { invalidate(); setDeleting(null) },
  })
  const resendMut = useMutation({
    mutationFn: (id) => resendInvite(id),
    onSuccess: (data, id) => { const u = users.find(x => x.id === id); if (data?.invite_link) setLink({ email: u?.email, url: data.invite_link }) },
  })

  const fmtLast = (s) => s ? new Date(s).toLocaleString() : 'never'
  function statusBadge(u) {
    if (u.status === 'invited') return <span className="text-[10px] px-1.5 py-0.5 rounded border bg-amber-100/70 text-amber-700 border-amber-200 dark:bg-amber-950/60 dark:text-amber-300 dark:border-amber-800/40">invited</span>
    if (!u.email_verified) return <span className="text-[10px] px-1.5 py-0.5 rounded border bg-surface-raised border-border-strong text-content-faint">unverified</span>
    return <span className="text-[10px] px-1.5 py-0.5 rounded border bg-emerald-100/70 text-emerald-700 border-emerald-200 dark:bg-emerald-950/60 dark:text-emerald-300 dark:border-emerald-800/40">verified</span>
  }

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-content-strong">Users</h2>
          <p className="text-sm text-content-subtle mt-0.5">Invite users by email and set their global role. Per-workspace access is managed on each workspace's Members tab.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Invite user</Btn>
      </div>

      <div className="space-y-2">
        {users.map(u => (
          <div key={u.id} className="flex items-center gap-4 p-4 bg-surface border border-border rounded-xl">
            <div className="flex-shrink-0 w-8 h-8 rounded-full bg-surface-raised flex items-center justify-center text-sm">{(u.email[0] || u.username[0] || '?').toUpperCase()}</div>
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2 flex-wrap">
                <p className="text-sm font-semibold text-content-strong truncate">{u.email || u.username}</p>
                {me?.uid === u.id && <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint">you</span>}
                <span className={`text-[10px] px-1.5 py-0.5 rounded border uppercase tracking-wider ${ROLE_BADGE[u.role] || ROLE_BADGE.viewer}`}>{u.role}</span>
                {statusBadge(u)}
              </div>
              <p className="text-xs text-content-subtle mt-0.5">
                {u.username}{u.phone ? ` · ${u.phone}` : ''} · {u.status === 'invited' ? 'invite pending' : `last login: ${fmtLast(u.last_login_at)}`}
              </p>
            </div>
            <div className="flex items-center gap-2">
              {u.status === 'invited'
                ? <Btn variant="ghost" size="sm" onClick={() => resendMut.mutate(u.id)} disabled={resendMut.isPending}>Resend invite</Btn>
                : <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: u })}>Edit</Btn>}
              <Btn variant="danger" size="sm" onClick={() => setDeleting(u)} disabled={me?.uid === u.id}>Delete</Btn>
            </div>
          </div>
        ))}
      </div>

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={() => setModal(null)}>
          <div className="bg-surface border border-border rounded-xl w-full max-w-md mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Invite user' : `Edit "${modal.editing.email}"`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            {modal === 'new'
              ? <InviteForm onSave={(body) => inviteMut.mutateAsync(body)} onCancel={() => setModal(null)} saving={inviteMut.isPending} />
              : <EditUserForm initial={modal.editing} onSave={(body) => editMut.mutateAsync({ id: modal.editing.id, body })} onCancel={() => setModal(null)} saving={editMut.isPending} />}
          </div>
        </div>
      )}

      {link && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={() => setLink(null)}>
          <div className="bg-surface border border-border rounded-xl w-full max-w-lg p-6 space-y-3" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Invite link {link.email ? `for ${link.email}` : ''}</h3>
            <p className="text-sm text-content-muted">No system email is configured, so share this link with the user to complete their registration:</p>
            <div className="bg-surface-raised border border-border-strong rounded-lg p-3 text-xs font-mono break-all text-content">{link.url}</div>
            <div className="flex gap-2 justify-end">
              <Btn variant="secondary" onClick={() => navigator.clipboard?.writeText(link.url)}>Copy</Btn>
              <Btn onClick={() => setLink(null)}>Done</Btn>
            </div>
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`user "${deleting.email || deleting.username}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

// ── System email (transactional SMTP for invites/verification) ────────────────

function SystemEmailTab() {
  const qc = useQueryClient()
  const { data: cfg, isLoading } = useQuery({ queryKey: ['system-email'], queryFn: fetchSystemEmail })
  const [f, setF] = useState(null)
  const [pw, setPw] = useState('')
  const [saved, setSaved] = useState(false)
  if (!f && cfg) setF({ host: cfg.host || '', port: cfg.port || '587', username: cfg.username || '', from: cfg.from || '', tls: cfg.tls !== false, base_url: cfg.base_url || '' })

  const mut = useMutation({
    mutationFn: () => updateSystemEmail({ ...f, password: pw }),
    onSuccess: () => { setPw(''); setSaved(true); qc.invalidateQueries({ queryKey: ['system-email'] }) },
  })
  const set = (k, v) => { setF(s => ({ ...s, [k]: v })); setSaved(false) }
  if (isLoading || !f) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <div className="max-w-xl">
      <div className="mb-6">
        <h2 className="text-base font-semibold text-content-strong">System email</h2>
        <p className="text-sm text-content-subtle mt-0.5">SMTP Rigger uses to send invite &amp; verification links. Separate from alert notification channels. If left empty, links are surfaced in the UI instead of emailed.</p>
      </div>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <div className="grid grid-cols-2 gap-4">
          <div><Label>SMTP host</Label><Input value={f.host} onChange={v => set('host', v)} placeholder="smtp.example.com" /></div>
          <div><Label>Port</Label><Input value={f.port} onChange={v => set('port', v)} type="number" placeholder="587" /></div>
          <div><Label>Username</Label><Input value={f.username} onChange={v => set('username', v)} placeholder="apikey" /></div>
          <div><Label>Password</Label><Input value={pw} onChange={setPw} type="password" placeholder={cfg.has_password ? '(unchanged)' : '••••••••'} /></div>
          <div><Label>From address</Label><Input value={f.from} onChange={v => set('from', v)} placeholder="Rigger <noreply@example.com>" /></div>
          <div><Label>Public base URL</Label><Input value={f.base_url} onChange={v => set('base_url', v)} placeholder="https://rigger.example.com" /></div>
        </div>
        <Toggle checked={f.tls} onChange={v => set('tls', v)} label="Use STARTTLS (recommended; port 465 uses implicit TLS)" />
        <p className="text-xs text-content-faint">Base URL is used to build links in emails; leave blank to derive from the request host.</p>
        <div className="flex items-center gap-3">
          <Btn onClick={() => mut.mutate()} disabled={mut.isPending}>{mut.isPending ? 'Saving…' : 'Save'}</Btn>
          {saved && <span className="text-xs text-success-fg">✓ Saved</span>}
        </div>
      </div>
    </div>
  )
}

// Tab order mirrors the Manage Workspace screen for the shared resource tabs
// ── Updates (self-update Phase 2) ─────────────────────────────────────────────
// Shows the running Rigger version, checks GitHub Releases for a newer one, and
// renders its changelog. Applying the update (Phase 3) is not wired yet, so for
// now this surfaces the manual command.
function UpdatesTab() {
  const { data: ver } = useQuery({ queryKey: ['rigger-version'], queryFn: fetchVersion, staleTime: Infinity })
  const [checking, setChecking] = useState(false)
  const [info, setInfo] = useState(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const [restarting, setRestarting] = useState(false)

  const current = info?.current || ver?.version || '—'
  const isDev = info ? info.dev : (ver?.version === 'dev')

  async function runCheck() {
    setChecking(true); setErr('')
    try {
      setInfo(await checkUpdates(true))
    } catch (e) {
      setErr(e?.response?.data?.error || 'Failed to check for updates')
    } finally {
      setChecking(false)
    }
  }

  // After apply/rollback the container restarts; poll until it's reachable again
  // on a (usually different) version, then reload. Falls back to a reload after 2m.
  function waitForRestart() {
    const start = Date.now()
    const iv = setInterval(async () => {
      try {
        const v = await fetchVersion()
        if (v?.version !== current || Date.now() - start > 25000) {
          clearInterval(iv); window.location.reload()
        }
      } catch { /* mid-restart — keep polling */ }
      if (Date.now() - start > 120000) { clearInterval(iv); window.location.reload() }
    }, 4000)
  }

  async function doApply(tag) {
    if (!window.confirm(`Update Rigger to ${tag}? Rigger will restart and this page will reconnect automatically.`)) return
    setBusy(true); setErr('')
    try {
      await applyUpdate(tag)
      setRestarting(true); waitForRestart()
    } catch (e) {
      setErr(e?.response?.data?.error || 'Update failed to start'); setBusy(false)
    }
  }

  async function doRollback() {
    if (!window.confirm('Roll back to the previously running version? Rigger will restart.')) return
    setBusy(true); setErr('')
    try {
      await rollbackUpdate()
      setRestarting(true); waitForRestart()
    } catch (e) {
      setErr(e?.response?.data?.error || 'Rollback failed to start'); setBusy(false)
    }
  }

  return (
    <div className="max-w-2xl">
      <div className="mb-6">
        <h2 className="text-base font-semibold text-content-strong">Updates</h2>
        <p className="text-sm text-content-subtle mt-0.5">Check for a newer Rigger release and see what changed.</p>
      </div>

      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        {restarting && (
          <div className="rounded-lg bg-warning-subtle/40 border border-warning-border/60 px-3 py-2 text-sm text-warning-fg flex items-center gap-2">
            <span className="inline-block w-2 h-2 rounded-full bg-amber-400 animate-pulse" />
            Rigger is updating and will restart — this page will reconnect automatically.
          </div>
        )}
        <div className="flex items-center justify-between gap-4">
          <div className="min-w-0">
            <p className="text-xs font-semibold uppercase tracking-wide text-content-muted">Current version</p>
            <p className="text-lg font-semibold text-content-strong mt-0.5">
              {current === 'dev' ? 'dev build' : current}
              {ver?.commit && <span className="ml-2 text-xs font-mono text-content-faint">{ver.commit.slice(0, 7)}</span>}
            </p>
          </div>
          <button onClick={runCheck} disabled={checking || restarting}
            className="shrink-0 text-sm font-semibold px-3 py-2 rounded-lg bg-brand-600 hover:bg-brand-700 text-white transition-colors disabled:opacity-50">
            {checking ? 'Checking…' : 'Check for updates'}
          </button>
        </div>

        {isDev && (
          <p className="text-xs text-content-faint border-t border-border pt-3">
            This is a source/dev build — version comparison only works for released images installed from GHCR.
          </p>
        )}

        {err && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{err}</p>}

        {info && !err && (
          <div className="border-t border-border pt-4 space-y-3">
            {info.error ? (
              <p className="text-sm text-warning-fg">Couldn’t check: {info.error}</p>
            ) : !info.latest ? (
              <p className="text-sm text-content-subtle">No published releases found yet.</p>
            ) : info.update_available ? (
              <>
                <div className="flex items-center gap-2">
                  <span className="text-xs font-semibold px-2 py-0.5 rounded-full bg-success-subtle text-success-fg border border-success-border/60">Update available</span>
                  <span className="text-sm text-content-strong font-semibold">{info.latest}</span>
                  {info.published_at && <span className="text-xs text-content-faint">· {new Date(info.published_at).toLocaleDateString()}</span>}
                </div>
                {info.notes && (
                  <div>
                    <p className="text-xs font-semibold uppercase tracking-wide text-content-muted mb-1">Changelog</p>
                    <pre className="text-xs whitespace-pre-wrap break-words bg-surface-raised border border-border-strong rounded-lg p-3 max-h-72 overflow-y-auto text-content">{info.notes}</pre>
                  </div>
                )}
                <div className="flex items-center gap-3 flex-wrap">
                  <button onClick={() => doApply(info.latest)} disabled={busy || restarting}
                    className="text-sm font-semibold px-3 py-2 rounded-lg bg-brand-600 hover:bg-brand-700 text-white transition-colors disabled:opacity-50">
                    {busy ? 'Starting…' : `Update to ${info.latest}`}
                  </button>
                  {info.html_url && <a href={info.html_url} target="_blank" rel="noreferrer" className="text-xs text-brand-400 hover:text-brand-300">View release on GitHub ↗</a>}
                </div>
                <p className="text-[11px] text-content-faint">Or update manually: <code className="font-mono">cd &lt;install&gt;/src &amp;&amp; docker compose pull &amp;&amp; docker compose up -d</code></p>
              </>
            ) : (
              <p className="text-sm text-success-fg">✓ You’re on the latest release ({info.latest}).</p>
            )}
          </div>
        )}

        <div className="border-t border-border pt-3">
          <button onClick={doRollback} disabled={busy || restarting}
            className="text-xs text-content-muted hover:text-content-strong disabled:opacity-50">
            ↩ Roll back to previous version
          </button>
          <p className="text-[11px] text-content-faint mt-0.5">Re-runs the version that was active before the last update.</p>
        </div>
      </div>
    </div>
  )
}

// (Remote Hosts → Docker Registries → Backup Targets → Notifications → Alert
// Rules) so the two settings surfaces feel consistent.
const TABS = [
  { id: 'general',        label: 'General',          icon: '⚙' },
  { id: 'domains',        label: 'Domains & TLS',    icon: '🌐' },
  { id: 'preferences',    label: 'Preferences',      icon: '🎨' },
  { id: 'users',          label: 'Users',            icon: '👤' },
  { id: 'access-requests', label: 'Access Requests', icon: '🔑' },
  { id: 'system-email',   label: 'System Email',     icon: '✉' },
  { id: 'api-keys',       label: 'API Keys',         icon: '🔑' },
  { id: 'updates',        label: 'Updates',          icon: '⬆' },
  { group: 'Shared resources' },
  { id: 'hosts',          label: 'Remote Hosts',     icon: '🖥' },
  { id: 'registries',     label: 'Docker Registries', icon: '📦' },
  { id: 'backup-targets', label: 'Backup Targets',   icon: '💾' },
  { id: 'notifications',  label: 'Notifications',    icon: '📣' },
  { id: 'alerts',         label: 'Alert Rules',      icon: '🚨' },
]

export default function SettingsPage() {
  const [tab, setTab] = useState('general')

  return (
    <Layout>
      <div className="max-w-7xl mx-auto px-6 py-8">
        {/* Page header */}
        <div className="mb-6">
          <h1 className="text-xl font-bold text-content-strong">Admin</h1>
          <p className="text-sm text-content-subtle mt-0.5">Global settings — users, SSL, integrations, and shared resources for the whole control plane.</p>
        </div>

        <VerticalTabs tabs={TABS} active={tab} onChange={setTab}>
          {tab === 'general'        && <GeneralTab />}
          {tab === 'domains'        && <DomainsTab />}
          {tab === 'preferences'    && <PreferencesTab />}
          {tab === 'users'          && <UsersTab />}
          {tab === 'access-requests' && <AccessRequestsInbox />}
          {tab === 'system-email'   && <SystemEmailTab />}
          {tab === 'api-keys'       && <ApiKeysManager />}
          {tab === 'updates'        && <UpdatesTab />}
          {tab === 'alerts'         && <RulesTab />}
          {tab === 'notifications'  && <NotificationsTab />}
          {tab === 'registries'     && <RegistriesTab />}
          {tab === 'backup-targets' && <BackupTargetsTab />}
          {tab === 'hosts'          && <HostsTab />}
        </VerticalTabs>
      </div>
    </Layout>
  )
}
