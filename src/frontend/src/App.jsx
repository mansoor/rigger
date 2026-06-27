import { useEffect, useState } from 'react'
import { BrowserRouter, Routes, Route, Navigate, useLocation } from 'react-router-dom'
import { useAuthStore } from './store/auth'
import { changePassword } from './lib/api'
import ErrorBoundary from './components/ErrorBoundary'
import { ConfirmProvider } from './context/ConfirmContext'
import LoginPage from './pages/LoginPage'
import SetupPage from './pages/SetupPage'
import RegisterPage from './pages/RegisterPage'
import VerifyEmailPage from './pages/VerifyEmailPage'
import ForgotPasswordPage from './pages/ForgotPasswordPage'
import ResetPasswordPage from './pages/ResetPasswordPage'
import DashboardPage from './pages/DashboardPage'
import ProjectPage from './pages/ProjectPage'
import NewProjectPage from './pages/NewProjectPage'
import EditProjectPage from './pages/EditProjectPage'
import ManageWorkspacePage from './pages/ManageWorkspacePage'
import SettingsPage from './pages/SettingsPage'
import HousekeepingPage from './pages/HousekeepingPage'
import ToolsPage from './pages/ToolsPage'
import ProxyServicePage from './pages/ProxyServicePage'

function RequireAuth({ children }) {
  const token = useAuthStore((s) => s.token)
  const user  = useAuthStore((s) => s.user)
  const ready  = useAuthStore((s) => s.ready)
  if (!ready) return null
  if (!token) return <Navigate to="/login" replace />
  // Rotation policy: a password past its max age blocks the app until changed.
  if (user?.mcp) return <ForcePasswordChange />
  return children
}

// Shown in place of the app when the user's password has expired (rotation policy).
// On success we refresh the access token so the `mcp` claim clears and the app unblocks.
function ForcePasswordChange() {
  const tryRefresh = useAuthStore((s) => s.tryRefresh)
  const logout     = useAuthStore((s) => s.logout)
  const [current, setCurrent] = useState('')
  const [next, setNext]       = useState('')
  const [confirm, setConfirm] = useState('')
  const [error, setError]     = useState('')
  const [busy, setBusy]       = useState(false)

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (next !== confirm) { setError('Passwords do not match'); return }
    setBusy(true)
    try {
      await changePassword(current, next)
      await tryRefresh() // fresh token has mcp cleared → app unblocks
    } catch (err) {
      setError(err.response?.data?.error || 'Could not change password')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-canvas">
      <div className="w-full max-w-sm">
        <div className="bg-surface border border-border rounded-xl p-8">
          <h1 className="text-lg font-semibold text-content-strong mb-1">Your password has expired</h1>
          <p className="text-content-muted text-sm mb-5">Set a new password to continue.</p>
          {error && (
            <div className="mb-4 px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">{error}</div>
          )}
          <form onSubmit={submit} className="space-y-4">
            {[['Current password', current, setCurrent], ['New password', next, setNext], ['Confirm new password', confirm, setConfirm]].map(([label, val, set]) => (
              <div key={label}>
                <label className="block text-sm font-medium text-content mb-1">{label}</label>
                <input type="password" value={val} onChange={e => set(e.target.value)} required
                  className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500 transition-colors" />
              </div>
            ))}
            <button type="submit" disabled={busy}
              className="w-full py-2.5 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white font-semibold rounded-lg transition-colors mt-2">
              {busy ? 'Saving…' : 'Change password'}
            </button>
          </form>
          <div className="mt-4 text-center">
            <button type="button" onClick={() => logout()} className="text-sm text-content-muted hover:text-content underline">
              Sign out
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}

function AppRoutes() {
  const tryRefresh = useAuthStore((s) => s.tryRefresh)
  const ready      = useAuthStore((s) => s.ready)
  const location   = useLocation()

  useEffect(() => {
    const timeout = setTimeout(() => {
      if (!useAuthStore.getState().ready) {
        useAuthStore.setState({ ready: true })
      }
    }, 5000)
    tryRefresh().finally(() => clearTimeout(timeout))
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  if (!ready) {
    return (
      <div className="min-h-screen bg-canvas flex items-center justify-center">
        <span className="text-content-faint text-sm">Loading…</span>
      </div>
    )
  }

  return (
    // Keyed by route so a crash on one page clears itself when the user
    // navigates elsewhere, instead of staying stuck on the fallback.
    <ErrorBoundary key={location.pathname}>
      <Routes>
        <Route path="/setup" element={<SetupPage />} />
        <Route path="/login" element={<LoginPage />} />
        <Route path="/register" element={<RegisterPage />} />
        <Route path="/verify-email" element={<VerifyEmailPage />} />
        <Route path="/forgot-password" element={<ForgotPasswordPage />} />
        <Route path="/reset-password" element={<ResetPasswordPage />} />
        <Route path="/" element={<RequireAuth><DashboardPage /></RequireAuth>} />
        <Route path="/workspaces/:workspace/manage" element={<RequireAuth><ManageWorkspacePage /></RequireAuth>} />
        <Route path="/workspaces/:workspace/projects/new" element={<RequireAuth><NewProjectPage /></RequireAuth>} />
        <Route path="/workspaces/:workspace/projects/:name" element={<RequireAuth><ProjectPage /></RequireAuth>} />
        <Route path="/workspaces/:workspace/projects/:name/edit" element={<RequireAuth><EditProjectPage /></RequireAuth>} />
        {/* Back-compat: bare /new resolves to the create wizard (it reads the selected workspace) */}
        <Route path="/new" element={<RequireAuth><NewProjectPage /></RequireAuth>} />
        <Route path="/settings" element={<RequireAuth><SettingsPage /></RequireAuth>} />
        <Route path="/housekeeping" element={<RequireAuth><HousekeepingPage /></RequireAuth>} />
        <Route path="/tools"        element={<RequireAuth><ToolsPage /></RequireAuth>} />
        <Route path="/proxy"        element={<RequireAuth><ProxyServicePage /></RequireAuth>} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </ErrorBoundary>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <ConfirmProvider>
        <AppRoutes />
      </ConfirmProvider>
    </BrowserRouter>
  )
}
