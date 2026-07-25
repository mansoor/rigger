import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import HostForm from '../components/HostForm'
import HostHealthModal from '../components/HostHealthModal'
import HostCapabilityBadges from '../components/HostBadges'
import RegistryForm from '../components/RegistryForm'
import BackupTargetForm from '../components/BackupTargetForm'
import ChannelForm from '../components/ChannelForm'
import AccessRequestsInbox from '../components/AccessRequestsInbox'
import ApiKeysManager from '../components/ApiKeysManager'
import VerticalTabs from '../components/VerticalTabs'
import RoleHelp from '../components/RoleHelp'
import AppearanceDefaultEditor from '../components/AppearanceDefaultEditor'
import ConfirmDefaultEditor from '../components/ConfirmDefaultEditor'
import {
  fetchWorkspaces, fetchProjects, renameWorkspaceTier, deleteWorkspaceTier, transferWorkspace,
  fetchWorkspaceHosts, createWorkspaceHost, updateWorkspaceHost, deleteWorkspaceHost, testWorkspaceHost, installWorkspaceHostEdge, fetchWorkspaceHostRouteImpact,
  fetchWorkspaceRegistries, createWorkspaceRegistry, updateWorkspaceRegistry, deleteWorkspaceRegistry, testWorkspaceRegistry, markWorkspaceRegistrySystem,
  fetchWorkspaceGitProviders, createWorkspaceGitProvider, updateWorkspaceGitProvider, deleteWorkspaceGitProvider, testWorkspaceGitProvider, startGitHubAppManifest, gitHubAppInstallURL,
  fetchWorkspaceBackupTargets, createWorkspaceBackupTarget, updateWorkspaceBackupTarget, deleteWorkspaceBackupTarget, testWorkspaceBackupTarget,
  fetchWorkspaceNotificationChannels, createWorkspaceNotificationChannel, updateWorkspaceNotificationChannel, deleteWorkspaceNotificationChannel, testWorkspaceNotificationChannel,
  fetchWorkspaceAccessLists, createWorkspaceAccessList, updateWorkspaceAccessList, deleteWorkspaceAccessList, fetchProxyPlugins,
  fetchAlertMeta, fetchWorkspaceAlertRules, createWorkspaceAlertRule, updateWorkspaceAlertRule, deleteWorkspaceAlertRule,
  fetchWorkspaceSettings, updateWorkspaceSettings,
  fetchMemberCandidates, fetchWorkspaceMembers, setWorkspaceMember, removeWorkspaceMember, setProjectOverride, removeProjectOverride,
} from '../lib/api'
import { useWorkspaceStore } from '../store/workspace'
import { WS_ROLES, wsRoleOptions } from '../lib/roles'
import { Hint, Checkbox, Btn, IconBtn, CloseBtn, CONTROL } from '../components/ui'

const TABS = [
  { id: 'general',        label: 'General',          icon: '⚙' },
  { id: 'preferences',    label: 'Preferences',      icon: '🎨' },
  { id: 'members',        label: 'Members',          icon: '👥' },
  { id: 'access-requests', label: 'Access Requests', icon: '🔑' },
  { id: 'api-keys',       label: 'API Keys',         icon: '🔑' },
  { group: 'Shared resources' },
  { id: 'domains',        label: 'Domains & TLS',    icon: '🌐' },
  { id: 'hosts',          label: 'Remote Hosts',     icon: '🖥' },
  { id: 'registries',     label: 'Docker Registries', icon: '📦' },
  { id: 'git',            label: 'Git', icon: '🔑' },
  { id: 'backup-targets', label: 'Backup Targets',   icon: '💾' },
  { id: 'notifications',  label: 'Notifications',    icon: '📣' },
  { id: 'access-lists',   label: 'Access Lists',     icon: '🔒' },
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
  const [tab, setTab] = useState(() => {
    const t = new URLSearchParams(window.location.search).get('tab')
    return TABS.some(x => x.id === t) ? t : 'general'
  })

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
              <WorkspaceTierOrder workspace={workspace} qc={qc} />
              <WorkspaceDefaults workspace={workspace} qc={qc} />
            </div>
          )}
          {tab === 'domains'        && <WorkspaceDomainsSettings workspace={workspace} qc={qc} />}
          {tab === 'preferences'    && (
            <div className="space-y-8">
              <WorkspaceAppearanceDefault workspace={workspace} qc={qc} />
              <WorkspaceConfirmDefault workspace={workspace} qc={qc} />
            </div>
          )}
          {tab === 'members'        && <MembersSection workspace={workspace} projects={projects} qc={qc} />}
          {tab === 'access-requests' && <AccessRequestsInbox wsKey={workspace} />}
          {tab === 'hosts'          && <HostsSection workspace={workspace} qc={qc} />}
          {tab === 'registries'     && <RegistriesSection workspace={workspace} qc={qc} />}
          {tab === 'git'            && <GitSection workspace={workspace} qc={qc} />}
          {tab === 'backup-targets' && <BackupTargetsSection workspace={workspace} qc={qc} />}
          {tab === 'notifications'  && <NotificationsSection workspace={workspace} qc={qc} />}
          {tab === 'access-lists'   && <AccessListsSection workspace={workspace} qc={qc} />}
          {tab === 'alerts'         && <AlertRulesSection workspace={workspace} projects={projects} qc={qc} />}
          {tab === 'api-keys'       && <ApiKeysManager workspace={workspace} />}
          {tab === 'danger'         && <DangerZone workspace={workspace} ws={ws} projects={projects} others={others} qc={qc} setCurrent={setCurrent} navigate={navigate} />}
        </VerticalTabs>
      </div>
    </Layout>
  )
}

