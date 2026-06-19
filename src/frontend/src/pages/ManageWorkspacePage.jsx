import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
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
import {
  fetchWorkspaces, fetchProjects, renameWorkspaceTier, deleteWorkspaceTier, transferWorkspace,
  fetchWorkspaceHosts, createWorkspaceHost, updateWorkspaceHost, deleteWorkspaceHost, testWorkspaceHost,
  fetchWorkspaceRegistries, createWorkspaceRegistry, updateWorkspaceRegistry, deleteWorkspaceRegistry, testWorkspaceRegistry, markWorkspaceRegistrySystem,
  fetchWorkspaceBackupTargets, createWorkspaceBackupTarget, updateWorkspaceBackupTarget, deleteWorkspaceBackupTarget, testWorkspaceBackupTarget,
  fetchWorkspaceNotificationChannels, createWorkspaceNotificationChannel, updateWorkspaceNotificationChannel, deleteWorkspaceNotificationChannel, testWorkspaceNotificationChannel,
  fetchAlertMeta, fetchWorkspaceAlertRules, createWorkspaceAlertRule, updateWorkspaceAlertRule, deleteWorkspaceAlertRule,
  fetchWorkspaceSettings, updateWorkspaceSettings,
  fetchMemberCandidates, fetchWorkspaceMembers, setWorkspaceMember, removeWorkspaceMember, setProjectOverride, removeProjectOverride,
} from '../lib/api'
import { useWorkspaceStore } from '../store/workspace'
import { WS_ROLES, wsRoleOptions } from '../lib/roles'

const TABS = [
  { id: 'general',        label: 'General',          icon: '⚙' },
  { id: 'members',        label: 'Members',          icon: '👥' },
  { id: 'access-requests', label: 'Access Requests', icon: '🔑' },
  { id: 'api-keys',       label: 'API Keys',         icon: '🔑' },
  { group: 'Shared resources' },
  { id: 'hosts',          label: 'Remote Hosts',     icon: '🖥' },
  { id: 'registries',     label: 'Docker Registries', icon: '📦' },
  { id: 'backup-targets', label: 'Backup Targets',   icon: '💾' },
  { id: 'notifications',  label: 'Notifications',    icon: '📣' },
  { id: 'alerts',         label: 'Alert Rules',      icon: '🚨' },
  { group: 'Workspace' },
  { id: 'danger',         label: 'Danger Zone',      icon: '⚠', danger: true },
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
      <div className="max-w-7xl mx-auto px-6 py-8">
        <div className="mb-6">
          <p className="text-xs font-medium uppercase tracking-wider text-content-subtle">Manage workspace</p>
          <div className="flex items-center gap-2.5 mt-0.5">
            <h1 className="text-2xl font-bold text-content-strong">{ws?.name || workspace}</h1>
            <span className="text-xs font-mono text-content-faint px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong" title="Workspace key (folder / URL / Docker prefix)">{workspace}</span>
          </div>
          <p className="text-sm text-content-muted mt-1">{projects.length} project{projects.length !== 1 ? 's' : ''}</p>
        </div>

        <VerticalTabs tabs={TABS} active={tab} onChange={setTab}>
          {tab === 'general' && (
            <div className="space-y-8">
              <GeneralSection workspace={workspace} ws={ws} qc={qc} setCurrent={setCurrent} />
              <WorkspaceGeneralSettings workspace={workspace} qc={qc} />
              <WorkspaceDefaults workspace={workspace} qc={qc} />
              <WorkspaceAppearanceDefault workspace={workspace} qc={qc} />
            </div>
          )}
          {tab === 'members'        && <MembersSection workspace={workspace} projects={projects} qc={qc} />}
          {tab === 'access-requests' && <AccessRequestsInbox wsKey={workspace} />}
          {tab === 'hosts'          && <HostsSection workspace={workspace} qc={qc} />}
          {tab === 'registries'     && <RegistriesSection workspace={workspace} qc={qc} />}
          {tab === 'backup-targets' && <BackupTargetsSection workspace={workspace} qc={qc} />}
          {tab === 'notifications'  && <NotificationsSection workspace={workspace} qc={qc} />}
          {tab === 'alerts'         && <AlertRulesSection workspace={workspace} projects={projects} qc={qc} />}
          {tab === 'api-keys'       && <ApiKeysManager workspace={workspace} />}
          {tab === 'danger'         && <DangerZone workspace={workspace} ws={ws} projects={projects} others={others} qc={qc} setCurrent={setCurrent} navigate={navigate} />}
        </VerticalTabs>
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
  const [tiers, setTiers] = useState('')
  const [seeded, setSeeded] = useState(false)
  if (!seeded && saved) { setAcme(saved.acme_email || ''); setDomain(saved.domain || ''); setTiers(saved.env_tier_names || ''); setSeeded(true) }

  const mut = useMutation({
    mutationFn: () => updateWorkspaceSettings(workspace, { acme_email: acme.trim(), domain: domain.trim(), env_tier_names: tiers.trim() }),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKey }),
  })
  const dirty = saved && (acme.trim() !== (saved.acme_email || '') || domain.trim() !== (saved.domain || '') || tiers.trim() !== (saved.env_tier_names || ''))

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-3">SSL &amp; domain</h2>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">ACME email</label>
          <input value={acme} onChange={e => setAcme(e.target.value)} type="email" placeholder="ops@example.com"
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500" />
          <p className="text-xs text-content-subtle mt-1">Default Let's Encrypt registration email for this workspace's certificates. Overrides the instance-wide email (Settings → General); a project environment can override it again in its SSL settings. Blank inherits the global default.</p>
        </div>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Apps base domain</label>
          <input value={domain} onChange={e => setDomain(e.target.value)} placeholder="inherits the global default (Settings → General)"
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500" />
          <p className="text-xs text-content-subtle mt-1">Overrides the instance-wide <strong>Apps base domain</strong> (Settings → General) for this workspace only. Domain-routed environments get a URL of <code className="font-mono">{'{workspace}-{project}-{env}'}.{domain.trim() || '{base}'}</code> with an automatic Let&apos;s Encrypt cert. Leave blank to inherit the global default (or the auto-URL/<code className="font-mono">*.localhost</code> fallback when none is set). Needs a wildcard DNS record (<code className="font-mono">*.{domain.trim() || '{base}'}</code> → this host).</p>
        </div>
        <p className="text-xs text-content-faint">Per-hostname certs are issued on demand via Let&apos;s Encrypt HTTP-01; Traefik uses the global <code className="font-mono">ACME_EMAIL</code>.</p>
        <div className="pt-2 border-t border-border">
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1 mt-2">Environment tier order</label>
          <textarea value={tiers} onChange={e => setTiers(e.target.value)} rows={2}
            placeholder="dev, staging, qa, uat, preprod, prod"
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-brand-500" />
          <p className="text-xs text-content-subtle mt-1">Tier names from lowest to highest (comma or newline). Used to auto-guess each project's deploy order (dev → prod) for the release pipeline. A project can override with an explicit order. Leave blank for the built-in default.</p>
        </div>
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

