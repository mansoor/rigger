import { useEffect, useRef, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { fetchStats, fetchEnvStatus, fetchAlertSummary, fetchLiveStats, fetchBackupCoverage } from '../lib/api'
import Layout from '../components/Layout'

// Aggregate the live per-project stats down to one workspace. Containers (running
// and total) and resource usage sum across envs; services is the distinct count of
// compose service names unioned across envs (each env shares the same template).
function aggregateLive(ws, live) {
  let running = 0, total = 0, cpu = 0, mem = 0, net = 0
  const svc = new Set()
  for (const env of (ws.envs || [])) {
    const p = live[`${ws.name}_${env}`]
    if (!p) continue
    running += p.running || 0
    total   += p.total || 0
    for (const s of (p.service_names || [])) svc.add(s)
    cpu     += p.cpu_pct || 0
    mem     += p.mem_mb || 0
    net     += (p.net_rx_bytes || 0) + (p.net_tx_bytes || 0)
  }
  return { running, total, services: svc.size, cpu, mem, net }
}

function fmtMB(mb) {
  const v = Number(mb) || 0
  if (v <= 0) return <span className="text-content-faint">—</span>
  return v >= 1024 ? `${(v / 1024).toFixed(1)} GB` : `${v.toFixed(v < 10 ? 1 : 0)} MB`
}

function fmtRate(bps) {
  const v = Number(bps) || 0
  if (v <= 0) return <span className="text-content-faint">0 B/s</span>
  const u = ['B', 'KB', 'MB', 'GB']
  const i = Math.min(Math.floor(Math.log(v) / Math.log(1024)), u.length - 1)
  return `${(v / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${u[i]}/s`
}

// Per-workspace alert styling for dashboard row highlights (Phase 6e).
function wsAlertStyle(wa) {
  if (!wa || wa.total === 0) return { row: '', cell: '', badge: null }
  const label = `${wa.total} alert${wa.total > 1 ? 's' : ''}`
  if (wa.critical > 0) {
    return { row: 'bg-red-500/5', cell: 'border-l-2 border-danger',
      badge: { cls: 'bg-red-500/15 text-danger-fg border-danger-border/50', text: label } }
  }
  return { row: 'bg-amber-500/5', cell: 'border-l-2 border-warning',
    badge: { cls: 'bg-amber-500/15 text-warning-fg border-warning-border/50', text: label } }
}

// ── Helpers ───────────────────────────────────────────────────────────────────

function safeNum(n) {
  const v = Number(n)
  return isFinite(v) ? v : 0
}

function fmt(n, decimals = 1) {
  const v = safeNum(n)
  return v === 0 && n == null ? '—' : v.toFixed(decimals)
}

function formatUptime(seconds) {
  const s = safeNum(seconds)
  if (s === 0) return '—'
  const d = Math.floor(s / 86400)
  const h = Math.floor((s % 86400) / 3600)
  const m = Math.floor((s % 3600) / 60)
  if (d > 0) return `${d}d ${h}h ${m}m`
  if (h > 0) return `${h}h ${m}m`
  return `${m}m`
}

// ── Stat card ─────────────────────────────────────────────────────────────────

function StatCard({ label, value, sub, accent }) {
  const styles = {
    green:  'border-success/25 bg-green-500/5 text-success-fg',
    red:    'border-danger/25   bg-red-500/5   text-danger-fg',
    blue:   'border-info/25  bg-blue-500/5  text-info-fg',
    amber:  'border-warning/25 bg-amber-500/5 text-warning-fg',
    purple: 'border-purple-500/25 bg-purple-500/5 text-purple-400',
    gray:   'border-border-strong     bg-surface    text-content',
  }
  const cls = styles[accent] || styles.gray
  const [border, bg, valColor] = cls.split(' ')

  return (
    <div className={`rounded-xl border p-5 flex flex-col gap-1 ${border} ${bg}`}>
      <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider mb-1">{label}</p>
      <p className={`text-3xl font-bold tabular-nums ${valColor}`}>{value ?? '—'}</p>
      {sub && <p className="text-xs text-content-subtle mt-0.5">{sub}</p>}
    </div>
  )
}

// ── Progress bar ──────────────────────────────────────────────────────────────

function Bar({ pct }) {
  const p = safeNum(pct)
  const color = p > 85 ? 'bg-red-500' : p > 65 ? 'bg-amber-400' : 'bg-brand-500'
  return (
    <div className="h-2 bg-surface-raised rounded-full overflow-hidden">
      <div className={`h-full rounded-full transition-all ${color}`} style={{ width: `${Math.min(p, 100)}%` }} />
    </div>
  )
}

// ── Env status dot ────────────────────────────────────────────────────────────

function EnvDot({ wsName, envName }) {
  const { data } = useQuery({
    queryKey:      ['envstatus', wsName, envName],
    queryFn:       () => fetchEnvStatus(wsName, envName),
    refetchInterval: 60_000,
    retry: false,
  })
  const status = data?.status || 'unknown'
  const dot = { running: 'bg-green-400', partial: 'bg-amber-400 animate-pulse', stopped: 'bg-red-500', unknown: 'bg-surface-overlay' }
  return <span title={`${envName}: ${status}`} className={`w-2 h-2 rounded-full inline-block ${dot[status] || dot.unknown}`} />
}

// ── Info row ──────────────────────────────────────────────────────────────────

function InfoRow({ label, value, mono }) {
  return (
    <div className="flex items-start justify-between gap-2 py-1.5 border-b border-border/60 last:border-0">
      <span className="text-xs text-content-subtle shrink-0">{label}</span>
      <span className={`text-xs text-content text-right truncate max-w-[55%] ${mono ? 'font-mono' : ''}`} title={String(value)}>{value ?? '—'}</span>
    </div>
  )
}

// ── Skeleton shimmer primitives ───────────────────────────────────────────────

function Skel({ className }) {
  return <div className={`animate-pulse rounded bg-surface-raised ${className}`} />
}

function SkeletonDashboard() {
  return (
    <div className="p-6 space-y-6 w-full min-w-0">
      {/* Header */}
      <div className="flex items-center justify-between">
        <div className="space-y-2">
          <Skel className="h-7 w-36" />
          <Skel className="h-4 w-64" />
        </div>
      </div>

      {/* Stat cards */}
      <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-4">
        {[...Array(6)].map((_, i) => (
          <div key={i} className="rounded-xl border border-border bg-surface p-5 flex flex-col gap-2">
            <Skel className="h-3 w-20" />
            <Skel className="h-9 w-14" />
            <Skel className="h-3 w-28" />
          </div>
        ))}
      </div>

      {/* Workspaces table */}
      <div className="bg-surface border border-border rounded-xl overflow-hidden">
        <div className="px-5 py-4 border-b border-border flex items-center justify-between">
          <Skel className="h-4 w-24" />
          <Skel className="h-4 w-28" />
        </div>
        <div className="divide-y divide-border">
          {[...Array(3)].map((_, i) => (
            <div key={i} className="px-5 py-3.5 flex items-center gap-6">
              <Skel className="h-4 w-28" />
              <Skel className="h-5 w-14 rounded-full" />
              <div className="flex items-center gap-3 flex-1">
                <Skel className="h-4 w-20" />
                <Skel className="h-4 w-20" />
              </div>
              <Skel className="h-4 w-8 ml-auto" />
              <Skel className="h-4 w-8" />
              <Skel className="h-4 w-14" />
              <Skel className="h-4 w-14" />
              <Skel className="h-4 w-12" />
            </div>
          ))}
        </div>
      </div>

      {/* Bottom panels */}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {[0, 1].map(p => (
          <div key={p} className="bg-surface border border-border rounded-xl overflow-hidden">
            <div className="px-5 py-4 border-b border-border">
              <Skel className="h-4 w-28" />
            </div>
            <div className="px-5 py-3 space-y-3.5">
              {[...Array(4)].map((_, i) => (
                <div key={i} className="flex justify-between">
                  <Skel className="h-3.5 w-28" />
                  <Skel className="h-3.5 w-36" />
                </div>
              ))}
            </div>
            {p === 1 && (
              <div className="px-5 py-4 border-t border-border space-y-4">
                {[0, 1].map(b => (
                  <div key={b} className="space-y-2">
                    <div className="flex justify-between">
                      <Skel className="h-3 w-16" />
                      <Skel className="h-3 w-32" />
                    </div>
                    <Skel className="h-2 w-full rounded-full" />
                  </div>
                ))}
              </div>
            )}
          </div>
        ))}
      </div>
    </div>
  )
}

// ── Main ──────────────────────────────────────────────────────────────────────

// ── Backup coverage (Phase 11b) ───────────────────────────────────────────────

const BACKUP_HEALTH = {
  current:  { dot: 'bg-success',       label: 'Current', cls: 'text-success-fg' },
  stale:    { dot: 'bg-warning',       label: 'Stale',   cls: 'text-warning-fg' },
  never:    { dot: 'bg-danger',        label: 'Never',   cls: 'text-danger-fg' },
  disabled: { dot: 'bg-content-faint', label: 'Off',     cls: 'text-content-faint' },
}

function fmtBackupAge(h) {
  if (h == null || h < 0) return 'never'
  if (h < 1) return '<1h ago'
  if (h < 48) return `${Math.round(h)}h ago`
  return `${Math.round(h / 24)}d ago`
}

function BackupCoverage() {
  const { data: rows = [] } = useQuery({
    queryKey: ['backup-coverage'],
    queryFn: fetchBackupCoverage,
    refetchInterval: 60_000,
  })
  if (!rows.length) return null

  return (
    <div className="bg-surface border border-border rounded-xl overflow-hidden">
      <div className="px-5 py-4 border-b border-border flex items-center justify-between">
        <h2 className="text-sm font-semibold text-content">Backup coverage</h2>
        <span className="text-xs text-content-subtle">{rows.length} environment{rows.length !== 1 ? 's' : ''}</span>
      </div>
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs text-content-subtle border-b border-border">
              <th className="px-5 py-2 font-medium">Workspace / Env</th>
              <th className="px-3 py-2 font-medium">Status</th>
              <th className="px-3 py-2 font-medium">Last backup</th>
              <th className="px-3 py-2 font-medium">Schedule</th>
              <th className="px-3 py-2 font-medium">Snapshots</th>
              <th className="px-5 py-2 font-medium">Remote</th>
            </tr>
          </thead>
          <tbody>
            {rows.map((r, i) => {
              const h = BACKUP_HEALTH[r.health] || BACKUP_HEALTH.disabled
              return (
                <tr key={i} className="border-b border-border/50 last:border-0">
                  <td className="px-5 py-2"><span className="text-content">{r.workspace}</span> <span className="text-content-faint">/ {r.env}</span></td>
                  <td className="px-3 py-2">
                    <span className="inline-flex items-center gap-1.5">
                      <span className={`w-2 h-2 rounded-full ${h.dot}`} />
                      <span className={h.cls}>{h.label}</span>
                    </span>
                  </td>
                  <td className="px-3 py-2 text-content-muted">{fmtBackupAge(r.age_hours)}</td>
                  <td className="px-3 py-2 text-content-muted">
                    {r.enabled && r.schedule !== 'manual' ? r.schedule : <span className="text-content-faint">manual</span>}
                  </td>
                  <td className="px-3 py-2 text-content-muted">
                    {r.count}{r.retention > 0 && <span className="text-content-faint"> / {r.retention}</span>}
                  </td>
                  <td className="px-5 py-2">
                    {r.sync?.status === 'ok'
                      ? <span className="text-success-fg text-xs" title={`Synced to ${r.sync.target}`}>↑ {r.sync.target}</span>
                      : r.sync?.status === 'fail'
                        ? <span className="text-danger-fg text-xs">sync failed</span>
                        : <span className="text-content-faint text-xs">—</span>}
                  </td>
                </tr>
              )
            })}
          </tbody>
        </table>
      </div>
    </div>
  )
}

export default function DashboardPage() {
  const { data: stats, isLoading } = useQuery({
    queryKey:      ['stats'],
    queryFn:       fetchStats,
    refetchInterval: 30_000,
  })

  // Active-alert summary (Phase 6e) — SSE invalidates on fire/resolve; this poll
  // is the reconnect fallback.
  const { data: alertSummary } = useQuery({
    queryKey:      ['alertSummary'],
    queryFn:       fetchAlertSummary,
    refetchInterval: 30_000,
    retry: false,
  })
  const summary    = alertSummary || { total: 0, critical: 0, warning: 0, info: 0, by_workspace: {} }
  const alertAccent = summary.critical > 0 ? 'red' : summary.warning > 0 ? 'amber' : summary.total > 0 ? 'blue' : 'green'
  const alertSub    = summary.total > 0
    ? `${summary.critical} critical · ${summary.warning} warning`
    : 'all clear'

  const docker = stats?.docker || {}
  const host   = stats?.host   || {}
  const ws     = stats?.workspaces || {}
  const workspaces = ws.workspaces || []

  // Near-real-time per-project stats (cpu/mem/net/running/services). Polled fast
  // and invalidated by Docker events (useDockerEvents), so the table tracks
  // changes within a few seconds without the expensive /api/stats (du) call.
  const { data: live = {} } = useQuery({
    queryKey:      ['liveStats'],
    queryFn:       fetchLiveStats,
    refetchInterval: 4000,
    retry: false,
  })

  // Network counters are cumulative; derive per-workspace in/out throughput rates
  // from the delta between consecutive live samples (rx = incoming, tx = outgoing).
  const prevNet = useRef(null)
  const [netRates, setNetRates] = useState({})
  useEffect(() => {
    if (!live || Object.keys(live).length === 0) return
    const now = Date.now()
    const totals = {} // name → { rx, tx } cumulative bytes
    for (const w of workspaces) {
      let rx = 0, tx = 0
      for (const env of (w.envs || [])) {
        const p = live[`${w.name}_${env}`]
        if (p) { rx += p.net_rx_bytes || 0; tx += p.net_tx_bytes || 0 }
      }
      totals[w.name] = { rx, tx }
    }
    if (prevNet.current) {
      const dt = (now - prevNet.current.t) / 1000
      const rates = {}
      for (const name in totals) {
        const prev = prevNet.current.totals[name] || { rx: 0, tx: 0 }
        rates[name] = {
          rx: dt > 0 ? Math.max(0, (totals[name].rx - prev.rx) / dt) : 0,
          tx: dt > 0 ? Math.max(0, (totals[name].tx - prev.tx) / dt) : 0,
        }
      }
      setNetRates(rates)
    }
    prevNet.current = { t: now, totals }
  }, [live]) // eslint-disable-line react-hooks/exhaustive-deps

  const memFreeMB  = safeNum(host.mem_avail_mb)
  const memTotalMB = safeNum(host.mem_total_mb)
  const diskFreeGB = safeNum(host.disk_free_gb)
  const diskTotalGB = safeNum(host.disk_total_gb)

  if (isLoading) {
    return <Layout><SkeletonDashboard /></Layout>
  }

  return (
    <Layout>
      <div className="p-6 space-y-6 w-full min-w-0">

        {/* Header */}
        <div className="flex items-center justify-between">
          <div>
            <h1 className="text-2xl font-bold text-content-strong">Dashboard</h1>
            <p className="text-sm text-content-muted mt-0.5">Overview of your Docker stacks and host system</p>
          </div>
        </div>

        {/* ── Stat cards ── */}
        <div className="grid grid-cols-2 sm:grid-cols-3 lg:grid-cols-6 gap-4">
          <StatCard
            label="Active alerts"
            value={summary.total}
            sub={alertSub}
            accent={alertAccent}
          />
          <StatCard
            label="Workspaces"
            value={ws.total ?? '—'}
            sub={`${ws.by_type?.image || 0} image · ${ws.by_type?.custom || 0} custom`}
            accent="blue"
          />
          <StatCard
            label="Environments"
            value={workspaces.reduce((n, w) => n + (w.envs?.length || 0), 0) || '—'}
            sub="across all workspaces"
            accent="purple"
          />
          <StatCard
            label="Running containers"
            value={docker.containers_running ?? '—'}
            sub={`${docker.containers_stopped ?? 0} stopped · ${docker.containers_paused ?? 0} paused`}
            accent={docker.containers_running > 0 ? 'green' : 'gray'}
          />
          <StatCard
            label="Docker images"
            value={docker.images_total ?? '—'}
            sub={`${docker.volumes_total ?? 0} volumes`}
            accent="gray"
          />
          <StatCard
            label="Docker networks"
            value={docker.networks_total ?? '—'}
            sub={docker.server_version ? `Engine v${docker.server_version}` : '—'}
            accent="gray"
          />
        </div>

        {/* ── Workspaces table ── */}
        <div className="bg-surface border border-border rounded-xl overflow-hidden">
          <div className="px-5 py-4 border-b border-border flex items-center justify-between">
            <h2 className="text-sm font-semibold text-content">Workspaces</h2>
            <Link to="/new" className="text-xs text-brand-400 hover:text-brand-300 transition-colors font-medium">+ New workspace</Link>
          </div>

          {workspaces.length === 0 ? (
            <div className="px-5 py-10 text-center">
              <p className="text-sm text-content-subtle">No workspaces yet.</p>
              <Link to="/new" className="text-xs text-brand-400 hover:text-brand-300 mt-2 inline-block">Create your first workspace →</Link>
            </div>
          ) : (
            <div className="overflow-x-auto">
              <table className="w-full text-sm">
                <thead>
                  <tr className="border-b border-border">
                    <th className="px-5 py-2.5 text-left text-xs font-semibold text-content-subtle uppercase tracking-wider">Name</th>
                    <th className="px-4 py-2.5 text-left text-xs font-semibold text-content-subtle uppercase tracking-wider">Type</th>
                    <th className="px-4 py-2.5 text-left text-xs font-semibold text-content-subtle uppercase tracking-wider">Environments</th>
                    <th className="px-4 py-2.5 text-center text-xs font-semibold text-content-subtle uppercase tracking-wider">Services</th>
                    <th className="px-4 py-2.5 text-center text-xs font-semibold text-content-subtle uppercase tracking-wider">Containers</th>
                    <th className="px-4 py-2.5 text-right text-xs font-semibold text-content-subtle uppercase tracking-wider">CPU</th>
                    <th className="px-4 py-2.5 text-right text-xs font-semibold text-content-subtle uppercase tracking-wider">Memory</th>
                    <th className="px-4 py-2.5 text-right text-xs font-semibold text-content-subtle uppercase tracking-wider">Disk</th>
                    <th className="px-4 py-2.5 text-right text-xs font-semibold text-content-subtle uppercase tracking-wider">Net I/O</th>
                    <th className="px-4 py-2.5 text-right text-xs font-semibold text-content-subtle uppercase tracking-wider"></th>
                  </tr>
                </thead>
                <tbody className="divide-y divide-border">
                  {workspaces.map(w => {
                    const as = wsAlertStyle(summary.by_workspace?.[w.name])
                    const lv = aggregateLive(w, live)
                    const netRate = netRates[w.name] || { rx: 0, tx: 0 }
                    return (
                    <tr key={w.name} className={`hover:bg-surface-raised/40 transition-colors group ${as.row}`}>
                      <td className={`px-5 py-3 ${as.cell}`}>
                        <div className="flex items-center gap-2">
                          <Link to={`/workspaces/${w.name}`} className="font-medium text-content-strong group-hover:text-brand-400 transition-colors">
                            {w.name}
                          </Link>
                          {as.badge && (
                            <span className={`text-[10px] font-semibold px-1.5 py-0.5 rounded border ${as.badge.cls}`} title="Active alerts">
                              {as.badge.text}
                            </span>
                          )}
                        </div>
                      </td>
                      <td className="px-4 py-3">
                        <span className={`text-xs px-2 py-0.5 rounded-full font-medium ${
                          w.type === 'image' ? 'bg-info-subtle text-info-fg' : 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300'
                        }`}>{w.type}</span>
                      </td>
                      <td className="px-4 py-3">
                        <div className="flex items-center gap-3">
                          {(w.envs || []).map(env => {
                            const eh = w.env_hosts?.[env]
                            return (
                            <div key={env} className="flex items-center gap-1.5">
                              <EnvDot wsName={w.name} envName={env} />
                              <span className="text-xs text-content-muted">{env}</span>
                              {eh?.host_name && (
                                <span title={`runs on ${eh.host_name}`} className="text-[10px] px-1 py-0.5 rounded bg-indigo-100/70 text-indigo-700 border border-indigo-200 dark:bg-indigo-950/60 dark:text-indigo-300 dark:border-indigo-800/40">🖥 {eh.host_name}</span>
                              )}
                            </div>
                          )})}
                        </div>
                      </td>
                      {/* Services — distinct compose service count (live), falls back to configured image count */}
                      <td className="px-4 py-3 text-center">
                        <span className="text-sm text-content tabular-nums">
                          {lv.services > 0 ? lv.services : (w.image_count > 0 ? w.image_count : <span className="text-content-faint">—</span>)}
                        </span>
                      </td>
                      {/* Containers — running/total (live) */}
                      <td className="px-4 py-3 text-center">
                        {lv.total > 0 ? (
                          <span className={`text-sm font-medium tabular-nums ${
                            lv.running === lv.total ? 'text-success-fg' : lv.running > 0 ? 'text-warning-fg' : 'text-content-faint'
                          }`}>
                            {lv.running}/{lv.total}
                          </span>
                        ) : <span className="text-sm text-content-faint tabular-nums">—</span>}
                      </td>
                      {/* CPU (live) */}
                      <td className="px-4 py-3 text-right">
                        <span className={`text-xs tabular-nums ${lv.cpu > 0 ? 'text-content-muted' : 'text-content-faint'}`}>
                          {lv.cpu > 0 ? `${lv.cpu.toFixed(1)}%` : '—'}
                        </span>
                      </td>
                      {/* Memory (live) */}
                      <td className="px-4 py-3 text-right">
                        <span className={`text-xs tabular-nums ${lv.mem > 0 ? 'text-content-muted' : 'text-content-faint'}`}>{fmtMB(lv.mem)}</span>
                      </td>
                      {/* Disk — from /api/stats (du is too costly to poll fast) */}
                      <td className="px-4 py-3 text-right">
                        <span className="text-xs text-content-muted tabular-nums">
                          {w.disk_mb >= 1024
                            ? `${(w.disk_mb / 1024).toFixed(1)} GB`
                            : w.disk_mb >= 0.1 ? `${w.disk_mb.toFixed(1)} MB`
                            : w.disk_mb > 0   ? `${(w.disk_mb * 1024).toFixed(0)} KB`
                            : <span className="text-content-faint">—</span>}
                        </span>
                      </td>
                      {/* Net I/O throughput — incoming (rx) and outgoing (tx), live derived rate */}
                      <td className="px-4 py-3 text-right">
                        <div className="flex flex-col items-end leading-tight">
                          <span className="text-xs text-content-muted tabular-nums" title="incoming">
                            <span className="text-content-faint">↓</span> {fmtRate(netRate.rx)}
                          </span>
                          <span className="text-xs text-content-muted tabular-nums" title="outgoing">
                            <span className="text-content-faint">↑</span> {fmtRate(netRate.tx)}
                          </span>
                        </div>
                      </td>
                      <td className="px-4 py-3 text-right">
                        <Link to={`/workspaces/${w.name}`} className="text-xs text-content-faint group-hover:text-content-muted transition-colors">
                          Open →
                        </Link>
                      </td>
                    </tr>
                    )
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>

        {/* ── Backup coverage (11b) ── */}
        <BackupCoverage />

        {/* ── Bottom: Docker + Host system ── */}
        <div className="grid grid-cols-1 md:grid-cols-2 gap-6">

          {/* Docker info */}
          <div className="bg-surface border border-border rounded-xl overflow-hidden">
            <div className="px-5 py-4 border-b border-border">
              <h2 className="text-sm font-semibold text-content">Docker engine</h2>
            </div>
            {docker.error ? (
              <p className="px-5 py-4 text-xs text-danger-fg">{docker.error}</p>
            ) : (
              <div className="px-5 py-2">
                <InfoRow label="Engine version"   value={docker.server_version ? `v${docker.server_version}` : null} />
                <InfoRow label="Storage driver"   value={docker.storage_driver} />
                <InfoRow label="Root directory"   value={docker.docker_root_dir} mono />
                <InfoRow label="Containers"       value={`${docker.containers_running ?? 0} running · ${docker.containers_stopped ?? 0} stopped · ${docker.containers_paused ?? 0} paused`} />
                <InfoRow label="Images"           value={docker.images_total} />
                <InfoRow label="Volumes"          value={docker.volumes_total} />
                <InfoRow label="Networks"         value={docker.networks_total} />
              </div>
            )}
          </div>

          {/* Host system */}
          <div className="bg-surface border border-border rounded-xl overflow-hidden">
            <div className="px-5 py-4 border-b border-border">
              <h2 className="text-sm font-semibold text-content">Host system</h2>
            </div>
            <div className="px-5 py-2">
              <InfoRow label="Operating system" value={host.os} />
              <InfoRow label="Architecture"     value={host.arch} />
              <InfoRow label="CPU cores"        value={host.cpus} />
              <InfoRow label="Uptime"           value={formatUptime(host.uptime_seconds)} />
            </div>
            <div className="px-5 py-4 space-y-4 border-t border-border">
              {/* Memory */}
              <div>
                <div className="flex justify-between mb-1.5">
                  <span className="text-xs text-content-muted font-medium">Memory</span>
                  <span className="text-xs text-content-subtle">
                    {fmt(memFreeMB / 1024)} GB free of {fmt(memTotalMB / 1024)} GB
                  </span>
                </div>
                <Bar pct={safeNum(host.mem_used_pct)} />
                <p className="text-xs text-content-faint mt-1 text-right">{fmt(host.mem_used_pct, 0)}% used</p>
              </div>
              {/* Disk */}
              <div>
                <div className="flex justify-between mb-1.5">
                  <span className="text-xs text-content-muted font-medium">Disk ( / )</span>
                  <span className="text-xs text-content-subtle">
                    {fmt(diskFreeGB)} GB free of {fmt(diskTotalGB)} GB
                  </span>
                </div>
                <Bar pct={safeNum(host.disk_used_pct)} />
                <p className="text-xs text-content-faint mt-1 text-right">{fmt(host.disk_used_pct, 0)}% used</p>
              </div>
            </div>
          </div>

        </div>
      </div>
    </Layout>
  )
}
