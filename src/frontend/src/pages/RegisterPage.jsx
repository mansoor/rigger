import { useState, useEffect } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { fetchRegisterInfo, completeRegistration } from '../lib/api'
import { useAuthStore } from '../store/auth'
import { Btn } from '../components/ui'

// Invite registration (Phase 5.1b). The invitee arrives via an emailed/shared
// link (/register?token=...), sets a password (+optional phone), and is logged in.
export default function RegisterPage() {
  const [params]   = useSearchParams()
  const token      = params.get('token') || ''
  const navigate   = useNavigate()
  const setSession = useAuthStore(s => s.setSession)

  const [info, setInfo]       = useState(null)   // { email, username }
  const [loadErr, setLoadErr] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [confirm, setConfirm]   = useState('')
  const [phone, setPhone]       = useState('')
  const [error, setError]       = useState('')
  const [loading, setLoading]   = useState(false)

  useEffect(() => {
    if (!token) { setLoadErr('Missing invite token.'); return }
    fetchRegisterInfo(token)
      .then(d => { setInfo(d); setUsername(d.username || '') })
      .catch(e => setLoadErr(e.response?.data?.error || 'This invite link is invalid or has expired.'))
  }, [token])

  async function handleSubmit(e) {
    e.preventDefault()
    setError('')
    if (password !== confirm) { setError('Passwords do not match'); return }
    if (password.length < 8)  { setError('Password must be at least 8 characters'); return }
    setLoading(true)
    try {
      const { token: access } = await completeRegistration({ token, password, phone: phone.trim(), username: username.trim() })
      setSession(access)
      navigate('/', { replace: true })
    } catch (err) {
      setError(err.response?.data?.error || 'Could not complete registration')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-canvas">
      <div className="w-full max-w-md">
        <div className="text-center mb-8">
          <h1 className="text-3xl font-bold text-content-strong">Rigger</h1>
          <p className="text-content-muted mt-1">Complete your account</p>
        </div>
        <div className="bg-surface border border-border rounded-xl p-8">
          {loadErr ? (
            <div className="space-y-4">
              <div className="px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">{loadErr}</div>
              <Btn variant="primary" size="md" onClick={() => navigate('/login')} className="w-full">Go to sign in</Btn>
            </div>
          ) : !info ? (
            <p className="text-sm text-content-subtle text-center">Loading…</p>
          ) : (
            <>
              <h2 className="text-lg font-semibold text-content-strong mb-1">Welcome</h2>
              <p className="text-sm text-content-muted mb-6">Setting up <strong className="text-content">{info.email}</strong>. Choose a password to finish.</p>
              {error && <div className="mb-4 px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">{error}</div>}
              <form onSubmit={handleSubmit} className="space-y-4">
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Display name <span className="text-content-subtle font-normal">(optional)</span></label>
                  <input type="text" value={username} onChange={e => setUsername(e.target.value)}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500" />
                </div>
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Password</label>
                  <input type="password" value={password} onChange={e => setPassword(e.target.value)} autoFocus
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500" required />
                </div>
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Confirm password</label>
                  <input type="password" value={confirm} onChange={e => setConfirm(e.target.value)}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500" required />
                </div>
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Phone <span className="text-content-subtle font-normal">(optional, for SMS alerts)</span></label>
                  <input type="tel" value={phone} onChange={e => setPhone(e.target.value)} placeholder="+1 555 0100"
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong focus:outline-none focus:border-brand-500" />
                </div>
                <Btn variant="primary" size="md" type="submit" disabled={loading} className="w-full">
                  {loading ? 'Finishing…' : 'Complete registration'}
                </Btn>
              </form>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
