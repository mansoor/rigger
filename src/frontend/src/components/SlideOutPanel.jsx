import { useState, useEffect, useRef } from 'react'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { fetchAllActivity, fetchBackups, fetchWorkspaces, openActionSocket, deleteBackup,
  syncEnvBackup, verifyRestore, fetchAlertEvents, dismissAlert, dismissAllAlerts } from '../lib/api'
import { Btn, IconBtn, CONTROL } from './ui'

// ── Shared helpers ─────────────────────────────────────────────────────────────

function formatBytes(bytes) {
  if (!bytes || bytes === 0) return '0 B'
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function timeAgo(ts) {
  if (!ts) return ''
  let ms = NaN
  for (const attempt of [ts.replace(' ', 'T') + (ts.includes('Z') ? '' : 'Z'), ts, ts + 'Z']) {
    const t = new Date(attempt).getTime()
    if (!isNaN(t)) { ms = t; break }
  }
  if (isNaN(ms)) return ts.slice(0, 16)
  const diff = Math.floor((Date.now() - ms) / 1000)
  if (diff < 60)    return `${diff}s ago`
  if (diff < 3600)  return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

function formatDate(dateStr) {
  const [date, time] = (dateStr || '').split('_')
  if (!date) return dateStr
  return `${date} ${(time || '').replace(/-/g, ':')}`
}

const CMD_COLOR = {
  start:   'bg-green-500/20 text-success-fg',
  deploy:  'bg-green-500/20 text-success-fg',
  stop:    'bg-red-500/20   text-danger-fg',
  down:    'bg-red-500/20   text-danger-fg',
  restart: 'bg-amber-400/20 text-warning-fg',
  update:  'bg-amber-400/20 text-warning-fg',
  backup:  'bg-surface-overlay/20  text-content-muted',
  build:   'bg-brand-500/20 text-accent-text',
  promote: 'bg-purple-500/20 text-purple-400',
  delete:  'bg-red-700/20   text-danger',
}

// ── Filter bar — workspace search + type selector ─────────────────────────────

function FilterBar({ workspaceFilter, setWorkspaceFilter, typeFilter, setTypeFilter }) {
  return (
    <div className="flex items-center gap-2 px-5 py-3 border-b border-border bg-surface/60 shrink-0">
      <input
        type="text"
        placeholder="Filter by workspace…"
        value={workspaceFilter}
        onChange={e => setWorkspaceFilter(e.target.value)}
        className={`${CONTROL} flex-1`}
      />
      <select
        value={typeFilter}
        onChange={e => setTypeFilter(e.target.value)}
        className={`${CONTROL} w-full`}
      >
        <option value="all">All types</option>
        <option value="image">Image stacks / Compose</option>
        <option value="custom">Custom apps</option>
      </select>
      {(workspaceFilter || typeFilter !== 'all') && (
        <button
          onClick={() => { setWorkspaceFilter(''); setTypeFilter('all') }}
          className="text-xs text-content-subtle hover:text-content transition-colors px-2"
        >
          Clear
        </button>
      )}
    </div>
  )
}

// ── Activity content ──────────────────────────────────────────────────────────

function ActivityContent({ workspaceFilter, typeFilter, wsTypes }) {
  const { data, isLoading } = useQuery({
    queryKey: ['allActivity'],
    queryFn: fetchAllActivity,
    refetchInterval: 15_000,
  })

  const items = (data || []).filter(item => {
    if (workspaceFilter && !item.workspace?.toLowerCase().includes(workspaceFilter.toLowerCase())) return false
    if (typeFilter !== 'all') {
      const wsType = wsTypes[item.workspace]
      if (wsType && wsType !== typeFilter) return false
    }
    return true
  })

  if (isLoading) return <p className="text-sm text-content-subtle p-5">Loading…</p>
  if (items.length === 0) return <p className="text-sm text-content-subtle p-5">No activity matches the current filter.</p>

  return (
    <div className="divide-y divide-border">
      {items.map((item, i) => {
        const cls = CMD_COLOR[item.command] || 'bg-surface-overlay/20 text-content-muted'
        return (
          <div key={i} className="flex items-center gap-4 px-5 py-3 hover:bg-surface-raised/40 transition-colors">
            <span className={`text-xs font-medium px-2 py-0.5 rounded-full shrink-0 ${cls}`}>
              {item.command}
            </span>
            <div className="flex-1 min-w-0">
              <p className="text-sm text-content-strong font-medium truncate">{item.workspace}</p>
              <p className="text-xs text-content-subtle">
                {item.env && <span className="mr-2">env: <span className="text-content-muted">{item.env}</span></span>}
                by <span className="text-content-muted">{item.username}</span>
              </p>
            </div>
            <span className="text-xs text-content-faint shrink-0" title={item.created_at}>{timeAgo(item.created_at)}</span>
          </div>
        )
      })}
    </div>
  )
}

// ── Restore output modal ──────────────────────────────────────────────────────

function RestoreModal({ snap, onClose }) {
  const qc = useQueryClient()
  const [lines, setLines]   = useState([])
  const [done, setDone]     = useState(false)
  const [error, setError]   = useState(false)
  const scrollRef           = useRef(null)

  useEffect(() => {
    const ws = openActionSocket(snap.workspace, snap.project, 'restore', snap.env, [snap.date])

    ws.addEventListener('message', e => {
      setLines(prev => [...prev, e.data])
      if (e.data.includes('Restore Complete') || e.data.includes('restored successfully')) {
        setDone(true)
        qc.invalidateQueries({ queryKey: ['projects'] })
        qc.invalidateQueries({ queryKey: ['envstatus', snap.workspace, snap.project] })
      }
    })
    ws.addEventListener('error', () => {
      setLines(prev => [...prev, '\x1b[31m[connection error]\x1b[0m'])
      setError(true)
      setDone(true)
    })
    ws.addEventListener('close', () => setDone(true))

    return () => ws.close()
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Auto-scroll output
  useEffect(() => {
    if (scrollRef.current) scrollRef.current.scrollTop = scrollRef.current.scrollHeight
  }, [lines])

  // Strip ANSI codes for plain-text display
  function stripAnsi(str) {
    return str.replace(/\x1b\[[0-9;]*m/g, '')
  }

  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/70 backdrop-blur-sm">
      <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-2xl mx-4 flex flex-col shadow-2xl" style={{ maxHeight: '80vh' }}>
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-border shrink-0">
          <div>
            <h3 className="font-semibold text-content-strong text-sm">
              Restoring {snap.workspace} / {snap.project} / {snap.env}
            </h3>
            <p className="text-xs text-content-subtle mt-0.5 font-mono">{formatDate(snap.date)}</p>
          </div>
          <div className="flex items-center gap-3">
            {!done && (
              <span className="flex items-center gap-1.5 text-xs text-warning-fg">
                <span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />
                Running…
              </span>
            )}
            {done && !error && (
              <span className="flex items-center gap-1.5 text-xs text-success-fg">
                <span className="w-1.5 h-1.5 rounded-full bg-green-400" />
                Complete
              </span>
            )}
            {done && error && (
              <span className="text-xs text-danger-fg">Error</span>
            )}
          </div>
        </div>

        {/* Output */}
        <div
          ref={scrollRef}
          className="flex-1 overflow-y-auto min-h-0 p-4 font-mono text-xs bg-canvas rounded-b-none"
          style={{ minHeight: 280 }}
        >
          {lines.length === 0 && (
            <p className="text-content-faint">Connecting…</p>
          )}
          {lines.map((line, i) => (
            <div key={i} className={`leading-5 whitespace-pre-wrap ${
              line.includes('ERROR') || line.includes('error') ? 'text-danger-fg' :
              line.includes('✓') || line.includes('Complete') || line.includes('restored') ? 'text-success-fg' :
              line.includes('⚑') || line.includes('Step') ? 'text-accent-text font-semibold' :
              line.includes('⚠') || line.includes('WARN') ? 'text-warning-fg' :
              'text-content'
            }`}>
              {stripAnsi(line)}
            </div>
          ))}
        </div>

        {/* Footer */}
        <div className="px-5 py-3 border-t border-border shrink-0 flex justify-end">
          <Btn variant="secondary" size="sm" onClick={onClose}
            disabled={!done}
            
          >
            {done ? 'Close' : 'Running…'}
          </Btn>
        </div>
      </div>
    </div>
  )
}

// ── Restore confirm modal ─────────────────────────────────────────────────────

function RestoreConfirmModal({ snap, onConfirm, onClose }) {
  return (
    <div className="fixed inset-0 z-[60] flex items-center justify-center bg-black/70 backdrop-blur-sm" onClick={onClose}>
      <div
        className="bg-surface border border-border-strong rounded-2xl w-full max-w-md mx-4 p-6 shadow-2xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-start gap-3 mb-4">
          <span className="text-2xl mt-0.5">⚠️</span>
          <div>
            <h3 className="font-semibold text-content-strong">Restore from backup?</h3>
            <p className="text-sm text-content-muted mt-1">
              This will <span className="text-content-strong font-medium">stop</span> the{' '}
              <span className="text-content-strong font-medium">{snap.workspace}/{snap.project}/{snap.env}</span> stack,
              overwrite all database and volume data with the snapshot from{' '}
              <span className="font-mono text-warning-fg text-xs">{formatDate(snap.date)}</span>,
              then restart the stack.
            </p>
            <p className="text-sm text-danger-fg mt-2 font-medium">Current data will be lost.</p>
          </div>
        </div>

        <div className="bg-surface-raised/60 border border-border-strong/60 rounded-lg px-4 py-2.5 mb-4">
          <p className="text-xs text-content-muted">
            Files in this snapshot: {(snap.files || []).map(f => f.name).join(', ') || 'none'}
          </p>
        </div>

        <div className="flex gap-2 justify-end">
          <Btn variant="secondary" size="md" onClick={onClose}
            
          >
            Cancel
          </Btn>
          <Btn variant="danger" size="md" onClick={onConfirm}
            
          >
            Restore
          </Btn>
        </div>
      </div>
    </div>
  )
}

// ── Backup content ────────────────────────────────────────────────────────────

function BackupContent({ workspaceFilter, typeFilter, wsTypes }) {
  const qc = useQueryClient()
  // Fetch ALL backups (filtered client-side by workspaceFilter below). Must wrap in an
  // arrow — a bare `queryFn: fetchBackups` passes React Query's context object as the
  // workspace arg, producing ?workspace=[object Object] → backend matches nothing → empty.
  const { data, isLoading } = useQuery({ queryKey: ['backups'], queryFn: () => fetchBackups(), refetchInterval: 60_000 })
  const [expanded, setExpanded]           = useState(null)
  const [confirmSnap, setConfirmSnap]     = useState(null)
  const [restoringSnap, setRestoringSnap] = useState(null)
  const [deleteConfirm, setDeleteConfirm] = useState(null) // snap to delete

  const deleteMut = useMutation({
    mutationFn: (snap) => deleteBackup(snap.workspace, snap.project, snap.env, snap.date),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['backups'] })
      setDeleteConfirm(null)
    },
  })

  const [syncErr, setSyncErr] = useState('') // "key: message" of the last failed sync
  const syncMut = useMutation({
    mutationFn: (snap) => syncEnvBackup(snap.workspace, snap.project, snap.env, { date: snap.date }),
    onSuccess: () => { setSyncErr(''); qc.invalidateQueries({ queryKey: ['backups'] }) },
    onError: (e, snap) => setSyncErr(`${snap.workspace}/${snap.project}-${snap.env}-${snap.date}: ${e.response?.data?.error || 'Sync failed'}`),
  })
  const syncingKey = (s) => `${s.workspace}/${s.project}-${s.env}-${s.date}`

  // 11c: restore dry-run verify — result keyed by snapshot, shown inline.
  const [verify, setVerify] = useState(null) // { key, data?, error? }
  const verifyMut = useMutation({
    mutationFn: (snap) => verifyRestore(snap.workspace, snap.project, snap.env, snap.date),
    onSuccess: (data, snap) => setVerify({ key: `${snap.workspace}/${snap.project}-${snap.env}-${snap.date}`, data }),
    onError: (e, snap) => setVerify({ key: `${snap.workspace}/${snap.project}-${snap.env}-${snap.date}`, error: e.response?.data?.error || 'Verify failed' }),
  })

  const items = (data || []).filter(b => {
    if (workspaceFilter && !b.workspace?.toLowerCase().includes(workspaceFilter.toLowerCase())) return false
    if (typeFilter !== 'all') {
      const wsType = wsTypes[b.workspace]
      if (wsType && wsType !== typeFilter) return false
    }
    return true
  })

  if (isLoading) return <p className="text-sm text-content-subtle p-5">Loading…</p>
  if (items.length === 0) return <p className="text-sm text-content-subtle p-5">No backups match the current filter.</p>

  return (
    <>
      {syncErr && (
        <p className="mx-5 my-2 text-xs text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">
          {syncErr}
        </p>
      )}
      <div className="divide-y divide-border">
        {items.map((snap, i) => {
          const key = `${snap.workspace}/${snap.project}-${snap.env}-${snap.date}`
          const isOpen = expanded === key
          return (
            <div key={i}>
              {/* Row header — click to expand file list */}
              <div className="flex items-center gap-3 px-5 py-3 hover:bg-surface-raised/40 transition-colors">
                <button
                  className="flex items-center gap-3 flex-1 min-w-0 text-left"
                  onClick={() => setExpanded(isOpen ? null : key)}
                >
                  <span className="text-xs font-medium px-2 py-0.5 rounded-full bg-surface-overlay/40 text-content shrink-0">
                    {snap.env}
                  </span>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm text-content-strong font-medium truncate">
                      <span className="text-content-subtle">{snap.workspace} / </span>{snap.project}
                    </p>
                    <p className="text-xs text-content-subtle font-mono">{formatDate(snap.date)}</p>
                    {(snap.trigger || snap.services) && (
                      <p className="text-[11px] text-content-faint truncate">
                        {(snap.services && snap.services.length > 0) ? snap.services.join(', ') : 'all data'}
                        {' · '}{snap.schedule || (snap.trigger === 'manual' ? 'manual' : snap.trigger || 'backup')}
                      </p>
                    )}
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    {snap.sync?.status === 'ok' && (
                      <span title={`Synced to ${snap.sync.target}`}
                        className="text-[10px] px-1.5 py-0.5 rounded bg-success-subtle text-success-fg border border-success-border/60">↑ {snap.sync.target}</span>
                    )}
                    {snap.sync?.status === 'fail' && (
                      <span title="Last sync failed"
                        className="text-[10px] px-1.5 py-0.5 rounded bg-danger-subtle text-danger-fg border border-danger-border/60">↑!</span>
                    )}
                    <span className="text-xs text-content-subtle">{formatBytes(snap.size_bytes)}</span>
                    <span className="text-xs text-content-faint">{isOpen ? '▲' : '▼'}</span>
                  </div>
                </button>
                {/* Sync-to-remote button */}
                <Btn variant="secondary" size="xs" onClick={e => { e.stopPropagation(); setSyncErr(''); syncMut.mutate(snap) }}
                  disabled={syncMut.isPending && syncingKey(syncMut.variables || {}) === key}
                  className="shrink-0"
                  title="Push this snapshot to the workspace's remote backup target"
                >
                  {syncMut.isPending && syncingKey(syncMut.variables || {}) === key ? '…' : 'Sync'}
                </Btn>
                {/* Verify (restore dry-run) button */}
                <Btn variant="secondary" size="xs" onClick={e => { e.stopPropagation(); setVerify({ key }); verifyMut.mutate(snap) }}
                  disabled={verifyMut.isPending && syncingKey(verifyMut.variables || {}) === key}
                  className="shrink-0"
                  title="Dry-run: check this snapshot is complete and restorable"
                >
                  {verifyMut.isPending && syncingKey(verifyMut.variables || {}) === key ? '…' : 'Verify'}
                </Btn>
                {/* Restore button */}
                <button
                  onClick={e => { e.stopPropagation(); setConfirmSnap(snap) }}
                  className="shrink-0 px-2.5 py-1 text-xs font-medium rounded-lg bg-warning-subtle/50 hover:bg-warning/20 text-warning-fg border border-warning-border/50 transition-colors"
                  title={`Restore ${snap.workspace}/${snap.project}/${snap.env} from ${snap.date}`}
                >
                  Restore
                </button>
                {/* Delete button */}
                <Btn variant="dangerSubtle" size="xs" onClick={e => { e.stopPropagation(); setDeleteConfirm(snap) }}
                  className="shrink-0"
                  title="Delete this backup snapshot"
                >
                  ✕
                </Btn>
              </div>

              {/* Restore-verify report (11c) */}
              {verify?.key === key && (
                <div className="mx-5 mb-3 -mt-1 text-xs">
                  {verify.error ? (
                    <p className="text-danger-fg rounded-lg border border-danger-border/60 bg-danger-subtle/40 px-3 py-2">Verify failed: {verify.error}</p>
                  ) : !verify.data ? (
                    <p className="text-content-subtle rounded-lg border border-border bg-surface-raised/40 px-3 py-2">Verifying…</p>
                  ) : (
                    <div className={`rounded-lg border px-3 py-2 ${verify.data.ok ? 'border-success-border/60 bg-success-subtle/40' : 'border-danger-border/60 bg-danger-subtle/40'}`}>
                      <p className={`font-medium ${verify.data.ok ? 'text-success-fg' : 'text-danger-fg'}`}>
                        {verify.data.ok ? '✓ Restorable' : '✗ Problems found'} · {verify.data.files.length} file{verify.data.files.length !== 1 ? 's' : ''} · {formatBytes(verify.data.total_bytes)}
                      </p>
                      <div className="mt-1 space-y-0.5">
                        {verify.data.files.map((f, fi) => (
                          <div key={fi} className="flex items-center justify-between gap-2">
                            <span className="font-mono text-content-muted truncate">{f.ok ? '✓' : '✗'} {f.name}</span>
                            <span className={f.ok ? 'text-content-faint' : 'text-danger-fg'}>{f.ok ? formatBytes(f.size) : f.issue}</span>
                          </div>
                        ))}
                      </div>
                    </div>
                  )}
                </div>
              )}

              {/* Expanded file list */}
              {isOpen && (
                <div className="px-5 pb-3 bg-surface/40">
                  {(snap.files || []).map((f, fi) => (
                    <div key={fi} className="flex justify-between py-1 border-b border-border/40 last:border-0">
                      <span className="font-mono text-xs text-content-muted">{f.name}</span>
                      <span className="text-xs text-content-faint">{formatBytes(f.size)}</span>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )
        })}
      </div>

      {/* Confirm modal */}
      {confirmSnap && (
        <RestoreConfirmModal
          snap={confirmSnap}
          onClose={() => setConfirmSnap(null)}
          onConfirm={() => { setRestoringSnap(confirmSnap); setConfirmSnap(null) }}
        />
      )}

      {/* Restore output modal */}
      {restoringSnap && (
        <RestoreModal
          snap={restoringSnap}
          onClose={() => setRestoringSnap(null)}
        />
      )}

      {/* Delete confirmation modal */}
      {deleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/70">
          <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-sm p-6 shadow-2xl">
            <h3 className="font-semibold text-content-strong mb-2">Delete backup?</h3>
            <p className="text-sm text-content-muted mb-1">
              <span className="text-content font-medium">{deleteConfirm.workspace} / {deleteConfirm.project}</span> / {deleteConfirm.env}
            </p>
            <p className="text-xs text-content-subtle font-mono mb-4">{formatDate(deleteConfirm.date)}</p>
            <p className="text-sm text-danger-fg mb-5">
              This will permanently delete the snapshot and all its files. This cannot be undone.
            </p>
            <div className="flex gap-3">
              <Btn variant="secondary" size="md" onClick={() => setDeleteConfirm(null)}
                className="flex-1"
              >
                Cancel
              </Btn>
              <Btn variant="danger" size="md" onClick={() => deleteMut.mutate(deleteConfirm)}
                disabled={deleteMut.isPending}
                className="flex-1"
              >
                {deleteMut.isPending ? 'Deleting…' : 'Delete'}
              </Btn>
            </div>
            {deleteMut.isError && (
              <p className="text-xs text-danger-fg mt-3">{deleteMut.error?.response?.data?.error || 'Delete failed'}</p>
            )}
          </div>
        </div>
      )}
    </>
  )
}

// ── Version log content ───────────────────────────────────────────────────────

function VersionContent({ workspaceFilter, typeFilter, wsTypes }) {
  const { data, isLoading } = useQuery({ queryKey: ['allActivity'], queryFn: fetchAllActivity, refetchInterval: 15_000 })
  const { data: stats } = useQuery({ queryKey: ['stats'], queryFn: () => import('../lib/api').then(m => m.fetchStats()) })

  const wsList = stats?.workspaces?.workspaces || []

  const filtered = wsList.filter(ws => {
    if (workspaceFilter && !ws.name?.toLowerCase().includes(workspaceFilter.toLowerCase())) return false
    if (typeFilter !== 'all' && ws.type !== typeFilter) return false
    return true
  })

  const activityByWs = {}
  ;(data || []).forEach(a => {
    if (!activityByWs[a.workspace]) activityByWs[a.workspace] = []
    if (['build', 'promote', 'version'].includes(a.command)) {
      activityByWs[a.workspace].push(a)
    }
  })

  if (isLoading) return <p className="text-sm text-content-subtle p-5">Loading…</p>
  if (filtered.length === 0) return <p className="text-sm text-content-subtle p-5">No workspaces match the current filter.</p>

  return (
    <div className="divide-y divide-border">
      {filtered.map(ws => {
        // Activity is keyed by audit_log.project = the resource prefix ({ws}_{proj}),
        // not the bare project key — group/look up by resource_prefix or events never match.
        const events = activityByWs[ws.resource_prefix] || activityByWs[ws.name] || []
        const isImage = ws.type === 'image'
        return (
          <div key={ws.name} className="px-5 py-3">
            <div className="flex items-center justify-between mb-1">
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium text-content-strong">{ws.name}</span>
                <span className={`text-xs px-1.5 py-0.5 rounded ${isImage ? 'bg-info-subtle text-info-fg' : 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300'}`}>
                  {ws.type}
                </span>
              </div>
              {isImage
                ? <span className="text-xs text-content-faint">image stack — no semver</span>
                : <span className="text-xs text-content-muted font-mono">
                    {/* version would come from config — use stats */}
                  </span>
              }
            </div>
            {!isImage && events.length > 0 ? (
              <div className="mt-1.5 space-y-1">
                {events.slice(0, 5).map((e, i) => (
                  <div key={i} className="flex items-center gap-3 text-xs">
                    <span className={`px-1.5 py-0.5 rounded ${CMD_COLOR[e.command] || 'bg-surface-overlay/20 text-content-subtle'}`}>{e.command}</span>
                    {e.env && <span className="text-content-subtle">{e.env}</span>}
                    <span className="text-content-faint ml-auto">{timeAgo(e.created_at)}</span>
                  </div>
                ))}
              </div>
            ) : !isImage ? (
              <p className="text-xs text-content-faint mt-1">No build/promote events recorded yet.</p>
            ) : null}
          </div>
        )
      })}
    </div>
  )
}

// ── Alerts inbox content (6c) ─────────────────────────────────────────────────

const SEVERITY = {
  critical: { dot: 'bg-red-500',    badge: 'bg-red-500/15 text-danger-fg border-danger-border/50',     label: 'Critical' },
  warning:  { dot: 'bg-amber-400',  badge: 'bg-amber-500/15 text-warning-fg border-warning-border/50', label: 'Warning' },
  info:     { dot: 'bg-blue-400',   badge: 'bg-blue-500/15 text-info-fg border-info-border/50',   label: 'Info' },
}

function AlertsContent() {
  const qc = useQueryClient()
  const [showResolved, setShowResolved] = useState(false)

  const { data, isLoading } = useQuery({
    queryKey: ['alertEvents', showResolved],
    queryFn: () => fetchAlertEvents({ resolved: showResolved ? 'true' : '' }),
    refetchInterval: 60_000, // SSE drives real-time; this is a reconnect fallback
  })

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['alertEvents'] })
    qc.invalidateQueries({ queryKey: ['alertUnread'] })
  }
  const dismissMut    = useMutation({ mutationFn: dismissAlert,     onSuccess: invalidate })
  const dismissAllMut = useMutation({ mutationFn: dismissAllAlerts, onSuccess: invalidate })

  const events = data || []
  const activeCount = events.filter(e => !e.resolved_at).length

  return (
    <>
      {/* Controls */}
      <div className="flex items-center justify-between px-5 py-3 border-b border-border bg-surface/60 shrink-0">
        <label className="flex items-center gap-2 text-sm text-content-muted cursor-pointer select-none">
          <input
            type="checkbox" checked={showResolved} onChange={e => setShowResolved(e.target.checked)}
            className="accent-brand-500"
          />
          Show resolved
        </label>
        <Btn variant="secondary" size="xs" onClick={() => dismissAllMut.mutate()}
          disabled={events.length === 0 || dismissAllMut.isPending}
          
        >
          {dismissAllMut.isPending ? 'Dismissing…' : 'Dismiss all'}
        </Btn>
      </div>

      {isLoading ? (
        <p className="text-sm text-content-subtle p-5">Loading…</p>
      ) : events.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-20 text-center">
          <div className="text-4xl mb-3 opacity-30">✓</div>
          <p className="text-content font-medium mb-1">No active alerts</p>
          <p className="text-sm text-content-subtle">
            {showResolved ? 'Nothing in the inbox.' : 'Everything is healthy. Toggle “Show resolved” to see history.'}
          </p>
        </div>
      ) : (
        <div className="divide-y divide-border">
          {activeCount === 0 && (
            <p className="text-xs text-content-subtle px-5 py-2 bg-surface/40">All shown alerts are resolved.</p>
          )}
          {events.map(ev => {
            const sev = SEVERITY[ev.severity] || SEVERITY.warning
            const resolved = !!ev.resolved_at
            return (
              <div key={ev.id} className={`flex items-start gap-3 px-5 py-3 hover:bg-surface-raised/40 transition-colors ${resolved ? 'opacity-60' : ''}`}>
                <span className={`mt-1.5 w-2 h-2 rounded-full shrink-0 ${resolved ? 'bg-surface-overlay' : sev.dot}`} />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className={`text-[10px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded border ${sev.badge}`}>
                      {sev.label}
                    </span>
                    {ev.workspace && (
                      <span className="text-xs text-content-muted">
                        {ev.workspace}{ev.env ? ` / ${ev.env}` : ''}
                      </span>
                    )}
                    {resolved && <span className="text-[10px] text-success-fg font-medium">resolved</span>}
                  </div>
                  <p className="text-sm text-content mt-1 break-words">{ev.message}</p>
                  <p className="text-xs text-content-faint mt-0.5">
                    {timeAgo(ev.fired_at)}
                    {resolved && ev.resolved_at && <span> · cleared {timeAgo(ev.resolved_at)}</span>}
                  </p>
                </div>
                <IconBtn variant="ghost" size="sm" onClick={() => dismissMut.mutate(ev.id)}
                  disabled={dismissMut.isPending}
                  title="Dismiss"
                  
                >
                  ×
                </IconBtn>
              </div>
            )
          })}
        </div>
      )}
    </>
  )
}

// ── Main SlideOutPanel ────────────────────────────────────────────────────────

const PANEL_CONFIG = {
  activity: { title: 'Recent Activity',  icon: '◎' },
  backup:   { title: 'Backup History',   icon: '○' },
  version:  { title: 'Version Log',      icon: '○' },
  alerts:   { title: 'Alerts',           icon: '◔' },
}

export default function SlideOutPanel({ panel, onClose, workspace }) {
  // Default the view to the selected workspace (item 10) — entries are scoped to
  // it on open; the user can clear the filter to see everything they can access.
  const [workspaceFilter, setWorkspaceFilter] = useState(workspace || '')
  const [typeFilter, setTypeFilter]           = useState('all')
  const panelRef = useRef(null)

  // Build a ws-name → type map for cross-content filtering
  const { data: workspaces } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const wsTypes = {}
  ;(workspaces || []).forEach(ws => { wsTypes[ws.name] = ws.config?.project?.type || 'custom' })

  // Reset filters when switching panels — re-scope to the selected workspace.
  useEffect(() => {
    setWorkspaceFilter(workspace || '')
    setTypeFilter('all')
  }, [panel, workspace])

  // Close on Escape
  useEffect(() => {
    function onKey(e) { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onClose])

  const cfg = PANEL_CONFIG[panel] || {}

  return (
    <>
      {/* Backdrop */}
      <div
        className="fixed inset-0 z-40 bg-black/40 backdrop-blur-sm"
        onClick={onClose}
      />

      {/* Panel — slides in from the right, 70% width */}
      <div
        ref={panelRef}
        className="fixed top-0 right-0 bottom-0 z-50 flex flex-col bg-canvas border-l border-border shadow-2xl"
        style={{ width: '70%' }}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-border shrink-0">
          <div className="flex items-center gap-2">
            <span className="text-xs text-content-subtle">{cfg.icon}</span>
            <h2 className="text-base font-semibold text-content-strong">{cfg.title}</h2>
          </div>
          <button
            onClick={onClose}
            className="text-content-subtle hover:text-content-strong transition-colors text-xl leading-none"
          >
            ×
          </button>
        </div>

        {/* Filter bar — not shown for the alerts inbox, which has its own controls */}
        {panel !== 'alerts' && (
          <FilterBar
            workspaceFilter={workspaceFilter}
            setWorkspaceFilter={setWorkspaceFilter}
            typeFilter={typeFilter}
            setTypeFilter={setTypeFilter}
          />
        )}

        {/* Scrollable content */}
        <div className="flex-1 overflow-y-auto min-h-0">
          {panel === 'activity' && (
            <ActivityContent workspaceFilter={workspaceFilter} typeFilter={typeFilter} wsTypes={wsTypes} />
          )}
          {panel === 'backup' && (
            <BackupContent workspaceFilter={workspaceFilter} typeFilter={typeFilter} wsTypes={wsTypes} />
          )}
          {panel === 'version' && (
            <VersionContent workspaceFilter={workspaceFilter} typeFilter={typeFilter} wsTypes={wsTypes} />
          )}
          {panel === 'alerts' && <AlertsContent />}
        </div>
      </div>
    </>
  )
}
