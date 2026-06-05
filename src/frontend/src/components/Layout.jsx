import { Link, useNavigate, useParams } from 'react-router-dom'
import { useState, useRef, useEffect } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { useAuthStore } from '../store/auth'
import { fetchWorkspaces, fetchEnvStatus, changePassword, fetchAlertUnread } from '../lib/api'
import { useDockerEvents } from '../hooks/useDockerEvents'
import SlideOutPanel from './SlideOutPanel'

const STATUS_DOT = {
  running: 'bg-green-400',
  partial: 'bg-amber-400 animate-pulse',
  stopped: 'bg-red-500',
  building: 'bg-amber-400 animate-pulse',
  unknown:  'bg-gray-600',
}

// Polls the first environment of a workspace to determine its dot color
function WorkspaceStatusDot({ name, envs }) {
  const firstEnv = envs?.[0]
  const { data } = useQuery({
    queryKey: ['envstatus', name, firstEnv],
    queryFn: () => fetchEnvStatus(name, firstEnv),
    enabled: !!firstEnv,
    refetchInterval: 30_000, // SSE handles real-time; this is just a fallback
    retry: false,
  })
  const status = data?.status || 'unknown'
  return <span className={`w-2 h-2 rounded-full shrink-0 ${STATUS_DOT[status] || STATUS_DOT.unknown}`} />
}

