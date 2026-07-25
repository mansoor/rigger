import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchAccessTargets, createAccessRequest, fetchMyAccessRequests } from '../lib/api'
import { wsRoleOptions } from '../lib/roles'
import RoleHelp from './RoleHelp'
import { Btn, CloseBtn } from './ui'

const STATUS_BADGE = {
  pending:  'bg-amber-100/70 text-amber-700 border-amber-200 dark:bg-amber-950/60 dark:text-amber-300 dark:border-amber-800/40',
  approved: 'bg-emerald-100/70 text-emerald-700 border-emerald-200 dark:bg-emerald-950/60 dark:text-emerald-300 dark:border-emerald-800/40',
  rejected: 'bg-red-100/70 text-red-700 border-red-200 dark:bg-red-950/60 dark:text-red-300 dark:border-red-800/40',
}

// RequestAccessModal lets any signed-in user request access to a workspace (and
// optionally a single project) at a chosen role, and shows their past requests.
export default function RequestAccessModal({ onClose }) {
  const qc = useQueryClient()
  const { data: targets = [] } = useQuery({ queryKey: ['access-targets'], queryFn: fetchAccessTargets })
  const { data: mine = [] } = useQuery({ queryKey: ['my-access-requests'], queryFn: fetchMyAccessRequests })

  const [wsKey, setWsKey]     = useState('')
  const [projKey, setProjKey] = useState('')
  const [role, setRole]       = useState('viewer')
  const [message, setMessage] = useState('')
  const [error, setError]     = useState('')

  const ws = targets.find(t => t.ws_key === wsKey)
  const projects = ws?.projects || []

  const mut = useMutation({
    mutationFn: () => createAccessRequest({ ws_key: wsKey, proj_key: projKey, role, message }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['my-access-requests'] })
      setMessage(''); setError('')
    },
    onError: (e) => setError(e?.response?.data?.error || 'Failed to submit request'),
  })

  function submit(e) {
    e.preventDefault()
    setError('')
    if (!wsKey) { setError('Pick a workspace'); return }
    mut.mutate()
  }

  const inp = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
  const lbl = 'block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1'

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-md mx-4 p-6 space-y-5" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">Request access</h3>
          <CloseBtn onClick={onClose} />
        </div>

        <form onSubmit={submit} className="space-y-4">
          <div>
            <label className={lbl}>Workspace</label>
            <select value={wsKey} onChange={e => { setWsKey(e.target.value); setProjKey('') }} className={inp}>
              <option value="">— select a workspace —</option>
              {targets.map(t => <option key={t.ws_key} value={t.ws_key}>{t.ws_name || t.ws_key}</option>)}
            </select>
          </div>
          {wsKey && (
            <div>
              <label className={lbl}>Project <span className="font-normal normal-case text-content-faint">(optional)</span></label>
              <select value={projKey} onChange={e => setProjKey(e.target.value)} className={inp}>
                <option value="">Whole workspace</option>
                {projects.map(p => <option key={p.key} value={p.key}>{p.name || p.key}</option>)}
              </select>
            </div>
          )}
          <div className="space-y-2">
            <label className={lbl}>Role requested</label>
            <select value={role} onChange={e => setRole(e.target.value)} className={inp}>
              {wsRoleOptions.map(r => <option key={r.value} value={r.value}>{r.label}</option>)}
            </select>
            <RoleHelp scope="workspace" />
          </div>
          <div>
            <label className={lbl}>Message <span className="font-normal normal-case text-content-faint">(optional)</span></label>
            <textarea value={message} onChange={e => setMessage(e.target.value)} rows={2} placeholder="Why you need access" className={inp} />
          </div>
          {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
          <div className="flex gap-2 justify-end">
            <Btn variant="secondary" size="md" onClick={onClose} >Close</Btn>
            <Btn variant="primary" size="md" type="submit" disabled={mut.isPending} >
              {mut.isPending ? 'Submitting…' : 'Submit request'}
            </Btn>
          </div>
        </form>

        {mine.length > 0 && (
          <div className="border-t border-border pt-4">
            <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider mb-2">Your requests</p>
            <div className="space-y-1.5 max-h-48 overflow-y-auto">
              {mine.map(rq => (
                <div key={rq.id} className="flex items-center justify-between gap-2 text-xs bg-surface-raised border border-border rounded-lg px-3 py-2">
                  <span className="text-content truncate">
                    {rq.ws_key}{rq.proj_key ? ` / ${rq.proj_key}` : ''} · <span className="text-content-faint">{rq.role}</span>
                  </span>
                  <span className={`shrink-0 text-[10px] px-1.5 py-0.5 rounded border uppercase tracking-wider ${STATUS_BADGE[rq.status] || ''}`}>{rq.status}</span>
                </div>
              ))}
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
