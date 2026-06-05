import { useState, useEffect, useRef } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import {
  fetchHousekeepingStatus, fetchHousekeepingLog,
  fetchHousekeepingImages, fetchStoppedContainers, fetchDanglingVolumes,
  pruneDanglingImages, pruneUnusedImages, pruneContainers, pruneVolumes,
  pruneNetworks, pruneBuildCache,
  fetchJournalStats, journalVacuum, fetchKernels, cleanKernels, aptClean, cleanTmp,
  fetchMigrationLeftovers, cleanMigrationLeftover, dismissMigrationLeftover,
} from '../lib/api'

// ── Shared primitives ─────────────────────────────────────────────────────────

function fmtBytes(b) {
  if (!b || b === 0) return '0 B'
  const GB = 1024 * 1024 * 1024, MB = 1024 * 1024, KB = 1024
  if (b >= GB) return `${(b / GB).toFixed(1)} GB`
  if (b >= MB) return `${(b / MB).toFixed(1)} MB`
  if (b >= KB) return `${(b / KB).toFixed(1)} KB`
  return `${b} B`
}

function timeAgo(ts) {
  if (!ts) return ''
  const d = new Date(ts.includes('T') ? ts : ts.replace(' ', 'T') + 'Z')
  const diff = Math.floor((Date.now() - d.getTime()) / 1000)
  if (diff < 60) return `${diff}s ago`
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`
  return `${Math.floor(diff / 86400)}d ago`
}

function StatusBadge({ status }) {
  const map = {
    HEALTHY: { color: 'bg-green-900/60 text-green-300 border-green-800/60', icon: '✓', label: 'Healthy' },
    CLEANUP_ADVISED: { color: 'bg-amber-900/60 text-amber-300 border-amber-800/60', icon: '⚠', label: 'Cleanup Advised' },
    CRITICAL_SPACE_DEFICIT: { color: 'bg-red-900/60 text-red-300 border-red-800/60', icon: '✕', label: 'Critical Space Deficit' },
  }
  const s = map[status] || map.HEALTHY
  return (
    <span className={`inline-flex items-center gap-1.5 px-3 py-1 rounded-full border text-sm font-semibold ${s.color}`}>
      <span>{s.icon}</span>{s.label}
    </span>
  )
}

function DiskBar({ label, used, total, color = 'bg-brand-500' }) {
  const pct = total > 0 ? Math.min(100, (used / total) * 100) : 0
  const barColor = pct > 90 ? 'bg-red-500' : pct > 70 ? 'bg-amber-500' : color
  return (
    <div>
      <div className="flex justify-between text-xs text-gray-400 mb-1">
        <span>{label}</span>
        <span>{fmtBytes(used)} / {fmtBytes(total)}</span>
      </div>
      <div className="h-2 bg-gray-800 rounded-full overflow-hidden">
        <div className={`h-full rounded-full transition-all ${barColor}`} style={{ width: `${pct}%` }} />
      </div>
    </div>
  )
}

function OutputModal({ title, output, onClose }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm">
      <div className="bg-gray-900 border border-gray-800 rounded-2xl w-full max-w-2xl mx-4 flex flex-col shadow-2xl" style={{ maxHeight: '80vh' }}>
        <div className="flex justify-between items-center px-5 py-4 border-b border-gray-800 shrink-0">
          <h3 className="font-semibold text-white text-sm">{title}</h3>
          <button onClick={onClose} className="text-gray-500 hover:text-white text-xl">×</button>
        </div>
        <pre className="flex-1 overflow-auto p-4 text-xs text-gray-300 font-mono whitespace-pre-wrap bg-gray-950 rounded-b-2xl">
          {output || '(no output)'}
        </pre>
      </div>
    </div>
  )
}

// ── Tab 1: Dashboard ──────────────────────────────────────────────────────────

function DashboardTab({ status, onQuickAction }) {
  const qc = useQueryClient()
  const [actionOutput, setActionOutput] = useState(null)

  const networkMut = useMutation({
    mutationFn: pruneNetworks,
    onSuccess: (d) => { setActionOutput({ title: 'Network Prune', output: d.output }); qc.invalidateQueries({ queryKey: ['hk-status'] }) },
  })
  const danglingMut = useMutation({
    mutationFn: pruneDanglingImages,
    onSuccess: (d) => { setActionOutput({ title: 'Dangling Images Pruned', output: d.output }); qc.invalidateQueries({ queryKey: ['hk-status'] }) },
  })

  const docker = status?.docker || {}
  const totalSize = (docker.images?.size_bytes || 0) + (docker.containers?.size_bytes || 0) +
    (docker.volumes?.size_bytes || 0) + (docker.build_cache?.size_bytes || 0)
  const totalReclaimable = (docker.images?.reclaimable_bytes || 0) + (docker.containers?.reclaimable_bytes || 0) +
    (docker.volumes?.reclaimable_bytes || 0) + (docker.build_cache?.reclaimable_bytes || 0)

  return (
    <div className="space-y-6">
      {/* Health header */}
      <div className="flex items-center justify-between p-4 bg-gray-900 border border-gray-800 rounded-xl">
        <div>
          <p className="text-sm text-gray-400 mb-1">System Health</p>
          <StatusBadge status={status?.health_status || 'HEALTHY'} />
          {totalReclaimable > 0 && (
            <p className="text-xs text-gray-500 mt-2">
              {fmtBytes(totalReclaimable)} reclaimable across Docker resources
            </p>
          )}
        </div>
        <div className="text-right">
          <p className="text-xs text-gray-500">Total Docker storage</p>
          <p className="text-2xl font-bold text-white">{fmtBytes(totalSize)}</p>
        </div>
      </div>

      {/* Docker breakdown */}
      <div>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-3">Docker Storage Breakdown</h2>
        <div className="grid grid-cols-2 gap-3">
          {[
            { label: 'Images', key: 'images', icon: '🐳', color: 'bg-blue-500' },
            { label: 'Containers', key: 'containers', icon: '📦', color: 'bg-purple-500' },
            { label: 'Volumes', key: 'volumes', icon: '💾', color: 'bg-amber-500' },
            { label: 'Build Cache', key: 'build_cache', icon: '⚙', color: 'bg-cyan-500' },
          ].map(({ label, key, icon, color }) => {
            const sec = docker[key] || {}
            return (
              <div key={key} className="bg-gray-900 border border-gray-800 rounded-xl p-4">
                <div className="flex items-center gap-2 mb-2">
                  <span className="text-lg">{icon}</span>
                  <span className="text-sm font-medium text-gray-300">{label}</span>
                  <span className="ml-auto text-xs text-gray-500">{sec.count || 0}</span>
                </div>
                <p className="text-lg font-bold text-white">{fmtBytes(sec.size_bytes || 0)}</p>
                {sec.reclaimable_bytes > 0 && (
                  <p className="text-xs text-amber-400 mt-0.5">{fmtBytes(sec.reclaimable_bytes)} reclaimable</p>
                )}
                <div className="mt-2 h-1.5 bg-gray-800 rounded-full overflow-hidden">
                  <div className={`h-full rounded-full ${color}`}
                    style={{ width: totalSize > 0 ? `${((sec.size_bytes || 0) / totalSize) * 100}%` : '0%' }} />
                </div>
              </div>
            )
          })}
        </div>
      </div>

      {/* One-click safe actions */}
      <div>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-3">Safe Quick Actions (No Approval Required)</h2>
        <div className="grid grid-cols-2 gap-3">
          <button
            onClick={() => networkMut.mutate()}
            disabled={networkMut.isPending}
            className="flex items-center gap-3 p-4 bg-gray-900 border border-gray-800 hover:border-gray-600 rounded-xl transition-colors disabled:opacity-50 text-left"
          >
            <span className="text-2xl">🌐</span>
            <div>
              <p className="text-sm font-semibold text-white">Prune Unused Networks</p>
              <p className="text-xs text-gray-500">Remove leftover bridge/overlay networks</p>
            </div>
            {networkMut.isPending && <span className="ml-auto text-xs text-gray-500">Running…</span>}
          </button>
          <button
            onClick={() => danglingMut.mutate()}
            disabled={danglingMut.isPending}
            className="flex items-center gap-3 p-4 bg-gray-900 border border-gray-800 hover:border-gray-600 rounded-xl transition-colors disabled:opacity-50 text-left"
          >
            <span className="text-2xl">🗑</span>
            <div>
              <p className="text-sm font-semibold text-white">Prune Dangling Images</p>
              <p className="text-xs text-gray-500">Remove {'<none>:<none>'} build layers</p>
            </div>
            {danglingMut.isPending && <span className="ml-auto text-xs text-gray-500">Running…</span>}
          </button>
        </div>
      </div>

      {/* Recent activity */}
      {(status?.last_runs || []).length > 0 && (
        <div>
          <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-3">Recent Housekeeping</h2>
          <div className="space-y-1">
            {status.last_runs.map((r, i) => (
              <div key={i} className="flex items-center gap-3 px-3 py-2 bg-gray-900 border border-gray-800 rounded-lg">
                <span className="text-xs font-mono text-gray-400">{r.task}</span>
                <span className="text-xs text-gray-600 ml-auto">{r.freed_gb !== '0.00 GB' ? r.freed_gb + ' freed' : ''}</span>
                <span className="text-xs text-gray-600">{timeAgo(r.run_at)}</span>
              </div>
            ))}
          </div>
        </div>
      )}

      {actionOutput && (
        <OutputModal title={actionOutput.title} output={actionOutput.output} onClose={() => setActionOutput(null)} />
      )}
    </div>
  )
}

// ── Tab 2: Safety Center ──────────────────────────────────────────────────────

// ── 2a: Unused Images ─────────────────────────────────────────────────────────
function UnusedImagesSection() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState({})
  const [output, setOutput] = useState(null)
  const { data: images = [], isLoading, refetch } = useQuery({
    queryKey: ['hk-images'], queryFn: fetchHousekeepingImages, enabled: open,
  })
  const unusedImages = images.filter(i => !i.in_use && i.repository !== '<none>')
  const selectedIDs = Object.entries(selected).filter(([, v]) => v).map(([k]) => k)
  const selectedSize = unusedImages.filter(i => selected[i.id]).reduce((s, i) => s + (i.size_bytes || 0), 0)

  const purgeMut = useMutation({
    mutationFn: () => pruneUnusedImages({ image_ids: selectedIDs }),
    onSuccess: (d) => { setOutput(d.output); qc.invalidateQueries({ queryKey: ['hk-status', 'hk-images'] }) },
  })

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
      <button
        className="w-full flex items-center justify-between p-4 hover:bg-gray-800/40 transition-colors"
        onClick={() => { setOpen(o => !o); if (!open) refetch() }}
      >
        <div className="flex items-center gap-3">
          <span className="text-xl">🐳</span>
          <div className="text-left">
            <p className="text-sm font-semibold text-white">Unused Image Pruning</p>
            <p className="text-xs text-gray-500">Remove old image versions not used by any container</p>
          </div>
        </div>
        <span className="text-xs bg-amber-900/50 text-amber-300 px-2 py-0.5 rounded-full">Approval Required</span>
      </button>

      {open && (
        <div className="border-t border-gray-800 p-4 space-y-3">
          {isLoading && <p className="text-sm text-gray-500">Analysing images…</p>}
          {!isLoading && unusedImages.length === 0 && (
            <p className="text-sm text-gray-500">No unused images found.</p>
          )}
          {unusedImages.length > 0 && (
            <>
              <div className="flex items-center justify-between mb-2">
                <button onClick={() => setSelected(Object.fromEntries(unusedImages.map(i => [i.id, true])))}
                  className="text-xs text-brand-400 hover:text-brand-300">Select all</button>
                <button onClick={() => setSelected({})} className="text-xs text-gray-500 hover:text-gray-300">Clear</button>
              </div>
              <div className="space-y-1 max-h-64 overflow-y-auto">
                {unusedImages.map(img => (
                  <label key={img.id} className="flex items-center gap-3 px-3 py-2 rounded-lg hover:bg-gray-800/60 cursor-pointer">
                    <input type="checkbox" checked={!!selected[img.id]} onChange={e => setSelected(s => ({ ...s, [img.id]: e.target.checked }))}
                      className="rounded border-gray-600 bg-gray-700 text-brand-500 focus:ring-brand-500" />
                    <div className="flex-1 min-w-0">
                      <span className="text-sm text-white font-mono">{img.repository}:{img.tag}</span>
                    </div>
                    <span className="text-xs text-gray-500 shrink-0">{img.size}</span>
                    <span className="text-xs text-gray-600 shrink-0 w-28 text-right truncate">{img.created?.slice(0, 10)}</span>
                  </label>
                ))}
              </div>
              <div className="flex items-center justify-between pt-2 border-t border-gray-800">
                <p className="text-xs text-gray-400">
                  {selectedIDs.length} selected · {fmtBytes(selectedSize)} to free
                </p>
                <button
                  onClick={() => purgeMut.mutate()}
                  disabled={selectedIDs.length === 0 || purgeMut.isPending}
                  className="px-4 py-2 bg-red-700 hover:bg-red-600 disabled:opacity-40 text-white text-xs font-semibold rounded-lg transition-colors"
                >
                  {purgeMut.isPending ? 'Purging…' : 'Approve & Purge Images'}
                </button>
              </div>
            </>
          )}
          {output && <OutputModal title="Image Purge Output" output={output} onClose={() => setOutput(null)} />}
        </div>
      )}
    </div>
  )
}

// ── 2b: Stopped Containers ────────────────────────────────────────────────────
function StoppedContainersSection() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [confirm, setConfirm] = useState('')
  const [output, setOutput] = useState(null)
  const { data: containers = [], isLoading, refetch } = useQuery({
    queryKey: ['hk-containers'], queryFn: fetchStoppedContainers, enabled: open,
  })
  const purgeMut = useMutation({
    mutationFn: pruneContainers,
    onSuccess: (d) => { setOutput(d.output); qc.invalidateQueries({ queryKey: ['hk-status', 'hk-containers'] }) },
  })

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
      <button
        className="w-full flex items-center justify-between p-4 hover:bg-gray-800/40 transition-colors"
        onClick={() => { setOpen(o => !o); if (!open) refetch() }}
      >
        <div className="flex items-center gap-3">
          <span className="text-xl">📦</span>
          <div className="text-left">
            <p className="text-sm font-semibold text-white">Stopped Container Removal</p>
            <p className="text-xs text-gray-500">Remove exited/dead containers from the namespace</p>
          </div>
        </div>
        <span className="text-xs bg-amber-900/50 text-amber-300 px-2 py-0.5 rounded-full">Approval Required</span>
      </button>

      {open && (
        <div className="border-t border-gray-800 p-4 space-y-3">
          {isLoading && <p className="text-sm text-gray-500">Loading containers…</p>}
          {!isLoading && containers.length === 0 && <p className="text-sm text-gray-500">No stopped containers found.</p>}
          {containers.length > 0 && (
            <>
              <div className="flex items-center gap-2 px-3 py-2 bg-amber-950/40 border border-amber-800/50 rounded-lg">
                <span className="text-amber-400">⚠</span>
                <p className="text-xs text-amber-300">Warning: Overwritten runtime states cannot be recovered.</p>
              </div>
              <div className="overflow-x-auto rounded-lg border border-gray-800">
                <table className="w-full text-xs">
                  <thead className="bg-gray-800/60">
                    <tr>
                      {['ID', 'Name', 'Image', 'Exit Code', 'Stopped'].map(h => (
                        <th key={h} className="px-3 py-2 text-left text-gray-400 font-medium">{h}</th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-gray-800">
                    {containers.map((c, i) => (
                      <tr key={i} className="hover:bg-gray-800/40">
                        <td className="px-3 py-2 font-mono text-gray-500">{c.id}</td>
                        <td className="px-3 py-2 text-white">{c.name}</td>
                        <td className="px-3 py-2 text-gray-400 truncate max-w-32">{c.image}</td>
                        <td className="px-3 py-2">
                          <span className={c.exit_code === '0' ? 'text-green-400' : 'text-red-400'}>{c.exit_code || '—'}</span>
                        </td>
                        <td className="px-3 py-2 text-gray-500">{c.finished_at}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <div className="pt-2 border-t border-gray-800 space-y-3">
                <p className="text-xs text-gray-400">Type <code className="font-mono bg-gray-800 px-1 rounded">PRUNE</code> to confirm removal of all {containers.length} stopped container(s):</p>
                <input
                  type="text" value={confirm} onChange={e => setConfirm(e.target.value)}
                  placeholder="PRUNE"
                  className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm font-mono focus:outline-none focus:border-red-500"
                />
                <button
                  onClick={() => purgeMut.mutate()}
                  disabled={confirm !== 'PRUNE' || purgeMut.isPending}
                  className="px-4 py-2 bg-red-700 hover:bg-red-600 disabled:opacity-40 text-white text-xs font-semibold rounded-lg transition-colors"
                >
                  {purgeMut.isPending ? 'Removing…' : `Remove ${containers.length} container(s)`}
                </button>
              </div>
            </>
          )}
          {output && <OutputModal title="Container Prune Output" output={output} onClose={() => setOutput(null)} />}
        </div>
      )}
    </div>
  )
}

// ── 2c: Volume Purging (CRITICAL) ─────────────────────────────────────────────
function VolumePurgingSection() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [toggled, setToggled] = useState({})
  const [holdProgress, setHoldProgress] = useState(0)
  const holdTimer = useRef(null)
  const [output, setOutput] = useState(null)
  const { data: volumes = [], isLoading, refetch } = useQuery({
    queryKey: ['hk-volumes'], queryFn: fetchDanglingVolumes, enabled: open,
  })
  const selectedNames = volumes.filter(v => toggled[v.name]).map(v => v.name)

  const purgeMut = useMutation({
    mutationFn: () => pruneVolumes({ volume_names: selectedNames }),
    onSuccess: (d) => { setOutput(d.output); qc.invalidateQueries({ queryKey: ['hk-status', 'hk-volumes'] }) },
  })

  function startHold() {
    setHoldProgress(0)
    const start = Date.now()
    holdTimer.current = setInterval(() => {
      const pct = Math.min(100, ((Date.now() - start) / 3000) * 100)
      setHoldProgress(pct)
      if (pct >= 100) {
        clearInterval(holdTimer.current)
        purgeMut.mutate()
      }
    }, 50)
  }
  function stopHold() {
    clearInterval(holdTimer.current)
    if (holdProgress < 100) setHoldProgress(0)
  }
  useEffect(() => () => clearInterval(holdTimer.current), [])

  return (
    <div className="bg-red-950/20 border-2 border-red-800/60 rounded-xl overflow-hidden">
      <button
        className="w-full flex items-center justify-between p-4 hover:bg-red-950/30 transition-colors"
        onClick={() => { setOpen(o => !o); if (!open) refetch() }}
      >
        <div className="flex items-center gap-3">
          <span className="text-xl">💾</span>
          <div className="text-left">
            <p className="text-sm font-semibold text-white">Volume Purging</p>
            <p className="text-xs text-red-400">⚠ CRITICAL RISK — Irreversible data destruction</p>
          </div>
        </div>
        <span className="text-xs bg-red-900/60 text-red-300 px-2 py-0.5 rounded-full border border-red-800/50">Critical Risk</span>
      </button>

      {open && (
        <div className="border-t border-red-800/60 p-4 space-y-4">
          {isLoading && <p className="text-sm text-gray-500">Loading dangling volumes…</p>}
          {!isLoading && volumes.length === 0 && <p className="text-sm text-gray-500">No dangling volumes found.</p>}
          {volumes.length > 0 && (
            <>
              <div className="px-3 py-2.5 bg-red-950/60 border border-red-800/60 rounded-lg">
                <p className="text-xs text-red-300 font-semibold">
                  ⚠ WARNING: Volumes contain application data. Deletion is permanent and cannot be undone. Only remove volumes you are certain are abandoned.
                </p>
              </div>
              <div className="space-y-2">
                {volumes.map(v => (
                  <div key={v.name} className="flex items-center gap-3 p-3 bg-gray-900 border border-gray-800 rounded-lg">
                    <button
                      type="button" onClick={() => setToggled(t => ({ ...t, [v.name]: !t[v.name] }))}
                      className={`relative w-10 h-5 rounded-full transition-colors shrink-0 ${toggled[v.name] ? 'bg-red-600' : 'bg-gray-700'}`}
                    >
                      <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${toggled[v.name] ? 'translate-x-5' : ''}`} />
                    </button>
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-mono text-white truncate">{v.name}</p>
                      <p className="text-xs text-gray-500">{v.driver} · {v.mount_point || 'local'}</p>
                    </div>
                  </div>
                ))}
              </div>

              {selectedNames.length > 0 && (
                <div className="pt-2 border-t border-red-800/40">
                  <p className="text-xs text-red-400 mb-3">
                    {selectedNames.length} volume(s) selected for destruction. Hold the button for 3 seconds to authorize.
                  </p>
                  <div className="relative">
                    <button
                      onMouseDown={startHold} onMouseUp={stopHold} onMouseLeave={stopHold}
                      onTouchStart={startHold} onTouchEnd={stopHold}
                      disabled={purgeMut.isPending}
                      className="w-full py-3 bg-red-800 hover:bg-red-700 disabled:opacity-50 text-white text-sm font-semibold rounded-lg transition-colors relative overflow-hidden select-none"
                    >
                      <div
                        className="absolute inset-y-0 left-0 bg-red-600/60 transition-all"
                        style={{ width: `${holdProgress}%` }}
                      />
                      <span className="relative z-10">
                        {purgeMut.isPending ? 'Destroying…' :
                         holdProgress > 0 ? `Hold… ${Math.ceil((100 - holdProgress) / 33)}s` :
                         '⚠ Authorize Irreversible Volume Destruction'}
                      </span>
                    </button>
                  </div>
                </div>
              )}
            </>
          )}
          {output && <OutputModal title="Volume Purge Output" output={output} onClose={() => setOutput(null)} />}
        </div>
      )}
    </div>
  )
}

