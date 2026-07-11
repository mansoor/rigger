import { useQuery } from '@tanstack/react-query'
import { fetchHostStats, fetchWorkspaceHostStats } from '../lib/api'
import HostComponents from './HostComponents'

// HostHealthModal is the single per-host detail view used by BOTH admin Settings →
// Remote Hosts and Workspace Settings → Remote Hosts. It shows the provisionable
// Components panel (Docker / Nixpacks / edge / workspaces dir) plus live Docker &
// system health. Pass `workspace` for a workspace-pool host, or omit for the admin
// (global) view — the two surfaces then render identically.
export default function HostHealthModal({ host, workspace = null, onClose }) {
  const ws = workspace
  const { data, isLoading, error } = useQuery({
    queryKey: ['host-stats', ws || 'admin', host.id],
    queryFn: () => ws ? fetchWorkspaceHostStats(ws, host.id) : fetchHostStats(host.id),
    refetchInterval: 5000,
  })
  const failed = error || data?.status === 'error'
  const d = data?.docker || {}
  const hs = data?.host || {}
  const fmtUptime = (s) => {
    if (!s) return '—'
    const days = Math.floor(s / 86400), hrs = Math.floor((s % 86400) / 3600)
    return days > 0 ? `${days}d ${hrs}h` : `${hrs}h ${Math.floor((s % 3600) / 60)}m`
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-5">
          <h3 className="font-semibold text-content-strong">Host · {host.name}</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>

        <div className="space-y-5">
          <HostComponents hostId={host.id} workspace={ws} />

          <div>
            <p className="text-xs font-semibold text-content-muted uppercase tracking-wider mb-2">Health</p>
            {isLoading && <div className="py-6 text-center text-content-subtle text-sm">Loading…</div>}
            {!isLoading && failed && (
              <div className="py-3 px-4 bg-red-500/10 border border-danger/30 rounded-lg text-sm text-danger-fg">
                {data?.error || error?.response?.data?.error || 'Failed to reach host'}
              </div>
            )}
            {!isLoading && !failed && (
              <div className="space-y-4">
                {d.error ? (
                  <p className="text-sm text-danger-fg">{d.error}</p>
                ) : (
                  <div className="grid grid-cols-2 gap-3">
                    <StatCell label="Containers" value={`${d.containers_running || 0} up · ${d.containers_stopped || 0} stopped`} />
                    <StatCell label="Images" value={d.images_total ?? 0} />
                    <StatCell label="Storage driver" value={d.storage_driver || '—'} />
                    <StatCell label="Networks" value={d.networks_total ?? 0} />
                  </div>
                )}
                <div className="grid grid-cols-2 gap-3">
                  <StatCell label="OS" value={hs.os || '—'} />
                  <StatCell label="Arch · CPUs" value={`${hs.arch || '—'} · ${hs.cpus || 0}`} />
                  <StatCell label="Memory" value={`${(hs.mem_used_pct || 0).toFixed(0)}% of ${(hs.mem_total_mb / 1024 || 0).toFixed(1)} GB`} />
                  <StatCell label="Disk" value={`${(hs.disk_used_pct || 0).toFixed(0)}% of ${(hs.disk_total_gb || 0).toFixed(0)} GB`} />
                  <StatCell label="Uptime" value={fmtUptime(hs.uptime_seconds)} />
                </div>
              </div>
            )}
          </div>

          <div className="flex justify-end">
            <button onClick={onClose} className="px-3 py-1.5 text-sm rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised">Close</button>
          </div>
        </div>
      </div>
    </div>
  )
}

function StatCell({ label, value }) {
  return (
    <div className="bg-canvas/50 border border-border rounded-lg px-3 py-2">
      <p className="text-[11px] text-content-subtle uppercase tracking-wider">{label}</p>
      <p className="text-sm text-content-strong mt-0.5 truncate" title={String(value)}>{value}</p>
    </div>
  )
}
