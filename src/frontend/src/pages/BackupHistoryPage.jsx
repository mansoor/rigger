import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchBackups, syncEnvBackup } from '../lib/api'
import Layout from '../components/Layout'
import { useWorkspaceStore } from '../store/workspace'
import { Hint, Btn } from '../components/ui'

function formatBytes(bytes) {
  if (bytes === 0) return '0 B'
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

function formatDate(dateStr) {
  // Format: 2026-05-30_14-22-05
  const [date, time] = dateStr.split('_')
  if (!date || !time) return dateStr
  const t = time.replace(/-/g, ':')
  return `${date} ${t}`
}

function SnapshotRow({ snap, isOpen, onToggle }) {
  const qc = useQueryClient()
  const [err, setErr] = useState('')
  const syncMut = useMutation({
    mutationFn: () => syncEnvBackup(snap.workspace, snap.env, { date: snap.date }),
    onSuccess: () => { setErr(''); qc.invalidateQueries({ queryKey: ['backups'] }) },
    onError: (e) => setErr(e.response?.data?.error || 'Sync failed'),
  })

  const sync = snap.sync
  return (
    <div>
      <div className="flex items-center hover:bg-surface-raised/50 transition-colors">
        <button
          className="flex-1 min-w-0 px-5 py-3 flex items-center justify-between text-left"
          onClick={onToggle}
        >
          <div className="flex items-center gap-4 min-w-0">
            <span className="text-xs font-medium px-2 py-0.5 rounded-full bg-surface-raised text-content shrink-0">{snap.env}</span>
            <span className="font-mono text-sm text-content truncate">{formatDate(snap.date)}</span>
            {sync && sync.status === 'ok' && (
              <span className="text-[11px] px-1.5 py-0.5 rounded bg-success-subtle text-success-fg border border-success-border/60 shrink-0"
                title={`Synced to ${sync.target}`}>↑ {sync.target}</span>
            )}
            {sync && sync.status === 'fail' && (
              <span className="text-[11px] px-1.5 py-0.5 rounded bg-danger-subtle text-danger-fg border border-danger-border/60 shrink-0"
                title="Last sync failed">sync failed</span>
            )}
          </div>
          <div className="flex items-center gap-4 shrink-0">
            <span className="text-xs text-content-subtle">{formatBytes(snap.size_bytes)}</span>
            <span className="text-xs text-content-subtle">{snap.files?.length || 0} file{snap.files?.length !== 1 ? 's' : ''}</span>
            <span className="text-content-faint text-xs">{isOpen ? '▲' : '▼'}</span>
          </div>
        </button>
        <div className="pr-5 pl-2 shrink-0">
          <Btn variant="secondary" size="xs" onClick={() => syncMut.mutate()}
            disabled={syncMut.isPending}
            title="Push this snapshot to the workspace's remote backup target"
            
          >
            {syncMut.isPending ? 'Syncing…' : 'Sync'}
          </Btn>
        </div>
      </div>

      {err && <p className="px-5 pb-2 text-xs text-danger-fg">{err}</p>}

      {isOpen && (
        <div className="px-5 pb-4 bg-surface/60">
          {(snap.files || []).length === 0
            ? <p className="text-xs text-content-subtle">No files in this snapshot.</p>
            : (
              <div className="space-y-1">
                {snap.files.map((f, fi) => (
                  <div key={fi} className="flex items-center justify-between py-1.5 border-b border-border/60 last:border-0">
                    <span className="font-mono text-xs text-content">{f.name}</span>
                    <span className="text-xs text-content-subtle">{formatBytes(f.size)}</span>
                  </div>
                ))}
              </div>
            )
          }
        </div>
      )}
    </div>
  )
}

export default function BackupHistoryPage() {
  const current = useWorkspaceStore(s => s.current)
  const { data: backups, isLoading } = useQuery({
    queryKey: ['backups', current],
    queryFn: () => fetchBackups(current),
  })

  const [expanded, setExpanded] = useState(null)
  const [filter, setFilter] = useState('')

  const items = (backups || []).filter(b =>
    !filter || b.workspace.includes(filter) || b.env.includes(filter)
  )

  // Group by workspace
  const grouped = {}
  for (const b of items) {
    if (!grouped[b.workspace]) grouped[b.workspace] = []
    grouped[b.workspace].push(b)
  }

  return (
    <Layout>
      <div className="p-6 max-w-4xl">
        <div className="flex items-center justify-between mb-6">
          <div>
            <h1 className="text-xl font-bold text-content-strong">Backup history</h1>
            <p className="text-sm text-content-muted mt-0.5">
              {current ? `Snapshots in this workspace, across every environment` : 'All snapshots across every workspace and environment'}
            </p>
          </div>
          <input
            type="text"
            placeholder="Filter by workspace or env…"
            value={filter}
            onChange={e => setFilter(e.target.value)}
            className="px-3 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-sm text-content-strong placeholder-content-subtle focus:outline-none focus:border-brand-500 w-56"
          />
        </div>

        {isLoading && <p className="text-content-subtle text-sm">Loading…</p>}

        {!isLoading && items.length === 0 && (
          <div className="bg-surface border border-border rounded-xl p-8 text-center">
            <p className="text-content-subtle text-sm">No backups found.</p>
            <Hint tone="faint">Run a backup from any environment card to create one.</Hint>
          </div>
        )}

        <div className="space-y-6">
          {Object.entries(grouped).map(([wsName, snapshots]) => (
            <div key={wsName} className="bg-surface border border-border rounded-xl overflow-hidden">
              <div className="px-5 py-3 border-b border-border flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-brand-500 shrink-0" />
                <h2 className="text-sm font-semibold text-content-strong">{wsName}</h2>
                <span className="text-xs text-content-subtle">{snapshots.length} snapshot{snapshots.length !== 1 ? 's' : ''}</span>
              </div>

              <div className="divide-y divide-border">
                {snapshots.map((snap, i) => {
                  const key = `${snap.workspace}-${snap.env}-${snap.date}`
                  return (
                    <SnapshotRow
                      key={i}
                      snap={snap}
                      isOpen={expanded === key}
                      onToggle={() => setExpanded(expanded === key ? null : key)}
                    />
                  )
                })}
              </div>
            </div>
          ))}
        </div>
      </div>
    </Layout>
  )
}
