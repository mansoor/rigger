import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import api from '../lib/api'

export default function SetupPage() {
  const [email, setEmail]       = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm]   = useState('')
  const [error, setError]       = useState('')
  const [loading, setLoading]   = useState(false)
  const [done, setDone]         = useState(null) // { verify_link?, email_sent? }
  const navigate = useNavigate()

  async function handleSubmit(e) {
    e.preventDefault()
    setError('')
    if (password !== confirm) { setError('Passwords do not match'); return }
    if (password.length < 8)  { setError('Password must be at least 8 characters'); return }
    setLoading(true)
    try {
      const { data } = await api.post('/setup', { email: email.trim(), username: username.trim(), password })
      setDone(data)
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
          {done ? (
            <div className="space-y-4">
              <h2 className="text-lg font-semibold text-content-strong">Admin account created</h2>
              {done.email_sent ? (
                <p className="text-sm text-content-muted">We sent a verification link to <strong className="text-content">{email}</strong>. Open it to verify your email, then sign in.</p>
              ) : (
                <>
                  <p className="text-sm text-content-muted">No system email is configured yet, so open this link to verify your address (you can also do this later from your profile):</p>
                  <div className="bg-surface-raised border border-border-strong rounded-lg p-3 text-xs font-mono break-all text-content">
                    <a className="text-brand-400 hover:underline" href={done.verify_link}>{done.verify_link}</a>
                  </div>
                </>
              )}
              <button onClick={() => navigate('/login', { state: { message: 'Account created — please sign in.' } })}
                className="w-full py-2.5 bg-brand-600 hover:bg-brand-700 text-white font-medium rounded-lg transition-colors">
                Go to sign in
              </button>
            </div>
          ) : (
            <>
              <h2 className="text-lg font-semibold text-content-strong mb-1">First-run setup</h2>
              <p className="text-sm text-content-muted mb-6">Create your admin account. Your email is your sign-in and where alerts are sent.</p>

              {error && (
                <div className="mb-4 px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">{error}</div>
              )}

              <form onSubmit={handleSubmit} className="space-y-4">
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Email</label>
                  <input type="email" value={email} onChange={e => setEmail(e.target.value)}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500"
                    autoFocus required />
                </div>
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Display name <span className="text-content-subtle font-normal">(optional)</span></label>
                  <input type="text" value={username} onChange={e => setUsername(e.target.value)}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500"
                    placeholder="defaults to the part before @" />
                </div>
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Password</label>
                  <input type="password" value={password} onChange={e => setPassword(e.target.value)}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500"
                    required />
                </div>
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Confirm password</label>
                  <input type="password" value={confirm} onChange={e => setConfirm(e.target.value)}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500"
                    required />
                </div>
                <button type="submit" disabled={loading}
                  className="w-full py-2.5 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white font-medium rounded-lg transition-colors">
                  {loading ? 'Creating account…' : 'Create admin account'}
                </button>
              </form>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