// WorkspaceDomainsSettings — workspace-scoped domain/SSL overrides (ACME email, base
// domain, wildcard DNS-01 provider + Cloudflare token, auto-URL fallback). Resolved
// workspace → global. Lives on its own "Domains & TLS" tab, mirroring Admin.
function WorkspaceDomainsSettings({ workspace, qc }) {
  const settingsKey = ['ws-settings', workspace]
  const { data: saved } = useQuery({
    queryKey: settingsKey, queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace,
  })
  const [acme, setAcme] = useState('')
  const [domain, setDomain] = useState('')
  // Domain/TLS override parity with the admin Domains & TLS tab (resolved workspace → global).
  const [autoMode, setAutoMode] = useState('')   // "" = inherit global
  const [autoHost, setAutoHost] = useState('')
  const [dnsProvider, setDnsProvider] = useState('') // "" = inherit global
  const [dnsToken, setDnsToken] = useState('') // masked sentinel when already set
  const [keepPorts, setKeepPorts] = useState(false) // keep web host ports under Traefik (default: strip)
  const [manageDns, setManageDns] = useState(true)  // auto-manage public DNS A records for public hosts
  const [seeded, setSeeded] = useState(false)
  if (!seeded && saved) {
    setAcme(saved.acme_email || ''); setDomain(saved.domain || '')
    setAutoMode(saved.auto_url_mode || ''); setAutoHost(saved.auto_url_host || ''); setDnsProvider(saved.apps_dns_provider || '')
    setDnsToken(saved.apps_dns_token || '')
    setKeepPorts(saved.keep_host_ports_under_traefik === 'true')
    setManageDns(saved.apps_manage_dns !== 'false')
    setSeeded(true)
  }

  const mut = useMutation({
    mutationFn: () => updateWorkspaceSettings(workspace, {
      acme_email: acme.trim(), domain: domain.trim(),
      auto_url_mode: autoMode, auto_url_host: autoHost.trim(), apps_dns_provider: dnsProvider,
      apps_dns_token: dnsToken, // backend keeps current on blank/masked, encrypts a new value
      keep_host_ports_under_traefik: keepPorts ? 'true' : 'false',
      apps_manage_dns: manageDns ? 'true' : 'false',
    }),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKey }),
  })
  const dirty = saved && (acme.trim() !== (saved.acme_email || '') || domain.trim() !== (saved.domain || '')
    || autoMode !== (saved.auto_url_mode || '') || autoHost.trim() !== (saved.auto_url_host || '') || dnsProvider !== (saved.apps_dns_provider || '')
    || dnsToken !== (saved.apps_dns_token || '')
    || keepPorts !== (saved.keep_host_ports_under_traefik === 'true')
    || manageDns !== (saved.apps_manage_dns !== 'false'))

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-3">SSL &amp; domain</h2>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">ACME email</label>
          <input value={acme} onChange={e => setAcme(e.target.value)} type="email" placeholder="ops@example.com"
            className={`${CONTROL} w-full`} />
          <Hint>Default Let's Encrypt registration email for this workspace's certificates. Overrides the instance-wide email (Settings → General); a project environment can override it again in its SSL settings. Blank inherits the global default.</Hint>
        </div>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Apps base domain</label>
          <input value={domain} onChange={e => setDomain(e.target.value)} placeholder="inherits the global default (Settings → General)"
            className={`${CONTROL} w-full`} />
          <Hint>Overrides the instance-wide <strong>Apps base domain</strong> (Settings → General) for this workspace only. Domain-routed environments get a URL of <code className="font-mono">{'{workspace}-{project}-{env}'}.{domain.trim() || '{base}'}</code> with an automatic Let&apos;s Encrypt cert. Leave blank to inherit the global default (or the auto-URL/<code className="font-mono">*.localhost</code> fallback when none is set). Needs a wildcard DNS record (<code className="font-mono">*.{domain.trim() || '{base}'}</code> → this host).</Hint>
        </div>
        {/* Wildcard cert provider — shown when this workspace overrides the base domain
            (mirrors Admin → General). Cloudflare + a per-workspace token issues a
            *.{domain} cert under the workspace's OWN zone, out-of-band (no clash with the
            shared global resolver). Blank inherits the global provider. */}
        {domain.trim() && (
          <div>
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Wildcard cert (DNS-01)</label>
            <select value={dnsProvider} onChange={e => setDnsProvider(e.target.value)}
              className={`${CONTROL} w-full`}>
              <option value="">Inherit global (Settings → General — per-host HTTP-01 unless the global picks Cloudflare)</option>
              <option value="cloudflare">Cloudflare — one wildcard cert for *.{domain.trim()}</option>
            </select>
            {dnsProvider === 'cloudflare' ? (
              <div className="mt-3">
                <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Cloudflare API token</label>
                <input type="password" value={dnsToken} onChange={e => setDnsToken(e.target.value)}
                  placeholder="paste a Zone:DNS:Edit + Zone:Read token"
                  className={`${CONTROL} w-full font-mono`} />
                <Hint>Scoped to <code className="font-mono">{domain.trim()}</code> (Cloudflare → My Profile → API Tokens, <strong>Zone:DNS:Edit</strong> + <strong>Zone:Read</strong>). Rigger issues a single <code className="font-mono">*.{domain.trim()}</code> cert under this workspace&apos;s own zone, out-of-band (no Traefik restart, no clash with the global token). Stored <strong>encrypted at rest</strong> and never shown again — leave the masked value to keep the current token. Leave blank to fall back to the global Cloudflare token.</Hint>
              </div>
            ) : (
              <Hint>Overrides the instance-wide DNS-01 provider for this workspace. <strong>Cloudflare</strong> issues a single <code className="font-mono">*.{domain.trim()}</code> cert (no port-80 challenge, no per-app rate limits).</Hint>
            )}
          </div>
        )}
        {/* Auto-manage public DNS records for public-host apps (model #2 / direct). */}
        <label className="flex items-start gap-2.5 cursor-pointer select-none">
          <input type="checkbox" checked={manageDns} onChange={e => setManageDns(e.target.checked)} className="accent-brand-500 mt-0.5" />
          <span>
            <span className="text-sm text-content">Auto-manage DNS records</span>
            <Hint className="mt-0.5">When a base domain + Cloudflare token are set, Rigger upserts an A record (<code className="font-mono">{'{app}'}.{domain.trim() || '{base}'}</code> → the host&apos;s IP) on deploy for apps on <strong>public</strong> hosts, so they resolve straight to that host. Uncheck if you manage DNS yourself.</Hint>
          </span>
        </label>
        {/* Auto-URL fallback (when no base domain) — mirrors Admin → General. */}
        {!domain.trim() && (
          <div>
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Auto-URL fallback (when no base domain)</label>
            <select value={autoMode} onChange={e => setAutoMode(e.target.value)}
              className={`${CONTROL} w-full`}>
              <option value="">Inherit global (Settings → General)</option>
              <option value="localhost">localhost (host-only — not reachable from other machines)</option>
              <option value="sslip">sslip.io (recommended — {'{label}'}.&lt;ip&gt;.sslip.io)</option>
              <option value="nip">nip.io</option>
              <option value="traefikme">traefik.me</option>
              <option value="off">off</option>
            </select>
            {autoMode && autoMode !== 'localhost' && autoMode !== 'off' && (
              <div className="mt-2">
                <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Auto-URL host / IP</label>
                <input value={autoHost} onChange={e => setAutoHost(e.target.value)} placeholder="inherits global App host (Settings → General)"
                  className={`${CONTROL} w-full font-mono`} />
                <Hint>The IP embedded in the magic-DNS name for this workspace&apos;s LOCAL envs — e.g. <code className="font-mono">myws-myapp-dev.{(autoHost.trim() || '10.10.10.111')}.{autoMode === 'nip' ? 'nip.io' : autoMode === 'traefikme' ? 'traefik.me' : 'sslip.io'}</code>. Blank inherits the global App host. (Remote-host envs always use their own host&apos;s address.)</Hint>
              </div>
            )}
            <Hint>How env URLs are built when no base domain is set. Blank inherits the instance default.</Hint>
          </div>
        )}
        <Hint tone="faint">Per-hostname certs are issued on demand via Let&apos;s Encrypt HTTP-01; Traefik uses the global <code className="font-mono">ACME_EMAIL</code>.</Hint>
        {/* Traefik routing: strip vs keep host ports (workspace default; per-env override in Edit Project). */}
        <div className="pt-3 border-t border-border/60">
          <Checkbox
            checked={keepPorts}
            onChange={setKeepPorts}
            label="Keep host ports under Traefik"
            hint={<>With Traefik routing on, an app is reached by its domain, so Rigger <strong>drops the redundant host-port mapping by default</strong> (this is what avoids host-port conflicts). Turn this on to also publish each web service&apos;s host port for direct <code className="font-mono">host:port</code> access. A single environment can override this either way in Edit Project → Environments. Applies on the next refresh/redeploy.</>}
          />
        </div>
        <div className="flex items-center gap-3">
          <Btn variant="primary" size="md" onClick={() => mut.mutate()} disabled={!dirty || mut.isPending}
            >
            {mut.isPending ? 'Saving…' : 'Save'}
          </Btn>
          {mut.isSuccess && !dirty && <span className="text-xs text-success-fg">✓ Saved</span>}
        </div>
      </div>
    </section>
  )
}

