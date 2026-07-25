import { useState } from 'react'
import { useNavigate } from 'react-router-dom'
import { forgotPassword } from '../lib/api'
import { Btn, CONTROL } from '../components/ui'

// Self-service "forgot password" request. Always shows the same generic success
// message regardless of whether the email is registered (the server never reveals
// account existence). If system SMTP isn't configured, the request silently no-ops
// and an admin can reset from the Users tab instead.
export default function ForgotPasswordPage() {
  const [email, setEmail]     = useState('')
  const [loading, setLoading] = useState(false)
  const [sent, setSent]       = useState(false)
  const [error, setError]     = useState('')
  const navigate = useNavigate()

  async function handleSubmit(e) {
    e.preventDefault()
    setError('')
    setLoading(true)
    try {
      await forgotPassword(email)
      setSent(true)
    } catch (err) {
      setError(err.response?.data?.error || 'Something went wrong — please try again.')
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
          {sent ? (
            <div className="space-y-5 text-center">
              <div className="px-4 py-3 bg-success-subtle border border-success-border text-success-fg rounded-lg text-sm">
                If an account exists for that email, we’ve sent a password reset link.
                Check your inbox — the link expires in 1 hour.
              </div>
              <Btn variant="primary" size="md" onClick={() => navigate('/login')}
                className="w-full"
              >
                Back to sign in
              </Btn>
            </div>
          ) : (
            <>
              <h1 className="text-lg font-semibold text-content-strong mb-1">Reset your password</h1>
              <p className="text-content-muted text-sm mb-5">
                Enter your email and we’ll send you a link to set a new password.
              </p>
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
                    className={`${CONTROL} w-full`}
                    autoFocus required
                  />
                </div>
                <Btn variant="primary" size="md" type="submit" disabled={loading}
                  className="w-full mt-2"
                >
                  {loading ? 'Sending…' : 'Send reset link'}
                </Btn>
              </form>
              <div className="mt-4 text-center">
                <button
                  type="button"
                  onClick={() => navigate('/login')}
                  className="text-sm text-content-muted hover:text-content underline"
                >
                  Back to sign in
                </button>
              </div>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
