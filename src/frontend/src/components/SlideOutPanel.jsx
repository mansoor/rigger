import { useState, useEffect, useRef } from 'react'
import { useQuery, useQueryClient, useMutation } from '@tanstack/react-query'
import { fetchAllActivity, fetchBackups, fetchWorkspaces, openActionSocket, deleteBackup,
  fetchAlertEvents, dismissAlert, dismissAllAlerts } from '../lib/api'

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
  start:   'bg-green-500/20 text-green-400',
  deploy:  'bg-green-500/20 text-green-400',
  stop:    'bg-red-500/20   text-red-400',
  down:    'bg-red-500/20   text-red-400',
  restart: 'bg-amber-400/20 text-amber-300',
  update:  'bg-amber-400/20 text-amber-300',
  backup:  'bg-gray-500/20  text-gray-400',
  build:   'bg-brand-500/20 text-brand-400',
  promote: 'bg-purple-500/20 text-purple-400',
  delete:  'bg-red-700/20   text-red-500',
}

// ── Filter bar — workspace search + type selector ─────────────────────────────

function FilterBar({ workspaceFilter, setWorkspaceFilter, typeFilter, setTypeFilter }) {
  return (
    <div className="flex items-center gap-2 px-5 py-3 border-b border-gray-800 bg-gray-900/60 shrink-0">
      <input
        type="text"
        placeholder="Filter by workspace…"
        value={workspaceFilter}
        onChange={e => setWorkspaceFilter(e.target.value)}
        className="flex-1 px-3 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-sm text-white placeholder-gray-500 focus:outline-none focus:border-brand-500"
      />
      <select
        value={typeFilter}
        onChange={e => setTypeFilter(e.target.value)}
        className="px-3 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-sm text-gray-300 focus:outline-none focus:border-brand-500"
      >
        <option value="all">All types</option>
        <option value="image">Image stacks</option>
        <option value="custom">Custom apps</option>
      </select>
      {(workspaceFilter || typeFilter !== 'all') && (
        <button
          onClick={() => { setWorkspaceFilter(''); setTypeFilter('all') }}
          className="text-xs text-gray-500 hover:text-gray-300 transition-colors px-2"
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

  if (isLoading) return <p className="text-sm text-gray-500 p-5">Loading…</p>
  if (items.length === 0) return <p className="text-sm text-gray-500 p-5">No activity matches the current filter.</p>

  return (
    <div className="divide-y divide-gray-800">
      {items.map((item, i) => {
        const cls = CMD_COLOR[item.command] || 'bg-gray-700/20 text-gray-400'
        return (
          <div key={i} className="flex items-center gap-4 px-5 py-3 hover:bg-gray-800/40 transition-colors">
            <span className={`text-xs font-medium px-2 py-0.5 rounded-full shrink-0 ${cls}`}>
              {item.command}
            </span>
            <div className="flex-1 min-w-0">
              <p className="text-sm text-white font-medium truncate">{item.workspace}</p>
              <p className="text-xs text-gray-500">
                {item.env && <span className="mr-2">env: <span className="text-gray-400">{item.env}</span></span>}
                by <span className="text-gray-400">{item.username}</span>
              </p>
            </div>
            <span className="text-xs text-gray-600 shrink-0" title={item.created_at}>{timeAgo(item.created_at)}</span>
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
    const ws = openActionSocket(snap.workspace, 'restore', snap.env, [snap.date])

    ws.addEventListener('message', e => {
      setLines(prev => [...prev, e.data])
      if (e.data.includes('Restore Complete') || e.data.includes('restored successfully')) {
        setDone(true)
        qc.invalidateQueries({ queryKey: ['workspaces'] })
        qc.invalidateQueries({ queryKey: ['envstatus', snap.workspace] })
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
      <div className="bg-gray-900 border border-gray-700 rounded-2xl w-full max-w-2xl mx-4 flex flex-col shadow-2xl" style={{ maxHeight: '80vh' }}>
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800 shrink-0">
          <div>
            <h3 className="font-semibold text-white text-sm">
              Restoring {snap.workspace} / {snap.env}
            </h3>
            <p className="text-xs text-gray-500 mt-0.5 font-mono">{formatDate(snap.date)}</p>
          </div>
          <div className="flex items-center gap-3">
            {!done && (
              <span className="flex items-center gap-1.5 text-xs text-amber-400">
                <span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />
                Running…
              </span>
            )}
            {done && !error && (
              <span className="flex items-center gap-1.5 text-xs text-green-400">
                <span className="w-1.5 h-1.5 rounded-full bg-green-400" />
                Complete
              </span>
            )}
            {done && error && (
              <span className="text-xs text-red-400">Error</span>
            )}
          </div>
        </div>

        {/* Output */}
        <div
          ref={scrollRef}
          className="flex-1 overflow-y-auto min-h-0 p-4 font-mono text-xs bg-gray-950 rounded-b-none"
          style={{ minHeight: 280 }}
        >
          {lines.length === 0 && (
            <p className="text-gray-600">Connecting…</p>
          )}
          {lines.map((line, i) => (
            <div key={i} className={`leading-5 whitespace-pre-wrap ${
              line.includes('ERROR') || line.includes('error') ? 'text-red-400' :
              line.includes('✓') || line.includes('Complete') || line.includes('restored') ? 'text-green-400' :
              line.includes('⚑') || line.includes('Step') ? 'text-brand-400 font-semibold' :
              line.includes('⚠') || line.includes('WARN') ? 'text-amber-400' :
              'text-gray-300'
            }`}>
              {stripAnsi(line)}
            </div>
          ))}
        </div>

        {/* Footer */}
        <div className="px-5 py-3 border-t border-gray-800 shrink-0 flex justify-end">
          <button
            onClick={onClose}
            disabled={!done}
            className="px-4 py-1.5 text-sm font-medium rounded-lg transition-colors disabled:opacity-40 bg-gray-700 hover:bg-gray-600 text-white"
          >
            {done ? 'Close' : 'Running…'}
          </button>
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
        className="bg-gray-900 border border-gray-700 rounded-2xl w-full max-w-md mx-4 p-6 shadow-2xl"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-start gap-3 mb-4">
          <span className="text-2xl mt-0.5">⚠️</span>
          <div>
            <h3 className="font-semibold text-white">Restore from backup?</h3>
            <p className="text-sm text-gray-400 mt-1">
              This will <span className="text-white font-medium">stop</span> the{' '}
              <span className="text-white font-medium">{snap.workspace}/{snap.env}</span> stack,
              overwrite all database and volume data with the snapshot from{' '}
              <span className="font-mono text-amber-300 text-xs">{formatDate(snap.date)}</span>,
              then restart the stack.
            </p>
            <p className="text-sm text-red-400 mt-2 font-medium">Current data will be lost.</p>
          </div>
        </div>

        <div className="bg-gray-800/60 border border-gray-700/60 rounded-lg px-4 py-2.5 mb-4">
          <p className="text-xs text-gray-400">
            Files in this snapshot: {(snap.files || []).map(f => f.name).join(', ') || 'none'}
          </p>
        </div>

        <div className="flex gap-2 justify-end">
          <button
            onClick={onClose}
            className="px-4 py-2 text-sm font-medium rounded-lg bg-gray-700 hover:bg-gray-600 text-white transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={onConfirm}
            className="px-4 py-2 text-sm font-medium rounded-lg bg-red-700 hover:bg-red-600 text-white transition-colors"
          >
            Restore
          </button>
        </div>
      </div>
    </div>
  )
}

// ── Backup content ────────────────────────────────────────────────────────────

function BackupContent({ workspaceFilter, typeFilter, wsTypes }) {
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({ queryKey: ['backups'], queryFn: fetchBackups, refetchInterval: 60_000 })
  const [expanded, setExpanded]           = useState(null)
  const [confirmSnap, setConfirmSnap]     = useState(null)
  const [restoringSnap, setRestoringSnap] = useState(null)
  const [deleteConfirm, setDeleteConfirm] = useState(null) // snap to delete

  const deleteMut = useMutation({
    mutationFn: (snap) => deleteBackup(snap.workspace, snap.env, snap.date),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['backups'] })
      setDeleteConfirm(null)
    },
  })

  const items = (data || []).filter(b => {
    if (workspaceFilter && !b.workspace?.toLowerCase().includes(workspaceFilter.toLowerCase())) return false
    if (typeFilter !== 'all') {
      const wsType = wsTypes[b.workspace]
      if (wsType && wsType !== typeFilter) return false
    }
    return true
  })

  if (isLoading) return <p className="text-sm text-gray-500 p-5">Loading…</p>
  if (items.length === 0) return <p className="text-sm text-gray-500 p-5">No backups match the current filter.</p>

  return (
    <>
      <div className="divide-y divide-gray-800">
        {items.map((snap, i) => {
          const key = `${snap.workspace}-${snap.env}-${snap.date}`
          const isOpen = expanded === key
          return (
            <div key={i}>
              {/* Row header — click to expand file list */}
              <div className="flex items-center gap-3 px-5 py-3 hover:bg-gray-800/40 transition-colors">
                <button
                  className="flex items-center gap-3 flex-1 min-w-0 text-left"
                  onClick={() => setExpanded(isOpen ? null : key)}
                >
                  <span className="text-xs font-medium px-2 py-0.5 rounded-full bg-gray-700/40 text-gray-300 shrink-0">
                    {snap.env}
                  </span>
                  <div className="flex-1 min-w-0">
                    <p className="text-sm text-white font-medium truncate">{snap.workspace}</p>
                    <p className="text-xs text-gray-500 font-mono">{formatDate(snap.date)}</p>
                  </div>
                  <div className="flex items-center gap-2 shrink-0">
                    <span className="text-xs text-gray-500">{formatBytes(snap.size_bytes)}</span>
                    <span className="text-xs text-gray-600">{isOpen ? '▲' : '▼'}</span>
                  </div>
                </button>
                {/* Restore button */}
                <button
                  onClick={e => { e.stopPropagation(); setConfirmSnap(snap) }}
                  className="shrink-0 px-2.5 py-1 text-xs font-medium rounded-lg bg-amber-900/50 hover:bg-amber-800/70 text-amber-300 border border-amber-800/50 transition-colors"
                  title={`Restore ${snap.workspace}/${snap.env} from ${snap.date}`}
                >
                  Restore
                </button>
                {/* Delete button */}
                <button
                  onClick={e => { e.stopPropagation(); setDeleteConfirm(snap) }}
                  className="shrink-0 px-2 py-1 text-xs font-medium rounded-lg bg-red-950/50 hover:bg-red-900/60 text-red-400 border border-red-900/50 transition-colors"
                  title="Delete this backup snapshot"
                >
                  ✕
                </button>
              </div>

              {/* Expanded file list */}
              {isOpen && (
                <div className="px-5 pb-3 bg-gray-900/40">
                  {(snap.files || []).map((f, fi) => (
                    <div key={fi} className="flex justify-between py-1 border-b border-gray-800/40 last:border-0">
                      <span className="font-mono text-xs text-gray-400">{f.name}</span>
                      <span className="text-xs text-gray-600">{formatBytes(f.size)}</span>
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
          <div className="bg-gray-900 border border-gray-700 rounded-2xl w-full max-w-sm p-6 shadow-2xl">
            <h3 className="font-semibold text-white mb-2">Delete backup?</h3>
            <p className="text-sm text-gray-400 mb-1">
              <span className="text-gray-300 font-medium">{deleteConfirm.workspace}</span> / {deleteConfirm.env}
            </p>
            <p className="text-xs text-gray-500 font-mono mb-4">{formatDate(deleteConfirm.date)}</p>
            <p className="text-sm text-red-400 mb-5">
              This will permanently delete the snapshot and all its files. This cannot be undone.
            </p>
            <div className="flex gap-3">
              <button
                onClick={() => setDeleteConfirm(null)}
                className="flex-1 py-2 rounded-lg border border-gray-700 text-gray-300 hover:bg-gray-800 text-sm transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={() => deleteMut.mutate(deleteConfirm)}
                disabled={deleteMut.isPending}
                className="flex-1 py-2 rounded-lg bg-red-800 hover:bg-red-700 disabled:opacity-50 text-white text-sm font-semibold transition-colors"
              >
                {deleteMut.isPending ? 'Deleting…' : 'Delete'}
              </button>
            </div>
            {deleteMut.isError && (
              <p className="text-xs text-red-400 mt-3">{deleteMut.error?.response?.data?.error || 'Delete failed'}</p>
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

  if (isLoading) return <p className="text-sm text-gray-500 p-5">Loading…</p>
  if (filtered.length === 0) return <p className="text-sm text-gray-500 p-5">No workspaces match the current filter.</p>

  return (
    <div className="divide-y divide-gray-800">
      {filtered.map(ws => {
        const events = activityByWs[ws.name] || []
        const isImage = ws.type === 'image'
        return (
          <div key={ws.name} className="px-5 py-3">
            <div className="flex items-center justify-between mb-1">
              <div className="flex items-center gap-2">
                <span className="text-sm font-medium text-white">{ws.name}</span>
                <span className={`text-xs px-1.5 py-0.5 rounded ${isImage ? 'bg-blue-950 text-blue-300' : 'bg-purple-950 text-purple-300'}`}>
                  {ws.type}
                </span>
              </div>
              {isImage
                ? <span className="text-xs text-gray-600">image stack — no semver</span>
                : <span className="text-xs text-gray-400 font-mono">
                    {/* version would come from config — use stats */}
                  </span>
              }
            </div>
            {!isImage && events.length > 0 ? (
              <div className="mt-1.5 space-y-1">
                {events.slice(0, 5).map((e, i) => (
                  <div key={i} className="flex items-center gap-3 text-xs">
                    <span className={`px-1.5 py-0.5 rounded ${CMD_COLOR[e.command] || 'bg-gray-700/20 text-gray-500'}`}>{e.command}</span>
                    {e.env && <span className="text-gray-500">{e.env}</span>}
                    <span className="text-gray-600 ml-auto">{timeAgo(e.created_at)}</span>
                  </div>
                ))}
              </div>
            ) : !isImage ? (
              <p className="text-xs text-gray-600 mt-1">No build/promote events recorded yet.</p>
            ) : null}
          </div>
        )
      })}
    </div>
  )
}

// ── Alerts inbox content (6c) ─────────────────────────────────────────────────

const SEVERITY = {
  critical: { dot: 'bg-red-500',    badge: 'bg-red-500/15 text-red-300 border-red-800/50',     label: 'Critical' },
  warning:  { dot: 'bg-amber-400',  badge: 'bg-amber-500/15 text-amber-300 border-amber-700/50', label: 'Warning' },
  info:     { dot: 'bg-blue-400',   badge: 'bg-blue-500/15 text-blue-300 border-blue-700/50',   label: 'Info' },
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
      <div className="flex items-center justify-between px-5 py-3 border-b border-gray-800 bg-gray-900/60 shrink-0">
        <label className="flex items-center gap-2 text-sm text-gray-400 cursor-pointer select-none">
          <input
            type="checkbox" checked={showResolved} onChange={e => setShowResolved(e.target.checked)}
            className="accent-brand-500"
          />
          Show resolved
        </label>
        <button
          onClick={() => dismissAllMut.mutate()}
          disabled={events.length === 0 || dismissAllMut.isPending}
          className="text-xs font-medium px-3 py-1.5 rounded-lg bg-gray-800 hover:bg-gray-700 text-gray-300 disabled:opacity-40 transition-colors"
        >
          {dismissAllMut.isPending ? 'Dismissing…' : 'Dismiss all'}
        </button>
      </div>

      {isLoading ? (
        <p className="text-sm text-gray-500 p-5">Loading…</p>
      ) : events.length === 0 ? (
        <div className="flex flex-col items-center justify-center py-20 text-center">
          <div className="text-4xl mb-3 opacity-30">✓</div>
          <p className="text-gray-300 font-medium mb-1">No active alerts</p>
          <p className="text-sm text-gray-500">
            {showResolved ? 'Nothing in the inbox.' : 'Everything is healthy. Toggle “Show resolved” to see history.'}
          </p>
        </div>
      ) : (
        <div className="divide-y divide-gray-800">
          {activeCount === 0 && (
            <p className="text-xs text-gray-500 px-5 py-2 bg-gray-900/40">All shown alerts are resolved.</p>
          )}
          {events.map(ev => {
            const sev = SEVERITY[ev.severity] || SEVERITY.warning
            const resolved = !!ev.resolved_at
            return (
              <div key={ev.id} className={`flex items-start gap-3 px-5 py-3 hover:bg-gray-800/40 transition-colors ${resolved ? 'opacity-60' : ''}`}>
                <span className={`mt-1.5 w-2 h-2 rounded-full shrink-0 ${resolved ? 'bg-gray-600' : sev.dot}`} />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className={`text-[10px] font-semibold uppercase tracking-wider px-1.5 py-0.5 rounded border ${sev.badge}`}>
                      {sev.label}
                    </span>
                    {ev.workspace && (
                      <span className="text-xs text-gray-400">
                        {ev.workspace}{ev.env ? ` / ${ev.env}` : ''}
                      </span>
                    )}
                    {resolved && <span className="text-[10px] text-green-400 font-medium">resolved</span>}
                  </div>
                  <p className="text-sm text-gray-200 mt-1 break-words">{ev.message}</p>
                  <p className="text-xs text-gray-600 mt-0.5">
                    {timeAgo(ev.fired_at)}
                    {resolved && ev.resolved_at && <span> · cleared {timeAgo(ev.resolved_at)}</span>}
                  </p>
                </div>
                <button
                  onClick={() => dismissMut.mutate(ev.id)}
                  disabled={dismissMut.isPending}
                  title="Dismiss"
                  className="shrink-0 text-gray-600 hover:text-gray-300 transition-colors text-lg leading-none px-1"
                >
                  ×
                </button>
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

export default function SlideOutPanel({ panel, onClose }) {
  const [workspaceFilter, setWorkspaceFilter] = useState('')
  const [typeFilter, setTypeFilter]           = useState('all')
  const panelRef = useRef(null)

  // Build a ws-name → type map for cross-content filtering
  const { data: workspaces } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const wsTypes = {}
  ;(workspaces || []).forEach(ws => { wsTypes[ws.name] = ws.config?.project?.type || 'custom' })

  // Reset filters when switching panels
  useEffect(() => {
    setWorkspaceFilter('')
    setTypeFilter('all')
  }, [panel])

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
        className="fixed top-0 right-0 bottom-0 z-50 flex flex-col bg-gray-950 border-l border-gray-800 shadow-2xl"
        style={{ width: '70%' }}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800 shrink-0">
          <div className="flex items-center gap-2">
            <span className="text-xs text-gray-500">{cfg.icon}</span>
            <h2 className="text-base font-semibold text-white">{cfg.title}</h2>
          </div>
          <button
            onClick={onClose}
            className="text-gray-500 hover:text-white transition-colors text-xl leading-none"
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
