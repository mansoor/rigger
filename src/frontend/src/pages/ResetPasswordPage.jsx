import { useState, useEffect } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { resetPassword, fetchPasswordPolicy } from '../lib/api'
import { Btn } from '../components/ui'

// Builds the human-readable requirement list from the active password policy.
function policyHints(p) {
  if (!p) return []
  const hints = [`At least ${p.min_length} characters`]
  if (p.require_upper)  hints.push('An uppercase letter')
  if (p.require_lower)  hints.push('A lowercase letter')
  if (p.require_number) hints.push('A number')
  if (p.require_symbol) hints.push('A symbol')
  return hints
}

// Sets a new password using the token from the emailed reset link.
export default function ResetPasswordPage() {
  const [params]   = useSearchParams()
  const token      = params.get('token') || ''
  const navigate   = useNavigate()

  const [password, setPassword]   = useState('')
  const [confirm, setConfirm]     = useState('')
  const [policy, setPolicy]       = useState(null)
  const [loading, setLoading]     = useState(false)
  const [error, setError]         = useState('')

  useEffect(() => {
    fetchPasswordPolicy().then(setPolicy).catch(() => {})
  }, [])

  async function handleSubmit(e) {
    e.preventDefault()
    setError('')
    if (password !== confirm) {
      setError('Passwords do not match.')
      return
    }
    setLoading(true)
    try {
      await resetPassword(token, password)
      navigate('/login', { replace: true, state: { message: 'Your password has been reset. Please sign in.' } })
    } catch (err) {
      setError(err.response?.data?.error || 'Could not reset your password — the link may have expired.')
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
            alt="Rigger"
            className="w-36 h-36 mx-auto drop-shadow-[0_0_24px_rgba(99,102,241,0.4)]"
          />
        </div>

        <div className="bg-surface border border-border rounded-xl p-8">
          <h1 className="text-lg font-semibold text-content-strong mb-1">Set a new password</h1>

          {!token ? (
            <div className="mt-4 px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">
              This reset link is missing its token. Request a new link from the sign-in page.
            </div>
          ) : (
            <>
              <p className="text-content-muted text-sm mb-5">Choose a strong password you don’t use elsewhere.</p>
              {error && (
                <div className="mb-4 px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">
                  {error}
                </div>
              )}
              <form onSubmit={handleSubmit} className="space-y-4">
                <div>
                  <label className="block text-sm font-medium text-content mb-1">New password</label>
                  <input
                    type="password" value={password} onChange={e => setPassword(e.target.value)}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors"
                    autoFocus required
                  />
                </div>
                <div>
                  <label className="block text-sm font-medium text-content mb-1">Confirm password</label>
                  <input
                    type="password" value={confirm} onChange={e => setConfirm(e.target.value)}
                    className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors"
                    required
                  />
                </div>
                {policyHints(policy).length > 0 && (
                  <ul className="text-xs text-content-muted list-disc list-inside space-y-0.5">
                    {policyHints(policy).map(h => <li key={h}>{h}</li>)}
                  </ul>
                )}
                <Btn variant="primary" size="md" type="submit" disabled={loading}
                  className="w-full mt-2"
                >
                  {loading ? 'Saving…' : 'Set new password'}
                </Btn>
              </form>
            </>
          )}

          <div className="mt-4 text-center">
            <button
              type="button"
              onClick={() => navigate('/login')}
              className="text-sm text-content-muted hover:text-content underline"
            >
              Back to sign in
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
