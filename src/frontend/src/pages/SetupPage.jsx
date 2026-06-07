import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import api from '../lib/api'

export default function SetupPage() {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm]   = useState('')
  const [error, setError]       = useState('')
  const [loading, setLoading]   = useState(false)
  const navigate = useNavigate()

  async function handleSubmit(e) {
    e.preventDefault()
    setError('')
    if (password !== confirm) { setError('Passwords do not match'); return }
    if (password.length < 8)  { setError('Password must be at least 8 characters'); return }
    setLoading(true)
    try {
      await api.post('/setup', { username, password })
      navigate('/login', { state: { message: 'Admin account created — please log in.' } })
    } catch (err) {
      setError(err.response?.data?.error || 'Setup failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-canvas">
      <div className="w-full max-w-md">
        <div className="text-center mb-8">
          <h1 className="text-3xl font-bold text-content-strong">Rigger</h1>
          <p className="text-content-muted mt-1">Rig once. Deploy anywhere</p>
        </div>

        <div className="bg-surface border border-border rounded-xl p-8">
          <h2 className="text-lg font-semibold text-content-strong mb-1">First-run setup</h2>
          <p className="text-sm text-content-muted mb-6">Create your admin account to get started.</p>

          {error && (
            <div className="mb-4 px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">
              {error}
            </div>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-content mb-1">Username</label>
              <input
                type="text" value={username} onChange={e => setUsername(e.target.value)}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500"
                autoFocus required
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-content mb-1">Password</label>
              <input
                type="password" value={password} onChange={e => setPassword(e.target.value)}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500"
                required
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-content mb-1">Confirm password</label>
              <input
                type="password" value={confirm} onChange={e => setConfirm(e.target.value)}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500"
                required
              />
            </div>
            <button
              type="submit" disabled={loading}
              className="w-full py-2.5 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white font-medium rounded-lg transition-colors"
            >
              {loading ? 'Creating account…' : 'Create admin account'}
            </button>
          </form>
        </div>
      </div>
    </div>
  )
}
