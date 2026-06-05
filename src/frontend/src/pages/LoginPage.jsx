import { useState, useEffect } from 'react'
import { useNavigate, useLocation } from 'react-router-dom'
import { useAuthStore } from '../store/auth'
import api from '../lib/api'

export default function LoginPage() {
  const [username, setUsername] = useState('')
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
      await login(username, password)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err.response?.data?.error || 'Login failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-950">
      <div className="w-full max-w-sm">
        <div className="text-center mb-8">
          <img
            src="/rigger-logo.png"
            alt="Rigger — Rig once. Deploy anywhere"
            className="w-36 h-36 mx-auto drop-shadow-[0_0_24px_rgba(99,102,241,0.4)]"
          />
          <p className="text-gray-400 text-sm mt-5">Rig once. Deploy anywhere</p>
        </div>

        <div className="bg-gray-900 border border-gray-800 rounded-xl p-8">
          {notice && (
            <div className="mb-4 px-4 py-3 bg-green-950 border border-green-800 text-green-300 rounded-lg text-sm">
              {notice}
            </div>
          )}
          {error && (
            <div className="mb-4 px-4 py-3 bg-red-950 border border-red-800 text-red-300 rounded-lg text-sm">
              {error}
            </div>
          )}

          <form onSubmit={handleSubmit} className="space-y-4">
            <div>
              <label className="block text-sm font-medium text-gray-300 mb-1">Username</label>
              <input
                type="text" value={username} onChange={e => setUsername(e.target.value)}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:outline-none focus:border-brand-500 transition-colors"
                autoFocus required
              />
            </div>
            <div>
              <label className="block text-sm font-medium text-gray-300 mb-1">Password</label>
              <input
                type="password" value={password} onChange={e => setPassword(e.target.value)}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:outline-none focus:border-brand-500 transition-colors"
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
