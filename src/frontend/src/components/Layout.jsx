import { Link, useNavigate, useParams } from 'react-router-dom'
import { useState, useRef, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '../store/auth'
import { useWorkspaceStore } from '../store/workspace'
import { QRCodeSVG } from 'qrcode.react'
import { fetchWorkspaces, fetchProjects, createWorkspaceTier, fetchEnvStatus, changePassword, fetchAlertUnread, updateProfile, fetchProfile, resendVerification, fetchVersion, fetch2FAStatus, begin2FA, enable2FA, disable2FA, regen2FACodes } from '../lib/api'
import { useDockerEvents } from '../hooks/useDockerEvents'
import SlideOutPanel from './SlideOutPanel'
import ThemeToggle from './ThemeToggle'
import KeyField from './KeyField'
import RequestAccessModal from './RequestAccessModal'
import { AppearanceTab } from '../pages/SettingsPage'

const STATUS_DOT = {
  running: 'bg-green-400',
  partial: 'bg-amber-400 animate-pulse',
  stopped: 'bg-red-500',
  building: 'bg-amber-400 animate-pulse',
  unknown:  'bg-surface-overlay',
}

// Polls the first environment of a project to determine its dot color
// VersionFooter shows the running Rigger build version at the bottom of the
// sidebar (self-update Phase 0). The check-for-update / apply UI lands later.
function VersionFooter() {
  const { data } = useQuery({ queryKey: ['rigger-version'], queryFn: fetchVersion, staleTime: Infinity })
  const v = data?.version
  if (!v) return null
  return (
    <div className="mt-auto px-3 py-2 border-t border-border text-[10px] text-content-faint" title={data.commit ? `commit ${data.commit}` : undefined}>
      Rigger {v === 'dev' ? 'dev build' : v}
    </div>
  )
}

function ProjectStatusDot({ workspace, name, envs }) {
  const firstEnv = envs?.[0]
  const { data } = useQuery({
    queryKey: ['envstatus', workspace, name, firstEnv],
    queryFn: () => fetchEnvStatus(workspace, name, firstEnv),
    enabled: !!workspace && !!firstEnv,
    refetchInterval: 30_000, // SSE handles real-time; this is just a fallback
    retry: false,
  })
  const status = data?.status || 'unknown'
  return <span className={`w-2 h-2 rounded-full shrink-0 ${STATUS_DOT[status] || STATUS_DOT.unknown}`} />
}

function ProjectSidebarItem({ workspace, project, active }) {
  const cfg = project.config
  const type = cfg?.project?.type || 'custom'
  const envs = project.envs || []

  // Derive a short stack description from the unified service graph + managed deps.
  let stackLine = ''
  {
    const firstEnv = cfg?.environments?.[envs[0]] || {}
    const parts = (cfg?.services || []).map(s => s.name).filter(Boolean)
    if (firstEnv.database && firstEnv.database !== 'none') parts.push(firstEnv.database)
    if (firstEnv.redis_enabled) parts.push('redis')
    stackLine = parts.join(' · ') || type
  }

  return (
    <Link
      to={`/workspaces/${workspace}/projects/${project.name}`}
      className={`block px-3 py-2.5 rounded-lg transition-colors ${
        active
          ? 'bg-surface-raised text-content-strong'
          : 'text-content-muted hover:bg-surface-raised/60 hover:text-content'
      }`}
    >
      <div className="flex items-center gap-2.5">
        <ProjectStatusDot workspace={workspace} name={project.name} envs={project.envs} />
        <span className="font-medium text-sm truncate">{project.config?.project?.name || project.name}</span>
        <span className="text-[10px] font-mono text-content-faint shrink-0 ml-auto">{project.name}</span>
      </div>
      {stackLine && (
        <p className="text-xs text-content-subtle mt-0.5 ml-4.5 truncate pl-4">{stackLine}</p>
      )}
    </Link>
  )
}

// Top-nav dropdown to pick the active parent-tier Workspace. Changing it scopes
// the whole UI (sidebar projects, dashboard) and navigates home.
function WorkspaceSelector({ current, workspaces, onSelect, onNewWorkspace, onManage, canManage, canCreate }) {
  const [open, setOpen] = useState(false)
  const ref = useRef(null)
  useEffect(() => {
    if (!open) return
    function handler(e) { if (ref.current && !ref.current.contains(e.target)) setOpen(false) }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  // `current` is the workspace KEY; show its display name.
  const currentWs = (workspaces || []).find(w => w.key === current)
  const currentLabel = currentWs?.name || current

  return (
    <div ref={ref} className="relative">
      <button
        onClick={() => setOpen(o => !o)}
        className="flex items-center gap-2 px-3 py-1.5 text-sm rounded-lg border border-border-strong text-content hover:text-content-strong hover:bg-surface-raised transition-colors max-w-[220px]"
      >
        <span className="text-xs text-content-subtle uppercase tracking-wider shrink-0">WS</span>
        <span className="font-semibold truncate">{currentLabel || 'Select workspace'}</span>
        <span className="text-xs text-content-faint shrink-0">▾</span>
      </button>
      {open && (
        <div className="absolute left-0 top-full mt-1 z-30 bg-surface-raised border border-border-strong rounded-xl shadow-xl min-w-[240px] py-1 overflow-hidden">
          <div className="max-h-72 overflow-y-auto">
            {(workspaces || []).length === 0 && (
              <p className="px-3 py-2 text-xs text-content-subtle">No workspaces yet.</p>
            )}
            {(workspaces || []).map(ws => (
              <button
                key={ws.key}
                onClick={() => { setOpen(false); onSelect(ws.key) }}
                className={`w-full text-left px-3 py-2 text-sm transition-colors flex items-center justify-between gap-2 ${
                  ws.key === current
                    ? 'bg-surface-overlay text-content-strong font-semibold'
                    : 'text-content hover:bg-surface-overlay hover:text-content-strong'
                }`}
              >
                <span className="truncate">{ws.name}</span>
                <span className="text-[10px] font-mono text-content-faint shrink-0">{ws.key}</span>
              </button>
            ))}
          </div>
          <div className="border-t border-border-strong mt-1 pt-1">
            {canCreate && (
              <button
                onClick={() => { setOpen(false); onNewWorkspace() }}
                className="w-full text-left px-3 py-2 text-sm text-brand-400 hover:bg-surface-overlay transition-colors"
              >
                ＋ New workspace
              </button>
            )}
          </div>
        </div>
      )}
    </div>
  )
}

const DISPLAY_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9 _-]{0,31}$/

function NewWorkspaceModal({ onClose, onCreated }) {
  const [name, setName] = useState('')
  const [key, setKey]   = useState('')
  const [keyValid, setKeyValid] = useState(false)
  const [error, setError] = useState('')
  const mutation = useMutation({
    mutationFn: () => createWorkspaceTier(name.trim(), key),
    onSuccess: (data) => onCreated(data.key, data.name),
    onError: (e) => setError(e.response?.data?.error || 'Failed to create workspace'),
  })

  const nameOk = DISPLAY_NAME_RE.test(name.trim())

  function submit(e) {
    e.preventDefault()
    setError('')
    if (!nameOk) {
      setError('Name: 1–32 chars, letters/digits/space/dash/underscore (must start alphanumeric).')
      return
    }
    if (!keyValid) { setError('Pick a valid, available key.'); return }
    mutation.mutate()
  }

  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <form
        className="bg-surface border border-border rounded-xl w-full max-w-sm mx-4 p-6 space-y-4"
        onClick={e => e.stopPropagation()}
        onSubmit={submit}
      >
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">New workspace</h3>
          <button type="button" onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>
        <p className="text-sm text-content-muted">
          A workspace groups related projects. The display name can be descriptive;
          the short key identifies it in folders, URLs and Docker names.
        </p>
        {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Workspace name</label>
          <input
            autoFocus value={name} onChange={e => setName(e.target.value)} required maxLength={32}
            placeholder="e.g. Acme Corporation"
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 transition-colors"
          />
        </div>
        <KeyField type="workspace" name={name} label="Workspace key" onChange={(k, v) => { setKey(k); setKeyValid(v) }} />
        <button
          type="submit" disabled={mutation.isPending || !nameOk || !keyValid}
          className="w-full bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-semibold py-2 rounded-lg transition-colors"
        >
          {mutation.isPending ? 'Creating…' : 'Create workspace'}
        </button>
      </form>
    </div>
  )
}

function ChangePasswordModal({ onClose }) {
  const [current, setCurrent] = useState('')
  const [next, setNext]       = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError]     = useState('')

  const mutation = useMutation({
    mutationFn: () => changePassword(current, next),
    onSuccess: onClose,
    onError: (e) => setError(e.response?.data?.error || 'Failed to change password'),
  })

  function submit(e) {
    e.preventDefault()
    setError('')
    if (next.length < 8) { setError('New password must be at least 8 characters'); return }
    if (next !== confirm) { setError('Passwords do not match'); return }
    mutation.mutate()
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <form
        className="bg-surface border border-border rounded-xl w-full max-w-sm mx-4 p-6 space-y-4"
        onClick={e => e.stopPropagation()}
        onSubmit={submit}
      >
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">Change password</h3>
          <button type="button" onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>
        {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
        {['Current password', 'New password', 'Confirm new password'].map((label, i) => {
          const val  = [current, next, confirm][i]
          const set  = [setCurrent, setNext, setConfirm][i]
          return (
            <div key={label}>
              <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">{label}</label>
              <input
                type="password" value={val} onChange={e => set(e.target.value)} required
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 transition-colors"
              />
            </div>
          )
        })}
        <button
          type="submit" disabled={mutation.isPending}
          className="w-full bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-semibold py-2 rounded-lg transition-colors"
        >
          {mutation.isPending ? 'Saving…' : 'Update password'}
        </button>
      </form>
    </div>
  )
}

function UserMenu({ user, onLogout }) {
  const [open, setOpen]   = useState(false)
  const [acct, setAcct]   = useState(null) // null | 'general' | 'security' | 'appearance'
  const [reqOpen, setReqOpen] = useState(false)
  const ref = useRef(null)

  useEffect(() => {
    if (!open) return
    function handler(e) { if (ref.current && !ref.current.contains(e.target)) setOpen(false) }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  return (
    <>
      <div ref={ref} className="relative">
        <button
          onClick={() => setOpen(o => !o)}
          className="flex items-center gap-2 px-3 py-1.5 text-sm text-content-muted hover:text-content-strong rounded-lg hover:bg-surface-raised transition-colors"
        >
          <span className="w-6 h-6 rounded-full bg-brand-700 text-white text-xs font-bold flex items-center justify-center shrink-0">
            {user?.sub?.[0]?.toUpperCase() || '?'}
          </span>
          <span>{user?.sub}</span>
          <span className="text-xs text-content-faint">▾</span>
        </button>

        {open && (
          <div className="absolute right-0 top-full mt-1 z-30 bg-surface-raised border border-border-strong rounded-xl shadow-xl min-w-[180px] py-1 overflow-hidden">
            <div className="px-3 py-2 border-b border-border-strong">
              <p className="text-xs text-content-muted">Signed in as</p>
              <p className="text-sm font-semibold text-content-strong truncate">{user?.sub}</p>
            </div>
            <button
              onClick={() => { setOpen(false); setAcct('general') }}
              className="w-full text-left px-3 py-2 text-sm text-content hover:bg-surface-overlay hover:text-content-strong transition-colors"
            >
              Account settings
            </button>
            <button
              onClick={() => { setOpen(false); setReqOpen(true) }}
              className="w-full text-left px-3 py-2 text-sm text-content hover:bg-surface-overlay hover:text-content-strong transition-colors"
            >
              Request access
            </button>
            <div className="border-t border-border-strong mt-1 pt-1">
              <button
                onClick={() => { setOpen(false); onLogout() }}
                className="w-full text-left px-3 py-2 text-sm text-danger-fg hover:bg-surface-overlay hover:text-danger-fg transition-colors"
              >
                Sign out
              </button>
            </div>
          </div>
        )}
      </div>

      {acct && <AccountModal user={user} tab={acct} setTab={setAcct} onClose={() => setAcct(null)} />}
      {reqOpen && <RequestAccessModal onClose={() => setReqOpen(false)} />}
    </>
  )
}

function ProfileModal({ user, onClose }) {
  const tryRefresh = useAuthStore(s => s.tryRefresh)
  const [email, setEmail] = useState(user?.email || '')
  const [phone, setPhone] = useState('')
  const [username, setUsername] = useState(user?.sub || '')
  const [msg, setMsg] = useState('')
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)

  async function save(e) {
    e.preventDefault()
    setBusy(true); setMsg(''); setLink('')
    try {
      const res = await updateProfile({ email: email.trim(), phone: phone.trim(), username: username.trim() })
      if (res.verify_link) setLink(res.verify_link)
      else if (res.email_sent) setMsg('Saved — a verification link was emailed to you.')
      else setMsg('Saved.')
      await tryRefresh().catch(() => {})
    } catch (err) {
      setMsg(err.response?.data?.error || 'Failed to save')
    } finally { setBusy(false) }
  }

  async function resend() {
    setBusy(true); setMsg(''); setLink('')
    try {
      const res = await resendVerification()
      if (res.verify_link) setLink(res.verify_link)
      else if (res.already_verified) setMsg('Your email is already verified.')
      else setMsg('Verification link sent to your email.')
    } catch (err) {
      setMsg(err.response?.data?.error || 'Failed')
    } finally { setBusy(false) }
  }

  const inp = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
  const lbl = 'block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1'

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-md p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">Your profile</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>
        {user && user.ev === false && (
          <div className="flex items-center justify-between gap-2 px-3 py-2 bg-warning-subtle/40 border border-warning-border/50 rounded-lg">
            <span className="text-xs text-warning-fg">Your email isn't verified.</span>
            <button onClick={resend} disabled={busy} className="text-xs font-medium text-warning-fg underline underline-offset-2">Resend link</button>
          </div>
        )}
        <form onSubmit={save} className="space-y-3">
          <div><label className={lbl}>Email</label><input className={inp} type="email" value={email} onChange={e => setEmail(e.target.value)} /></div>
          <div><label className={lbl}>Display name</label><input className={inp} value={username} onChange={e => setUsername(e.target.value)} /></div>
          <div><label className={lbl}>Phone (optional)</label><input className={inp} type="tel" value={phone} onChange={e => setPhone(e.target.value)} placeholder="+1 555 0100" /></div>
          {msg && <p className="text-xs text-content-muted">{msg}</p>}
          {link && (
            <div className="text-xs">
              <p className="text-content-muted mb-1">No system email configured — open this link to verify:</p>
              <a href={link} className="text-brand-400 hover:underline font-mono break-all">{link}</a>
            </div>
          )}
          <div className="flex justify-end gap-2 pt-1">
            <button type="button" onClick={onClose} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Close</button>
            <button type="submit" disabled={busy} className="px-4 py-2 text-sm font-semibold rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white">{busy ? 'Saving…' : 'Save'}</button>
          </div>
        </form>
      </div>
    </div>
  )
}

export default function Layout({ children }) {
  const user     = useAuthStore((s) => s.user)
  const isAdmin  = user?.role === 'superadmin'  // global super-admin
  const logout   = useAuthStore((s) => s.logout)
  const navigate = useNavigate()
  const qc       = useQueryClient()
  const { workspace: routeWs, name: activeName } = useParams()
  const current    = useWorkspaceStore((s) => s.current)
  const setCurrent = useWorkspaceStore((s) => s.setCurrent)
  const [slidePanel, setSlidePanel] = useState(null) // 'activity' | 'backup' | 'version'
  const [newWsOpen, setNewWsOpen]   = useState(false)

  useDockerEvents()

  // List the parent-tier workspaces (the selector's options).
  const { data: workspaces } = useQuery({
    queryKey: ['workspaces'],
    queryFn: fetchWorkspaces,
    refetchInterval: 30_000,
  })

  // Phase 5.2b: only workspace admins (or global admins) manage a workspace or
  // create projects in it. my_role comes from the (membership-filtered) list.
  const currentWsInfo = (workspaces || []).find(w => w.key === current)
  const canAdminWs = isAdmin || currentWsInfo?.my_role === 'admin'
  // A signed-in user with no workspace membership (and not a super-admin) has no
  // access to anything — hide cross-workspace surfaces (activity/backup/alerts).
  const hasAccess = isAdmin || (Array.isArray(workspaces) && workspaces.length > 0)

  // The route is authoritative: a deep-link to /workspaces/:workspace/... syncs
  // the selected workspace. Otherwise, default to the first available workspace
  // once the list loads and nothing is selected yet.
  useEffect(() => {
    if (routeWs && routeWs !== current) { setCurrent(routeWs); return }
    if (!Array.isArray(workspaces)) return
    // A persisted selection the user can no longer access (e.g. a different user
    // signed in) must not stick — fall back to the first accessible workspace.
    if (current && !workspaces.some(w => w.key === current)) {
      setCurrent(workspaces[0]?.key || '')
      return
    }
    if (!current && workspaces.length) setCurrent(workspaces[0].key)
  }, [routeWs, current, workspaces, setCurrent])

  // Projects belonging to the selected workspace (the sidebar list).
  const { data: projects } = useQuery({
    queryKey: ['projects', current],
    queryFn: () => fetchProjects(current),
    enabled: !!current,
    refetchInterval: 30_000,
  })

  function selectWorkspace(ws) {
    if (ws === current) return
    setCurrent(ws)
    // Project-scoped caches belong to the previous workspace — drop them so the
    // new workspace's data can't be served from a stale same-named key.
    qc.removeQueries({ queryKey: ['projects'] })
    navigate('/')
  }

  async function handleLogout() {
    await logout()
    // Drop the selected workspace + all cached project data so the next user who
    // signs in (e.g. in the same tab) never sees the previous session's data.
    setCurrent('')
    qc.clear()
    navigate('/login')
  }

  return (
    <div className="h-screen overflow-hidden bg-canvas flex flex-col">
      {/* Top nav */}
      <nav className="border-b border-border bg-surface shrink-0 z-10">
        <div className="px-4 h-12 flex items-center justify-between">
          <div className="flex items-center gap-2.5">
            <Link to="/" className="flex items-center shrink-0">
              <img src="/rigger-icon.png" alt="Rigger" className="w-8 h-8 rounded-lg" />
            </Link>
            <WorkspaceSelector
              current={current}
              workspaces={workspaces}
              onSelect={selectWorkspace}
              onNewWorkspace={() => setNewWsOpen(true)}
              onManage={() => navigate(`/workspaces/${current}/manage`)}
              canManage={canAdminWs}
              canCreate={isAdmin}
            />
          </div>
          <div className="flex items-center gap-1">
            <NavBtn to="/" label="Dashboard" />
            {isAdmin && <NavBtn to="/housekeeping" label="Housekeeping" />}
            <NavBtn to="/tools" label="Tools" />
            {isAdmin && <NavBtn to="/settings" label="Admin" />}
            <div className="w-px h-4 bg-surface-overlay mx-1" />
            <ThemeToggle />
            {hasAccess && <AlertBell active={slidePanel === 'alerts'} onClick={() => setSlidePanel(p => p === 'alerts' ? null : 'alerts')} />}
            <UserMenu user={user} onLogout={handleLogout} />
          </div>
        </div>
      </nav>

      <div className="flex flex-1 min-h-0">
        {/* Sidebar */}
        <aside className="w-56 shrink-0 border-r border-border bg-surface flex flex-col min-h-0">
          {/* Project list — scrolls internally so the actions below stay in view */}
          <div className="flex-1 min-h-0 flex flex-col p-3 border-b border-border">
            <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider px-1 mb-2 shrink-0">Projects</p>
            <div className="space-y-0.5 overflow-y-auto min-h-0">
              {!current ? (
                <p className="text-xs text-content-subtle px-1 py-2">Select a workspace to see its projects.</p>
              ) : (projects || []).length === 0 ? (
                <p className="text-xs text-content-subtle px-1 py-2">No projects in this workspace yet.</p>
              ) : (projects || []).map(p => (
                <ProjectSidebarItem
                  key={p.name}
                  workspace={current}
                  project={p}
                  active={p.name === activeName && current === routeWs}
                />
              ))}
            </div>
          </div>

          {/* Actions — pinned at the bottom, always visible */}
          <div className="p-3 space-y-1.5 shrink-0">
            {canAdminWs && (
              <Link
                to={current ? `/workspaces/${current}/projects/new` : '#'}
                onClick={(e) => { if (!current) { e.preventDefault(); setNewWsOpen(true) } }}
                className="flex items-center gap-2 px-3 py-2 text-sm font-semibold text-white bg-brand-600 hover:bg-brand-700 rounded-lg transition-colors"
              >
                <span className="text-base leading-none">＋</span>
                New project
              </Link>
            )}
            {hasAccess && (
              <div className="pt-1 space-y-0.5">
                <SidebarBtn label="Recent activity" icon="◎" active={slidePanel === 'activity'} onClick={() => setSlidePanel(p => p === 'activity' ? null : 'activity')} />
                <SidebarBtn label="Backup history"  icon="○" active={slidePanel === 'backup'}   onClick={() => setSlidePanel(p => p === 'backup'   ? null : 'backup')} />
                <SidebarBtn label="Version log"     icon="○" active={slidePanel === 'version'}  onClick={() => setSlidePanel(p => p === 'version'  ? null : 'version')} />
              </div>
            )}
          </div>
          <VersionFooter />
        </aside>

        {/* Main content */}
        <main className="flex-1 overflow-auto">
          {user && user.ev === false && <UnverifiedBanner />}
          {!isAdmin && Array.isArray(workspaces) && workspaces.length === 0 ? <NoAccess /> : children}
        </main>
      </div>

      {/* Slide-out panels (Activity / Backup / Version) */}
      {slidePanel && (
        <SlideOutPanel panel={slidePanel} workspace={current} onClose={() => setSlidePanel(null)} />
      )}

      {/* New-workspace (tier) modal */}
      {newWsOpen && (
        <NewWorkspaceModal
          onClose={() => setNewWsOpen(false)}
          onCreated={async (key) => {
            setNewWsOpen(false)
            // Refetch the list FIRST so it contains the new key before we select it.
            // Otherwise the sync effect sees `current` as "not in workspaces" and
            // reverts to workspaces[0] (the previous workspace) — the just-created
            // workspace would silently lose the selection.
            await qc.refetchQueries({ queryKey: ['workspaces'] })
            setCurrent(key)
            navigate('/')
          }}
        />
      )}
    </div>
  )
}

// NoAccess is shown to a signed-in non-admin who isn't a member of any
// workspace yet — so they land on a clear message instead of a blank screen.
function NoAccess() {
  const [reqOpen, setReqOpen] = useState(false)
  return (
    <div className="h-full flex items-center justify-center p-8">
      <div className="max-w-md text-center">
        <div className="mx-auto w-14 h-14 rounded-2xl bg-surface-raised border border-border flex items-center justify-center text-2xl mb-4">🔒</div>
        <h1 className="text-lg font-semibold text-content-strong">No workspace access yet</h1>
        <p className="text-sm text-content-muted mt-2">
          You don't have access to any workspace or project. Please contact your administrator to get access.
        </p>
        <button
          onClick={() => setReqOpen(true)}
          className="mt-5 px-4 py-2 bg-brand-600 hover:bg-brand-700 text-white text-sm font-semibold rounded-lg transition-colors"
        >
          Request access
        </button>
        <p className="text-xs text-content-faint mt-4">
          Once you've been added to a workspace, it will appear in the selector at the top-left.
        </p>
      </div>
      {reqOpen && <RequestAccessModal onClose={() => setReqOpen(false)} />}
    </div>
  )
}

// AccountModal — tabbed account settings (General profile / Security password /
// Appearance). Appearance is per-user and overrides the workspace/global default.
function AccountModal({ user, tab, setTab, onClose }) {
  const tabs = [
    { id: 'general',    label: 'General' },
    { id: 'security',   label: 'Security' },
    { id: 'appearance', label: 'Appearance' },
  ]
  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-4">
          <h3 className="font-semibold text-content-strong">Account settings</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>
        <div className="flex gap-1 border-b border-border mb-5">
          {tabs.map(t => (
            <button key={t.id} onClick={() => setTab(t.id)}
              className={`px-4 py-2 text-sm font-medium border-b-2 transition-colors -mb-px ${
                tab === t.id ? 'border-brand-500 text-brand-400' : 'border-transparent text-content-subtle hover:text-content'
              }`}>
              {t.label}
            </button>
          ))}
        </div>
        {tab === 'general'    && <AccountGeneral user={user} />}
        {tab === 'security'   && <AccountSecurity onDone={onClose} />}
        {tab === 'appearance' && (
          <div className="space-y-3">
            <p className="text-sm text-content-subtle">Your personal appearance. It overrides the workspace and global defaults on every device you sign in to.</p>
            <AppearanceTab />
          </div>
        )}
      </div>
    </div>
  )
}

function AccountGeneral({ user }) {
  const tryRefresh = useAuthStore(s => s.tryRefresh)
  // Pre-fill from the DB record (authoritative) so editing one field doesn't blank
  // the others, and the display name shows the real value (not the login email).
  const { data: profile } = useQuery({ queryKey: ['my-profile'], queryFn: fetchProfile })
  const [email, setEmail] = useState(user?.email || '')
  const [phone, setPhone] = useState('')
  const [username, setUsername] = useState(user?.sub || '')
  const [seeded, setSeeded] = useState(false)
  const [msg, setMsg] = useState('')
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)
  useEffect(() => {
    if (profile && !seeded) {
      setEmail(profile.email || '')
      setUsername(profile.username || '')
      setPhone(profile.phone || '')
      setSeeded(true)
    }
  }, [profile, seeded])
  const inp = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
  const lbl = 'block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1'
  async function save(e) {
    e.preventDefault(); setBusy(true); setMsg(''); setLink('')
    try {
      const res = await updateProfile({ email: email.trim(), phone: phone.trim(), username: username.trim() })
      if (res.verify_link) setLink(res.verify_link)
      else if (res.email_sent) setMsg('Saved — a verification link was emailed to you.')
      else setMsg('Saved.')
      await tryRefresh().catch(() => {})
    } catch (err) { setMsg(err.response?.data?.error || 'Failed to save') } finally { setBusy(false) }
  }
  return (
    <form onSubmit={save} className="space-y-3 max-w-md">
      <div><label className={lbl}>Email</label><input className={inp} type="email" value={email} onChange={e => setEmail(e.target.value)} /></div>
      <div><label className={lbl}>Display name</label><input className={inp} value={username} onChange={e => setUsername(e.target.value)} /></div>
      <div><label className={lbl}>Phone (optional)</label><input className={inp} type="tel" value={phone} onChange={e => setPhone(e.target.value)} placeholder="+1 555 0100" /></div>
      {msg && <p className="text-xs text-content-muted">{msg}</p>}
      {link && <div className="text-xs"><p className="text-content-muted mb-1">No system email configured — open this link to verify:</p><a href={link} className="text-brand-400 hover:underline font-mono break-all">{link}</a></div>}
      <button type="submit" disabled={busy} className="bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-semibold px-4 py-2 rounded-lg">{busy ? 'Saving…' : 'Save profile'}</button>
    </form>
  )
}