function WorkspaceSidebarItem({ ws, active }) {
  const cfg = ws.config
  const type = cfg?.project?.type || 'custom'
  const envs = ws.envs || []

  // Derive a short stack description from config
  let stackLine = ''
  if (type === 'image') {
    const images = cfg?.images || []
    stackLine = images.map(i => i.image?.split('/').pop()).join(' · ')
  } else {
    const firstEnv = cfg?.environments?.[envs[0]] || {}
    const parts = [firstEnv.backend, firstEnv.frontend].filter(Boolean)
    stackLine = parts.join(' · ') || type
  }

  return (
    <Link
      to={`/workspaces/${ws.name}`}
      className={`block px-3 py-2.5 rounded-lg transition-colors ${
        active
          ? 'bg-gray-800 text-white'
          : 'text-gray-400 hover:bg-gray-800/60 hover:text-gray-200'
      }`}
    >
      <div className="flex items-center gap-2.5">
        <WorkspaceStatusDot name={ws.name} envs={ws.envs} />
        <span className="font-medium text-sm truncate">{ws.name}</span>
      </div>
      {stackLine && (
        <p className="text-xs text-gray-500 mt-0.5 ml-4.5 truncate pl-4">{stackLine}</p>
      )}
    </Link>
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
        className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-sm mx-4 p-6 space-y-4"
        onClick={e => e.stopPropagation()}
        onSubmit={submit}
      >
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-white">Change password</h3>
          <button type="button" onClick={onClose} className="text-gray-500 hover:text-white text-xl">×</button>
        </div>
        {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}
        {['Current password', 'New password', 'Confirm new password'].map((label, i) => {
          const val  = [current, next, confirm][i]
          const set  = [setCurrent, setNext, setConfirm][i]
          return (
            <div key={label}>
              <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">{label}</label>
              <input
                type="password" value={val} onChange={e => set(e.target.value)} required
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500 transition-colors"
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
  const [pwOpen, setPwOpen] = useState(false)
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
          className="flex items-center gap-2 px-3 py-1.5 text-sm text-gray-400 hover:text-white rounded-lg hover:bg-gray-800 transition-colors"
        >
          <span className="w-6 h-6 rounded-full bg-brand-700 text-white text-xs font-bold flex items-center justify-center shrink-0">
            {user?.sub?.[0]?.toUpperCase() || '?'}
          </span>
          <span>{user?.sub}</span>
          <span className="text-xs text-gray-600">▾</span>
        </button>

        {open && (
          <div className="absolute right-0 top-full mt-1 z-30 bg-gray-800 border border-gray-700 rounded-xl shadow-xl min-w-[180px] py-1 overflow-hidden">
            <div className="px-3 py-2 border-b border-gray-700">
              <p className="text-xs text-gray-400">Signed in as</p>
              <p className="text-sm font-semibold text-white truncate">{user?.sub}</p>
            </div>
            <button
              onClick={() => { setOpen(false); setPwOpen(true) }}
              className="w-full text-left px-3 py-2 text-sm text-gray-300 hover:bg-gray-700 hover:text-white transition-colors"
            >
              Change password
            </button>
            <div className="border-t border-gray-700 mt-1 pt-1">
              <button
                onClick={() => { setOpen(false); onLogout() }}
                className="w-full text-left px-3 py-2 text-sm text-red-400 hover:bg-gray-700 hover:text-red-300 transition-colors"
              >
                Sign out
              </button>
            </div>
          </div>
        )}
      </div>

      {pwOpen && <ChangePasswordModal onClose={() => setPwOpen(false)} />}
    </>
  )
}

export default function Layout({ children }) {
  const user     = useAuthStore((s) => s.user)
  const logout   = useAuthStore((s) => s.logout)
  const navigate = useNavigate()
  const { name: activeName } = useParams()
  const [slidePanel, setSlidePanel] = useState(null) // 'activity' | 'backup' | 'version'

  useDockerEvents()

  const { data: workspaces } = useQuery({
    queryKey: ['workspaces'],
    queryFn: fetchWorkspaces,
    refetchInterval: 30_000,
  })

  async function handleLogout() {
    await logout()
    navigate('/login')
  }

  return (
    <div className="h-screen overflow-hidden bg-gray-950 flex flex-col">
      {/* Top nav */}
      <nav className="border-b border-gray-800 bg-gray-900 shrink-0 z-10">
        <div className="px-4 h-12 flex items-center justify-between">
          <div className="flex items-center gap-2.5">
            <Link to="/" className="flex items-center shrink-0">
              <img src="/rigger-icon.png" alt="Rigger" className="w-8 h-8 rounded-lg" />
            </Link>
            <span className="text-gray-400 text-sm hidden sm:inline">Rig once. Deploy anywhere</span>
          </div>
          <div className="flex items-center gap-1">
            <NavBtn to="/" label="Dashboard" />
            <NavBtn to="/housekeeping" label="Housekeeping" />
            <NavBtn to="/tools" label="Tools" />
            <NavBtn to="/settings" label="Settings" />
            <div className="w-px h-4 bg-gray-700 mx-1" />
            <AlertBell active={slidePanel === 'alerts'} onClick={() => setSlidePanel(p => p === 'alerts' ? null : 'alerts')} />
            <UserMenu user={user} onLogout={handleLogout} />
          </div>
        </div>
      </nav>

      <div className="flex flex-1 min-h-0">
        {/* Sidebar */}
        <aside className="w-56 shrink-0 border-r border-gray-800 bg-gray-900 flex flex-col min-h-0">
          {/* Workspace list — scrolls internally so the actions below stay in view */}
          <div className="flex-1 min-h-0 flex flex-col p-3 border-b border-gray-800">
            <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider px-1 mb-2 shrink-0">Workspaces</p>
            <div className="space-y-0.5 overflow-y-auto min-h-0">
              {(workspaces || []).map(ws => (
                <WorkspaceSidebarItem key={ws.name} ws={ws} active={ws.name === activeName} />
              ))}
            </div>
          </div>

          {/* Actions — pinned at the bottom, always visible */}
          <div className="p-3 space-y-1.5 shrink-0">
            <Link
              to="/new"
              className="flex items-center gap-2 px-3 py-2 text-sm font-semibold text-white bg-brand-600 hover:bg-brand-700 rounded-lg transition-colors"
            >
              <span className="text-base leading-none">＋</span>
              New workspace
            </Link>
            <div className="pt-1 space-y-0.5">
              <SidebarBtn label="Recent activity" icon="◎" active={slidePanel === 'activity'} onClick={() => setSlidePanel(p => p === 'activity' ? null : 'activity')} />
              <SidebarBtn label="Backup history"  icon="○" active={slidePanel === 'backup'}   onClick={() => setSlidePanel(p => p === 'backup'   ? null : 'backup')} />
              <SidebarBtn label="Version log"     icon="○" active={slidePanel === 'version'}  onClick={() => setSlidePanel(p => p === 'version'  ? null : 'version')} />
            </div>
          </div>
        </aside>

        {/* Main content */}
        <main className="flex-1 overflow-auto">
          {children}
        </main>
      </div>

      {/* Slide-out panels (Activity / Backup / Version) */}
      {slidePanel && (
        <SlideOutPanel panel={slidePanel} onClose={() => setSlidePanel(null)} />
      )}
    </div>
  )
}

function NavBtn({ to, label }) {
  return (
    <Link
      to={to}
      className="px-3 py-1.5 text-sm text-gray-300 hover:text-white rounded-lg hover:bg-gray-800 transition-colors border border-gray-700 hover:border-gray-600"
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
        active ? 'bg-gray-800 text-white' : 'text-gray-400 hover:text-white hover:bg-gray-800'
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
      className="flex items-center gap-2 px-3 py-2 text-sm text-gray-500 hover:text-gray-300 rounded-lg hover:bg-gray-800/60 transition-colors"
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
          ? 'bg-gray-800 text-gray-200'
          : 'text-gray-500 hover:text-gray-300 hover:bg-gray-800/60'
      }`}
    >
      <span className="text-xs">{icon}</span>
      {label}
      {active && <span className="ml-auto text-xs text-gray-500">▶</span>}
    </button>
  )
}