// ── 2d: Build Cache ───────────────────────────────────────────────────────────
function BuildCacheSection({ docker }) {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [sliderUnlocked, setSliderUnlocked] = useState(false)
  const [output, setOutput] = useState(null)
  const buildCacheBytes = docker?.build_cache?.size_bytes || 0
  const imagesBytes = docker?.images?.size_bytes || 0
  const volumesBytes = docker?.volumes?.size_bytes || 0
  const total = buildCacheBytes + imagesBytes + volumesBytes || 1

  const purgeMut = useMutation({
    mutationFn: pruneBuildCache,
    onSuccess: (d) => { setOutput(d.output); qc.invalidateQueries({ queryKey: ['hk-status'] }) },
  })

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
      <button
        className="w-full flex items-center justify-between p-4 hover:bg-gray-800/40 transition-colors"
        onClick={() => setOpen(o => !o)}
      >
        <div className="flex items-center gap-3">
          <span className="text-xl">⚙</span>
          <div className="text-left">
            <p className="text-sm font-semibold text-white">System-Wide Cache & Build Overhaul</p>
            <p className="text-xs text-gray-500">Reclaim BuildKit cache — slows next build but frees large disk space</p>
          </div>
        </div>
        <span className="text-xs bg-amber-900/50 text-amber-300 px-2 py-0.5 rounded-full">Approval Required</span>
      </button>

      {open && (
        <div className="border-t border-gray-800 p-4 space-y-4">
          {/* Visual disk breakdown */}
          <div className="space-y-2">
            <DiskBar label="Build Cache" used={buildCacheBytes} total={total} color="bg-cyan-500" />
            <DiskBar label="Images" used={imagesBytes} total={total} color="bg-blue-500" />
            <DiskBar label="Volumes" used={volumesBytes} total={total} color="bg-amber-500" />
          </div>

          <div className="px-3 py-2.5 bg-amber-950/40 border border-amber-800/50 rounded-lg">
            <p className="text-xs text-amber-300">
              Reclaim Build Caches? This will slow down the next build compilation but free up massive disk space.
              Build cache: <strong>{fmtBytes(buildCacheBytes)}</strong>
            </p>
          </div>

          {/* Slider unlock */}
          <div className="space-y-2">
            <p className="text-xs text-gray-400">Slide to unlock the clean action:</p>
            <input
              type="range" min="0" max="100"
              value={sliderUnlocked ? 100 : 0}
              onChange={e => setSliderUnlocked(parseInt(e.target.value) === 100)}
              className="w-full accent-brand-500"
            />
            <div className="flex justify-between text-xs text-gray-600">
              <span>Locked</span><span>Unlocked ✓</span>
            </div>
          </div>

          <button
            onClick={() => purgeMut.mutate()}
            disabled={!sliderUnlocked || purgeMut.isPending}
            className="w-full py-2.5 bg-amber-700 hover:bg-amber-600 disabled:opacity-40 text-white text-sm font-semibold rounded-lg transition-colors"
          >
            {purgeMut.isPending ? 'Cleaning…' : 'Execute Global System Clean'}
          </button>

          {output && <OutputModal title="Build Cache Purge Output" output={output} onClose={() => setOutput(null)} />}
        </div>
      )}
    </div>
  )
}

