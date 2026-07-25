import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { fetchDeployHistory, rollbackEnv } from '../lib/api'
import { Hint, Btn } from './ui'

// Phase 9e — Rollback dialog. For custom stacks it lists the env's deploy history
// and pins a chosen prior image set on confirm. For image stacks (no per-env image
// override) it explains that rollback is done via Backup restore instead.
export default function RollbackModal({ workspace, name, envName, isImage, onClose, onDone }) {
  const [picked, setPicked] = useState(null)
  const [err, setErr] = useState('')

  const { data: history = [], isLoading } = useQuery({
    queryKey: ['deploy-history', workspace, name, envName],
    queryFn: () => fetchDeployHistory(workspace, name, envName),
    enabled: !isImage,
  })

  const rbMut = useMutation({
    mutationFn: (toId) => rollbackEnv(workspace, name, envName, toId),
    onSuccess: () => { onDone?.(); onClose() },
    onError: (e) => setErr(e?.response?.data?.error || 'Rollback failed'),
  })

  // The newest entry is the current deploy; rollback targets a prior one.
  const targets = history.slice(1)

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border-strong rounded-xl w-full max-w-2xl max-h-[80vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-3 border-b border-border">
          <h3 className="font-semibold text-content-strong">↩ Rollback — {envName}</h3>
          <button onClick={onClose} className="text-content-faint hover:text-content text-lg leading-none">✕</button>
        </div>

        {isImage ? (
          <div className="p-5 space-y-3 text-sm text-content-muted">
            <p>This is an <strong className="text-content">image-type</strong> environment. Its image tags are fixed in the project config (not per-environment), so there's no previous image to redeploy.</p>
            <p>To revert an image-app environment, <strong className="text-content">restore a data/config snapshot</strong> from this environment's Backups instead. Note that a restore rolls back <em>data</em>, which is a different operation from a code rollback.</p>
            <div className="flex justify-end pt-2">
              <Btn variant="secondary" size="sm" onClick={onClose} >Close</Btn>
            </div>
          </div>
        ) : (
          <>
            <div className="flex-1 overflow-y-auto p-5 space-y-3">
              <Hint>Pick a previous deploy to redeploy. The old image must still exist locally or in the registry. <strong className="text-warning-fg">Database changes from the newer version are NOT reverted</strong> — run your app's down-migrations or restore a data backup separately.</Hint>

              {isLoading ? (
                <p className="text-sm text-content-subtle">Loading…</p>
              ) : history.length === 0 ? (
                <p className="text-sm text-content-subtle">No deploy history yet — deploy at least twice to enable rollback.</p>
              ) : (
                <div className="space-y-2">
                  {history.length > 0 && (
                    <div className="text-[11px] uppercase tracking-wide text-content-faint">Current</div>
                  )}
                  <DeployRow entry={history[0]} current />
                  {targets.length > 0 && <div className="text-[11px] uppercase tracking-wide text-content-faint pt-2">Roll back to</div>}
                  {targets.map(e => (
                    <DeployRow key={e.id} entry={e} selected={picked === e.id} onPick={() => { setPicked(e.id); setErr('') }} />
                  ))}
                  {targets.length === 0 && history.length > 0 && (
                    <p className="text-sm text-content-subtle">Only one deploy recorded — nothing earlier to roll back to.</p>
                  )}
                </div>
              )}

              {err && <div className="px-3 py-2 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm whitespace-pre-wrap">{err}</div>}
            </div>
            <div className="flex items-center justify-end gap-3 px-5 py-3 border-t border-border">
              <Btn variant="secondary" size="sm" onClick={onClose} >Cancel</Btn>
              <Btn variant="primary" size="sm" onClick={() => picked && rbMut.mutate(picked)} disabled={!picked || rbMut.isPending}
                >
                {rbMut.isPending ? 'Rolling back…' : 'Roll back & redeploy'}
              </Btn>
            </div>
          </>
        )}
      </div>
    </div>
  )
}

function DeployRow({ entry, current, selected, onPick }) {
  const images = Object.entries(entry.images || {})
  return (
    <button
      type="button"
      disabled={current}
      onClick={onPick}
      className={`w-full text-left rounded-lg border px-3 py-2 transition-colors ${
        current ? 'border-border bg-surface-raised/40 cursor-default'
          : selected ? 'border-brand-500 bg-brand-500/10'
            : 'border-border-strong hover:bg-surface-raised'
      }`}
    >
      <div className="flex items-center gap-2 text-sm">
        <span className="font-mono text-content-strong">{entry.version || '—'}</span>
        {current && <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-success-subtle text-success-fg">current</span>}
        <span className="ml-auto text-xs text-content-faint">{new Date(entry.created_at).toLocaleString()} · {entry.username || 'system'}</span>
      </div>
      {images.length > 0 && (
        <div className="mt-1 space-y-0.5">
          {images.map(([svc, ref]) => (
            <div key={svc} className="text-[11px] font-mono text-content-muted truncate"><span className="text-content-faint">{svc}:</span> {ref}</div>
          ))}
        </div>
      )}
    </button>
  )
}