function AccountSecurity({ onDone }) {
  const [current, setCurrent] = useState('')
  const [next, setNext] = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError] = useState('')
  const mutation = useMutation({
    mutationFn: () => changePassword(current, next),
    onSuccess: onDone,
    onError: (e) => setError(e.response?.data?.error || 'Failed to change password'),
  })
  function submit(e) {
    e.preventDefault(); setError('')
    if (next.length < 8) { setError('New password must be at least 8 characters'); return }
    if (next !== confirm) { setError('Passwords do not match'); return }
    mutation.mutate()
  }
  return (
    <div className="space-y-10 max-w-md">
    <form onSubmit={submit} className="space-y-3">
      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
      {['Current password', 'New password', 'Confirm new password'].map((label, i) => {
        const val = [current, next, confirm][i]; const set = [setCurrent, setNext, setConfirm][i]
        return (
          <div key={label}>
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">{label}</label>
            <input type="password" value={val} onChange={e => set(e.target.value)} required
              className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500" />
          </div>
        )
      })}
      <button type="submit" disabled={mutation.isPending} className="bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-semibold px-4 py-2 rounded-lg">{mutation.isPending ? 'Saving…' : 'Update password'}</button>
    </form>
    <TwoFactorSection />
    </div>
  )
}

// RecoveryCodeList — shows a freshly-generated set of single-use codes ONCE, with
// copy/download. The user must save these before continuing.
function RecoveryCodeList({ codes, onDone }) {
  function copy() { navigator.clipboard?.writeText(codes.join('\n')).catch(() => {}) }
  function download() {
    const blob = new Blob([`Rigger 2FA recovery codes\nKeep these somewhere safe — each works once.\n\n${codes.join('\n')}\n`], { type: 'text/plain' })
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a'); a.href = url; a.download = 'rigger-recovery-codes.txt'; a.click()
    URL.revokeObjectURL(url)
  }
  return (
    <div className="space-y-3 p-4 bg-warning-subtle/30 border border-warning-border/50 rounded-lg">
      <p className="text-sm text-warning-fg font-semibold">Save your recovery codes</p>
      <p className="text-xs text-content-muted">Each code works once. Use one to sign in if you lose your authenticator. They won't be shown again.</p>
      <div className="grid grid-cols-2 gap-x-6 gap-y-1 font-mono text-sm text-content-strong">
        {codes.map(c => <span key={c} className="select-all">{c}</span>)}
      </div>
      <div className="flex gap-2 pt-1">
        <button onClick={copy} className="text-sm bg-surface-raised border border-border rounded-lg px-3 py-1.5 hover:bg-surface-hover">Copy</button>
        <button onClick={download} className="text-sm bg-surface-raised border border-border rounded-lg px-3 py-1.5 hover:bg-surface-hover">Download</button>
        <button onClick={onDone} className="text-sm bg-brand-600 hover:bg-brand-700 text-white font-semibold rounded-lg px-3 py-1.5">I've saved them</button>
      </div>
    </div>
  )
}

