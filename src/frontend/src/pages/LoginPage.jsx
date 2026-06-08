import { useState, useEffect } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { useAuthStore } from '../store/auth'
import api from '../lib/api'

export default function LoginPage() {
  const [email, setEmail]       = useState('')
  const [password, setPassword] = useState('')
  const [error, setError]       = useState('')
  const [loading, setLoading]   = useState(false)
  const [notice, setNotice]     = useState('')
  const navigate  = useNavigate()
  const location  = useLocation()
  const login     = useAuthStore((s) => s.login)

  useEffect(() => {
    // Check if first-run setup is needed
    api.get('/setup/status').then(({ data }) => {
      if (data.setup_required) navigate('/setup', { replace: true })
    })
    if (location.state?.message) setNotice(location.state.message)
  }, [])

  async function handleSubmit(e) {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      await login(email, password)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err.response?.data?.error || 'Login failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-canvas">
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <img
            src="/rigger-logo.png"
            alt="With Rigger — More Dev, Less Ops."
            className="w-36 h-36 mx-auto drop-shadow-[0_0_24px_rgba(99,102,241,0.4)]"
          />
          <p className="text-content-muted text-sm mt-5">With Rigger — More Dev, Less Ops.</p>
        </div>

        <div className="bg-surface border border-border rounded-xl p-8">
          {notice && (
            <div className="mb-4 px-4 py-3 bg-success-subtle border border-success-border text-success-fg rounded-lg text-sm">
              {notice}
            </div>
          )}
          {error && (
            <div className="mb-4 px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">
              {error}
            </div>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-content mb-1">Email</label>
              <input
                type="email" value={email} onChange={e => setEmail(e.target.value)}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors"
                autoFocus required
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-content mb-1">Password</label>
              <input
                type="password" value={password} onChange={e => setPassword(e.target.value)}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors"
                required
              />
            </div>
            <button
              type="submit" disabled={loading}
              className="w-full py-2.5 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white font-semibold rounded-lg transition-colors mt-2"
            >
              {loading ? 'Signing in…' : 'Sign in'}
            </button>
          </form>
        </div>
      </div>
    </div>
  )
}