// WorkspaceAppearanceDefault sets the workspace's default theme (W7). Members who
// haven't set a personal appearance inherit this; a per-user choice overrides it.
const WS_THEME_OPTIONS = [
  { value: '',       label: 'No default — use the global default' },
  { value: 'system', label: 'System' },
  { value: 'light',  label: 'Light' },
  { value: 'dark',   label: 'Dark' },
]
function WorkspaceAppearanceDefault({ workspace, qc }) {
  const settingsKey = ['ws-settings', workspace]
  const { data: saved } = useQuery({
    queryKey: settingsKey, queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace,
  })
  const [theme, setTheme] = useState(null) // null = not yet seeded
  if (theme === null && saved !== undefined) {
    let t = ''
    try { t = saved?.appearance_prefs ? (JSON.parse(saved.appearance_prefs).theme || '') : '' } catch { /* ignore */ }
    setTheme(t)
  }
  const mut = useMutation({
    mutationFn: () => updateWorkspaceSettings(workspace, { appearance_prefs: theme ? JSON.stringify({ theme }) : '' }),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKey }),
  })
  let savedTheme = ''
  try { savedTheme = saved?.appearance_prefs ? (JSON.parse(saved.appearance_prefs).theme || '') : '' } catch { /* ignore */ }
  const dirty = theme !== null && theme !== savedTheme

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-3">Default appearance</h2>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Default theme</label>
          <select value={theme ?? ''} onChange={e => setTheme(e.target.value)}
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500">
            {WS_THEME_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
          <p className="text-xs text-content-subtle mt-1">Applied to members who haven't set their own appearance. A personal choice (Account → Appearance) always overrides this.</p>
        </div>
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
        <div className="grid sm:grid-cols-5 gap-4">
          <div className="sm:col-span-4">
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Display name</label>
            <input
              value={name} onChange={e => setName(e.target.value)} maxLength={32}
              className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500"
            />
            <p className="text-xs text-content-subtle mt-1">1–32 chars; editable anytime.</p>
          </div>
          <div className="sm:col-span-1">
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Key <span className="font-normal normal-case text-content-faint">(fixed)</span></label>
            <input
              value={workspace} readOnly disabled
              className="w-full px-3 py-2 bg-surface-raised/60 border border-border-strong rounded-lg text-content-muted text-sm font-mono cursor-not-allowed"
            />
            <p className="text-xs text-content-subtle mt-1">Fixed identity.</p>
          </div>
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

const MEMBER_ROLES = wsRoleOptions
const OVERRIDE_ROLES = [
  { value: 'none', label: 'No access' },
  ...WS_ROLES.map(r => ({ value: r.value, label: r.label })),
]

// MembersSection — workspace membership + per-project overrides (Phase 5.2a).
// Non-admins see only workspaces they're a member of; per-project overrides
// (incl. "No access") refine access within. Roles take effect once enforcement
// (5.2b) lands.
function MembersSection({ workspace, projects, qc }) {
  const membersKey = ['ws-members', workspace]
  const { data: members = [], isLoading } = useQuery({ queryKey: membersKey, queryFn: () => fetchWorkspaceMembers(workspace), enabled: !!workspace })
  const { data: candidates = [] } = useQuery({ queryKey: ['member-candidates', workspace], queryFn: () => fetchMemberCandidates(workspace), enabled: !!workspace })
  const [expanded, setExpanded] = useState(null) // user_id with open overrides
  const [addUid, setAddUid] = useState('')
  const [addRole, setAddRole] = useState('viewer')
  const sel = 'px-2 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
  const invalidate = () => { qc.invalidateQueries({ queryKey: membersKey }); qc.invalidateQueries({ queryKey: ['member-candidates', workspace] }) }

  const setMut = useMutation({ mutationFn: ({ uid, role }) => setWorkspaceMember(workspace, uid, role), onSuccess: invalidate })
  const rmMut  = useMutation({ mutationFn: (uid) => removeWorkspaceMember(workspace, uid), onSuccess: invalidate })
  const ovSet  = useMutation({ mutationFn: ({ uid, proj, role }) => setProjectOverride(workspace, uid, proj, role), onSuccess: invalidate })
  const ovRm   = useMutation({ mutationFn: ({ uid, proj }) => removeProjectOverride(workspace, uid, proj), onSuccess: invalidate })

  const addable = candidates // server already excludes existing members + global admins

  function addMember() {
    if (!addUid) return
    setMut.mutate({ uid: Number(addUid), role: addRole })
    setAddUid('')
  }
  const projLabel = (key) => projects.find(p => p.name === key)?.config?.project?.name || key

  if (isLoading) return <div className="py-12 text-center text-content-subtle text-sm">Loading…</div>

  return (
    <section>
      <div className="mb-3">
        <h2 className="text-sm font-semibold text-content">Members</h2>
        <p className="text-xs text-content-subtle mt-0.5">Who can access this workspace and at what level. Global admins always have full access. Roles take effect once access control is enforced.</p>
      </div>

      <div className="bg-surface border border-border rounded-xl p-4 mb-4 flex items-center gap-2 flex-wrap">
        <span className="text-sm text-content-muted">Add member:</span>
        <select value={addUid} onChange={e => setAddUid(e.target.value)} className={sel}>
          <option value="">Select a user…</option>
          {addable.map(u => <option key={u.id} value={String(u.id)}>{u.email || u.username}</option>)}
        </select>
        <select value={addRole} onChange={e => setAddRole(e.target.value)} className={sel}>
          {MEMBER_ROLES.map(r => <option key={r.value} value={r.value}>{r.label}</option>)}
        </select>
        <button onClick={addMember} disabled={!addUid || setMut.isPending}
          className="px-3 py-1.5 text-sm font-medium rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white">Add</button>
        {addable.length === 0 && <span className="text-xs text-content-faint">All users are already members or global admins. Invite more from Admin → Users.</span>}
      </div>

      <RoleHelp scope="workspace" className="mb-4" />

      <div className="bg-surface border border-border rounded-xl">
        {members.length === 0 ? (
          <p className="p-5 text-sm text-content-subtle">No members yet. Global admins can already access this workspace; add operators/viewers above.</p>
        ) : (
          <div className="divide-y divide-border">
            {members.map(m => (
              <div key={m.user_id} className="p-4">
                <div className="flex items-center gap-3">
                  <div className="flex-shrink-0 w-8 h-8 rounded-full bg-surface-raised flex items-center justify-center text-sm">{(m.email[0] || m.username[0] || '?').toUpperCase()}</div>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm font-semibold text-content-strong truncate">{m.email || m.username}</p>
                    <p className="text-xs text-content-subtle mt-0.5">
                      {m.overrides.length > 0 ? `${m.overrides.length} project override${m.overrides.length !== 1 ? 's' : ''}` : 'workspace-wide role'}
                    </p>
                  </div>
                  <select value={m.role} onChange={e => setMut.mutate({ uid: m.user_id, role: e.target.value })} className={sel}>
                    {MEMBER_ROLES.map(r => <option key={r.value} value={r.value}>{r.label}</option>)}
                  </select>
                  <button onClick={() => setExpanded(x => x === m.user_id ? null : m.user_id)}
                    className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised">Per-project</button>
                  <button onClick={() => rmMut.mutate(m.user_id)}
                    className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-danger-fg hover:bg-danger/20">Remove</button>
                </div>

                {expanded === m.user_id && (
                  <div className="mt-3 ml-11 border border-border-strong rounded-lg p-3 space-y-2">
                    <p className="text-xs text-content-muted">Override this member's role on specific projects (defaults to their workspace role).</p>
                    {m.overrides.map(o => (
                      <div key={o.proj_key} className="flex items-center gap-2">
                        <span className="text-sm text-content flex-1 truncate">{projLabel(o.proj_key)}</span>
                        <select value={o.role} onChange={e => ovSet.mutate({ uid: m.user_id, proj: o.proj_key, role: e.target.value })} className={sel}>
                          {OVERRIDE_ROLES.map(r => <option key={r.value} value={r.value}>{r.label}</option>)}
                        </select>
                        <button onClick={() => ovRm.mutate({ uid: m.user_id, proj: o.proj_key })}
                          className="px-2 py-1 text-xs rounded-lg text-content-muted hover:text-danger-fg">✕</button>
                      </div>
                    ))}
                    <AddOverride projects={projects.filter(p => !m.overrides.some(o => o.proj_key === p.name))}
                      onAdd={(proj, role) => ovSet.mutate({ uid: m.user_id, proj, role })} />
                  </div>
                )}
              </div>
            ))}
          </div>
        )}
      </div>
    </section>
  )
}

function AddOverride({ projects, onAdd }) {
  const [proj, setProj] = useState('')
  const [role, setRole] = useState('viewer')
  const sel = 'px-2 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
  if (projects.length === 0) return <p className="text-xs text-content-faint">All projects have an override (or there are none).</p>
  return (
    <div className="flex items-center gap-2 pt-1">
      <select value={proj} onChange={e => setProj(e.target.value)} className={sel}>
        <option value="">Add project override…</option>
        {projects.map(p => <option key={p.name} value={p.name}>{p.config?.project?.name || p.name}</option>)}
      </select>
      <select value={role} onChange={e => setRole(e.target.value)} className={sel}>
        {OVERRIDE_ROLES.map(r => <option key={r.value} value={r.value}>{r.label}</option>)}
      </select>
      <button onClick={() => { if (proj) { onAdd(proj, role); setProj('') } }} disabled={!proj}
        className="px-3 py-1.5 text-xs font-medium rounded-lg border border-border-strong text-content hover:bg-surface-raised disabled:opacity-40">Add</button>
    </div>
  )
}

// WorkspaceDefaults — defaults new projects in this workspace inherit (Phase 4).
// Picked from the workspace's own resource pools; the New Project wizard prefills
// them. Stored as ids in workspace settings.
function WorkspaceDefaults({ workspace, qc }) {
  const settingsKey = ['ws-settings', workspace]
  const { data: saved } = useQuery({ queryKey: settingsKey, queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace })
  const { data: registries = [] } = useQuery({ queryKey: ['ws-registries', workspace], queryFn: () => fetchWorkspaceRegistries(workspace), enabled: !!workspace })
  const { data: hosts = [] } = useQuery({ queryKey: ['ws-hosts', workspace], queryFn: () => fetchWorkspaceHosts(workspace), enabled: !!workspace })
  const { data: targets = [] } = useQuery({ queryKey: ['ws-backup-targets', workspace], queryFn: () => fetchWorkspaceBackupTargets(workspace), enabled: !!workspace })

  const [reg, setReg]       = useState('')
  const [host, setHost]     = useState('')
  const [target, setTarget] = useState('')
  const [buildHost, setBuildHost] = useState('')
  const [seeded, setSeeded] = useState(false)
  if (!seeded && saved) {
    setReg(saved.default_registry_id || '')
    setHost(saved.default_host_id || '')
    setTarget(saved.default_backup_target_id || '')
    setBuildHost(saved.default_build_host_id || '')
    setSeeded(true)
  }

  const mut = useMutation({
    mutationFn: () => updateWorkspaceSettings(workspace, {
      default_registry_id: reg, default_host_id: host, default_backup_target_id: target,
      default_build_host_id: buildHost,
    }),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKey }),
  })
  const dirty = saved && (
    reg !== (saved.default_registry_id || '') ||
    host !== (saved.default_host_id || '') ||
    target !== (saved.default_backup_target_id || '') ||
    buildHost !== (saved.default_build_host_id || '')
  )
  const sel = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
  const lbl = 'block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1'

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-3">Defaults for new projects</h2>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <p className="text-xs text-content-subtle">New projects created in this workspace start with these selections. They can be changed per project in the create wizard.</p>
        <div>
          <label className={lbl}>Default registry</label>
          <select value={reg} onChange={e => setReg(e.target.value)} className={sel}>
            <option value="">No default (choose per project)</option>
            {registries.map(r => <option key={r.id} value={String(r.id)}>{r.name} ({r.url})</option>)}
          </select>
        </div>
        <div>
          <label className={lbl}>Default deploy host</label>
          <select value={host} onChange={e => setHost(e.target.value)} className={sel}>
            <option value="">No default (Local)</option>
            <option value="0">Local control plane</option>
            {hosts.filter(h => !h.build_only).map(h => <option key={h.id} value={String(h.id)}>{h.name} ({h.address})</option>)}
          </select>
        </div>
        <div>
          <label className={lbl}>Default backup target</label>
          <select value={target} onChange={e => setTarget(e.target.value)} className={sel}>
            <option value="">No default (Local filesystem)</option>
            {targets.map(t => <option key={t.id} value={String(t.id)}>{t.name} ({String(t.type).toUpperCase()})</option>)}
          </select>
        </div>
        <div>
          <label className={lbl}>Default build host</label>
          <select value={buildHost} onChange={e => setBuildHost(e.target.value)} className={sel}>
            <option value="">Inherit (build on each env's deploy host)</option>
            {hosts.map(h => <option key={h.id} value={String(h.id)}>{h.name} ({h.address})</option>)}
          </select>
          <p className="text-[11px] text-content-faint mt-1">Where image builds run for this workspace's projects (overridable per project). A dedicated builder must push to a registry the deploy targets can pull — set a system registry. Applies live, not just to new projects.</p>
        </div>
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
                      <HostCapabilityBadges host={host} showRole />
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
              showBuildOnly
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
  const sysMut = useMutation({
    mutationFn: ({ id, system }) => markWorkspaceRegistrySystem(workspace, id, system),
    onSuccess: () => qc.invalidateQueries({ queryKey: regsKey }),
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
          <p className="text-xs text-content-subtle mt-0.5">Registries this workspace's projects can pull/push images from: its own plus any shared by an administrator. Mark one of your own <span className="text-content-muted font-medium">system</span> to use it for projects here that set no registry (overrides the global default).</p>
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
                      {r.system && (
                        <span className="text-[10px] px-1.5 py-0.5 rounded bg-emerald-100/70 text-emerald-700 border border-emerald-200 dark:bg-emerald-950/60 dark:text-emerald-300 dark:border-emerald-800/40" title="Used by this workspace's projects that set no registry">★ system</span>
                      )}
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
                        <button onClick={() => sysMut.mutate({ id: r.id, system: !r.system })} disabled={sysMut.isPending}
                          title={r.system ? 'Stop using this as the workspace system registry' : 'Use for this workspace\'s projects that set no registry'}
                          className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised disabled:opacity-50">{r.system ? 'Unset system' : 'Set system'}</button>
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

function channelSummary(ch) {
  let c = ch.config
  if (typeof c === 'string') { try { c = JSON.parse(c) } catch { c = {} } }
  c = c || {}
  if (ch.type === 'email') return `${c.from || '—'} → ${c.to || '—'}`
  const urls = (c.urls || '').split(/[\n,]/).map(s => s.trim()).filter(Boolean)
  return urls.length ? `${urls.length} Apprise URL${urls.length > 1 ? 's' : ''}: ${urls[0].split('://')[0]}…` : 'no URLs'
}

function NotificationsSection({ workspace, qc }) {
  const chKey = ['ws-notification-channels', workspace]
  const { data: channels = [], isLoading } = useQuery({
    queryKey: chKey, queryFn: () => fetchWorkspaceNotificationChannels(workspace), enabled: !!workspace,
  })
  const [modal, setModal]       = useState(null)
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({})

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateWorkspaceNotificationChannel(workspace, id, body) : createWorkspaceNotificationChannel(workspace, body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: chKey }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteWorkspaceNotificationChannel(workspace, id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: chKey }); setDeleting(null) },
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testWorkspaceNotificationChannel(workspace, id)
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Test failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 6000)
  }

  const isOwned = (ch) => ch.owner_scope === `ws:${workspace}`

  return (
    <section>
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Notification channels</h2>
          <p className="text-xs text-content-subtle mt-0.5">Where this workspace's alerts are delivered: its own channels plus any shared by an administrator. Assign them to rules on the Alert Rules tab.</p>
        </div>
        <button onClick={() => setModal('new')}
          className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg border border-border-strong text-content hover:bg-surface-raised transition-colors">
          ＋ Add channel
        </button>
      </div>

      <div className="bg-surface border border-border rounded-xl">
        {isLoading ? (
          <p className="p-5 text-sm text-content-subtle">Loading…</p>
        ) : channels.length === 0 ? (
          <p className="p-5 text-sm text-content-subtle">No notification channels available. Add one for this workspace, or ask an admin to share a global channel with it.</p>
        ) : (
          <div className="divide-y divide-border">
            {channels.map(ch => {
              const owned = isOwned(ch)
              const ts = testStatus[ch.id]
              return (
                <div key={ch.id} className="flex items-center gap-3 p-4">
                  <span className={`flex-shrink-0 inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider
                    ${ch.type === 'email' ? 'bg-cyan-100/70 text-cyan-700 dark:bg-cyan-900/60 dark:text-cyan-300' : 'bg-purple-100/70 text-purple-700 dark:bg-purple-900/60 dark:text-purple-300'}`}>
                    {ch.type}
                  </span>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-semibold text-content-strong truncate">{ch.name}</p>
                      {owned
                        ? <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40 shrink-0">this workspace</span>
                        : <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint shrink-0" title="Shared by an administrator — managed in Settings">shared</span>}
                      {!ch.enabled && <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint shrink-0">disabled</span>}
                    </div>
                    <p className="text-xs text-content-subtle mt-0.5 truncate">{channelSummary(ch)}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    {ts?.loading && <span className="text-xs text-content-subtle">Sending…</span>}
                    {ts?.ok && <span className="text-xs text-success-fg">✓ Sent</span>}
                    {ts?.error && <span className="text-xs text-danger-fg max-w-[180px] truncate" title={ts.error}>{ts.error}</span>}
                    <button onClick={() => handleTest(ch.id)} disabled={ts?.loading}
                      className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised disabled:opacity-50">Test</button>
                    {owned ? (
                      <>
                        <button onClick={() => setModal({ editing: ch })}
                          className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised">Edit</button>
                        <button onClick={() => setDeleting(ch)}
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
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add channel' : `Edit “${modal.editing.name}”`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <ChannelForm
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
            <p className="text-sm text-content-muted">Alert rules using this channel will stop notifying it. This cannot be undone.</p>
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

const SEVERITY_BADGE = {
  info:     'bg-sky-100/70 text-sky-700 border-sky-200 dark:bg-sky-950/60 dark:text-sky-300 dark:border-sky-800/40',
  warning:  'bg-amber-100/70 text-amber-700 border-amber-200 dark:bg-amber-950/60 dark:text-amber-300 dark:border-amber-800/40',
  critical: 'bg-red-100/70 text-red-700 border-red-200 dark:bg-red-950/60 dark:text-red-300 dark:border-red-800/40',
}

// WsRuleForm — workspace-scoped alert rule form. The workspace tier is fixed
// (ws_key set server-side); only stack-scope conditions are offered; the project
// picker lists this workspace's projects; channels come from the workspace pool.
function WsRuleForm({ initial, meta, projects, channels, onSave, onCancel, saving }) {
  const conditions = (meta?.conditions || []).filter(c => c.scope !== 'host')
  const isEdit = !!initial?.id
  const [name, setName]                   = useState(initial?.name || '')
  const [conditionType, setConditionType] = useState(initial?.condition_type || conditions[0]?.value || 'container_down')
  const [threshold, setThreshold]         = useState(initial?.threshold ?? 80)
  const [severity, setSeverity]           = useState(initial?.severity || 'warning')
  const [project, setProject]             = useState(initial?.workspace || '')
  const [env, setEnv]                     = useState(initial?.env || '')
  const [cooldown, setCooldown]           = useState(initial?.cooldown_minutes ?? 15)
  const [enabled, setEnabled]             = useState(initial?.enabled ?? true)
  const [notifyIds, setNotifyIds]         = useState(initial?.notify_channel_ids || [])
  const [error, setError]                 = useState('')

  const cond = conditions.find(c => c.value === conditionType) || {}
  const isNumeric = !!cond.numeric
  const projObj = projects.find(p => p.name === project)
  const inp = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
  const lbl = 'block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1'

  function toggleChannel(id) {
    setNotifyIds(ids => ids.includes(id) ? ids.filter(x => x !== id) : [...ids, id])
  }
  function changeProject(v) { setProject(v); if (!v) setEnv('') }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim()) { setError('Name is required'); return }
    if (isNumeric && Number(threshold) <= 0) { setError('Threshold must be greater than 0'); return }
    const body = {
      name: name.trim(),
      condition_type: conditionType,
      threshold: isNumeric ? Number(threshold) : 0,
      workspace: project,                 // project key within this workspace ('' = all projects)
      env: project ? env : '',
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
        <label className={lbl}>Rule name</label>
        <input value={name} onChange={e => setName(e.target.value)} placeholder="Production stack down" className={inp} />
      </div>
      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className={lbl}>Condition</label>
          <select value={conditionType} onChange={e => setConditionType(e.target.value)} className={inp}>
            {conditions.map(c => <option key={c.value} value={c.value}>{c.label}</option>)}
          </select>
        </div>
        <div>
          <label className={lbl}>Severity</label>
          <select value={severity} onChange={e => setSeverity(e.target.value)} className={inp}>
            {(meta?.severities || ['info', 'warning', 'critical']).map(s => <option key={s} value={s}>{s[0].toUpperCase() + s.slice(1)}</option>)}
          </select>
        </div>
      </div>

      {isNumeric && (
        <div>
          <label className={lbl}>Threshold {cond.unit ? `(${cond.unit})` : ''}</label>
          <input value={threshold} onChange={e => setThreshold(e.target.value)} type="number" placeholder="80" className={inp} />
        </div>
      )}

      <div className="grid grid-cols-2 gap-4">
        <div>
          <label className={lbl}>Project</label>
          <select value={project} onChange={e => changeProject(e.target.value)} className={inp}>
            <option value="">All projects</option>
            {projects.map(p => <option key={p.name} value={p.name}>{p.config?.project?.name || p.name}</option>)}
          </select>
        </div>
        <div>
          <label className={lbl}>Environment</label>
          <select value={env} onChange={e => setEnv(e.target.value)} disabled={!project} className={`${inp} disabled:opacity-50`}>
            <option value="">All environments</option>
            {(projObj?.envs || []).map(en => <option key={en} value={en}>{en}</option>)}
          </select>
          {!project && <p className="text-xs text-content-faint mt-1">Applies to all projects in this workspace.</p>}
        </div>
      </div>

      <div className="grid grid-cols-2 gap-4 items-end">
        <div>
          <label className={lbl}>Cooldown (minutes)</label>
          <input value={cooldown} onChange={e => setCooldown(e.target.value)} type="number" placeholder="15" className={inp} />
        </div>
        <label className="flex items-center gap-2 cursor-pointer pb-2">
          <input type="checkbox" checked={enabled} onChange={e => setEnabled(e.target.checked)} className="accent-brand-500" />
          <span className="text-sm text-content">{enabled ? 'Enabled' : 'Disabled'}</span>
        </label>
      </div>

      <div>
        <label className={lbl}>Notify channels</label>
        {channels.length === 0 ? (
          <p className="text-xs text-content-faint mt-1">No channels in this workspace yet — add one on the Notifications tab. The alert still shows in the inbox without a channel.</p>
        ) : (
          <div className="flex flex-wrap gap-2 mt-1">
            {channels.map(ch => {
              const on = notifyIds.includes(ch.id)
              return (
                <button key={ch.id} type="button" onClick={() => toggleChannel(ch.id)}
                  className={`px-2.5 py-1 rounded-lg text-xs font-medium border transition-colors ${on ? 'bg-brand-600/20 border-brand-500 text-brand-300' : 'bg-surface-raised border-border-strong text-content-muted hover:text-content'}`}>
                  {on ? '✓ ' : ''}{ch.name}<span className="ml-1 text-content-subtle">{ch.type}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <button type="button" onClick={onCancel} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
        <button type="submit" disabled={saving} className="px-4 py-2 text-sm font-semibold rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white">
          {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add rule'}
        </button>
      </div>
    </form>
  )
}

function AlertRulesSection({ workspace, projects, qc }) {
  const rulesKey = ['ws-alert-rules', workspace]
  const { data: rules = [], isLoading } = useQuery({ queryKey: rulesKey, queryFn: () => fetchWorkspaceAlertRules(workspace), enabled: !!workspace })
  const { data: meta } = useQuery({ queryKey: ['alert-meta'], queryFn: fetchAlertMeta })
  const { data: channels = [] } = useQuery({ queryKey: ['ws-notification-channels', workspace], queryFn: () => fetchWorkspaceNotificationChannels(workspace), enabled: !!workspace })
  const [modal, setModal]       = useState(null)
  const [deleting, setDeleting] = useState(null)

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateWorkspaceAlertRule(workspace, id, body) : createWorkspaceAlertRule(workspace, body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: rulesKey }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteWorkspaceAlertRule(workspace, id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: rulesKey }); setDeleting(null) },
  })
  const toggleMut = useMutation({
    mutationFn: (rule) => updateWorkspaceAlertRule(workspace, rule.id, { ...rule, enabled: !rule.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: rulesKey }),
  })

  const condLabel = (v) => meta?.conditions?.find(c => c.value === v)?.label || v
  const condUnit  = (v) => meta?.conditions?.find(c => c.value === v)?.unit || ''
  const projLabel = (key) => projects.find(p => p.name === key)?.config?.project?.name || key
  const targetLabel = (r) => !r.workspace ? 'All projects' : (r.env ? `${projLabel(r.workspace)} / ${r.env}` : `${projLabel(r.workspace)} (all envs)`)

  return (
    <section>
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Alert rules</h2>
          <p className="text-xs text-content-subtle mt-0.5">Per-project conditions for this workspace, evaluated every 60s. Host/infra rules are managed globally in Settings.</p>
        </div>
        <button onClick={() => setModal('new')}
          className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg border border-border-strong text-content hover:bg-surface-raised transition-colors">
          ＋ Add rule
        </button>
      </div>

      <div className="bg-surface border border-border rounded-xl">
        {isLoading ? (
          <p className="p-5 text-sm text-content-subtle">Loading…</p>
        ) : rules.length === 0 ? (
          <p className="p-5 text-sm text-content-subtle">No alert rules for this workspace yet. Add one to be notified when a container goes down, a backup goes stale, CPU/memory spikes, and more.</p>
        ) : (
          <div className="divide-y divide-border">
            {rules.map(r => (
              <div key={r.id} className="flex items-center gap-3 p-4">
                <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider border ${SEVERITY_BADGE[r.severity] || SEVERITY_BADGE.warning}`}>
                  {r.severity}
                </span>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-semibold text-content-strong truncate">{r.name}</p>
                  <p className="text-xs text-content-subtle mt-0.5 truncate">
                    {condLabel(r.condition_type)}{r.threshold > 0 ? ` ${r.threshold}${condUnit(r.condition_type)}` : ''} · {targetLabel(r)}
                    {r.notify_channel_ids?.length > 0 && <span className="text-content-muted"> · 🔔 {r.notify_channel_ids.length}</span>}
                  </p>
                </div>
                <label className="flex items-center gap-2 cursor-pointer">
                  <input type="checkbox" checked={r.enabled} onChange={() => toggleMut.mutate(r)} className="accent-brand-500" />
                </label>
                <div className="flex items-center gap-2">
                  <button onClick={() => setModal({ editing: r })} className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised">Edit</button>
                  <button onClick={() => setDeleting(r)} className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-danger-fg hover:bg-danger/20">Delete</button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={() => setModal(null)}>
          <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add alert rule' : `Edit “${modal.editing.name}”`}</h3>
              <button onClick={() => setModal(null)} className="text-content-subtle hover:text-content-strong text-xl">×</button>
            </div>
            <WsRuleForm
              initial={modal === 'new' ? null : modal.editing}
              meta={meta} projects={projects} channels={channels}
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
            <p className="text-sm text-content-muted">This cannot be undone.</p>
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
        <DeleteModal workspace={workspace} ws={ws} projects={projects} others={others}
          onClose={() => setMode(null)}
          onDone={(target) => { qc.invalidateQueries({ queryKey: ['workspaces'] }); setCurrent(target || ''); navigate('/') }} />
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

// DeleteModal handles deleting a workspace. When it still holds projects, it
// first asks how to proceed — Transfer the projects to another workspace, or
// Destroy everything (item 7).
function DeleteModal({ workspace, ws, projects, others = [], onClose, onDone }) {
  const hasProjects = projects.length > 0
  const [step, setStep] = useState(hasProjects ? 'choose' : 'destroy')
  const [target, setTarget] = useState(others[0]?.key || '')
  const [confirm, setConfirm] = useState('')
  const [err, setErr] = useState('')

  const destroyMut = useMutation({
    mutationFn: () => deleteWorkspaceTier(workspace),
    onSuccess: () => onDone(''),
    onError: (e) => setErr(e.response?.data?.error || 'Delete failed'),
  })
  // Transferring ALL projects removes the now-empty source workspace server-side.
  const transferMut = useMutation({
    mutationFn: () => transferWorkspace(workspace, target, []),
    onSuccess: () => onDone(target),
    onError: (e) => setErr(e.response?.data?.error || 'Transfer failed'),
  })

  const wrap = (children) => (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-danger-border/60 rounded-2xl w-full max-w-md p-6 space-y-4" onClick={e => e.stopPropagation()}>{children}</div>
    </div>
  )

  if (step === 'choose') {
    return wrap(<>
      <h3 className="font-semibold text-content-strong">There are active projects in this workspace</h3>
      <p className="text-sm text-content-muted">
        “{ws?.name || workspace}” still holds <span className="text-content-strong font-medium">{projects.length} project{projects.length !== 1 ? 's' : ''}</span>. How would you like to proceed?
      </p>
      <div className="space-y-2 pt-1">
        <button onClick={() => { setErr(''); setStep('transfer') }}
          className="w-full text-left p-4 rounded-xl border border-border-strong hover:border-brand-500 hover:bg-surface-raised transition-colors">
          <p className="text-sm font-semibold text-content-strong">Transfer projects</p>
          <p className="text-xs text-content-subtle mt-0.5">Move them to another workspace you administer (containers keep running), then delete this one.</p>
        </button>
        <button onClick={() => { setErr(''); setStep('destroy') }}
          className="w-full text-left p-4 rounded-xl border border-danger-border/60 hover:bg-danger-subtle/30 transition-colors">
          <p className="text-sm font-semibold text-danger-fg">Destroy everything</p>
          <p className="text-xs text-content-subtle mt-0.5">Permanently stop and remove every project, environment, container, network and volume.</p>
        </button>
      </div>
      <div className="flex justify-end pt-1">
        <button onClick={onClose} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
      </div>
    </>)
  }

  if (step === 'transfer') {
    if (others.length === 0) {
      return wrap(<>
        <h3 className="font-semibold text-content-strong">No other workspace available</h3>
        <p className="text-sm text-content-muted">
          You don't have access to any other workspace to transfer these projects to. Request access to another workspace
          (via <span className="text-content">Request access</span> in your account menu) before deleting this one — or choose Destroy to remove everything.
        </p>
        <div className="flex gap-2 justify-end">
          <button onClick={() => setStep('choose')} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Back</button>
        </div>
      </>)
    }
    return wrap(<>
      <h3 className="font-semibold text-content-strong">Transfer projects, then delete</h3>
      <p className="text-sm text-content-muted">All {projects.length} project{projects.length !== 1 ? 's' : ''} move to the chosen workspace; this workspace is removed afterwards. Resource prefixes (and running containers) are unchanged.</p>
      <div>
        <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Target workspace</label>
        <select value={target} onChange={e => setTarget(e.target.value)}
          className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500">
          {others.map(o => <option key={o.key} value={o.key}>{o.name} ({o.key})</option>)}
        </select>
      </div>
      {err && <p className="text-sm text-danger-fg">{err}</p>}
      <div className="flex gap-2 justify-end">
        <button onClick={() => setStep('choose')} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Back</button>
        <button onClick={() => transferMut.mutate()} disabled={!target || transferMut.isPending}
          className="px-4 py-2 text-sm font-semibold rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white">
          {transferMut.isPending ? 'Transferring…' : 'Transfer & delete'}
        </button>
      </div>
    </>)
  }

  // step === 'destroy'
  return wrap(<>
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
      {hasProjects
        ? <button onClick={() => setStep('choose')} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Back</button>
        : <button onClick={onClose} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>}
      <button onClick={() => destroyMut.mutate()} disabled={confirm !== workspace || destroyMut.isPending}
        className="px-4 py-2 text-sm font-semibold rounded-lg bg-red-800 hover:bg-red-700 disabled:opacity-40 text-white">
        {destroyMut.isPending ? 'Deleting…' : 'Delete workspace'}
      </button>
    </div>
  </>)
}