// TwoFactorSection — optional TOTP 2FA enrollment / disable, shown in the account
// modal's Security tab. Enrollment shows a QR (scan) + the secret (manual) and
// requires a verifying code; on success it surfaces single-use recovery codes.
function TwoFactorSection() {
  const qc = useQueryClient()
  const { data: status } = useQuery({ queryKey: ['twofa-status'], queryFn: fetch2FAStatus })
  const enabled = !!status?.enabled

  const [enroll, setEnroll] = useState(null)   // { secret, otpauth_url } while enrolling
  const [code, setCode]     = useState('')
  const [error, setError]   = useState('')
  const [newCodes, setNewCodes] = useState(null) // freshly-generated recovery codes to show once
  const [regen, setRegen]   = useState(false)    // showing the "regenerate" code prompt

  const beginMut = useMutation({
    mutationFn: begin2FA,
    onSuccess: (d) => { setEnroll(d); setCode(''); setError('') },
    onError: (e) => setError(e.response?.data?.error || 'Could not start enrollment'),
  })
  const enableMut = useMutation({
    mutationFn: () => enable2FA(code),
    onSuccess: (d) => { setEnroll(null); setCode(''); setError(''); setNewCodes(d.recovery_codes || []); qc.invalidateQueries({ queryKey: ['twofa-status'] }) },
    onError: (e) => setError(e.response?.data?.error || 'Invalid code'),
  })
  const disableMut = useMutation({
    mutationFn: () => disable2FA(code),
    onSuccess: () => { setCode(''); setError(''); qc.invalidateQueries({ queryKey: ['twofa-status'] }) },
    onError: (e) => setError(e.response?.data?.error || 'Invalid code'),
  })
  const regenMut = useMutation({
    mutationFn: () => regen2FACodes(code),
    onSuccess: (d) => { setCode(''); setError(''); setRegen(false); setNewCodes(d.recovery_codes || []); qc.invalidateQueries({ queryKey: ['twofa-status'] }) },
    onError: (e) => setError(e.response?.data?.error || 'Invalid code'),
  })

  // Accepts a 6-digit TOTP or an alphanumeric recovery code (uppercased to match
  // the server's normalization). The enable step gates on exactly 6 chars (TOTP only,
  // since recovery codes don't exist pre-enable); disable/regenerate accept either.
  const codeInput = (
    <input type="text" autoComplete="one-time-code" maxLength={14} value={code}
      onChange={e => setCode(e.target.value.toUpperCase())} placeholder="123456 or recovery code"
      className="w-56 px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong tracking-widest focus:outline-none focus:border-brand-500" />
  )

  // After enabling or regenerating, show the codes once before returning to status.
  if (newCodes) {
    return (
      <div className="space-y-3 border-t border-border/60 pt-6">
        <h3 className="font-semibold text-content-strong">Two-factor authentication</h3>
        <RecoveryCodeList codes={newCodes} onDone={() => setNewCodes(null)} />
      </div>
    )
  }

  return (
    <div className="space-y-3 border-t border-border/60 pt-6">
      <div>
        <h3 className="font-semibold text-content-strong">Two-factor authentication</h3>
        <p className="text-sm text-content-muted">
          {enabled
            ? `Enabled — a code from your authenticator app is required at sign-in.${typeof status?.recovery_remaining === 'number' ? ` ${status.recovery_remaining} recovery code${status.recovery_remaining === 1 ? '' : 's'} left.` : ''}`
            : 'Add a one-time code from an authenticator app (Google Authenticator, 1Password, Authy, …) as a second factor.'}
        </p>
      </div>
      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}

      {!enabled && !enroll && (
        <button onClick={() => beginMut.mutate()} disabled={beginMut.isPending}
          className="bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-semibold px-4 py-2 rounded-lg">
          {beginMut.isPending ? 'Starting…' : 'Enable 2FA'}
        </button>
      )}

      {!enabled && enroll && (
        <div className="space-y-3">
          <p className="text-sm text-content-muted">Scan this with your authenticator app, or add an account manually with the secret below.</p>
          <div className="inline-block bg-white p-3 rounded-lg">
            <QRCodeSVG value={enroll.otpauth_url} size={160} />
          </div>
          <div className="px-3 py-2 bg-surface-raised border border-border rounded-lg font-mono text-sm break-all select-all">{enroll.secret}</div>
          {codeInput}
          <div className="flex gap-2">
            <button onClick={() => enableMut.mutate()} disabled={enableMut.isPending || code.length !== 6}
              className="bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-semibold px-4 py-2 rounded-lg">
              {enableMut.isPending ? 'Verifying…' : 'Verify & enable'}
            </button>
            <button onClick={() => { setEnroll(null); setCode(''); setError('') }} className="text-sm text-content-muted hover:text-content px-3 py-2">Cancel</button>
          </div>
        </div>
      )}

      {enabled && !regen && (
        <div className="space-y-3">
          <button onClick={() => { setRegen(true); setCode(''); setError('') }}
            className="text-sm bg-surface-raised border border-border rounded-lg px-3 py-1.5 hover:bg-surface-hover">
            Regenerate recovery codes
          </button>
          <div className="space-y-2">
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider">Enter a current code to disable</label>
            {codeInput}
            <div>
              <button onClick={() => disableMut.mutate()} disabled={disableMut.isPending || code.length < 6}
                className="bg-red-600 hover:bg-red-700 disabled:opacity-50 text-white text-sm font-semibold px-4 py-2 rounded-lg">
                {disableMut.isPending ? 'Disabling…' : 'Disable 2FA'}
              </button>
            </div>
          </div>
        </div>
      )}

      {enabled && regen && (
        <div className="space-y-2">
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider">Enter a current code to issue new recovery codes</label>
          <p className="text-xs text-content-subtle">This invalidates your existing recovery codes.</p>
          {codeInput}
          <div className="flex gap-2">
            <button onClick={() => regenMut.mutate()} disabled={regenMut.isPending || code.length < 6}
              className="bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-semibold px-4 py-2 rounded-lg">
              {regenMut.isPending ? 'Generating…' : 'Generate new codes'}
            </button>
            <button onClick={() => { setRegen(false); setCode(''); setError('') }} className="text-sm text-content-muted hover:text-content px-3 py-2">Cancel</button>
          </div>
        </div>
      )}
    </div>
  )
}

