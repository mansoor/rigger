import { useEffect } from 'react'
import { BrowserRouter, Routes, Route, Navigate } from 'react-router-dom'
import { useAuthStore } from './store/auth'
import LoginPage from './pages/LoginPage'
import SetupPage from './pages/SetupPage'
import DashboardPage from './pages/DashboardPage'
import WorkspacePage from './pages/WorkspacePage'
import NewWorkspacePage from './pages/NewWorkspacePage'
import EditWorkspacePage from './pages/EditWorkspacePage'
import SettingsPage from './pages/SettingsPage'
import HousekeepingPage from './pages/HousekeepingPage'
import ToolsPage from './pages/ToolsPage'

function RequireAuth({ children }) {
  const token = useAuthStore((s) => s.token)
  const ready  = useAuthStore((s) => s.ready)
  if (!ready) return null
  return token ? children : <Navigate to="/login" replace />
}

function AppRoutes() {
  const tryRefresh = useAuthStore((s) => s.tryRefresh)
  const ready      = useAuthStore((s) => s.ready)

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
      <div className="min-h-screen bg-gray-950 flex items-center justify-center">
        <span className="text-gray-700 text-sm">Loading…</span>
      </div>
    )
  }

  return (
    <Routes>
      <Route path="/setup" element={<SetupPage />} />
      <Route path="/login" element={<LoginPage />} />
      <Route path="/" element={<RequireAuth><DashboardPage /></RequireAuth>} />
      <Route path="/workspaces/:name" element={<RequireAuth><WorkspacePage /></RequireAuth>} />
      <Route path="/new" element={<RequireAuth><NewWorkspacePage /></RequireAuth>} />
      <Route path="/workspaces/:name/edit" element={<RequireAuth><EditWorkspacePage /></RequireAuth>} />
      <Route path="/settings" element={<RequireAuth><SettingsPage /></RequireAuth>} />
      <Route path="/housekeeping" element={<RequireAuth><HousekeepingPage /></RequireAuth>} />
      <Route path="/tools"        element={<RequireAuth><ToolsPage /></RequireAuth>} />
      <Route path="*" element={<Navigate to="/" replace />} />
    </Routes>
  )
}

export default function App() {
  return (
    <BrowserRouter>
      <AppRoutes />
    </BrowserRouter>
  )
}
