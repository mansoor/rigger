import { useState, useEffect, useRef } from 'react'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { verifyEmail } from '../lib/api'
import { useAuthStore } from '../store/auth'
import { Btn } from '../components/ui'

// Email verification landing (Phase 5.1b): /verify-email?token=...
export default function VerifyEmailPage() {
  const [params]   = useSearchParams()
  const token      = params.get('token') || ''
  const navigate   = useNavigate()
  const tryRefresh = useAuthStore(s => s.tryRefresh)
  const [state, setState] = useState('working') // working | ok | error
  const ran = useRef(false)

  useEffect(() => {
    if (ran.current) return
    ran.current = true
    if (!token) { setState('error'); return }
    verifyEmail(token)
      .then(async () => { await tryRefresh().catch(() => {}); setState('ok') })
      .catch(() => setState('error'))
  }, [token])

  return (
    <div className="min-h-screen flex items-center justify-center bg-canvas">
      <div className="w-full max-w-md">
        <div className="text-center mb-8">
          <h1 className="text-3xl font-bold text-content-strong">Rigger</h1>
        </div>
        <div className="bg-surface border border-border rounded-xl p-8 text-center space-y-4">
          {state === 'working' && <p className="text-sm text-content-subtle">Verifying your email…</p>}
          {state === 'ok' && (
            <>
              <div className="text-4xl">✓</div>
              <h2 className="text-lg font-semibold text-content-strong">Email verified</h2>
              <p className="text-sm text-content-muted">Your email address is confirmed.</p>
              <Btn variant="primary" size="md" onClick={() => navigate('/')} className="w-full">Continue</Btn>
            </>
          )}
          {state === 'error' && (
            <>
              <div className="text-4xl opacity-60">⚠</div>
              <h2 className="text-lg font-semibold text-content-strong">Verification failed</h2>
              <p className="text-sm text-content-muted">This link is invalid or has expired. You can request a new one from your profile.</p>
              <Btn variant="primary" size="md" onClick={() => navigate('/')} className="w-full">Go to Rigger</Btn>
            </>
          )}
        </div>
      </div>
    </div>
  )
}