// UnverifiedBanner nudges the signed-in user to verify their email. Resend
// surfaces the link directly when system email isn't configured.
function UnverifiedBanner() {
  const tryRefresh = useAuthStore(s => s.tryRefresh)
  const [msg, setMsg] = useState('')
  const [link, setLink] = useState('')
  const [busy, setBusy] = useState(false)
  async function resend() {
    setBusy(true); setMsg(''); setLink('')
    try {
      const res = await resendVerification()
      if (res.verify_link) setLink(res.verify_link)
      else if (res.already_verified) { setMsg('Already verified.'); await tryRefresh().catch(() => {}) }
      else setMsg('Verification link sent to your email.')
    } catch { setMsg('Could not send link.') } finally { setBusy(false) }
  }
  return (
    <div className="px-4 py-2 bg-warning-subtle/40 border-b border-warning-border/50 text-sm text-warning-fg flex items-center gap-3 flex-wrap">
      <span>⚠ Your email isn't verified. Some actions (inviting users, email alerts) need a verified address.</span>
      <button onClick={resend} disabled={busy} className="font-medium underline underline-offset-2 disabled:opacity-50">{busy ? 'Sending…' : 'Resend link'}</button>
      {msg && <span className="text-content-muted">{msg}</span>}
      {link && <a href={link} className="font-mono text-xs text-brand-500 hover:underline break-all">{link}</a>}
    </div>
  )
}