// ── 2e: Kernel Cleanup ────────────────────────────────────────────────────────
function KernelCleanupSection() {
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const [selected, setSelected] = useState({})
  const [confirmStep, setConfirmStep] = useState(0) // 0=none, 1=first, 2=confirmed
  const [output, setOutput] = useState(null)
  const { data: kernelData, isLoading, refetch } = useQuery({
    queryKey: ['hk-kernels'], queryFn: fetchKernels, enabled: open,
  })
  const kernels = kernelData?.kernels || []
  const selectedPkgs = kernels.filter(k => selected[k.package] && !k.locked).map(k => k.package)

  const cleanMut = useMutation({
    mutationFn: () => cleanKernels({ packages: selectedPkgs }),
    onSuccess: (d) => { setOutput(d.output); qc.invalidateQueries({ queryKey: ['hk-kernels'] }) },
  })

  if (!kernelData?.available && !isLoading && open) {
    return (
      <div className="bg-gray-900 border border-gray-800 rounded-xl p-4">
        <p className="text-xs text-gray-500">Kernel cleanup requires host OS access (privileged mode). See the Automation tab for setup instructions.</p>
      </div>
    )
  }

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl overflow-hidden">
      <button
        className="w-full flex items-center justify-between p-4 hover:bg-gray-800/40 transition-colors"
        onClick={() => { setOpen(o => !o); if (!open) refetch() }}
      >
        <div className="flex items-center gap-3">
          <span className="text-xl">🐧</span>
          <div className="text-left">
            <p className="text-sm font-semibold text-white">Old Kernel Cleanup</p>
            <p className="text-xs text-gray-500">Remove obsolete kernel images to free /boot space</p>
          </div>
        </div>
        <span className="text-xs bg-amber-900/50 text-amber-300 px-2 py-0.5 rounded-full">Approval Required</span>
      </button>

      {open && (
        <div className="border-t border-gray-800 p-4 space-y-3">
          {isLoading && <p className="text-sm text-gray-500">Loading kernels…</p>}
          {!isLoading && kernels.length === 0 && <p className="text-sm text-gray-500">No kernel information available.</p>}
          {kernels.length > 0 && (
            <>
              <p className="text-xs text-gray-400">Active kernel: <code className="font-mono text-brand-400">{kernelData.active}</code></p>
              <div className="space-y-1">
                {kernels.map(k => (
                  <div key={k.package} className={`flex items-center gap-3 p-2.5 rounded-lg border ${k.locked ? 'border-gray-700/40 bg-gray-800/20' : 'border-gray-700 bg-gray-800/40'}`}>
                    {k.locked ? (
                      <span className="text-gray-600 text-sm shrink-0">🔒</span>
                    ) : (
                      <input type="checkbox" checked={!!selected[k.package]}
                        onChange={e => setSelected(s => ({ ...s, [k.package]: e.target.checked }))}
                        className="rounded border-gray-600 bg-gray-700 text-brand-500 focus:ring-brand-500" />
                    )}
                    <div className="flex-1">
                      <p className={`text-xs font-mono ${k.locked ? 'text-gray-500' : 'text-white'}`}>{k.package}</p>
                      {k.active && <span className="text-xs text-green-400">Active (locked)</span>}
                      {k.locked && !k.active && <span className="text-xs text-gray-500">Previous backup (locked)</span>}
                    </div>
                  </div>
                ))}
              </div>
              {selectedPkgs.length > 0 && confirmStep === 0 && (
                <button onClick={() => setConfirmStep(1)}
                  className="px-4 py-2 bg-amber-700 hover:bg-amber-600 text-white text-xs font-semibold rounded-lg">
                  Remove {selectedPkgs.length} kernel(s)
                </button>
              )}
              {confirmStep === 1 && (
                <div className="space-y-2 p-3 bg-amber-950/40 border border-amber-800/50 rounded-lg">
                  <p className="text-xs text-amber-300">This will permanently remove: {selectedPkgs.join(', ')}</p>
                  <div className="flex gap-2">
                    <button onClick={() => setConfirmStep(0)} className="px-3 py-1.5 text-xs bg-gray-700 hover:bg-gray-600 text-white rounded-lg">Cancel</button>
                    <button onClick={() => { setConfirmStep(2); cleanMut.mutate() }}
                      disabled={cleanMut.isPending}
                      className="px-3 py-1.5 text-xs bg-red-700 hover:bg-red-600 text-white rounded-lg disabled:opacity-50">
                      {cleanMut.isPending ? 'Removing…' : 'Confirm Removal'}
                    </button>
                  </div>
                </div>
              )}
            </>
          )}
          {output && <OutputModal title="Kernel Cleanup Output" output={output} onClose={() => setOutput(null)} />}
        </div>
      )}
    </div>
  )
}