// WorkspaceTierOrder — the release-pipeline env tier order (env_tier_names). Stays on the
// General tab (it's a pipeline default, not a domain setting). Saves only its own key.
function WorkspaceTierOrder({ workspace, qc }) {
  const settingsKey = ['ws-settings', workspace]
  const { data: saved } = useQuery({
    queryKey: settingsKey, queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace,
  })
  const [tiers, setTiers] = useState('')
  const [wipe, setWipe]   = useState('')
  const [seeded, setSeeded] = useState(false)
  if (!seeded && saved) { setTiers(saved.env_tier_names || ''); setWipe(saved.wipe_allowed_envs || ''); setSeeded(true) }

  const mut = useMutation({
    mutationFn: () => updateWorkspaceSettings(workspace, { env_tier_names: tiers.trim(), wipe_allowed_envs: wipe.trim() }),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKey }),
  })
  const dirty = saved && (tiers.trim() !== (saved.env_tier_names || '') || wipe.trim() !== (saved.wipe_allowed_envs || ''))

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-3">Environments</h2>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-5">
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1.5">Tier order</label>
          <textarea value={tiers} onChange={e => setTiers(e.target.value)} rows={2}
            placeholder="dev, staging, qa, uat, preprod, prod"
            className={`${CONTROL} w-full font-mono`} />
          <Hint>Tier names from lowest to highest (comma or newline). Used to auto-guess each project's deploy order (dev → prod) for the release pipeline. A project can override with an explicit order. Leave blank for the built-in default.</Hint>
        </div>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1.5">Environments allowed to wipe data</label>
          <textarea value={wipe} onChange={e => setWipe(e.target.value)} rows={1}
            placeholder="dev, test"
            className={`${CONTROL} w-full font-mono`} />
          <Hint>Environments whose application <strong className="text-content-muted">data</strong> may be wiped (reset for dev/test) from a project's Danger Zone — by a workspace admin, with a typed confirmation + password. Leave blank to disable everywhere. <strong className="text-content-muted">Do not list production.</strong></Hint>
        </div>
        <div className="flex items-center gap-3">
          <Btn variant="primary" size="md" onClick={() => mut.mutate()} disabled={!dirty || mut.isPending}
            >
            {mut.isPending ? 'Saving…' : 'Save'}
          </Btn>
          {mut.isSuccess && !dirty && <span className="text-xs text-success-fg">✓ Saved</span>}
        </div>
      </div>
    </section>
  )
}

// WorkspaceAppearanceDefault sets the workspace's default theme + typography (W7).
// Members who haven't set a personal appearance inherit it; a per-user choice
// (Profile → Appearance) always overrides. Any field left to "inherit" falls back
// to the global default. Lives in the workspace Preferences tab.
function WorkspaceAppearanceDefault({ workspace, qc }) {
  const settingsKey = ['ws-settings', workspace]
  const { data: saved } = useQuery({
    queryKey: settingsKey, queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace,
  })
  const mut = useMutation({
    mutationFn: (blob) => updateWorkspaceSettings(workspace, { appearance_prefs: blob ? JSON.stringify(blob) : '' }),
    onSuccess: () => qc.invalidateQueries({ queryKey: settingsKey }),
  })
  let value // undefined while loading → null when none → parsed object
  if (saved !== undefined) {
    try { value = saved?.appearance_prefs ? JSON.parse(saved.appearance_prefs) : null } catch { value = null }
  }

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-1">Default appearance</h2>
      <Hint className="mb-3">Applied to members who haven't set their own appearance (Profile → Appearance always overrides). Any field left to inherit falls back to the global default.</Hint>
      <div className="bg-surface border border-border rounded-xl p-5">
        <AppearanceDefaultEditor
          value={value}
          onSave={(blob) => mut.mutate(blob)}
          saving={mut.isPending}
          savedOk={mut.isSuccess}
          inheritLabel="Inherit global default"
        />
      </div>
    </section>
  )
}