function NavBtn({ to, label }) {
  return (
    <Link
      to={to}
      className="px-3 py-1.5 text-sm text-content hover:text-content-strong rounded-lg hover:bg-surface-raised transition-colors border border-border-strong hover:border-border-strong"
    >
      {label}
    </Link>
  )
}

// AlertBell — nav bell with an unread-alert badge. The count is kept live by
// SSE (useDockerEvents pushes unread_count into the ['alertUnread'] cache); the
// 60s refetch is just a reconnect-safety fallback.
function AlertBell({ active, onClick }) {
  const { data } = useQuery({
    queryKey: ['alertUnread'],
    queryFn: fetchAlertUnread,
    refetchInterval: 60_000,
    retry: false,
  })
  const count = data?.unread_count || 0
  return (
    <button
      onClick={onClick}
      title="Alerts"
      className={`relative flex items-center justify-center w-9 h-8 rounded-lg transition-colors ${
        active ? 'bg-surface-raised text-content-strong' : 'text-content-muted hover:text-content-strong hover:bg-surface-raised'
      }`}
    >
      <svg xmlns="http://www.w3.org/2000/svg" className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
        <path strokeLinecap="round" strokeLinejoin="round" d="M14.857 17.082a23.848 23.848 0 0 0 5.454-1.31A8.967 8.967 0 0 1 18 9.75V9A6 6 0 0 0 6 9v.75a8.967 8.967 0 0 1-2.312 6.022c1.733.64 3.56 1.085 5.455 1.31m6.714 0a3 3 0 1 1-6.714 0m6.714 0a23.88 23.88 0 0 1-6.714 0" />
      </svg>
      {count > 0 && (
        <span className="absolute -top-0.5 -right-0.5 min-w-[16px] h-4 px-1 flex items-center justify-center text-[10px] font-bold text-white bg-red-500 rounded-full">
          {count > 99 ? '99+' : count}
        </span>
      )}
    </button>
  )
}

function SidebarAction({ to, label, icon }) {
  return (
    <Link
      to={to}
      className="flex items-center gap-2 px-3 py-2 text-sm text-content-subtle hover:text-content rounded-lg hover:bg-surface-raised/60 transition-colors"
    >
      <span className="text-xs">{icon}</span>
      {label}
    </Link>
  )
}

function SidebarBtn({ label, icon, onClick, active }) {
  return (
    <button
      onClick={onClick}
      className={`w-full flex items-center gap-2 px-3 py-2 text-sm rounded-lg transition-colors ${
        active
          ? 'bg-surface-raised text-content'
          : 'text-content-subtle hover:text-content hover:bg-surface-raised/60'
      }`}
    >
      <span className="text-xs">{icon}</span>
      {label}
      {active && <span className="ml-auto text-xs text-content-subtle">▶</span>}
    </button>
  )
}