function SafetyCenterTab({ docker }) {
  return (
    <div className="space-y-4">
      <div className="px-4 py-3 bg-gray-900 border border-gray-800 rounded-xl">
        <p className="text-sm text-gray-400">
          Each item below requires explicit approval before execution. Expand a card to review what will be deleted and authorize the action.
        </p>
      </div>
      <UnusedImagesSection />
      <StoppedContainersSection />
      <VolumePurgingSection />
      <BuildCacheSection docker={docker} />
      <KernelCleanupSection />
    </div>
  )
}

// ── Tab 3: Automation & Logs ──────────────────────────────────────────────────

function AutomationTab({ hostPrivileged }) {
  const qc = useQueryClient()
  const { data: log = [] } = useQuery({ queryKey: ['hk-log'], queryFn: fetchHousekeepingLog, refetchInterval: 30_000 })
  const [journalCfg, setJournalCfg] = useState({ max_age_days: 14, max_size_gb: 2 })
  const [tmpCfg, setTmpCfg] = useState({ max_age_days: 7, exclude: '' })
  const [output, setOutput] = useState(null)
  const [selectedLog, setSelectedLog] = useState(null)

  const aptMut    = useMutation({ mutationFn: aptClean, onSuccess: (d) => { setOutput(d.output); qc.invalidateQueries({ queryKey: ['hk-log'] }) } })
  const jrnlMut   = useMutation({ mutationFn: () => journalVacuum(journalCfg), onSuccess: (d) => { setOutput(d.output); qc.invalidateQueries({ queryKey: ['hk-log'] }) } })
  const tmpMut    = useMutation({
    mutationFn: () => cleanTmp({ max_age_days: tmpCfg.max_age_days, exclude: tmpCfg.exclude.split(',').map(s => s.trim()).filter(Boolean) }),
    onSuccess: (d) => { setOutput(d.output); qc.invalidateQueries({ queryKey: ['hk-log'] }) },
  })

  return (
    <div className="space-y-6">
      {!hostPrivileged && (
        <div className="px-4 py-3 bg-amber-950/40 border border-amber-800/50 rounded-xl">
          <p className="text-xs text-amber-300 font-semibold mb-1">Host OS operations require privileged mode</p>
          <p className="text-xs text-amber-400">
            Add the following to <code className="font-mono bg-amber-900/40 px-1 rounded">src/docker-compose.yml</code> under the <code className="font-mono bg-amber-900/40 px-1 rounded">rigger</code> service:
          </p>
          <pre className="text-xs text-amber-300 font-mono mt-2 bg-amber-950/60 rounded p-2">
{`    privileged: true
    pid: host`}
          </pre>
        </div>
      )}

      {/* Automated tasks summary */}
      <div>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-3">Automated Tasks (Daily at 03:00 UTC)</h2>
        <div className="grid grid-cols-2 gap-3">
          {[
            { name: 'prune-networks', label: 'Network Cleanup', desc: 'docker network prune -f' },
            { name: 'prune-dangling-images', label: 'Dangling Image Prune', desc: 'docker image prune -f' },
          ].map(task => {
            const last = log.find(l => l.task === task.name)
            return (
              <div key={task.name} className="p-4 bg-gray-900 border border-gray-800 rounded-xl">
                <div className="flex items-center justify-between mb-1">
                  <p className="text-sm font-semibold text-white">{task.label}</p>
                  <span className="text-xs text-green-400">Auto</span>
                </div>
                <p className="text-xs text-gray-500 font-mono mb-2">{task.desc}</p>
                {last
                  ? <p className="text-xs text-gray-600">Last run: {timeAgo(last.created_at)} · {last.status}</p>
                  : <p className="text-xs text-gray-600">Not yet run</p>
                }
              </div>
            )
          })}
        </div>
      </div>

      {/* APT config */}
      <div className="p-4 bg-gray-900 border border-gray-800 rounded-xl space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-sm font-semibold text-white">APT Package Cache Cleanup</h3>
            <p className="text-xs text-gray-500 mt-0.5">apt-get autoremove && apt-get clean</p>
          </div>
          <span className="text-xs text-green-400 bg-green-900/30 px-2 py-0.5 rounded-full">Auto-safe</span>
        </div>
        <button onClick={() => aptMut.mutate()} disabled={aptMut.isPending}
          className="px-4 py-2 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-xs font-semibold rounded-lg transition-colors">
          {aptMut.isPending ? 'Running…' : 'Run Now'}
        </button>
        {aptMut.isSuccess && <p className="text-xs text-green-400">✓ Completed</p>}
      </div>

      {/* Journal config */}
      <div className="p-4 bg-gray-900 border border-gray-800 rounded-xl space-y-3">
        <div>
          <h3 className="text-sm font-semibold text-white">Systemd Journal Rotation</h3>
          <p className="text-xs text-gray-500 mt-0.5">journalctl --vacuum-time or --vacuum-size</p>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs text-gray-400 mb-1">Max Age (days)</label>
            <input type="number" value={journalCfg.max_age_days}
              onChange={e => setJournalCfg(c => ({ ...c, max_age_days: parseInt(e.target.value) || 14 }))}
              className="w-full px-3 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500" />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Max Size (GB)</label>
            <input type="number" value={journalCfg.max_size_gb}
              onChange={e => setJournalCfg(c => ({ ...c, max_size_gb: parseInt(e.target.value) || 2 }))}
              className="w-full px-3 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500" />
          </div>
        </div>
        <button onClick={() => jrnlMut.mutate()} disabled={jrnlMut.isPending}
          className="px-4 py-2 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-xs font-semibold rounded-lg transition-colors">
          {jrnlMut.isPending ? 'Running…' : 'Apply Vacuum'}
        </button>
      </div>

      {/* Temp cleanup config */}
      <div className="p-4 bg-gray-900 border border-gray-800 rounded-xl space-y-3">
        <div>
          <h3 className="text-sm font-semibold text-white">Temporary Directory Cleanup</h3>
          <p className="text-xs text-gray-500 mt-0.5">find /tmp -type f -atime +N -delete</p>
        </div>
        <div className="grid grid-cols-2 gap-3">
          <div>
            <label className="block text-xs text-gray-400 mb-1">Max Age (days unaccessed)</label>
            <input type="number" value={tmpCfg.max_age_days}
              onChange={e => setTmpCfg(c => ({ ...c, max_age_days: parseInt(e.target.value) || 7 }))}
              className="w-full px-3 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500" />
          </div>
          <div>
            <label className="block text-xs text-gray-400 mb-1">Exclude patterns (comma-separated)</label>
            <input type="text" value={tmpCfg.exclude} onChange={e => setTmpCfg(c => ({ ...c, exclude: e.target.value }))}
              placeholder="*.sock, *.lock"
              className="w-full px-3 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500" />
          </div>
        </div>
        <button onClick={() => tmpMut.mutate()} disabled={tmpMut.isPending}
          className="px-4 py-2 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-xs font-semibold rounded-lg transition-colors">
          {tmpMut.isPending ? 'Cleaning…' : 'Clean /tmp'}
        </button>
      </div>

      {/* Log table */}
      <div>
        <h2 className="text-sm font-semibold text-gray-400 uppercase tracking-wider mb-3">Task History</h2>
        {log.length === 0
          ? <p className="text-sm text-gray-500 text-center py-8">No housekeeping tasks recorded yet.</p>
          : (
            <div className="border border-gray-800 rounded-xl overflow-hidden">
              <table className="w-full text-xs">
                <thead className="bg-gray-800/60">
                  <tr>
                    {['Task', 'Trigger', 'Status', 'Freed', 'Run At'].map(h => (
                      <th key={h} className="px-3 py-2 text-left text-gray-400 font-medium">{h}</th>
                    ))}
                  </tr>
                </thead>
                <tbody className="divide-y divide-gray-800">
                  {log.map((entry, i) => (
                    <tr key={i} className="hover:bg-gray-800/40 cursor-pointer" onClick={() => setSelectedLog(entry)}>
                      <td className="px-3 py-2 font-mono text-gray-300">{entry.task}</td>
                      <td className="px-3 py-2">
                        <span className={`px-1.5 py-0.5 rounded text-xs ${entry.trigger === 'cron' ? 'bg-gray-700 text-gray-400' : 'bg-brand-900/50 text-brand-400'}`}>
                          {entry.trigger}
                        </span>
                      </td>
                      <td className="px-3 py-2">
                        <span className={entry.status === 'ok' ? 'text-green-400' : 'text-red-400'}>
                          {entry.status === 'ok' ? '✓' : '✕'} {entry.status}
                        </span>
                      </td>
                      <td className="px-3 py-2 text-gray-500">{entry.freed_bytes > 0 ? fmtBytes(entry.freed_bytes) : '—'}</td>
                      <td className="px-3 py-2 text-gray-500">{timeAgo(entry.created_at)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )
        }
      </div>

      {output && <OutputModal title="Command Output" output={output} onClose={() => setOutput(null)} />}
      {selectedLog && (
        <OutputModal
          title={`${selectedLog.task} · ${selectedLog.created_at}`}
          output={selectedLog.output}
          onClose={() => setSelectedLog(null)}
        />
      )}
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

const TABS = [
  { id: 'dashboard', label: '📊 Dashboard' },
  { id: 'safety',    label: '🛡 Safety Center' },
  { id: 'migrations', label: '🚚 Migration Leftovers' },
  { id: 'automation', label: '⚙ Automation & Logs' },
]

// MigrationLeftoversTab lists data/files left on source hosts after environment
// migrations and lets the user permanently wipe them (e.g. before decommissioning).
function MigrationLeftoversTab() {
  const qc = useQueryClient()
  const { data: items = [], isLoading } = useQuery({
    queryKey: ['migration-leftovers'], queryFn: fetchMigrationLeftovers,
  })
  const [confirm, setConfirm] = useState(null) // leftover pending cleanup confirmation
  const [busy, setBusy] = useState(null)       // id being cleaned
  const [log, setLog] = useState('')

  const dismiss = useMutation({
    mutationFn: (id) => dismissMigrationLeftover(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['migration-leftovers'] }),
  })

  async function doClean() {
    const it = confirm
    setConfirm(null); setBusy(it.id); setLog('')
    try {
      await cleanMigrationLeftover(it.id, (chunk) => setLog(l => l + chunk))
      qc.invalidateQueries({ queryKey: ['migration-leftovers'] })
    } catch (e) {
      setLog(l => l + `\n✗ ${e.message || 'cleanup failed'}\n`)
    }
    setBusy(null)
  }

  if (isLoading) return <div className="py-12 text-center text-gray-500 text-sm">Loading…</div>

  return (
    <div>
      <p className="text-sm text-gray-500 mb-4">
        When an environment is migrated to another host, its data, volumes and files (including
        <code className="font-mono text-xs"> .env</code> secrets) are left on the <strong>source</strong> host.
        Wipe them here before decommissioning a host so they can't be recovered by whoever gets the machine.
      </p>

      {items.length === 0 ? (
        <div className="py-12 text-center text-gray-500 text-sm border border-gray-800 rounded-xl">
          No migration leftovers — nothing to clean up.
        </div>
      ) : (
        <div className="space-y-2">
          {items.map(it => (
            <div key={it.id} className="flex items-center gap-4 p-4 bg-gray-900 border border-gray-800 rounded-xl">
              <div className="flex-1 min-w-0">
                <p className="text-sm font-semibold text-white">{it.workspace} / {it.env}</p>
                <p className="text-xs text-gray-500 mt-0.5">
                  left on <strong className="text-gray-400">{it.host_name || (it.host_id === 0 ? 'local control plane' : `host #${it.host_id}`)}</strong>
                  {' · '}stack <code className="font-mono">{it.stack}</code>
                </p>
              </div>
              <button
                onClick={() => dismiss.mutate(it.id)}
                disabled={busy !== null}
                className="shrink-0 px-3 py-2 text-sm text-gray-400 hover:text-gray-200 rounded-lg transition-colors disabled:opacity-40"
              >
                Dismiss
              </button>
              <button
                onClick={() => setConfirm(it)}
                disabled={busy !== null}
                className="shrink-0 px-3 py-2 bg-red-900/60 hover:bg-red-800/80 text-red-300 hover:text-red-200 text-sm font-medium rounded-lg border border-red-800/50 transition-colors disabled:opacity-40"
              >
                {busy === it.id ? 'Cleaning…' : 'Clean up'}
              </button>
            </div>
          ))}
        </div>
      )}

      {log && (
        <pre className="mt-4 max-h-72 overflow-auto bg-gray-950 border border-gray-800 rounded-lg p-3 text-xs text-gray-300 whitespace-pre-wrap">{log}</pre>
      )}

      {confirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm" onClick={() => setConfirm(null)}>
          <div className="bg-gray-900 border border-red-900/60 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <div className="flex items-center gap-3"><span className="text-2xl">🗑️</span><h3 className="font-semibold text-white">Permanently wipe leftover?</h3></div>
            <p className="text-sm text-gray-300">
              This removes the containers, volumes and files for <strong className="text-white">{confirm.workspace} / {confirm.env}</strong> on{' '}
              <strong className="text-white">{confirm.host_name || (confirm.host_id === 0 ? 'local control plane' : `host #${confirm.host_id}`)}</strong>, including
              <code className="font-mono text-xs"> .env</code> secrets. <strong className="text-red-300 block mt-1">This cannot be undone.</strong>
            </p>
            <div className="flex gap-3">
              <button onClick={doClean} className="flex-1 bg-red-700 hover:bg-red-600 text-white text-sm font-semibold py-2 rounded-lg transition-colors">Wipe permanently</button>
              <button onClick={() => setConfirm(null)} className="px-4 py-2 bg-gray-800 hover:bg-gray-700 text-gray-300 text-sm rounded-lg transition-colors">Cancel</button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

export default function HousekeepingPage() {
  const [tab, setTab] = useState('dashboard')
  const { data: status, isLoading } = useQuery({
    queryKey: ['hk-status'], queryFn: fetchHousekeepingStatus, refetchInterval: 60_000,
  })

  return (
    <Layout>
      <div className="max-w-4xl mx-auto px-6 py-8">
        <div className="mb-6 flex items-start justify-between">
          <div>
            <h1 className="text-xl font-bold text-white">Housekeeping</h1>
            <p className="text-sm text-gray-500 mt-0.5">Docker and host OS maintenance — automated and approval-gated.</p>
          </div>
          {!isLoading && status && <StatusBadge status={status.health_status} />}
        </div>

        {/* Tab bar */}
        <div className="flex gap-1 border-b border-gray-800 mb-6">
          {TABS.map(t => (
            <button key={t.id} onClick={() => setTab(t.id)}
              className={`px-4 py-2.5 text-sm font-medium border-b-2 transition-colors -mb-px ${
                tab === t.id ? 'border-brand-500 text-brand-400' : 'border-transparent text-gray-500 hover:text-gray-300'
              }`}
            >
              {t.label}
            </button>
          ))}
        </div>

        {isLoading ? (
          <div className="py-16 text-center text-gray-500">Loading system status…</div>
        ) : (
          <>
            {tab === 'dashboard'   && <DashboardTab status={status} />}
            {tab === 'safety'      && <SafetyCenterTab docker={status?.docker} />}
            {tab === 'migrations'  && <MigrationLeftoversTab />}
            {tab === 'automation'  && <AutomationTab hostPrivileged={status?.host_privileged} />}
          </>
        )}
      </div>
    </Layout>
  )
}
