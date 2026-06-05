import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchBackups } from '../lib/api'
import Layout from '../components/Layout'

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

export default function BackupHistoryPage() {
  const { data: backups, isLoading } = useQuery({
    queryKey: ['backups'],
    queryFn: fetchBackups,
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
            <h1 className="text-xl font-bold text-white">Backup history</h1>
            <p className="text-sm text-gray-400 mt-0.5">All snapshots across every workspace and environment</p>
          </div>
          <input
            type="text"
            placeholder="Filter by workspace or env…"
            value={filter}
            onChange={e => setFilter(e.target.value)}
            className="px-3 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-sm text-white placeholder-gray-500 focus:outline-none focus:border-brand-500 w-56"
          />
        </div>

        {isLoading && <p className="text-gray-500 text-sm">Loading…</p>}

        {!isLoading && items.length === 0 && (
          <div className="bg-gray-900 border border-gray-800 rounded-xl p-8 text-center">
            <p className="text-gray-500 text-sm">No backups found.</p>
            <p className="text-gray-600 text-xs mt-1">Run a backup from any environment card to create one.</p>
          </div>
        )}

        <div className="space-y-6">
          {Object.entries(grouped).map(([wsName, snapshots]) => (
            <div key={wsName} className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
              <div className="px-5 py-3 border-b border-gray-800 flex items-center gap-2">
                <span className="w-2 h-2 rounded-full bg-brand-500 shrink-0" />
                <h2 className="text-sm font-semibold text-white">{wsName}</h2>
                <span className="text-xs text-gray-500">{snapshots.length} snapshot{snapshots.length !== 1 ? 's' : ''}</span>
              </div>

              <div className="divide-y divide-gray-800">
                {snapshots.map((snap, i) => {
                  const key = `${snap.workspace}-${snap.env}-${snap.date}`
                  const isOpen = expanded === key
                  return (
                    <div key={i}>
                      <button
                        className="w-full px-5 py-3 flex items-center justify-between hover:bg-gray-800/50 transition-colors text-left"
                        onClick={() => setExpanded(isOpen ? null : key)}
                      >
                        <div className="flex items-center gap-4 min-w-0">
                          <span className="text-xs font-medium px-2 py-0.5 rounded-full bg-gray-800 text-gray-300 shrink-0">{snap.env}</span>
                          <span className="font-mono text-sm text-gray-300 truncate">{formatDate(snap.date)}</span>
                        </div>
                        <div className="flex items-center gap-4 shrink-0">
                          <span className="text-xs text-gray-500">{formatBytes(snap.size_bytes)}</span>
                          <span className="text-xs text-gray-500">{snap.files?.length || 0} file{snap.files?.length !== 1 ? 's' : ''}</span>
                          <span className="text-gray-600 text-xs">{isOpen ? '▲' : '▼'}</span>
                        </div>
                      </button>

                      {isOpen && (
                        <div className="px-5 pb-4 bg-gray-900/60">
                          {(snap.files || []).length === 0
                            ? <p className="text-xs text-gray-500">No files in this snapshot.</p>
                            : (
                              <div className="space-y-1">
                                {snap.files.map((f, fi) => (
                                  <div key={fi} className="flex items-center justify-between py-1.5 border-b border-gray-800/60 last:border-0">
                                    <span className="font-mono text-xs text-gray-300">{f.name}</span>
                                    <span className="text-xs text-gray-500">{formatBytes(f.size)}</span>
                                  </div>
                                ))}
                              </div>
                            )
                          }
                        </div>
                      )}
                    </div>
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