// WorkspaceConfirmDefault sets the workspace default for destructive-action
// confirmations + whether members may override it. Inherits the global default.
function WorkspaceConfirmDefault({ workspace, qc }) {
  const settingsKey = ['ws-settings', workspace]
  const { data: saved } = useQuery({
    queryKey: settingsKey, queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace,
  })
  const mut = useMutation({
    mutationFn: (c) => updateWorkspaceSettings(workspace, { confirm_destructive: c.confirm, confirm_destructive_allow_override: c.allow }),
    onSuccess: () => { qc.invalidateQueries({ queryKey: settingsKey }); qc.invalidateQueries({ queryKey: ['confirm-settings'] }) },
  })
  let value
  if (saved !== undefined) {
    value = { confirm: saved?.confirm_destructive || '', allow: saved?.confirm_destructive_allow_override || '' }
  }

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-1">Confirmations</h2>
      <Hint className="mb-3">Default for destructive-action confirmations in this workspace. Inherits the global default unless set; a personal choice (Profile → General) overrides it unless you lock it.</Hint>
      <div className="bg-surface border border-border rounded-xl p-5">
        <ConfirmDefaultEditor
          value={value}
          onSave={(c) => mut.mutate(c)}
          saving={mut.isPending}
          savedOk={mut.isSuccess}
          allowInherit
        />
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
              className={`${CONTROL} w-full`}
            />
            <Hint>1–32 chars; editable anytime.</Hint>
          </div>
          <div className="sm:col-span-1">
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Key <span className="font-normal normal-case text-content-faint">(fixed)</span></label>
            <input
              value={workspace} readOnly disabled
              className="w-full px-3 py-2 bg-surface-raised/60 border border-border-strong rounded-lg text-content-muted text-sm font-mono cursor-not-allowed"
            />
            <Hint>Fixed identity.</Hint>
          </div>
        </div>
        {err && <p className="text-sm text-danger-fg">{err}</p>}
        <div className="flex items-center gap-3">
          <Btn variant="primary" size="md" onClick={() => mut.mutate()} disabled={!ok || !dirty || mut.isPending}
            
          >
            {mut.isPending ? 'Saving…' : 'Save'}
          </Btn>
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
        <Hint className="mt-0.5">Who can access this workspace and at what level. Global admins always have full access. Roles take effect once access control is enforced.</Hint>
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
        <Btn variant="primary" size="sm" onClick={addMember} disabled={!addUid || setMut.isPending} >Add</Btn>
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
                  <Btn variant="secondary" size="xs" onClick={() => setExpanded(x => x === m.user_id ? null : m.user_id)}
                    >Per-project</Btn>
                  <Btn variant="danger" size="xs" onClick={() => rmMut.mutate(m.user_id)}
                    >Remove</Btn>
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
                        <Btn variant="dangerGhost" size="xs" onClick={() => ovRm.mutate({ uid: m.user_id, proj: o.proj_key })}
                          >✕</Btn>
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
      <Btn variant="secondary" size="xs" onClick={() => { if (proj) { onAdd(proj, role); setProj('') } }} disabled={!proj}
        >Add</Btn>
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
        <Hint className="mt-0">New projects created in this workspace start with these selections. They can be changed per project in the create wizard.</Hint>
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
          <Hint tone="faint" className="text-[11px]">Where image builds run for this workspace's projects (overridable per project). A dedicated builder must push to a registry the deploy targets can pull — set a system registry. Applies live, not just to new projects.</Hint>
        </div>
        <div className="flex items-center gap-3">
          <Btn variant="primary" size="md" onClick={() => mut.mutate()} disabled={!dirty || mut.isPending}
            >
            {mut.isPending ? 'Saving…' : 'Save'}
          </Btn>
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
  const [health, setHealth]     = useState(null) // host being inspected (components + stats)
  const [testStatus, setTestStatus] = useState({}) // id -> { loading, ok, msg, error }

  const [postSave, setPostSave] = useState(null) // { ok, text } — remote workspaces-dir status after create
  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateWorkspaceHost(workspace, id, body) : createWorkspaceHost(workspace, body),
    onSuccess: (data, vars) => {
      qc.invalidateQueries({ queryKey: hostsKey })
      setModal(null)
      // On create, surface remote workspaces-dir provisioning (parity with admin Settings).
      if (!vars.id && data) {
        const edgeNote = data.edge_installing ? ' Installing the Traefik edge in the background so web-routed apps are reachable here.' : ''
        if (data.workspaces_dir_created) setPostSave({ ok: true, text: `Created ${data.workspaces_dir} on ${data.name}.${edgeNote}` })
        else if (data.connect_error) setPostSave({ ok: false, text: `Host saved, but: ${data.connect_error}` })
        else if (data.workspaces_dir_exists) setPostSave({ ok: true, text: `${data.workspaces_dir} already present on ${data.name}.${edgeNote}` })
        if (data.workspaces_dir_created || data.connect_error || data.workspaces_dir_exists) setTimeout(() => setPostSave(null), 10000)
      }
    },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteWorkspaceHost(workspace, id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: hostsKey }); setDeleting(null) },
  })
  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      const res = await testWorkspaceHost(workspace, id)
      if (res.status === 'ok') {
        const edgeRunning = res.edge_running === true
        setTestStatus(s => ({ ...s, [id]: { ok: true, msg: res.message, edgeRunning } }))
        if (edgeRunning) setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 8000)
      } else {
        setTestStatus(s => ({ ...s, [id]: { error: res.error || 'Connection failed' } }))
        setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 8000)
      }
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Connection failed' } }))
      setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 8000)
    }
  }

  async function handleInstallEdge(id) {
    setTestStatus(s => ({ ...s, [id]: { ...s[id], edgeBusy: true, error: undefined } }))
    try {
      const res = await installWorkspaceHostEdge(workspace, id)
      if (res.status === 'ok') {
        setTestStatus(s => ({ ...s, [id]: { ...s[id], edgeBusy: false, edgeRunning: true, msg: res.message } }))
        setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 8000)
      } else {
        setTestStatus(s => ({ ...s, [id]: { ...s[id], edgeBusy: false, error: res.error || 'Edge install failed' } }))
      }
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { ...s[id], edgeBusy: false, error: err.response?.data?.error || 'Edge install failed' } }))
    }
  }

  const isOwned = (h) => h.owner_scope === `ws:${workspace}`

  return (
    <section>
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Remote hosts</h2>
          <Hint className="mt-0.5">Hosts this workspace can deploy to: its own plus any shared by an administrator.</Hint>
        </div>
        <Btn variant="secondary" size="md" onClick={() => setModal('new')}
          className="shrink-0">
          ＋ Add host
        </Btn>
      </div>

      {postSave && (
        <div className={`mb-3 text-xs px-3 py-2 rounded-lg border ${postSave.ok
          ? 'bg-success-subtle/40 border-success-border/50 text-success-fg'
          : 'bg-warning-subtle/40 border-warning-border/50 text-warning-fg'}`}>
          {postSave.text}
        </div>
      )}

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
                    {ts?.ok && ts.edgeRunning !== false && <span className="text-xs text-success-fg max-w-[180px] truncate" title={ts.msg}>✓ {ts.msg}</span>}
                    {ts?.ok && ts.edgeRunning === false && (
                      <span className="text-xs text-warning-fg flex items-center gap-1.5" title="No Traefik edge on this host — web-routed apps deployed here won't be reachable until it's installed.">
                        ⚠ no Traefik edge
                        <Btn variant="primary" size="xs" onClick={() => handleInstallEdge(host.id)} disabled={ts.edgeBusy}
                          >
                          {ts.edgeBusy ? 'Installing…' : 'Install edge'}
                        </Btn>
                      </span>
                    )}
                    {ts?.error && <span className="text-xs text-danger-fg max-w-[180px] truncate" title={ts.error}>{ts.error}</span>}
                    <Btn variant="secondary" size="xs" onClick={() => handleTest(host.id)} disabled={ts?.loading}
                      >Test</Btn>
                    <Btn variant="secondary" size="xs" onClick={() => setHealth(host)}
                      >Manage</Btn>
                    {owned ? (
                      <>
                        <Btn variant="secondary" size="xs" onClick={() => setModal({ editing: host })}
                          >Edit</Btn>
                        <Btn variant="danger" size="xs" onClick={() => setDeleting(host)}
                          >Delete</Btn>
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

      {health && (
        <HostHealthModal host={health} workspace={workspace} onClose={() => setHealth(null)} />
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={() => setModal(null)}>
          <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-content-strong">{modal === 'new' ? 'Add host' : `Edit “${modal.editing.name}”`}</h3>
              <CloseBtn onClick={() => setModal(null)} />
            </div>
            <HostForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={(body) => saveMut.mutateAsync({ id: modal?.editing?.id, body })}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
              showBuildOnly
              onCheckImpact={modal !== 'new' && modal?.editing?.id
                ? (body) => fetchWorkspaceHostRouteImpact(workspace, modal.editing.id, { address: body.address, public_address: body.public_address, reachability: body.reachability })
                : undefined}
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
              <Btn variant="secondary" size="md" onClick={() => setDeleting(null)} >Cancel</Btn>
              <Btn variant="danger" size="md" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                >
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </Btn>
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
          <Hint className="mt-0.5">Registries this workspace's projects can pull/push images from: its own plus any shared by an administrator. Mark one of your own <span className="text-content-muted font-medium">system</span> to use it for projects here that set no registry (overrides the global default).</Hint>
        </div>
        <Btn variant="secondary" size="md" onClick={() => setModal('new')}
          className="shrink-0">
          ＋ Add registry
        </Btn>
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
                    <Btn variant="secondary" size="xs" onClick={() => handleTest(r.id)} disabled={ts?.loading}
                      >Test</Btn>
                    {owned ? (
                      <>
                        <Btn variant="secondary" size="xs" onClick={() => sysMut.mutate({ id: r.id, system: !r.system })} disabled={sysMut.isPending}
                          title={r.system ? 'Stop using this as the workspace system registry' : 'Use for this workspace\'s projects that set no registry'}
                          >{r.system ? 'Unset system' : 'Set system'}</Btn>
                        <Btn variant="secondary" size="xs" onClick={() => setModal({ editing: r })}
                          >Edit</Btn>
                        <Btn variant="danger" size="xs" onClick={() => setDeleting(r)}
                          >Delete</Btn>
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
              <CloseBtn onClick={() => setModal(null)} />
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
              <Btn variant="secondary" size="md" onClick={() => setDeleting(null)} >Cancel</Btn>
              <Btn variant="danger" size="md" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                >
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </Btn>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

// GitSection — workspace Git provider connections (Phase 12). Credentials Rigger
// uses to clone PRIVATE repos: an HTTPS token, or an SSH deploy key Rigger generates
// (you paste the shown public key into your provider as a read-only deploy key).
function GitSection({ workspace, qc }) {
  const gpKey = ['ws-git-providers', workspace]
  const { data: providers = [], isLoading } = useQuery({
    queryKey: gpKey, queryFn: () => fetchWorkspaceGitProviders(workspace), enabled: !!workspace,
  })
  const [modal, setModal]       = useState(null) // null | 'new' | { editing }
  const [deleting, setDeleting] = useState(null)
  const [createdKey, setCreatedKey] = useState(null) // { name, public_key } after generating an SSH provider
  const [testStatus, setTestStatus] = useState({})   // id -> { loading, ok, error }

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateWorkspaceGitProvider(workspace, id, body) : createWorkspaceGitProvider(workspace, body),
    onSuccess: (saved) => {
      qc.invalidateQueries({ queryKey: gpKey })
      setModal(null)
      if (saved?.kind === 'ssh_key' && saved?.public_key) setCreatedKey({ name: saved.name, public_key: saved.public_key })
    },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteWorkspaceGitProvider(workspace, id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: gpKey }); setDeleting(null) },
  })

  async function handleTest(id) {
    const repo = window.prompt('Repository URL to test access against (HTTPS for a token, SSH for a deploy key):')
    if (!repo) return
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testWorkspaceGitProvider(workspace, id, repo.trim())
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Access failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 8000)
  }

  const isOwned = (p) => p.owner_scope === `ws:${workspace}`
  const kindLabel = (k) => k === 'ssh_key' ? 'SSH deploy key' : k === 'github_app' ? 'GitHub App' : 'HTTPS token'
  const ghMeta = (p) => { try { return JSON.parse(p.meta || '{}') } catch { return {} } }

  // Connect GitHub: ask the backend for the app manifest, then POST it to GitHub
  // (a top-level form submit) so GitHub creates the app and redirects back.
  const [connecting, setConnecting] = useState(false)
  async function connectGitHub() {
    setConnecting(true)
    try {
      const { create_url, manifest } = await startGitHubAppManifest(workspace, {})
      const f = document.createElement('form')
      f.method = 'POST'; f.action = create_url
      const i = document.createElement('input')
      i.type = 'hidden'; i.name = 'manifest'; i.value = manifest
      f.appendChild(i); document.body.appendChild(f); f.submit()
    } catch (err) {
      alert(err.response?.data?.error || 'Could not start GitHub App creation')
      setConnecting(false)
    }
  }
  async function finishInstall(id) {
    try {
      const { install_url } = await gitHubAppInstallURL(workspace, id)
      window.location.href = install_url
    } catch (err) {
      alert(err.response?.data?.error || 'Could not start GitHub install')
    }
  }

  return (
    <section>
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Git providers</h2>
          <Hint className="mt-0.5">Credentials this workspace's projects use to clone <span className="text-content-muted font-medium">private</span> repositories — an HTTPS token, or an SSH deploy key Rigger generates for you. Select one on a project's source in New / Edit Project. Secrets are encrypted at rest and never shown again.</Hint>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <Btn variant="ghost" size="md" onClick={connectGitHub} disabled={connecting} >
            {connecting ? 'Connecting…' : ' Connect GitHub'}
          </Btn>
          <Btn variant="secondary" size="md" onClick={() => setModal('new')}
            >
            ＋ Add provider
          </Btn>
        </div>
      </div>

      <div className="bg-surface border border-border rounded-xl">
        {isLoading ? (
          <p className="p-5 text-sm text-content-subtle">Loading…</p>
        ) : providers.length === 0 ? (
          <p className="p-5 text-sm text-content-subtle">No Git providers yet. Add a token or an SSH deploy key to deploy from a private repository.</p>
        ) : (
          <div className="divide-y divide-border">
            {providers.map(p => {
              const owned = isOwned(p)
              const ts = testStatus[p.id]
              return (
                <div key={p.id} className="flex items-center gap-3 p-4">
                  <div className="flex-shrink-0 w-8 h-8 rounded-lg bg-surface-raised flex items-center justify-center text-sm">{p.kind === 'ssh_key' ? '🔑' : '🔒'}</div>
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <p className="text-sm font-semibold text-content-strong">{p.name}</p>
                      <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint">{kindLabel(p.kind)}</span>
                      {p.kind === 'github_app' && (ghMeta(p).installation_id
                        ? <span className="text-[10px] px-1.5 py-0.5 rounded bg-emerald-100/70 text-emerald-700 border border-emerald-200 dark:bg-emerald-950/60 dark:text-emerald-300 dark:border-emerald-800/40">✓ installed</span>
                        : <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-100/70 text-amber-700 border border-amber-200 dark:bg-amber-950/60 dark:text-amber-300 dark:border-amber-800/40">not installed</span>)}
                      {!owned && <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint" title="Shared by an administrator">shared</span>}
                    </div>
                    <p className="text-xs text-content-subtle mt-0.5">{p.host || 'any host'}{p.username ? ` · ${p.username}` : ''}</p>
                  </div>
                  <div className="flex items-center gap-2">
                    {ts?.loading && <span className="text-xs text-content-subtle">Testing…</span>}
                    {ts?.ok && <span className="text-xs text-success-fg">✓ Access OK</span>}
                    {ts?.error && <span className="text-xs text-danger-fg max-w-[180px] truncate" title={ts.error}>{ts.error}</span>}
                    {p.kind === 'ssh_key' && p.public_key && (
                      <Btn variant="secondary" size="xs" onClick={() => setCreatedKey({ name: p.name, public_key: p.public_key })}
                        >Show key</Btn>
                    )}
                    {p.kind === 'github_app' && owned && !ghMeta(p).installation_id && (
                      <button onClick={() => finishInstall(p.id)}
                        className="px-2.5 py-1.5 text-xs font-medium rounded-lg text-brand-600 hover:bg-surface-raised">Finish install</button>
                    )}
                    <Btn variant="secondary" size="xs" onClick={() => handleTest(p.id)} disabled={ts?.loading}
                      >Test</Btn>
                    {owned ? (
                      <>
                        <Btn variant="secondary" size="xs" onClick={() => setModal({ editing: p })}
                          >Edit</Btn>
                        <Btn variant="danger" size="xs" onClick={() => setDeleting(p)}
                          >Delete</Btn>
                      </>
                    ) : <span className="text-[11px] text-content-faint px-2">read-only</span>}
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>

      {modal && (
        <GitProviderModal
          initial={modal === 'new' ? null : modal.editing}
          saving={saveMut.isPending}
          error={saveMut.error?.response?.data?.error}
          onSave={(body) => saveMut.mutate({ id: modal?.editing?.id, body })}
          onClose={() => setModal(null)}
        />
      )}

      {createdKey && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={() => setCreatedKey(null)}>
          <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-lg p-6 space-y-3" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Deploy public key — “{createdKey.name}”</h3>
            <p className="text-sm text-content-muted">Add this as a <strong>read-only deploy key</strong> on your repository or provider (GitHub: Settings → Deploy keys; GitLab: Settings → Repository → Deploy keys). The private key stays encrypted in Rigger.</p>
            <textarea readOnly value={createdKey.public_key} rows={3}
              className="w-full bg-surface-raised border border-border rounded-lg px-3 py-2 text-xs font-mono text-content break-all" onFocus={e => e.target.select()} />
            <div className="flex gap-2 justify-end">
              <Btn variant="secondary" size="md" onClick={() => { navigator.clipboard?.writeText(createdKey.public_key) }}
                >Copy</Btn>
              <Btn variant="primary" size="md" onClick={() => setCreatedKey(null)}
                >Done</Btn>
            </div>
          </div>
        </div>
      )}

      {deleting && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={() => setDeleting(null)}>
          <div className="bg-surface border border-border rounded-2xl w-full max-w-sm p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Delete “{deleting.name}”?</h3>
            <p className="text-sm text-content-muted">Projects using this provider will fail to clone their private repo until pointed at another. This cannot be undone.</p>
            <div className="flex gap-2 justify-end">
              <Btn variant="secondary" size="md" onClick={() => setDeleting(null)} >Cancel</Btn>
              <Btn variant="danger" size="md" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                >
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </Btn>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

// GitProviderModal — add/edit a Git provider. Token vs SSH-deploy-key; editing keeps
// the stored secret if the secret field is left blank.
// Token-provider presets: GitHub / GitLab / Bitbucket / Gitea all share one HTTPS-token
// engine (Basic user:token injected at clone time) — these just prefill the host + the
// right username convention and tailor the labels/help. "Other" = any HTTPS git host.
const GIT_SERVICES = {
  github:    { label: 'GitHub',    host: '',              hostPlaceholder: 'blank for github.com — or your GitHub Enterprise host', username: 'x-access-token', secretLabel: 'Personal access token', hint: 'Fine-grained or classic PAT with read access to the repo (Contents: Read).' },
  gitlab:    { label: 'GitLab',    host: 'gitlab.com',    hostPlaceholder: 'gitlab.com — or your self-managed GitLab host',        username: 'oauth2',         secretLabel: 'Personal access token', hint: 'PAT or project/group token with read_repository. Set Host to your server for self-managed GitLab.' },
  bitbucket: { label: 'Bitbucket', host: 'bitbucket.org', hostPlaceholder: 'bitbucket.org',                                        username: '',               secretLabel: 'App password',          hint: 'Bitbucket App password (Personal settings → App passwords) with Repositories: Read. Username = your Bitbucket username.' },
  gitea:     { label: 'Gitea',     host: '',              hostPlaceholder: 'your Gitea / Forgejo host, e.g. git.example.com',       username: '',               secretLabel: 'Access token',          hint: 'Gitea / Forgejo access token with repo read. Username = your account name.' },
  generic:   { label: 'Other',     host: '',              hostPlaceholder: 'your git host, e.g. git.example.com',                   username: '',               secretLabel: 'Token / password',      hint: 'Any HTTPS git host — username + token are sent as Basic auth at clone time.' },
}
function inferGitService(host) {
  const x = (host || '').toLowerCase()
  if (x.includes('gitlab')) return 'gitlab'
  if (x.includes('bitbucket')) return 'bitbucket'
  if (x.includes('gitea') || x.includes('forgejo')) return 'gitea'
  if (x === '' || x.includes('github')) return 'github'
  return 'generic'
}
// The provider persists its chosen service preset in meta json (the host string alone
// can't identify a self-hosted gitea/GitLab) — read it back so Edit reopens the right
// tab; fall back to host inference for providers saved before this was stored.
function serviceForEdit(p) {
  try { const s = JSON.parse(p?.meta || '{}').service; if (s && GIT_SERVICES[s]) return s } catch { /* ignore */ }
  return inferGitService(p?.host)
}

function GitProviderModal({ initial, onSave, onClose, saving, error }) {
  const editing = !!initial
  const [kind, setKind]   = useState(initial?.kind || 'token')
  const [name, setName]   = useState(initial?.name || '')
  const [host, setHost]   = useState(initial?.host || '')
  const [username, setUsername] = useState(initial?.username || '')
  const [service, setService] = useState(() => serviceForEdit(initial))
  const [secret, setSecret] = useState('')
  const svc = GIT_SERVICES[service] || GIT_SERVICES.generic
  const canSave = name.trim() && (editing || kind === 'ssh_key' || secret.trim())
  const submit = () => {
    if (!canSave) return
    onSave({ name: name.trim(), kind, host: host.trim(), username: username.trim(), secret, service: kind === 'token' ? service : '' })
  }
  const inp = 'w-full bg-surface-raised border border-border rounded-lg px-3 py-2 text-sm text-content focus:outline-none focus:border-brand-500'
  const lbl = 'block text-xs font-medium text-content-muted mb-1'
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={onClose}>
      <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-lg mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">{editing ? `Edit “${initial.name}”` : 'Add Git provider'}</h3>
          <CloseBtn onClick={onClose} />
        </div>

        {!editing && (
          <div>
            <label className={lbl}>Type</label>
            <div className="grid grid-cols-2 gap-2">
              <button type="button" onClick={() => setKind('token')}
                className={`px-3 py-2 text-sm rounded-lg border ${kind === 'token' ? 'border-brand-500 text-content-strong bg-surface-raised' : 'border-border text-content-muted'}`}>🔒 HTTPS token</button>
              <button type="button" onClick={() => setKind('ssh_key')}
                className={`px-3 py-2 text-sm rounded-lg border ${kind === 'ssh_key' ? 'border-brand-500 text-content-strong bg-surface-raised' : 'border-border text-content-muted'}`}>🔑 SSH deploy key</button>
            </div>
          </div>
        )}

        <div>
          <label className={lbl}>Name</label>
          <input className={inp} value={name} onChange={e => setName(e.target.value)} placeholder="e.g. GitHub (acme org)" />
        </div>
        {kind === 'token' && (
          <div>
            <label className={lbl}>Service</label>
            <div className="grid grid-cols-5 gap-1.5">
              {Object.entries(GIT_SERVICES).map(([k, s]) => (
                <button key={k} type="button"
                  onClick={() => { setService(k); if (s.host) setHost(s.host); if (s.username) setUsername(s.username) }}
                  className={`px-2 py-1.5 text-xs rounded-lg border ${service === k ? 'border-brand-500 text-content-strong bg-surface-raised' : 'border-border text-content-muted'}`}>{s.label}</button>
              ))}
            </div>
          </div>
        )}

        <div>
          <label className={lbl}>Host <span className="text-content-faint font-normal">(optional)</span></label>
          <input className={inp} value={host} onChange={e => setHost(e.target.value)} placeholder={kind === 'token' ? svc.hostPlaceholder : 'your git host (optional)'} />
        </div>

        {kind === 'token' ? (
          <>
            <div>
              <label className={lbl}>Username <span className="text-content-faint font-normal">(optional)</span></label>
              <input className={inp} value={username} onChange={e => setUsername(e.target.value)} placeholder={svc.username || 'your username'} />
            </div>
            <div>
              <label className={lbl}>{svc.secretLabel}{!editing && <span className="text-danger-fg ml-0.5">*</span>}</label>
              <input className={inp} type="password" value={secret} onChange={e => setSecret(e.target.value)} placeholder={editing ? 'leave blank to keep current' : 'paste the token'} />
              <Hint tone="faint" className="text-[11px]">{svc.hint} Stored encrypted; sent only via the git config header at clone time.</Hint>
            </div>
          </>
        ) : (
          <p className="text-xs text-content-subtle bg-surface-raised border border-border rounded-lg p-3">
            Rigger generates an <strong>ed25519 deploy keypair</strong>. After saving, copy the <strong>public key</strong> and add it to your repository/provider as a read-only deploy key. Use an <code className="font-mono">ssh://</code> / <code className="font-mono">git@…</code> repo URL on the project.
          </p>
        )}

        {error && <p className="text-xs text-danger-fg">{error}</p>}
        <div className="flex gap-2 justify-end pt-1">
          <Btn variant="secondary" size="md" onClick={onClose} >Cancel</Btn>
          <Btn variant="primary" size="md" onClick={submit} disabled={!canSave || saving} >
            {saving ? 'Saving…' : editing ? 'Save' : kind === 'ssh_key' ? 'Generate & save' : 'Add provider'}
          </Btn>
        </div>
      </div>
    </div>
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
          <Hint className="mt-0.5">Off-site destinations this workspace's environments can back up to: its own plus any shared by an administrator.</Hint>
        </div>
        <Btn variant="secondary" size="md" onClick={() => setModal('new')}
          className="shrink-0">
          ＋ Add target
        </Btn>
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
                    <Btn variant="secondary" size="xs" onClick={() => handleTest(t.id)} disabled={ts?.loading}
                      >Test</Btn>
                    {owned ? (
                      <>
                        <Btn variant="secondary" size="xs" onClick={() => setModal({ editing: t })}
                          >Edit</Btn>
                        <Btn variant="danger" size="xs" onClick={() => setDeleting(t)}
                          >Delete</Btn>
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
              <CloseBtn onClick={() => setModal(null)} />
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
              <Btn variant="secondary" size="md" onClick={() => setDeleting(null)} >Cancel</Btn>
              <Btn variant="danger" size="md" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                >
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </Btn>
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
          <Hint className="mt-0.5">Where this workspace's alerts are delivered: its own channels plus any shared by an administrator. Assign them to rules on the Alert Rules tab.</Hint>
        </div>
        <Btn variant="secondary" size="md" onClick={() => setModal('new')}
          className="shrink-0">
          ＋ Add channel
        </Btn>
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
                    <Btn variant="secondary" size="xs" onClick={() => handleTest(ch.id)} disabled={ts?.loading}
                      >Test</Btn>
                    {owned ? (
                      <>
                        <Btn variant="secondary" size="xs" onClick={() => setModal({ editing: ch })}
                          >Edit</Btn>
                        <Btn variant="danger" size="xs" onClick={() => setDeleting(ch)}
                          >Delete</Btn>
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
              <CloseBtn onClick={() => setModal(null)} />
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
              <Btn variant="secondary" size="md" onClick={() => setDeleting(null)} >Cancel</Btn>
              <Btn variant="danger" size="md" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                >
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </Btn>
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
// ── Access Lists (workspace-scoped; strictly isolated) ──────────────────────────
// Reusable basic-auth users + IP allow/deny + GeoIP country policy this workspace's
// projects can attach to an env's app routers (Edit Project → env Security). Rendered as
// shared Traefik file-provider middlewares. See workspace-plugins-and-access-lists design.
const aclInput = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'

function AccessListsSection({ workspace, qc }) {
  const key = ['ws-access-lists', workspace]
  const { data: lists = [], isLoading } = useQuery({ queryKey: key, queryFn: () => fetchWorkspaceAccessLists(workspace), enabled: !!workspace })
  const { data: plugins } = useQuery({ queryKey: ['proxy-plugins'], queryFn: fetchProxyPlugins })
  const [modal, setModal] = useState(null)     // null | 'new' | {editing}
  const [deleting, setDeleting] = useState(null)
  const delMut = useMutation({
    mutationFn: (id) => deleteWorkspaceAccessList(workspace, id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: key }); setDeleting(null) },
  })
  return (
    <section>
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Access lists</h2>
          <Hint className="mt-0.5">Reusable basic-auth users + IP allow/deny + GeoIP country policy, private to this workspace. Attach one to a project environment under Edit Project → Security.</Hint>
        </div>
        <Btn variant="secondary" size="md" onClick={() => setModal('new')} className="shrink-0">＋ Add access list</Btn>
      </div>
      <div className="bg-surface border border-border rounded-xl">
        {isLoading ? <p className="p-5 text-sm text-content-subtle">Loading…</p>
          : lists.length === 0 ? <p className="p-5 text-sm text-content-subtle">No access lists yet. Create one to reuse the same auth + IP/Geo policy across this workspace's app environments.</p>
          : (
            <div className="divide-y divide-border">
              {lists.map(a => {
                const allow = (a.rules || []).filter(r => r.action === 'allow').length
                const deny = (a.rules || []).filter(r => r.action === 'deny').length
                return (
                  <div key={a.id} className="flex items-center gap-3 p-4">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="text-sm font-semibold text-content-strong">{a.name}</span>
                        {(a.users || []).length > 0 && <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 dark:bg-indigo-950/60 dark:text-indigo-300">{a.users.length} user{a.users.length > 1 ? 's' : ''}</span>}
                        {allow > 0 && <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint">{allow} allow</span>}
                        {deny > 0 && <span className="text-[10px] px-1.5 py-0.5 rounded bg-rose-100/70 text-rose-700 dark:bg-rose-950/60 dark:text-rose-300">{deny} deny</span>}
                        {a.geo_mode === 'allow' && <span className="text-[10px] px-1.5 py-0.5 rounded bg-indigo-100/70 text-indigo-700 dark:bg-indigo-950/60 dark:text-indigo-300">🌐 allow {(a.countries || []).length}</span>}
                        {a.geo_mode === 'block' && <span className="text-[10px] px-1.5 py-0.5 rounded bg-rose-100/70 text-rose-700 dark:bg-rose-950/60 dark:text-rose-300">🌐 block {(a.countries || []).length}</span>}
                        {!a.pass_auth && <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong text-content-faint">strips auth</span>}
                      </div>
                    </div>
                    <div className="flex items-center gap-1.5 shrink-0">
                      <Btn variant="secondary" size="xs" onClick={() => setModal({ editing: a })} >Edit</Btn>
                      <Btn variant="ghost" size="xs" onClick={() => setDeleting(a)} >Delete</Btn>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
      </div>
      {modal && <AccessListWSModal workspace={workspace} plugins={plugins} initial={modal === 'new' ? null : modal.editing}
        onClose={() => setModal(null)} onSaved={() => { qc.invalidateQueries({ queryKey: key }); setModal(null) }} />}
      {deleting && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60" onClick={() => setDeleting(null)}>
          <div className="bg-surface border border-border rounded-xl w-full max-w-sm mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <h3 className="font-semibold text-content-strong">Delete access list “{deleting.name}”?</h3>
            <p className="text-sm text-content-muted">Any environment using it becomes publicly accessible (no auth/IP restriction) on next deploy.</p>
            <div className="flex gap-2 justify-end">
              <Btn variant="secondary" size="md" onClick={() => setDeleting(null)} >Cancel</Btn>
              <Btn variant="ghost" size="md" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending} >{delMut.isPending ? 'Deleting…' : 'Delete'}</Btn>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

function AccessListWSModal({ workspace, plugins, initial, onClose, onSaved }) {
  const isEdit = !!initial
  const [f, setF] = useState(() => initial
    ? { name: initial.name || '', pass_auth: initial.pass_auth !== false, users: (initial.users || []).map(u => ({ user: u.user, password: '' })), rules: (initial.rules || []).map(r => ({ action: r.action || 'allow', address: r.address || '' })), geo_mode: initial.geo_mode || 'off', countriesText: (initial.countries || []).join(', ') }
    : { name: '', pass_auth: true, users: [], rules: [], geo_mode: 'off', countriesText: '' })
  const [err, setErr] = useState('')
  const set = (k, v) => { if (err) setErr(''); setF(s => ({ ...s, [k]: v })) }
  const save = useMutation({
    mutationFn: () => {
      const countries = (f.countriesText || '').split(/[\s,]+/).map(c => c.trim().toUpperCase()).filter(c => c.length === 2)
      const body = {
        name: f.name, pass_auth: !!f.pass_auth,
        users: f.users.filter(u => u.user).map(u => ({ user: u.user, password: u.password || '' })),
        rules: f.rules.filter(r => r.address).map(r => ({ action: r.action === 'deny' ? 'deny' : 'allow', address: r.address })),
        geo_mode: countries.length ? f.geo_mode : 'off', countries,
      }
      return isEdit ? updateWorkspaceAccessList(workspace, initial.id, body) : createWorkspaceAccessList(workspace, body)
    },
    onSuccess: onSaved,
    onError: (e) => setErr(e?.response?.data?.error || 'Save failed'),
  })
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/50 p-4 overflow-y-auto" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-xl my-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-3 border-b border-border">
          <span className="font-semibold text-content-strong text-sm">{isEdit ? `Edit access list — ${initial.name}` : 'Add access list'}</span>
          <CloseBtn onClick={onClose} />
        </div>
        <div className="px-5 py-4 space-y-4">
          <div>
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Name</label>
            <input value={f.name} onChange={e => set('name', e.target.value)} placeholder="Office + admins" className={aclInput} />
          </div>
          <div className="border-t border-border pt-3">
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Authorized users (basic auth)</label>
            <div className="space-y-2 mt-1">
              {f.users.map((u, i) => (
                <div key={i} className="flex items-center gap-2">
                  <input value={u.user} onChange={e => set('users', f.users.map((x, j) => j === i ? { ...x, user: e.target.value } : x))} placeholder="username" className={aclInput} />
                  <input type="password" value={u.password} onChange={e => set('users', f.users.map((x, j) => j === i ? { ...x, password: e.target.value } : x))} placeholder={isEdit ? '(unchanged)' : 'password'} className={aclInput} />
                  <IconBtn variant="dangerGhost" size="xs" onClick={() => set('users', f.users.filter((_, j) => j !== i))} >🗑</IconBtn>
                </div>
              ))}
              <button onClick={() => set('users', [...f.users, { user: '', password: '' }])} className="text-xs text-accent-text hover:text-accent-text-hover">＋ Add user</button>
            </div>
            <label className="flex items-center gap-2 mt-2 text-sm text-content cursor-pointer">
              <input type="checkbox" checked={!!f.pass_auth} onChange={e => set('pass_auth', e.target.checked)} />
              Forward the Authorization header to the upstream
            </label>
          </div>
          <div className="border-t border-border pt-3">
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">IP rules</label>
            <Hint tone="faint" className="text-[11px] mb-2">Traefik enforces an allow-list: if any Allow rules exist, only those ranges are permitted. A deny-only list can’t be enforced at the proxy.</Hint>
            <div className="space-y-2">
              {f.rules.map((r, i) => (
                <div key={i} className="flex items-center gap-2">
                  <select value={r.action} onChange={e => set('rules', f.rules.map((x, j) => j === i ? { ...x, action: e.target.value } : x))} className={`${CONTROL} w-full`}>
                    <option value="allow">Allow</option><option value="deny">Deny</option>
                  </select>
                  <input value={r.address} onChange={e => set('rules', f.rules.map((x, j) => j === i ? { ...x, address: e.target.value } : x))} placeholder="192.168.0.0/16 or 203.0.113.4" className={aclInput} />
                  <IconBtn variant="dangerGhost" size="xs" onClick={() => set('rules', f.rules.filter((_, j) => j !== i))} >🗑</IconBtn>
                </div>
              ))}
              <button onClick={() => set('rules', [...f.rules, { action: 'allow', address: '' }])} className="text-xs text-accent-text hover:text-accent-text-hover">＋ Add IP rule</button>
            </div>
          </div>
          <div className="border-t border-border pt-3">
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">GeoIP country policy</label>
            <div className="grid grid-cols-2 gap-3 mt-1">
              <select value={f.geo_mode} onChange={e => set('geo_mode', e.target.value)} className={`${CONTROL} w-full`}>
                <option value="off">Off</option><option value="allow">Allow only these countries</option><option value="block">Block these countries</option>
              </select>
              <input value={f.countriesText} onChange={e => set('countriesText', e.target.value)} placeholder="US, DE, GB" disabled={f.geo_mode === 'off'} className={aclInput} />
            </div>
            <Hint tone="faint" className="text-[11px]">Two-letter ISO country codes, comma-separated.{!plugins?.geoip_enabled && ' Enable the GeoIP plugin on the Proxy Service page for this to take effect.'}</Hint>
          </div>
          {err && <p className="text-sm text-rose-600 bg-rose-50 dark:bg-rose-950/40 border border-rose-200 dark:border-rose-900 rounded-lg px-3 py-2">{err}</p>}
        </div>
        <div className="flex items-center justify-end gap-2 px-5 py-3 border-t border-border">
          <Btn variant="secondary" size="md" onClick={onClose} >Cancel</Btn>
          <Btn variant="primary" size="md" onClick={() => { setErr(''); if (!f.name.trim()) { setErr('Name is required'); return } save.mutate() }} disabled={save.isPending} >{save.isPending ? 'Saving…' : 'Save access list'}</Btn>
        </div>
      </div>
    </div>
  )
}

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
          {!project && <Hint tone="faint">Applies to all projects in this workspace.</Hint>}
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
          <Hint tone="faint">No channels in this workspace yet — add one on the Notifications tab. The alert still shows in the inbox without a channel.</Hint>
        ) : (
          <div className="flex flex-wrap gap-2 mt-1">
            {channels.map(ch => {
              const on = notifyIds.includes(ch.id)
              return (
                <button key={ch.id} type="button" onClick={() => toggleChannel(ch.id)}
                  className={`px-2.5 py-1 rounded-lg text-xs font-medium border transition-colors ${on ? 'bg-brand-600/20 border-brand-500 text-accent-text' : 'bg-surface-raised border-border-strong text-content-muted hover:text-content'}`}>
                  {on ? '✓ ' : ''}{ch.name}<span className="ml-1 text-content-subtle">{ch.type}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" size="md" onClick={onCancel} >Cancel</Btn>
        <Btn variant="primary" size="md" type="submit" disabled={saving} >
          {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add rule'}
        </Btn>
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
          <Hint className="mt-0.5">Per-project conditions for this workspace, evaluated every 60s. Host/infra rules are managed globally in Settings.</Hint>
        </div>
        <Btn variant="secondary" size="md" onClick={() => setModal('new')}
          className="shrink-0">
          ＋ Add rule
        </Btn>
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
                  <Btn variant="secondary" size="xs" onClick={() => setModal({ editing: r })} >Edit</Btn>
                  <Btn variant="danger" size="xs" onClick={() => setDeleting(r)} >Delete</Btn>
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
              <CloseBtn onClick={() => setModal(null)} />
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
              <Btn variant="secondary" size="md" onClick={() => setDeleting(null)} >Cancel</Btn>
              <Btn variant="danger" size="md" onClick={() => delMut.mutate(deleting.id)} disabled={delMut.isPending}
                >
                {delMut.isPending ? 'Deleting…' : 'Delete'}
              </Btn>
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
            <Hint className="mt-0.5">Move all (or selected) projects to another workspace, then remove this one. Containers keep running (their resource prefix is unchanged).</Hint>
          </div>
          <Btn variant="secondary" size="md" onClick={() => setMode('transfer')} disabled={projects.length === 0 || others.length === 0}
            className="shrink-0">
            Transfer…
          </Btn>
        </div>
        <div className="p-5 flex items-center justify-between gap-4">
          <div>
            <p className="text-sm font-medium text-content-strong">Delete this workspace</p>
            <Hint className="mt-0.5">Stops and removes every project, environment, container, network and volume in this workspace, then deletes it. Cannot be undone.</Hint>
          </div>
          <Btn variant="danger" size="md" onClick={() => setMode('delete')}
            className="shrink-0">
            Delete…
          </Btn>
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
            className={`${CONTROL} w-full`}>
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
          <Hint>Keys are kept; on a name clash in the target the moved project's key is suffixed. Resource prefixes (and running containers) are unchanged.</Hint>
        </div>
        {err && <p className="text-sm text-danger-fg">{err}</p>}
        <div className="flex gap-2 justify-end">
          <Btn variant="secondary" size="md" onClick={onClose} >Cancel</Btn>
          <Btn variant="primary" size="md" onClick={() => mut.mutate()} disabled={!target || chosen.length === 0 || mut.isPending}
            >
            {mut.isPending ? 'Transferring…' : `Transfer ${chosen.length}`}
          </Btn>
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
        <Btn variant="secondary" size="md" onClick={onClose} >Cancel</Btn>
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
          <Btn variant="secondary" size="md" onClick={() => setStep('choose')} >Back</Btn>
        </div>
      </>)
    }
    return wrap(<>
      <h3 className="font-semibold text-content-strong">Transfer projects, then delete</h3>
      <p className="text-sm text-content-muted">All {projects.length} project{projects.length !== 1 ? 's' : ''} move to the chosen workspace; this workspace is removed afterwards. Resource prefixes (and running containers) are unchanged.</p>
      <div>
        <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Target workspace</label>
        <select value={target} onChange={e => setTarget(e.target.value)}
          className={`${CONTROL} w-full`}>
          {others.map(o => <option key={o.key} value={o.key}>{o.name} ({o.key})</option>)}
        </select>
      </div>
      {err && <p className="text-sm text-danger-fg">{err}</p>}
      <div className="flex gap-2 justify-end">
        <Btn variant="secondary" size="md" onClick={() => setStep('choose')} >Back</Btn>
        <Btn variant="primary" size="md" onClick={() => transferMut.mutate()} disabled={!target || transferMut.isPending}
          >
          {transferMut.isPending ? 'Transferring…' : 'Transfer & delete'}
        </Btn>
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
        ? <Btn variant="secondary" size="md" onClick={() => setStep('choose')} >Back</Btn>
        : <Btn variant="secondary" size="md" onClick={onClose} >Cancel</Btn>}
      <Btn variant="danger" size="md" onClick={() => destroyMut.mutate()} disabled={confirm !== workspace || destroyMut.isPending}
        >
        {destroyMut.isPending ? 'Deleting…' : 'Delete workspace'}
      </Btn>
    </div>
  </>)
}
