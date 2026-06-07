import { useState, useRef, useEffect, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchWorkspace, fetchEnvVars, fetchEnvStatus, fetchImageUpdates, fetchContainers, fetchEnvMetrics, fetchMetricsConfig, updateEnvVars, rotateSecret, fetchSecretEvents, openActionSocket, fetchActionRuns, clearActionRuns, fetchBackupStats, fetchBackupServices } from '../lib/api'
import { useAuthStore } from '../store/auth'
import { useConfirm } from '../context/ConfirmContext'
import Layout from '../components/Layout'
import ComposeEditor from '../components/ComposeEditor'
import TerminalModal from '../components/TerminalModal'
import ContainerInfoModal from '../components/ContainerInfoModal'
import FileBrowserModal from '../components/FileBrowserModal'
import Sparkline from '../components/Sparkline'

// ── Metrics history (Phase 6d) ──────────────────────────────────────────────────

function fmtBytes(b) {
  if (!b || b <= 0) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(b) / Math.log(1024)), u.length - 1)
  return `${(b / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}

function fmtRate(bps) {
  if (!bps || bps <= 0) return '0 B/s'
  return `${fmtBytes(bps)}/s`
}

// Selectable metric time windows (minutes). Default is 60m.
const METRIC_RANGES = [
  { label: '30m', v: 30 },
  { label: '60m', v: 60 },
  { label: '3h',  v: 180 },
  { label: '6h',  v: 360 },
  { label: '12h', v: 720 },
  { label: '1d',  v: 1440 },
  { label: '3d',  v: 4320 },
]

function MetricTile({ label, value, series, stroke }) {
  return (
    <div className="bg-surface/40 border border-border/60 rounded-lg px-3 py-2 min-w-0">
      <div className="flex items-center justify-between gap-1 mb-1">
        <span className="text-xs font-semibold text-content-subtle uppercase tracking-wider">{label}</span>
        <span className="text-xs font-mono text-content truncate">{value}</span>
      </div>
      <Sparkline values={series} stroke={stroke} height={24} />
    </div>
  )
}

// ── Env status badge ──────────────────────────────────────────────────────────

function StatusBadge({ label, color }) {
  const colors = {
    running:  'bg-green-500/20 text-success-fg border-success/30',
    partial:  'bg-amber-500/20 text-warning-fg border-warning/30',
    building: 'bg-amber-500/20 text-warning-fg border-warning/30',
    stopped:  'bg-red-500/15 text-danger-fg border-danger/30',
    unknown:  'bg-surface-overlay/40 text-content-subtle border-border-strong/30',
  }
  const dot = {
    running:  'bg-green-400',
    partial:  'bg-amber-400 animate-pulse',
    building: 'bg-amber-400 animate-pulse',
    stopped:  'bg-red-500',
    unknown:  'bg-surface-overlay',
  }
  const c = colors[color] || colors.unknown
  const d = dot[color] || dot.unknown
  return (
    <span className={`inline-flex items-center gap-1.5 text-xs font-medium px-2 py-0.5 rounded-full border ${c}`}>
      <span className={`w-1.5 h-1.5 rounded-full ${d}`} />
      {label}
    </span>
  )
}

// ── Environment card ──────────────────────────────────────────────────────────

// Returns { url, port, links, viaTraefik, domainUrl } where:
//   url        — primary clickable URL (first link or domain)
//   port       — port string shown on badge
//   links      — array of { url, label } for all linked ports (image stacks, Traefik off)
//   viaTraefik — true when Traefik+domain is the access method
//   domainUrl  — domain URL even when Traefik is off (for display alongside port links)
//
// Uses ws.env_access (server-resolved values) so ${VAR} references are already substituted.
function envAccess(cfg, ws, envName) {
  // For an env running on a remote host, direct host:port URLs must point at the
  // remote host's address, not the control plane's.
  const host    = ws?.env_hosts?.[envName]?.host_address || window.location.hostname
  const traefik = !!cfg?.traefik_enabled
  const ssl     = !!cfg?.ssl_enabled
  const isImage = ws?.config?.project?.type === 'image'
  const empty   = { url: null, port: null, links: [], viaTraefik: false, domainUrl: null }

  // Prefer server-resolved values; fall back to raw config fields
  const access   = ws?.env_access?.[envName] || {}
  const domain   = access.domain   || cfg?.domain   || ''
  const httpPort = access.http_port || String(cfg?.http_port || '')
  const images   = (access.images  || []).length > 0 ? access.images : (ws?.config?.images || [])

  const domainUrl = domain ? `${ssl ? 'https' : 'http'}://${domain}` : null

  if (traefik) {
    if (domain) return { url: domainUrl, port: null, links: [], viaTraefik: true, domainUrl }
    return empty
  }

  // Traefik OFF — direct host port access; show domain as informational link if set
  if (isImage) {
    const linked = images.flatMap(img => {
      const lp = img.link_ports || []
      const validPort = p => p && String(p).trim() !== '' && String(p) !== '0' && !String(p).includes('$')
      if (lp.length) {
        return lp.filter(validPort).map(p => ({ url: `http://${host}:${p}`, label: `${img.name}:${p}` }))
      }
      // Fallback: no link_ports — use host_port as implicit link
      const hp = img.host_port
      if (validPort(hp)) {
        return [{ url: `http://${host}:${hp}`, label: `${img.name}:${hp}` }]
      }
      return []
    })
    if (linked.length) {
      const primary = linked[0]
      const p = primary.url.split(':').pop()
      return { url: primary.url, port: p, links: linked, viaTraefik: false, domainUrl }
    }
  } else {
    // Custom stack: Nginx binds http_port on the host
    if (httpPort && httpPort !== '0' && !httpPort.includes('$')) {
      const url = httpPort === '80' ? `http://${host}` : `http://${host}:${httpPort}`
      return { url, port: httpPort, links: [], viaTraefik: false, domainUrl }
    }
  }

  return { ...empty, domainUrl }
}

// Keep old name for any remaining callers
function envUrl(cfg, ws) { return envAccess(cfg, ws).url }

// UrlBadge renders an access URL as a clickable link only when the env is
// reachable (running and not unhealthy); otherwise it's shown disabled so users
// don't click through to a dead endpoint.
function UrlBadge({ href, reachable, mono, children }) {
  const base = `text-xs ${mono ? 'font-mono ' : ''}px-2 py-0.5 rounded-full border shrink-0 truncate max-w-[140px] transition-colors`
  if (!reachable) {
    return (
      <span title="Not reachable — the environment is not running or is unhealthy"
        className={`${base} bg-surface-raised/30 text-content-faint border-border cursor-not-allowed`}>
        {children}
      </span>
    )
  }
  return (
    <a href={href} target="_blank" rel="noreferrer" title={`Open ${href}`}
      className={`${base} bg-surface-raised hover:bg-brand-900 text-content-muted hover:text-brand-300 border-border-strong hover:border-brand-600`}>
      {children}
    </a>
  )
}

// CtlBtn — a compact per-container action button used in the Services list.
// className overrides the text/hover colors (defaults to muted gray); all
// buttons share the same size, shape, and hover background.
function CtlBtn({ title, onClick, children, className = '' }) {
  return (
    <button type="button" title={title} onClick={onClick}
      className={`rounded px-1 py-0.5 text-xs leading-none transition-colors hover:bg-surface-raised ${className || 'text-content-faint hover:text-content'}`}>
      {children}
    </button>
  )
}

// ── EnvCard action UI (state-aware buttons + toolbar) ─────────────────────────
// Shared 24-viewBox icons. deploy/stop render filled; the rest are stroked.
const EI = {
  deploy:  <polygon points="7 4 20 12 7 20" />,
  update:  <><path d="M12 20V7" /><path d="M6 12l6-6 6 6" /><path d="M5 21h14" /></>,
  stop:    <rect x="6" y="6" width="12" height="12" rx="2" />,
  refresh: <><path d="M21 12a9 9 0 1 1-2.64-6.36" /><path d="M21 3v5h-5" /></>,
  restart: <><path d="M3 12a9 9 0 1 0 3-6.7" /><path d="M3 4v5h5" /></>,
  down:    <><path d="M12 3v9" /><path d="M7 6a8 8 0 1 0 10 0" /></>,
  vars:    <><path d="M4 8h16M4 16h16" /><circle cx="9" cy="8" r="2.4" fill="currentColor" stroke="none" /><circle cx="15" cy="16" r="2.4" fill="currentColor" stroke="none" /></>,
  compose: <><path d="M9 8l-4 4 4 4" /><path d="M15 8l4 4-4 4" /></>,
  terminal:<><path d="M6 7l5 5-5 5" /><path d="M13 17h6" /></>,
  backup:  <><ellipse cx="12" cy="6" rx="7" ry="2.6" /><path d="M5 6v12c0 1.4 3.1 2.6 7 2.6s7-1.2 7-2.6V6" /><path d="M5 12c0 1.4 3.1 2.6 7 2.6s7-1.2 7-2.6" /></>,
}
function EnvIcon({ name, fill, className = 'w-4 h-4' }) {
  return (
    <svg viewBox="0 0 24 24" className={className} fill={fill ? 'currentColor' : 'none'}
      stroke={fill ? 'none' : 'currentColor'} strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      {EI[name]}
    </svg>
  )
}

// PrimaryBtn — a prominent labelled lifecycle button (Deploy / Update / Stop).
function PrimaryBtn({ variant, icon, fill, disabled, pulse, onClick, title, children }) {
  const styles = {
    deploy: 'bg-brand-600 hover:bg-brand-700 text-white',
    update: 'bg-amber-500/15 hover:bg-amber-500/25 text-warning-fg border border-warning/25',
    stop:   'bg-danger-subtle/60 hover:bg-danger/20 text-danger-fg hover:text-danger-fg',
  }
  return (
    <button type="button" onClick={onClick} disabled={disabled} title={title}
      className={`flex-1 flex items-center justify-center gap-1.5 text-[13px] font-semibold px-2 py-2 rounded-lg transition-colors disabled:opacity-30 disabled:pointer-events-none ${styles[variant]}`}>
      <EnvIcon name={icon} fill={fill} className="w-3.5 h-3.5" />
      {children}
      {pulse && <span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />}
    </button>
  )
}

// ToolBtn — a compact muted icon button in the EnvCard action toolbar; gains its
// accent color on hover (className), greys out when disabled.
function ToolBtn({ icon, title, onClick, disabled, className = 'text-content-subtle hover:text-content' }) {
  return (
    <button type="button" onClick={onClick} disabled={disabled} title={title}
      className={`flex-1 flex items-center justify-center py-1.5 rounded-md transition-colors hover:bg-surface disabled:opacity-25 disabled:pointer-events-none ${className}`}>
      <EnvIcon name={icon} className="w-[15px] h-[15px]" />
    </button>
  )
}

// AccessUrls — env access links below the status badge. A single URL shows inline;
// two or more collapse into a dropdown so a domain + ports don't eat vertical space.
function AccessUrls({ urls, reachable }) {
  const [open, setOpen] = useState(false)
  const ref = useRef(null)
  useEffect(() => {
    if (!open) return
    const h = e => { if (ref.current && !ref.current.contains(e.target)) setOpen(false) }
    document.addEventListener('mousedown', h)
    return () => document.removeEventListener('mousedown', h)
  }, [open])
  if (urls.length === 0) return null
  if (urls.length === 1) return <UrlBadge href={urls[0].href} reachable={reachable} mono>{urls[0].label} ↗</UrlBadge>
  return (
    <div ref={ref} className="relative">
      <button type="button" onClick={() => setOpen(o => !o)}
        className="inline-flex items-center gap-1 font-mono text-xs px-2 py-0.5 rounded-full bg-surface-raised hover:bg-surface-overlay text-content border border-border-strong max-w-[150px] transition-colors">
        <span className="truncate">🌐 {urls[0].label}</span>
        <span className="opacity-60 shrink-0">+{urls.length - 1} ▾</span>
      </button>
      {open && (
        <div className="absolute right-0 top-full mt-1 z-20 bg-surface-raised border border-border-strong rounded-lg shadow-xl py-1 min-w-[160px]">
          {urls.map((u, i) => reachable ? (
            <a key={i} href={u.href} target="_blank" rel="noreferrer"
              className="flex items-center justify-between gap-3 px-3 py-1.5 text-xs font-mono text-content hover:bg-surface-overlay hover:text-brand-300 transition-colors">
              <span className="truncate">{u.label}</span><span className="opacity-60">↗</span>
            </a>
          ) : (
            <span key={i} title="Not reachable — the environment is not running or is unhealthy"
              className="flex items-center justify-between gap-3 px-3 py-1.5 text-xs font-mono text-content-faint cursor-not-allowed">
              <span className="truncate">{u.label}</span>
            </span>
          ))}
        </div>
      )}
    </div>
  )
}

// useMetricsInterval returns the server's metrics collection cadence in seconds
// (METRICS_INTERVAL_SECONDS, default 30). One shared query key means every
// EnvCard dedupes onto a single request, cached for the session.
function useMetricsInterval() {
  const { data } = useQuery({
    queryKey: ['metrics-config'],
    queryFn: fetchMetricsConfig,
    staleTime: Infinity,
    retry: false,
  })
  return data?.collect_interval_seconds || 30
}

// BackupStatsLine — compact per-env backup summary (Phase 11e): snapshot count,
// total size on disk, and the configured retention limit. Hidden when there are
// no local snapshots.
function BackupStatsLine({ name, envName }) {
  const { workspace } = useParams()
  const { data } = useQuery({
    queryKey: ['backup-stats', workspace, name, envName],
    queryFn: () => fetchBackupStats(workspace, name, envName),
    refetchInterval: 120_000,
    retry: false,
  })
  if (!data || data.count === 0) return null
  return (
    <div className="border-t border-border/60 pt-3 flex items-center gap-1.5 text-[11px] text-content-faint">
      <svg viewBox="0 0 24 24" className="w-3.5 h-3.5 text-content-subtle" fill="none" stroke="currentColor" strokeWidth="1.8">
        <ellipse cx="12" cy="6" rx="7" ry="2.6" /><path d="M5 6v12c0 1.4 3.1 2.6 7 2.6s7-1.2 7-2.6V6" /><path d="M5 12c0 1.4 3.1 2.6 7 2.6s7-1.2 7-2.6" />
      </svg>
      <span className="text-content-muted font-medium">{data.count}</span>
      <span>backup{data.count !== 1 ? 's' : ''}</span>
      <span>·</span>
      <span>{fmtBytes(data.total_bytes)}</span>
      {data.active_schedules > 0 && (
        <><span>·</span><span title="Active backup schedules for this environment">{data.active_schedules} schedule{data.active_schedules !== 1 ? 's' : ''}</span></>
      )}
    </div>
  )
}

function EnvCard({ name, ws, envName, cfg, onAction, onConfig, onCompose, onTerminal, onLogs, onActionDone }) {
  const qc         = useQueryClient()
  const { workspace } = useParams() // parent-tier workspace (from the route)
  // Use server-resolved domain (${VAR} already substituted) for display
  const domain     = ws?.env_access?.[envName]?.domain || cfg?.domain || '—'
  const gitBranch  = cfg?.git?.branch || ''
  const deployment = cfg?.deployment || 'compose'
  const isImage    = ws?.config?.project?.type === 'image'
  const { url, port, links, viaTraefik, domainUrl } = envAccess(cfg, ws, envName)

  // Poll container status every 15 seconds, refresh immediately after actions
  const { data: statusData, refetch: refetchStatus } = useQuery({
    queryKey: ['envstatus', workspace, name, envName],
    queryFn: () => fetchEnvStatus(workspace, name, envName),
    refetchInterval: 30_000,  // SSE invalidates immediately; polling is the fallback
    retry: false,
  })
  const containerStatus = statusData?.status || 'unknown'

  // Per-container health details — shared query key with LogViewer (React Query deduplicates)
  const { data: containers = [] } = useQuery({
    queryKey: ['containers', workspace, name, envName],
    queryFn: () => fetchContainers(workspace, name, envName),
    refetchInterval: 30_000,
    retry: false,
  })

  // Metrics history (Phase 6d) — per-env CPU/memory/disk/network for sparklines.
  // rangeMin is the selected time window (minutes); default 60.
  const [rangeMin, setRangeMin] = useState(60)
  // Refresh the sparklines at the server's collection cadence so the UI and the
  // collector move together (driven by METRICS_INTERVAL_SECONDS, default 30s).
  const metricsIntervalMs = useMetricsInterval() * 1000
  const { data: metrics = [] } = useQuery({
    queryKey: ['metrics', workspace, name, envName, rangeMin],
    queryFn: () => fetchEnvMetrics(workspace, name, envName, rangeMin),
    refetchInterval: metricsIntervalMs,
    retry: false,
  })
  const lastMetric = metrics[metrics.length - 1]
  const cpuSeries  = metrics.map(m => m.cpu_pct)
  const memSeries  = metrics.map(m => m.memory_bytes)
  const diskSeries = metrics.map(m => m.disk_bytes)
  // Network throughput: net_rx/tx are cumulative bytes, so derive a per-second
  // rate from the delta between consecutive snapshots (clamp negatives on restart).
  const netRateSeries = metrics.map((m, i) => {
    if (i === 0) return 0
    const prev = metrics[i - 1]
    const dt = (new Date(m.recorded_at) - new Date(prev.recorded_at)) / 1000
    const dBytes = ((m.net_rx_bytes || 0) + (m.net_tx_bytes || 0)) - ((prev.net_rx_bytes || 0) + (prev.net_tx_bytes || 0))
    return dt > 0 ? Math.max(0, dBytes / dt) : 0
  })
  const lastNetRate = netRateSeries[netRateSeries.length - 1] || 0

  // Derive short service names for display. Container names use the immutable
  // resource prefix ({workspace}_{project}); fall back to name for pre-tier cfgs.
  const resourcePrefix = ws?.config?.project?.resource_prefix || name
  const stackPrefix = `${resourcePrefix}_${envName}_`
  const containerDetails = containers.map(c => ({
    ...c,
    short: c.Service.startsWith(stackPrefix) ? c.Service.slice(stackPrefix.length) : c.Service,
  }))

  // URLs are only clickable when the env is actually reachable: running and with
  // no unhealthy container.
  const reachable = containerStatus === 'running' && !containerDetails.some(c => c.Health === 'unhealthy')

  // Image update check — results come from hourly background cache; poll every 10 min
  const { data: imgUpdates } = useQuery({
    queryKey: ['imageupdates', workspace, name, envName],
    queryFn: () => fetchImageUpdates(workspace, name, envName),
    enabled: isImage,
    // While the backend reports `pending` (a fresh check is in flight — e.g. for a
    // just-added env), poll quickly so the update badges appear without needing a
    // page remount; otherwise fall back to the slow 10-min cadence.
    refetchInterval: (q) => (q.state.data?.pending ? 4000 : 10 * 60 * 1000),
    retry: false,
  })
  const hasImageUpdate    = imgUpdates?.updates?.some(u => u.has_update) || false
  const hasIndeterminate  = !hasImageUpdate && imgUpdates?.updates?.some(u => u.indeterminate) || false
  const updateServices    = (imgUpdates?.updates || []).filter(u => u.has_update).map(u => `${u.service}: ${u.newer_tag}`)
  const indetermServices  = (imgUpdates?.updates || []).filter(u => u.indeterminate).map(u => u.service)
  // Update is shown permanently for image stacks; disabled once we've confirmed
  // everything is current (so its position never shifts).
  const updateChecked     = imgUpdates && !imgUpdates.pending
  const updateUpToDate    = updateChecked && !hasImageUpdate

  // Action availability by env state.
  const isRunning     = containerStatus === 'running' || containerStatus === 'partial'
  const hasContainers = containerStatus !== 'unknown'
  const hostName      = ws?.env_hosts?.[envName]?.host_name // unset ⇒ local

  // Access URLs collapsed into one list (domain and/or port links). Rendered
  // inline when there's one, as a dropdown when there are several.
  const accessUrls = []
  if (viaTraefik && url) accessUrls.push({ label: domain, href: url })
  if (!viaTraefik) {
    if (domainUrl) accessUrls.push({ label: domain, href: domainUrl })
    if (links && links.length) links.forEach(l => accessUrls.push({ label: l.label, href: l.url }))
    else if (port && url) accessUrls.push({ label: `:${port}`, href: url })
  }

  function handleAction(cmd, extra = [], services = []) {
    onAction(cmd, envName, () => {
      // Refresh env status, container details, image-update and metric state
      // after any action (deploy/refresh/etc.) so the card reflects reality.
      setTimeout(() => {
        refetchStatus()
        qc.invalidateQueries({ queryKey: ['containers', workspace, name, envName] })
        qc.invalidateQueries({ queryKey: ['metrics', workspace, name, envName] })
        qc.invalidateQueries({ queryKey: ['backup-stats', workspace, name, envName] })
        if (isImage) qc.invalidateQueries({ queryKey: ['imageupdates', workspace, name, envName] })
      }, 2000)
      // After update: backend invalidates its cache and runs a fresh check (~3-5s).
      // Wait 8s then refetch so the UI reflects the post-update digest comparison.
      if (cmd === 'update') {
        setTimeout(() => {
          qc.invalidateQueries({ queryKey: ['imageupdates', workspace, name, envName] })
        }, 8000)
      }
    }, extra, services)
  }

  const [backupModal, setBackupModal] = useState(false) // manual-backup service picker

  const [infoFor, setInfoFor]             = useState(null) // {service, short} for the Info inspector
  const [filesFor, setFilesFor]           = useState(null) // {service, short} for the file browser
  const confirm = useConfirm() // gated confirm dialog for destructive actions
  const [containersOpen, setContainersOpen] = useState(true)

  // Card collapse — hides the metrics + services detail to keep cards compact.
  // Defaults to collapsed; the choice is persisted per workspace+env so it
  // survives navigating away and back (the card remounts on route change).
  const collapseKey = `rigger:envcard:${name}:${envName}`
  const [expanded, setExpanded] = useState(() => {
    try { return localStorage.getItem(collapseKey) === '1' } catch { return false }
  })
  const toggleExpanded = () => setExpanded(v => {
    const next = !v
    try { localStorage.setItem(collapseKey, next ? '1' : '0') } catch {}
    return next
  })

  // Build a merged service list: all expected services + actual runtime state.
  // For image stacks: start from config.images so we show services not yet started.
  // For custom stacks: use whatever docker compose ps returned.
  const configImages = ws?.config?.images || []
  const serviceRows = isImage && configImages.length > 0
    ? configImages.map(img => {
        const live = containerDetails.find(c => c.short === img.name)
        return live || { short: img.name, Name: '', Service: `${name}_${envName}_${img.name}`, State: '', Health: '', Status: '' }
      })
    : containerDetails

  // Per-service update info (image stacks only)
  const updateByService = Object.fromEntries(
    (imgUpdates?.updates || []).map(u => [u.service, u])
  )

  // The metrics strip shows whenever the env is up (or has history); the services
  // panel shows whenever there are services. The collapse toggle only appears when
  // at least one of those sections has something to reveal.
  const showMetrics     = metrics.length > 0 || containerStatus === 'running' || containerStatus === 'partial'
  const hasCollapsible  = showMetrics || serviceRows.length > 0

  return (
    <div className="w-full bg-surface border border-border rounded-xl p-5 flex flex-col gap-4">
      {/* Card header: identity (name + host) on the left, state (status + access
          URLs) on the right. */}
      <div className="flex items-start justify-between gap-2">
        <div className="flex flex-col min-w-0">
          <div className="flex items-center gap-2 min-w-0">
            <h3 className="font-semibold text-content-strong text-base truncate">{envName}</h3>
            {/* Deployment mode is configured per-environment (compose / swarm). */}
            <span title={`Deployment mode: ${deployment}`}
              className="shrink-0 text-[10px] font-medium px-1.5 py-0.5 rounded-full bg-surface-raised text-content-muted">
              {deployment}
            </span>
          </div>
          <span
            title={hostName ? `Runs on remote host ${hostName}` : 'Runs on the local control plane'}
            className={`inline-flex items-center gap-1 text-[11px] mt-0.5 ${hostName ? 'text-indigo-300' : 'text-content-subtle'}`}
          >
            🖥 {hostName || 'local'}
          </span>
        </div>
        <div className="flex flex-col items-end gap-1.5 shrink-0">
          <StatusBadge label={containerStatus} color={containerStatus} />
          <AccessUrls urls={accessUrls} reachable={reachable} />
        </div>
      </div>

      {/* Details */}
      <div className="space-y-1.5 text-sm text-content-muted">
        {/* Only show domain/url row if neither badge above applies */}
        {!cfg?.domain && !port && <DetailRow icon="○" value="no url configured" />}
        {gitBranch && <DetailRow icon="○" value={gitBranch} />}
      </div>

      {/* Actions — state-aware: primary lifecycle buttons + a compact toolbar.
          Down (Inactivate) is destructive, so it asks to confirm first. */}
      <div className="mt-auto relative flex flex-col gap-2">
        {/* Primary lifecycle: Deploy / Update (image stacks) / Stop */}
        <div className="flex gap-2">
          <PrimaryBtn variant="deploy" icon="deploy" fill onClick={() => handleAction('start')}
            title="Deploy — bring the stack up (applies the current compose)">Deploy</PrimaryBtn>
          {isImage && (
            <PrimaryBtn variant="update" icon="update" pulse={hasImageUpdate} disabled={updateUpToDate}
              onClick={() => handleAction('update')}
              title={updateUpToDate ? 'Up to date — no update available'
                : hasImageUpdate ? `Update available${updateServices.length ? ': ' + updateServices.join(', ') : ''} — pull & recreate`
                : 'Pull latest images & recreate'}>Update</PrimaryBtn>
          )}
          <PrimaryBtn variant="stop" icon="stop" fill disabled={!isRunning} onClick={() => handleAction('stop')}
            title="Stop containers (keep state)">Stop</PrimaryBtn>
        </div>

        {/* Toolbar: secondary lifecycle + files + terminal */}
        <div className="flex items-center gap-0.5 p-1 bg-surface-raised/40 border border-border rounded-lg">
          <ToolBtn icon="refresh" title="Refresh — regenerate compose from config & deploy"
            onClick={() => handleAction('refresh')} className="text-content-subtle hover:text-sky-400" />
          <ToolBtn icon="restart" title="Restart the existing containers in place" disabled={!hasContainers}
            onClick={() => handleAction('restart')} className="text-content-subtle hover:text-success-fg" />
          <ToolBtn icon="down" title="Inactivate — remove containers (keeps volumes)" disabled={!hasContainers}
            onClick={async () => {
              if (await confirm({
                title: `Inactivate ${envName}?`,
                message: 'Removes the containers (volumes are kept). Deploy brings it back.',
                confirmLabel: 'Inactivate',
              })) handleAction('down')
            }} className="text-danger-fg/50 hover:text-danger-fg" />
          <span className="w-px self-stretch bg-surface-raised mx-1" />
          <ToolBtn icon="vars" title="Edit env vars" onClick={onConfig} className="text-content-subtle hover:text-violet-400" />
          <ToolBtn icon="compose" title="View Compose" onClick={onCompose} className="text-content-subtle hover:text-teal-400" />
          <ToolBtn icon="terminal" title="Open a terminal" disabled={!isRunning}
            onClick={() => onTerminal()} className="text-content-subtle hover:text-emerald-400" />
          <ToolBtn icon="backup" title="Back up this environment" disabled={!isRunning}
            onClick={() => setBackupModal(true)} className="text-content-subtle hover:text-indigo-400" />
        </div>
      </div>

      {/* Collapsible detail — metrics strip + services panel. Always mounted so a
          grid-template-rows transition can animate the height open/closed; the
          inner div is clipped to 0 height while collapsed. The animated region and
          its handle are one flex child so a collapsed card doesn't reserve the
          card's gap twice. */}
      {hasCollapsible && (
        <div className="-mt-1">
          <div className={`grid transition-[grid-template-rows] duration-300 ease-out ${expanded ? 'grid-rows-[1fr]' : 'grid-rows-[0fr]'}`}>
            <div className="overflow-hidden">
              <div className="flex flex-col gap-4 pb-1">
                {/* Resource history sparklines (Phase 6d). */}
                {showMetrics && (
                <div className="border-t border-border/60 pt-3">
          {/* Time-range selector — muted links, brighter on hover, active highlighted. */}
          <div className="flex items-center flex-wrap gap-x-2.5 gap-y-1 mb-2 text-[11px]">
            {METRIC_RANGES.map(r => (
              <button key={r.v} type="button" onClick={() => setRangeMin(r.v)}
                className={`transition-colors ${rangeMin === r.v ? 'text-content font-medium' : 'text-content-faint hover:text-content-muted'}`}>
                {r.label}
              </button>
            ))}
          </div>
          <div className="grid grid-cols-2 2xl:grid-cols-4 gap-3">
            <MetricTile label="CPU"     value={lastMetric ? `${lastMetric.cpu_pct.toFixed(1)}%` : '—'} series={cpuSeries}     stroke="#22d3ee" />
            <MetricTile label="Memory"  value={lastMetric ? fmtBytes(lastMetric.memory_bytes) : '—'}   series={memSeries}     stroke="#a78bfa" />
            <MetricTile label="Disk"    value={lastMetric ? fmtBytes(lastMetric.disk_bytes) : '—'}     series={diskSeries}    stroke="#34d399" />
            <MetricTile label="Network" value={lastMetric ? fmtRate(lastNetRate) : '—'}                series={netRateSeries} stroke="#fbbf24" />
          </div>
        </div>
      )}

                {/* Backup summary (Phase 11e) — count, total size, retention. */}
                <BackupStatsLine name={name} envName={envName} />

                {/* Container health panel — has its own inner toggle for the list. */}
                {serviceRows.length > 0 && (
                <div className="border-t border-border/60 pt-3">
          {/* Panel header / toggle */}
          <button
            type="button"
            onClick={() => setContainersOpen(o => !o)}
            className="flex items-center justify-between w-full group mb-2"
          >
            <span className="text-xs font-semibold text-content-subtle uppercase tracking-wider">
              Services
              <span className="ml-1.5 font-normal normal-case text-content-faint">
                ({serviceRows.filter(c => c.State === 'running').length}/{serviceRows.length})
              </span>
            </span>
            <span className="text-content-faint group-hover:text-content-muted text-xs transition-colors">
              {containersOpen ? '▲' : '▼'}
            </span>
          </button>

          {containersOpen && (
            <div className="space-y-1.5">
              {serviceRows.map(c => {
                const dotCls    = c.State ? containerDotClass(c) : 'bg-surface-overlay'
                const txtCls    = c.State ? containerTxtClass(c) : 'text-content-faint'
                const label     = c.State ? containerStatusLabel(c) : 'Not started'
                const isNeutral = !c.State || (c.State === 'running' && (c.Health === 'healthy' || c.Health === ''))
                const isRunning = c.State === 'running'
                const upd       = updateByService[c.short]
                return (
                  <div key={c.short} className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-1.5 min-w-0">
                      <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${dotCls}`} />
                      <span className="text-xs text-content-muted font-mono truncate">{c.short}</span>
                    </div>
                    <div className="flex items-center gap-1.5 shrink-0">
                      <span className={`text-xs ${isNeutral ? 'text-content-faint' : txtCls}`}>
                        {c.State === 'running' && c.Health ? `${c.State} · ${label}` : label}
                      </span>
                      {/* Per-container actions */}
                      <div className="flex items-center gap-0.5 ml-1 border-l border-border pl-1">
                        {/* Info, Terminal, Files, Logs and Restart need a live
                            container — only shown while running. Start/Stop
                            toggles by state. */}
                        {isRunning && (
                          <CtlBtn title="Info" onClick={() => setInfoFor({ service: c.Service, short: c.short })}>
                            <svg viewBox="0 0 20 20" className="w-3 h-3 inline-block align-middle" fill="currentColor" aria-hidden="true">
                              <path fillRule="evenodd" clipRule="evenodd" d="M18 10A8 8 0 11 2 10a8 8 0 0116 0zm-7-4a1 1 0 11-2 0 1 1 0 012 0zM9 9a1 1 0 000 2v3a1 1 0 001 1h1a1 1 0 100-2v-3a1 1 0 00-1-1H9z" />
                            </svg>
                          </CtlBtn>
                        )}
                        {isRunning && (
                          <CtlBtn title="Terminal" onClick={() => onTerminal(c.Service)}>
                            <svg viewBox="0 0 20 20" className="w-3 h-3 inline-block align-middle" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                              <path d="M5 6l4 4-4 4" />
                              <path d="M11 14h5" />
                            </svg>
                          </CtlBtn>
                        )}
                        {isRunning && (
                          <CtlBtn title="Files" onClick={() => setFilesFor({ service: c.Service, short: c.short })}>
                            <svg viewBox="0 0 20 20" className="w-3 h-3 inline-block align-middle" fill="currentColor" aria-hidden="true">
                              <path d="M2 5a2 2 0 012-2h3.2l1.6 1.6H16a2 2 0 012 2v6.8a2 2 0 01-2 2H4a2 2 0 01-2-2V5z" />
                            </svg>
                          </CtlBtn>
                        )}
                        {isRunning && (
                          <CtlBtn title="Logs" onClick={() => onLogs(c.short)}>▤</CtlBtn>
                        )}
                        {isRunning
                          ? <CtlBtn title="Stop" className="text-content-faint hover:text-danger-fg" onClick={() => handleAction('stop', [c.Service])}>■</CtlBtn>
                          : <CtlBtn title="Start" className="text-content-faint hover:text-info-fg" onClick={() => handleAction('start', [c.Service])}>
                              <svg viewBox="0 0 10 10" className="w-2.5 h-2.5 inline-block align-middle" fill="currentColor" aria-hidden="true">
                                <path d="M2 1.5L8.5 5L2 8.5Z" />
                              </svg>
                            </CtlBtn>}
                        {isRunning && (
                          <CtlBtn title="Restart" className="text-content-faint hover:text-success-fg" onClick={() => handleAction('restart', [c.Service])}>⟳</CtlBtn>
                        )}
                        {/* Update icon doubles as the indicator: pulses amber when an
                            update is available, muted when the digest can't be compared. */}
                        {isImage && (
                          <CtlBtn
                            title={upd?.has_update ? `Update available: ${upd.newer_tag} — pull & recreate`
                              : upd?.indeterminate ? 'Cannot compare digest — pull latest & recreate'
                              : 'Update image (pull & recreate)'}
                            className={upd?.has_update ? 'text-warning-fg hover:text-warning-fg animate-pulse'
                              : upd?.indeterminate ? 'text-content-subtle hover:text-content'
                              : 'text-content-faint hover:text-content'}
                            onClick={() => handleAction('update', [c.Service])}>
                            ↑
                          </CtlBtn>
                        )}
                      </div>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
                </div>
                )}
              </div>
            </div>
          </div>

          {/* Sleek expand/collapse handle — a full-width strip flush with the card's
              bottom edge, chevron tucked close to the border. Points down when
              collapsed, rotates up when expanded. */}
          <button
            type="button"
            onClick={toggleExpanded}
            title={expanded ? 'Collapse details' : 'Expand details'}
            aria-expanded={expanded}
            className="-mx-5 -mb-5 mt-1 flex w-[calc(100%+2.5rem)] items-center justify-center rounded-b-xl border-t border-border/60 py-1 text-content-faint hover:bg-surface-raised/40 hover:text-content transition-colors"
          >
            <svg viewBox="0 0 16 16" className={`w-3.5 h-3.5 transition-transform duration-300 ${expanded ? 'rotate-180' : ''}`}
              fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M4 6l4 4 4-4" />
            </svg>
          </button>
        </div>
      )}

      {/* Per-container Info inspector */}
      {infoFor && (
        <ContainerInfoModal workspace={workspace} wsName={name} env={envName} service={infoFor.service} short={infoFor.short}
          onClose={() => setInfoFor(null)} />
      )}

      {/* Per-container file browser */}
      {filesFor && (
        <FileBrowserModal workspace={workspace} wsName={name} env={envName} service={filesFor.service} short={filesFor.short}
          onClose={() => setFilesFor(null)} />
      )}

      {/* Manual backup — pick which services' data to include */}
      {backupModal && (
        <ManualBackupModal
          name={name} envName={envName}
          onClose={() => setBackupModal(false)}
          onRun={(services) => { setBackupModal(false); handleAction('backup', [], services) }}
        />
      )}

    </div>
  )
}

// ManualBackupModal — choose which services' data to include in a one-off backup.
function ManualBackupModal({ name, envName, onClose, onRun }) {
  const { workspace } = useParams()
  const { data: services = [], isLoading } = useQuery({
    queryKey: ['backup-services', workspace, name, envName],
    queryFn: () => fetchBackupServices(workspace, name, envName),
    retry: false,
  })
  const [selected, setSelected] = useState(null) // null = not yet initialized
  // Default: all services selected.
  const sel = selected ?? services.map(s => s.id)
  const toggle = (id) => {
    const cur = selected ?? services.map(s => s.id)
    setSelected(cur.includes(id) ? cur.filter(x => x !== id) : [...cur, id])
  }
  // Sending all services == empty filter (back up everything).
  const servicesArg = sel.length === services.length ? [] : sel

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-sm p-5" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-1">
          <h3 className="font-semibold text-content-strong">Back up {envName}</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl leading-none">×</button>
        </div>
        <p className="text-xs text-content-subtle mb-3">Choose which services' data to include.</p>

        {isLoading ? (
          <p className="text-sm text-content-subtle py-4">Loading services…</p>
        ) : services.length === 0 ? (
          <p className="text-sm text-content-subtle py-4">No data-bearing services detected. A backup will capture any volumes found.</p>
        ) : (
          <div className="space-y-1.5 max-h-64 overflow-y-auto">
            {services.map(s => (
              <label key={s.id} className="flex items-center gap-2.5 px-2 py-1.5 rounded-lg hover:bg-surface-raised/60 cursor-pointer">
                <input type="checkbox" checked={sel.includes(s.id)} onChange={() => toggle(s.id)} className="accent-brand-500" />
                <span className="flex-1 min-w-0">
                  <span className="text-sm text-content">{s.label}{s.kind === 'database' ? ' 🗄' : ''}</span>
                  {s.hint && <span className="block text-[11px] text-content-faint">{s.hint}</span>}
                </span>
              </label>
            ))}
          </div>
        )}

        <div className="flex justify-end gap-2 mt-4">
          <button onClick={onClose} className="px-3 py-1.5 text-sm rounded-lg border border-border-strong text-content-muted hover:text-content">Cancel</button>
          <button
            onClick={() => onRun(servicesArg)}
            disabled={!isLoading && services.length > 0 && sel.length === 0}
            className="px-4 py-1.5 text-sm rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white font-medium"
          >
            Back up
          </button>
        </div>
      </div>
    </div>
  )
}

function DetailRow({ icon, value }) {
  return (
    <div className="flex items-center gap-2">
      <span className="text-xs text-content-faint">{icon}</span>
      <span className="truncate">{value}</span>
    </div>
  )
}

// ── Release pipeline ──────────────────────────────────────────────────────────

function ReleasePipeline({ ws }) {
  const isImage = ws?.config?.project?.type === 'image'
  if (isImage) return null

  const v = ws?.config?.project?.version
  const vStr = v ? `v${v.major}.${v.minor}.${v.patch}-build.${v.build}` : '—'
  const envs = ws?.envs || []

  const steps = [
    { label: 'dev build',    status: 'done',    version: vStr },
    { label: 'stage build',  status: 'done',    version: vStr },
    { label: 'stage deploy', status: 'active',  version: null },
    { label: 'QA sign-off',  status: 'pending', version: null },
    { label: 'promote → prod', status: 'pending', version: null },
  ]

  const stepStyle = {
    done:    'bg-green-500 border-success text-green-900',
    active:  'bg-amber-400 border-warning text-amber-900 animate-pulse',
    pending: 'bg-surface-raised border-border-strong text-content-subtle',
  }

  return (
    <div className="bg-surface border border-border rounded-xl p-5">
      <h2 className="text-sm font-semibold text-content mb-5 flex items-center gap-2">
        <span className="text-xs">○</span> Release pipeline
      </h2>

      <div className="flex items-center gap-0 mb-5 overflow-x-auto pb-2">
        {steps.map((step, i) => (
          <div key={step.label} className="flex items-center">
            <div className="flex flex-col items-center gap-1.5 min-w-[90px]">
              <div className={`w-9 h-9 rounded-full border-2 flex items-center justify-center text-xs font-bold ${stepStyle[step.status]}`}>
                {step.status === 'done' ? '✓' : step.status === 'active' ? '◎' : '○'}
              </div>
              <span className={`text-xs text-center leading-tight ${step.status === 'pending' ? 'text-content-faint' : 'text-content'}`}>
                {step.label}
              </span>
              {step.version && (
                <span className="text-xs text-content-subtle font-mono">{step.version}</span>
              )}
              {step.status === 'active' && (
                <span className="text-xs text-warning-fg">in progress</span>
              )}
              {step.status === 'pending' && (
                <span className="text-xs text-content-faint">—</span>
              )}
            </div>
            {i < steps.length - 1 && (
              <div className={`h-0.5 w-8 shrink-0 mx-1 ${i < 2 ? 'bg-green-500' : 'bg-surface-overlay'}`} />
            )}
          </div>
        ))}
      </div>

      <div className="flex items-center justify-between bg-surface-raised/60 rounded-lg px-4 py-3">
        <p className="text-sm text-content">
          Ready to promote? <span className="font-mono text-content-strong">{vStr}</span> will be retagged and deployed to prod — no rebuild.
        </p>
        <button className="ml-4 shrink-0 bg-surface-overlay hover:bg-surface-overlay text-content hover:text-content-strong text-sm font-medium px-4 py-2 rounded-lg transition-colors flex items-center gap-1.5">
          <span className="text-xs">○</span> Promote to prod
        </button>
      </div>
    </div>
  )
}

// ── Inline action log — streams output from Deploy/Stop/Restart/Backup etc. ──

// ActionLog — a per-workspace history of action runs, loaded from the server
// (the action_runs table). Each run is rendered as a header (action · env ·
// user · timestamp), its captured output, and a result footer (✓/✗). A live run
// streams in over the action WebSocket and is also recorded server-side, so it
// reappears from the DB on the next load. A dropdown limits how many trailing
// lines are shown.
const MEM_CAP      = 8000                          // in-memory display cap
const TAIL_OPTIONS = [100, 250, 500, 1000, 2000, 0] // 0 = All

function fmtTs(ts) {
  const d = new Date(ts)
  const p = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// Flatten server run records (newest-first) into chronological display entries.
function runsToEntries(runs) {
  const out = []
  for (const run of [...(runs || [])].reverse()) {
    const extra = run.extra ? ` ${run.extra}` : ''
    out.push({ type: 'header', action: (run.command || 'action') + extra, env: run.env || '',
      user: run.username || 'unknown', ts: run.started_at })
    String(run.output || '').split(/\r?\n/).filter(l => l !== '').forEach(text => out.push({ type: 'out', text }))
    out.push({ type: 'result', ok: run.status === 'ok', ts: run.finished_at })
  }
  return out
}

// NOTE: mounted with key={wsName} by the parent, so it remounts per workspace.
function ActionLog({ wsName, actionWs, actionMeta }) {
  const { workspace } = useParams()
  const confirm = useConfirm()
  const [entries, setEntries] = useState([])
  const [running, setRunning] = useState(false)
  const [tail, setTail] = useState(() => {
    const v = Number(localStorage.getItem('rigger:actionlog:tail'))
    return TAIL_OPTIONS.includes(v) ? v : 500
  })
  const scrollRef = useRef(null)
  const wiredRef  = useRef(null) // last socket we attached listeners to (de-dupe)

  // Load recorded history from the server. Also wired to the header Refresh
  // button, since there's no auto-refresh (another window or the CLI may have
  // recorded runs since this view loaded).
  const loadHistory = () => {
    fetchActionRuns(workspace, wsName, 100).then(runs => setEntries(runsToEntries(runs))).catch(() => {})
  }
  useEffect(() => { loadHistory() }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Wire a freshly-started action: append a header, stream output, then a result.
  // The server records the same run, so it persists across reloads.
  useEffect(() => {
    if (!actionWs || wiredRef.current === actionWs) return
    wiredRef.current = actionWs
    const meta  = actionMeta || {}
    const extra = meta.extra && meta.extra.length ? ` ${meta.extra.join(' ')}` : ''
    const cap   = arr => arr.length > MEM_CAP ? arr.slice(-MEM_CAP) : arr
    setEntries(prev => cap([...prev, {
      type: 'header', action: (meta.cmd || 'action') + extra, env: meta.env || '',
      user: meta.user || 'unknown', ts: meta.ts || Date.now(),
    }]))
    setRunning(true)

    const acc = []
    const onMsg = e => {
      const newLines = String(e.data || '').split(/\r?\n/).filter(l => l !== '')
      if (!newLines.length) return
      acc.push(...newLines)
      setEntries(prev => cap([...prev, ...newLines.map(text => ({ type: 'out', text }))]))
    }
    const onEnd = () => {
      // The backend ends with a green ✓ or red ✗ marker line; treat ✗ as failure.
      const ok = !acc.some(l => l.includes('✗'))
      setEntries(prev => cap([...prev, { type: 'result', ok, ts: Date.now() }]))
      setRunning(false)
      actionWs.removeEventListener('message', onMsg)
    }
    actionWs.addEventListener('message', onMsg)
    actionWs.addEventListener('close', onEnd, { once: true })
    actionWs.addEventListener('error', () => setRunning(false), { once: true })
  }, [actionWs, actionMeta])

  // Keep the output container (not the page) scrolled to the newest line.
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [entries])

  function clearLog() {
    setEntries([])
    clearActionRuns(workspace, wsName).catch(() => {})
  }

  const shown = tail > 0 ? entries.slice(-tail) : entries

  return (
    <div className="relative bg-surface border border-border rounded-xl flex flex-col overflow-hidden" style={{ height: 380 }}>
      <div className="flex items-center justify-between px-4 py-3 border-b border-border shrink-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-content">Action output</span>
          {running && <span className="w-1.5 h-1.5 rounded-full bg-green-400 animate-pulse" />}
        </div>
        <div className="flex items-center gap-3">
          <label className="flex items-center gap-1 text-xs text-content-subtle">
            Lines
            <select
              value={tail}
              onChange={e => { const v = Number(e.target.value); setTail(v); try { localStorage.setItem('rigger:actionlog:tail', String(v)) } catch {} }}
              className="bg-surface-raised border border-border-strong text-content rounded px-1.5 py-0.5 focus:outline-none focus:border-brand-500"
            >
              {TAIL_OPTIONS.map(n => <option key={n} value={n}>{n === 0 ? 'All' : n}</option>)}
            </select>
          </label>
          <button onClick={loadHistory} title="Refresh history from the server"
            className="p-1 rounded text-content-subtle hover:text-content hover:bg-surface-raised transition-colors">
            <svg viewBox="0 0 24 24" className="w-3.5 h-3.5" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M21 12a9 9 0 1 1-2.64-6.36" /><path d="M21 3v5h-5" />
            </svg>
          </button>
          {entries.length > 0 && (
            <button
              onClick={async () => {
                if (await confirm({
                  title: 'Delete action history?',
                  message: "Permanently removes the recorded runs for this workspace from the server. This can't be undone.",
                  confirmLabel: 'Delete',
                })) clearLog()
              }}
              title="Delete recorded history"
              className="p-1 rounded text-content-subtle hover:text-danger-fg hover:bg-surface-raised transition-colors">
              <svg viewBox="0 0 24 24" className="w-3.5 h-3.5" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M4 6h16M9 6V4h6v2M7 6l1 14h8l1-14" /><path d="M10 10v6M14 10v6" />
              </svg>
            </button>
          )}
        </div>
      </div>

      <div ref={scrollRef} className="flex-1 overflow-y-auto p-3 font-mono text-xs leading-relaxed bg-canvas/60 min-h-0">
        {entries.length === 0 ? (
          <p className="text-content-faint pt-2">
            Run Deploy, Stop, Restart, Backup, Update or other actions — output is recorded here per workspace.
          </p>
        ) : (
          shown.map((it, i) =>
            it.type === 'header' ? (
              <div key={i} className="mt-3 first:mt-0 flex flex-wrap items-center gap-x-2 border-t border-border pt-2">
                <span className="text-brand-300 font-semibold">▶ {it.action}{it.env ? ` · ${it.env}` : ''}</span>
                <span className="text-content-faint">·</span>
                <span className="text-content-subtle">{it.user}</span>
                <span className="text-content-faint">·</span>
                <span className="text-content-subtle">{fmtTs(it.ts)}</span>
              </div>
            ) : it.type === 'result' ? (
              <div key={i} className={`mb-1 ${it.ok ? 'text-success-fg' : 'text-danger-fg'}`}>
                {it.ok ? '✓ Completed' : '✗ Failed'} <span className="text-content-faint">· {fmtTs(it.ts)}</span>
              </div>
            ) : (
              <div key={i} dangerouslySetInnerHTML={{ __html: ansiToHtml(it.text) }} />
            )
          )
        )}
      </div>
    </div>
  )
}

function capitalize(s) {
  return s ? s[0].toUpperCase() + s.slice(1) : ''
}

// ── Log line colouring by service ─────────────────────────────────────────────

// Palette of colours that read well on a dark (#030712) background
const SERVICE_COLORS = [
  '#22d3ee', // cyan-400
  '#f472b6', // pink-400   (index 1: high-contrast vs cyan so the first two services are easy to tell apart)
  '#fb923c', // orange-400
  '#c084fc', // purple-400
  '#4ade80', // green-400
  '#fbbf24', // amber-400
  '#60a5fa', // blue-400
  '#f87171', // red-400
  '#34d399', // emerald-400
  '#a78bfa', // violet-400
]

// Build a colour map that guarantees each service in the stack gets a unique colour.
// Colours are assigned in the order services appear in the container list.
// Falls back to hash-based assignment for any service not in the list (e.g. log lines
// from services that have since been removed).
function buildColorMap(containers, wsName, activeEnv) {
  const prefix = `${wsName}_${activeEnv}_`
  const map = {}
  let idx = 0
  for (const c of (containers || [])) {
    if (!(c.Service in map)) {
      map[c.Service] = SERVICE_COLORS[idx % SERVICE_COLORS.length]
      idx++
    }
  }
  return map
}

function serviceColorFallback(name, colorMap) {
  if (colorMap && colorMap[name]) return colorMap[name]
  // Fallback for log lines whose service name doesn't match any known container
  let h = 0
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) & 0xffff
  return SERVICE_COLORS[h % SERVICE_COLORS.length]
}

// Docker Compose log prefix: "service_name  | content"
// Capture everything before the first " | " as the service name.
const LOG_PREFIX_RE = /^([\w.-]+)\s+\|\s/

function parseLogLine(raw) {
  const m = raw.match(LOG_PREFIX_RE)
  if (m) return { svc: m[1], content: raw.slice(m[0].length) }
  return { svc: null, content: raw }
}

// ── Shared log connection hook ─────────────────────────────────────────────────

// activeContainers: string[] — empty = all containers, non-empty = specific services
function useLogStream({ workspace, wsName, activeEnv, activeContainers = [], token, maxLines = 2000 }) {
  const [lines, setLines]   = useState([])
  const [paused, setPaused] = useState(false)
  const wsRef    = useRef(null)
  const pausedRef = useRef(false)

  // Keep ref in sync with state so the WS message handler always sees current value
  useEffect(() => { pausedRef.current = paused }, [paused])

  const containerLabel = activeContainers.length === 0 ? '' : ` / ${activeContainers.join(', ')}`

  const connect = useCallback(() => {
    if (wsRef.current) { wsRef.current.close(); wsRef.current = null }
    setPaused(false)
    pausedRef.current = false
    setLines([`\x1b[2m--- connecting to ${activeEnv}${containerLabel} logs ---\x1b[0m`])
    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(`${proto}://${window.location.host}/api/workspaces/${workspace}/projects/${wsName}/action`)
    wsRef.current = ws
    ws.addEventListener('open', () => {
      ws.send(JSON.stringify({ token, command: 'logs', env: activeEnv, extra: activeContainers }))
    })
    ws.addEventListener('message', e => {
      if (pausedRef.current) return           // stream frozen — discard incoming
      const newLines = (e.data || '').split(/\r?\n/).filter(l => l !== '')
      setLines(prev => { const next = [...prev, ...newLines]; return next.length > maxLines ? next.slice(-maxLines) : next })
    })
    ws.addEventListener('close', () => setLines(prev => [...prev, '\x1b[2m--- stream closed ---\x1b[0m']))
    ws.addEventListener('error', () => setLines(prev => [...prev, '\x1b[31m--- connection error ---\x1b[0m']))
  }, [workspace, wsName, activeEnv, activeContainers.join(','), token, maxLines]) // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => { connect(); return () => wsRef.current?.close() }, [connect])

  return { lines, setLines, connect, paused, setPaused }
}

// ── Container multi-selector (shared between inline + modal) ──────────────────
// activeContainers: string[] — empty = all, non-empty = specific set
// onSelect: (string[]) => void

// Returns Tailwind classes for the status dot based on State + Health
function containerDotClass(c) {
  const health = (c.Health || '').toLowerCase()
  switch (c.State) {
    case 'running':
      if (health === 'unhealthy') return 'bg-red-500'
      if (health === 'starting')  return 'bg-amber-400 animate-pulse'
      return 'bg-green-400'
    case 'restarting': return 'bg-amber-400 animate-pulse'
    case 'paused':     return 'bg-amber-400'
    case 'exited':
    case 'dead':       return 'bg-red-500'
    case 'created':    return 'bg-surface-overlay'
    default:           return 'bg-surface-overlay'
  }
}

function containerTxtClass(c) {
  const health = (c.Health || '').toLowerCase()
  if (c.State === 'running') {
    if (health === 'unhealthy') return 'text-danger-fg'
    if (health === 'starting')  return 'text-warning-fg'
    return 'text-success-fg'
  }
  if (c.State === 'restarting' || c.State === 'paused') return 'text-warning-fg'
  if (c.State === 'exited' || c.State === 'dead')       return 'text-danger-fg'
  return 'text-content-muted'
}

// Human-readable status label for a container
function containerStatusLabel(c) {
  const health = (c.Health || '').toLowerCase()
  if (c.State === 'running') {
    if (health === 'unhealthy') return 'Unhealthy'
    if (health === 'starting')  return 'Starting…'
    if (health === 'healthy')   return 'Healthy'
    return 'Running'
  }
  if (c.State === 'restarting') return 'Restarting'
  if (c.State === 'paused')     return 'Paused'
  if (c.State === 'exited')     return 'Exited'
  if (c.State === 'dead')       return 'Dead'
  if (c.State === 'created')    return 'Created'
  return c.State || 'Unknown'
}

function ContainerSelector({ containers, workspace, wsName, activeEnv, activeContainers, onSelect, colorMap }) {
  if (!(containers || []).length) return null

  const allSelected = activeContainers.length === 0

  function toggleAll() { onSelect([]) }

  function toggleOne(name) {
    if (allSelected) {
      // Was "all" → select only this one
      onSelect([name])
    } else if (activeContainers.includes(name)) {
      const next = activeContainers.filter(n => n !== name)
      onSelect(next.length ? next : []) // if unchecking the last one → back to all
    } else {
      onSelect([...activeContainers, name])
    }
  }

  const shortNames = (containers || []).map(c => {
    const prefix = `${workspace}_${wsName}_${activeEnv}_`
    const short = c.Service.startsWith(prefix) ? c.Service.slice(prefix.length) : c.Service
    // Full service name as it appears in compose logs (used for color lookup)
    return { c, short, fullSvc: c.Service }
  })

  return (
    <div className="flex items-center gap-3 px-3 py-2 border-b border-border/40 shrink-0 overflow-x-auto">
      {/* All checkbox */}
      <label className="flex items-center gap-1.5 cursor-pointer shrink-0 select-none">
        <input type="checkbox" checked={allSelected} onChange={toggleAll}
          className="accent-brand-500 w-3 h-3" />
        <span className={`text-xs ${allSelected ? 'text-content-strong' : 'text-content-subtle'}`}>all</span>
      </label>
      <span className="w-px h-3 bg-surface-overlay shrink-0" />
      {shortNames.map(({ c, short, fullSvc }) => {
        const checked   = allSelected || activeContainers.includes(short)
        const color     = serviceColorFallback(fullSvc, colorMap)
        return (
          <label key={c.Name} className="flex items-center gap-1.5 cursor-pointer shrink-0 select-none"
            title={`${short} — ${containerStatusLabel(c)}`}>
            <input type="checkbox" checked={checked} onChange={() => toggleOne(short)}
              className="accent-brand-500 w-3 h-3" />
            {/* Status dot — colour reflects health, not just state */}
            <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${containerDotClass(c)}`} />
            {/* Service name is rendered in its log colour, so no separate swatch is needed */}
            <span className="text-xs" style={{ color: checked ? color : '#4b5563' }}>{short}</span>
          </label>
        )
      })}
    </div>
  )
}

// ── Log output panel (shared between inline + modal) ──────────────────────────
// rowLimit: 0 = unlimited, N = show last N filtered lines
// showRowNumbers: prefix each line with its sequential number

function LogOutput({ lines, filter, wrap, autoScroll, rowLimit = 0, showRowNumbers = false, colorMap }) {
  const scrollRef = useRef(null)

  const filtered = filter.trim()
    ? lines.filter(l => l.toLowerCase().includes(filter.toLowerCase()))
    : lines

  const displayed = rowLimit > 0 && filtered.length > rowLimit
    ? filtered.slice(-rowLimit)
    : filtered

  const rowOffset = filtered.length - displayed.length

  // Scroll the log container itself (not scrollIntoView, which would also scroll
  // the page/main and yank the whole window down on every refresh).
  useEffect(() => {
    if (!autoScroll) return
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines, autoScroll])

  return (
    <div ref={scrollRef} className={`flex-1 overflow-y-auto p-3 font-mono text-xs leading-relaxed bg-canvas/60 ${wrap ? 'break-all' : 'overflow-x-auto whitespace-nowrap'}`}>
      {displayed.map((line, i) => {
        const { svc, content } = parseLogLine(line)
        const color = svc ? serviceColorFallback(svc, colorMap) : null
        return (
          <div key={rowOffset + i} className="flex items-start gap-0">
            {showRowNumbers && (
              <span className="text-content-faint select-none shrink-0 w-10 text-right mr-2">{rowOffset + i + 1}</span>
            )}
            {svc && (
              <span
                className="shrink-0 mr-1 select-none"
                style={{ color, opacity: 0.85 }}
              >{svc} <span style={{ color: '#4b5563' }}>|</span> </span>
            )}
            <span dangerouslySetInnerHTML={{ __html: ansiToHtml(content) }} />
          </div>
        )
      })}
    </div>
  )
}

// ── Maximized log modal ────────────────────────────────────────────────────────

const ROW_LIMIT_OPTIONS = [
  { label: 'All',  value: 0    },
  { label: '100',  value: 100  },
  { label: '500',  value: 500  },
  { label: '1 000', value: 1000 },
  { label: '5 000', value: 5000 },
]

function LogModal({ wsName, envs, initialEnv, initialContainers, onClose }) {
  const { workspace } = useParams()
  const token = useAuthStore(s => s.token)
  const [activeEnv, setActiveEnv]           = useState(initialEnv)
  const [activeContainers, setContainers]   = useState(initialContainers || [])
  const [filter, setFilter]                 = useState('')
  const [wrap, setWrap]                     = useState(false)
  const [autoScroll, setAutoScroll]         = useState(true)
  const [rowLimit, setRowLimit]             = useState(0)
  const [showRowNumbers, setShowRowNumbers] = useState(false)

  const { data: containers } = useQuery({
    queryKey: ['containers', workspace, wsName, activeEnv, 'modal'],
    queryFn:  () => fetchContainers(workspace, wsName, activeEnv),
    enabled:  !!activeEnv, refetchInterval: 15_000, retry: false,
  })

  const { lines, setLines, connect, paused, setPaused } =
    useLogStream({ workspace, wsName, activeEnv, activeContainers, token, maxLines: 10000 })

  function switchEnv(env) { setActiveEnv(env); setContainers([]) }

  // Sequential colour map — built from the known container list so no two services share a colour
  const colorMap = buildColorMap(containers, wsName, activeEnv)

  const filtered = filter.trim() ? lines.filter(l => l.toLowerCase().includes(filter.toLowerCase())) : lines
  const displayedCount = rowLimit > 0 ? Math.min(rowLimit, filtered.length) : filtered.length

  function stripAnsi(s) { return s.replace(/\x1b\[[0-9;]*m/g, '') }

  function copyAll() {
    const src = rowLimit > 0 ? filtered.slice(-rowLimit) : filtered
    const plain = src.map((l, i) => showRowNumbers ? `${filtered.length - src.length + i + 1} | ${stripAnsi(l)}` : stripAnsi(l)).join('\n')
    navigator.clipboard.writeText(plain).catch(() => {})
  }

  function download() {
    const src = rowLimit > 0 ? filtered.slice(-rowLimit) : filtered
    const plain = src.map((l, i) => showRowNumbers ? `${filtered.length - src.length + i + 1} | ${stripAnsi(l)}` : stripAnsi(l)).join('\n')
    const blob = new Blob([plain], { type: 'text/plain' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${wsName}-${activeEnv}${activeContainers.length ? '-' + activeContainers.join('+') : ''}-logs.txt`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  useEffect(() => {
    function handler(e) { if (e.key === 'Escape') onClose() }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [onClose])

  const toggleBtn = (active, onClick, label, title) => (
    <button onClick={onClick} title={title}
      className={`text-xs px-2 py-1 rounded border transition-colors shrink-0 ${active ? 'border-brand-600 text-brand-400 bg-brand-950' : 'border-border-strong text-content-subtle hover:text-content'}`}>
      {label}
    </button>
  )

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-canvas" style={{ fontFamily: 'inherit' }}>
      {/* ── Top bar ── */}
      <div className="flex items-center gap-2 px-4 py-2 border-b border-border bg-surface shrink-0 flex-wrap">
        <h2 className="text-sm font-semibold text-content shrink-0">Logs</h2>

        {/* Env tabs */}
        <div className="flex items-center gap-1 overflow-x-auto shrink-0">
          {envs.map(env => (
            <button key={env} onClick={() => switchEnv(env)}
              className={`px-3 py-1 text-xs font-medium rounded-md transition-colors shrink-0 ${activeEnv === env ? 'bg-brand-600 text-white' : 'text-content-muted hover:text-white hover:bg-surface-raised'}`}
            >{env}</button>
          ))}
        </div>

        <span className="w-px h-4 bg-surface-overlay shrink-0" />

        {/* Filter */}
        <div className="relative shrink-0">
          <input type="text" value={filter} onChange={e => setFilter(e.target.value)}
            placeholder="Filter lines…"
            className="w-44 px-3 py-1 text-xs bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle focus:outline-none focus:border-brand-500 font-mono" />
          {filter && <button onClick={() => setFilter('')} className="absolute right-2 top-1/2 -translate-y-1/2 text-content-subtle hover:text-content text-xs">×</button>}
        </div>

        {/* Line count */}
        <span className="text-xs text-content-faint shrink-0">
          {filter.trim() || rowLimit > 0
            ? `${displayedCount} / ${lines.length}`
            : `${lines.length}`} lines
        </span>

        <span className="w-px h-4 bg-surface-overlay shrink-0" />

        {/* Row limit */}
        <div className="flex items-center gap-1 shrink-0">
          <span className="text-xs text-content-faint">show</span>
          <select value={rowLimit} onChange={e => setRowLimit(Number(e.target.value))}
            className="text-xs bg-surface-raised border border-border-strong rounded px-1.5 py-1 text-content focus:outline-none focus:border-brand-500">
            {ROW_LIMIT_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </div>

        <span className="w-px h-4 bg-surface-overlay shrink-0" />

        {/* Toggle buttons */}
        {toggleBtn(wrap,           () => setWrap(v => !v),           'wrap',       'Toggle line wrap')}
        {toggleBtn(autoScroll,     () => setAutoScroll(v => !v),     '↓ auto',     'Toggle auto-scroll')}
        {toggleBtn(paused,         () => setPaused(v => !v),         paused ? '▶ resume' : '⏸ pause', 'Pause / resume stream')}
        {toggleBtn(showRowNumbers, () => setShowRowNumbers(v => !v), '# rows',     'Toggle row numbers')}

        <span className="w-px h-4 bg-surface-overlay shrink-0" />

        {/* Action buttons */}
        <button onClick={connect}       title="Reconnect"          className="text-xs text-content-subtle hover:text-content transition-colors shrink-0">↺</button>
        <button onClick={() => setLines([])} title="Clear buffer"  className="text-xs text-content-subtle hover:text-danger-fg transition-colors shrink-0">clear</button>
        <button onClick={copyAll}       title="Copy visible log"   className="text-xs text-content-subtle hover:text-content transition-colors shrink-0">⎘ copy</button>
        <button onClick={download}      title="Download as .txt"   className="text-xs text-content-subtle hover:text-content transition-colors shrink-0">⬇ download</button>

        <div className="flex-1" />
        <button onClick={onClose} title="Close (Esc)" className="text-content-subtle hover:text-content-strong transition-colors text-lg leading-none shrink-0">✕</button>
      </div>

      {/* Container multi-selector */}
      <ContainerSelector containers={containers} workspace={workspace} wsName={wsName} activeEnv={activeEnv}
        activeContainers={activeContainers} onSelect={setContainers} colorMap={colorMap} />

      {/* Pause banner */}
      {paused && (
        <div className="bg-warning-subtle/60 border-b border-warning-border/40 px-4 py-1.5 shrink-0 flex items-center gap-2">
          <span className="text-xs text-warning-fg font-medium">⏸ Stream paused — new log lines are being discarded</span>
          <button onClick={() => setPaused(false)} className="text-xs text-warning-fg hover:text-content-strong underline">Resume</button>
        </div>
      )}

      {/* Log output */}
      <LogOutput lines={lines} filter={filter} wrap={wrap} autoScroll={autoScroll}
        rowLimit={rowLimit} showRowNumbers={showRowNumbers} colorMap={colorMap} />
    </div>
  )
}

// ── Inline log viewer ─────────────────────────────────────────────────────────

function LogViewer({ wsName, envs }) {
  const { workspace } = useParams()
  const token = useAuthStore(s => s.token)
  const [activeEnv, setActiveEnv]         = useState(envs[0] || '')
  const [activeContainers, setContainers] = useState([])
  const [maximized, setMaximized]         = useState(false)
  const [filter, setFilter]               = useState('')
  const [autoScroll, setAutoScroll]       = useState(true)

  const { data: containers } = useQuery({
    queryKey: ['containers', workspace, wsName, activeEnv],
    queryFn:  () => fetchContainers(workspace, wsName, activeEnv),
    enabled:  !!activeEnv, refetchInterval: 15_000, retry: false,
  })

  const { lines, connect, paused, setPaused } =
    useLogStream({ workspace, wsName, activeEnv, activeContainers, token })

  function switchEnv(env) { setActiveEnv(env); setContainers([]) }

  const colorMap = buildColorMap(containers, wsName, activeEnv)

  return (
    <>
      <div className="bg-surface border border-border rounded-xl flex flex-col overflow-hidden" style={{ height: 380 }}>
        {/* Header */}
        <div className="flex items-center justify-between px-3 py-2.5 border-b border-border shrink-0 gap-2 flex-wrap">
          <h2 className="text-sm font-semibold text-content shrink-0">Logs</h2>

          <div className="flex items-center gap-2 flex-wrap">
            {/* Filter */}
            <div className="relative">
              <input type="text" value={filter} onChange={e => setFilter(e.target.value)}
                placeholder="filter…"
                className="w-28 px-2 py-0.5 text-xs bg-surface-raised border border-border-strong rounded text-content-strong placeholder-content-faint focus:outline-none focus:border-brand-500 font-mono" />
              {filter && <button onClick={() => setFilter('')} className="absolute right-1.5 top-1/2 -translate-y-1/2 text-content-subtle hover:text-content text-xs">×</button>}
            </div>

            {/* Auto-scroll checkbox */}
            <label className="flex items-center gap-1 cursor-pointer select-none shrink-0">
              <input type="checkbox" checked={autoScroll} onChange={e => setAutoScroll(e.target.checked)}
                className="accent-brand-500 w-3 h-3" />
              <span className="text-xs text-content-subtle">auto</span>
            </label>

            {/* Pause */}
            <button onClick={() => setPaused(v => !v)} title={paused ? 'Resume stream' : 'Pause stream'}
              className={`text-xs transition-colors shrink-0 ${paused ? 'text-warning-fg hover:text-warning-fg' : 'text-content-subtle hover:text-content'}`}>
              {paused ? '▶' : '⏸'}
            </button>

            <button onClick={connect} title="Reconnect" className="text-xs text-content-subtle hover:text-content transition-colors shrink-0">↺</button>
            <button onClick={() => setMaximized(true)} title="Maximize" className="text-xs text-content-subtle hover:text-content transition-colors shrink-0">⛶</button>
          </div>
        </div>

        {/* Env tabs */}
        <div className="flex items-center gap-1 px-3 py-2 border-b border-border/60 shrink-0 overflow-x-auto">
          {envs.map(env => (
            <button key={env} onClick={() => switchEnv(env)}
              className={`px-3 py-1 text-xs font-medium rounded-md transition-colors shrink-0 ${activeEnv === env ? 'bg-brand-600 text-white' : 'text-content-muted hover:text-white hover:bg-surface-raised'}`}
            >{env}</button>
          ))}
        </div>

        {/* Container multi-selector */}
        <ContainerSelector containers={containers} workspace={workspace} wsName={wsName} activeEnv={activeEnv}
          activeContainers={activeContainers} onSelect={setContainers} colorMap={colorMap} />

        {/* Pause banner */}
        {paused && (
          <div className="bg-warning-subtle/50 px-3 py-1 shrink-0 flex items-center gap-2 border-b border-warning-border/30">
            <span className="text-xs text-amber-500">⏸ paused</span>
            <button onClick={() => setPaused(false)} className="text-xs text-warning-fg hover:text-warning-fg underline">resume</button>
          </div>
        )}

        {/* Log output */}
        <LogOutput lines={lines} filter={filter} wrap={false} autoScroll={autoScroll} colorMap={colorMap} />
      </div>

      {/* Maximized modal — passes current env/container selection */}
      {maximized && (
        <LogModal
          wsName={wsName}
          envs={envs}
          initialEnv={activeEnv}
          initialContainers={activeContainers}
          onClose={() => setMaximized(false)}
        />
      )}
    </>
  )
}

// Minimal ANSI → HTML converter for the most common codes
function ansiToHtml(text) {
  const safe = text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')

  return safe
    .replace(/\x1b\[0m/g, '</span>')
    .replace(/\x1b\[1m/g, '<span style="font-weight:bold">')
    .replace(/\x1b\[2m/g, '<span style="opacity:0.5">')
    .replace(/\x1b\[31m/g, '<span style="color:#f87171">')
    .replace(/\x1b\[32m/g, '<span style="color:#4ade80">')
    .replace(/\x1b\[33m/g, '<span style="color:#fbbf24">')
    .replace(/\x1b\[34m/g, '<span style="color:#60a5fa">')
    .replace(/\x1b\[35m/g, '<span style="color:#c084fc">')
    .replace(/\x1b\[36m/g, '<span style="color:#22d3ee">')
    .replace(/\x1b\[37m/g, '<span style="color:#e5e7eb">')
    .replace(/\x1b\[[0-9;]*m/g, '') // strip remaining codes
}

// ── Env vars editor (modal) ───────────────────────────────────────────────────

function EnvVarsModal({ name, env, deployment, onClose }) {
  const { workspace } = useParams()
  const qc = useQueryClient()
  const swarm = deployment === 'swarm'
  const [reveal, setReveal] = useState(false)
  const [edits, setEdits]   = useState({})
  const [deletes, setDeletes] = useState(new Set())
  const [flags, setFlags]   = useState({}) // per-key secret-flag overrides
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [newSecret, setNewSecret] = useState(false)
  const [rotateKey, setRotateKey] = useState(null)
  const [rotateVal, setRotateVal] = useState('')
  const [showAudit, setShowAudit] = useState(false)

  const { data: vars, isLoading } = useQuery({
    queryKey: ['envvars', workspace, name, env, reveal],
    queryFn: () => fetchEnvVars(workspace, name, env, reveal),
  })

  // Effective secret flag for a key: a pending toggle wins, else the server value.
  const isSecret = (k) => (k in flags ? flags[k] : !!vars?.[k]?.secret)

  const mutation = useMutation({
    mutationFn: ({ updates, dels, secretKeys }) => updateEnvVars(workspace, name, env, updates, dels, secretKeys),
    onSuccess: () => {
      setEdits({}); setDeletes(new Set()); setFlags({})
      setNewKey(''); setNewVal(''); setNewSecret(false)
      qc.invalidateQueries({ queryKey: ['envvars', workspace, name, env] })
    },
  })

  const rotateMut = useMutation({
    mutationFn: ({ key, value }) => rotateSecret(workspace, name, env, key, value),
    onSuccess: () => {
      setRotateKey(null); setRotateVal('')
      qc.invalidateQueries({ queryKey: ['envvars', workspace, name, env] })
    },
  })

  function toggleDelete(k) {
    setDeletes(prev => { const n = new Set(prev); n.has(k) ? n.delete(k) : n.add(k); return n })
    setEdits(prev => { const n = { ...prev }; delete n[k]; return n })
  }

  function toggleFlag(k) {
    setFlags(prev => ({ ...prev, [k]: !isSecret(k) }))
  }

  function buildSecretKeys(includeNew) {
    const keys = new Set()
    for (const k of Object.keys(vars || {})) {
      if (!deletes.has(k) && isSecret(k)) keys.add(k)
    }
    if (includeNew && newKey.trim() && newSecret) keys.add(newKey.trim())
    return [...keys]
  }

  function handleSave() {
    const updates = { ...edits }
    if (newKey.trim()) updates[newKey.trim()] = newVal
    mutation.mutate({ updates, dels: [...deletes], secretKeys: buildSecretKeys(true) })
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-3">
          <h3 className="font-semibold text-content-strong">Env vars — {env}</h3>
          <div className="flex items-center gap-2">
            <button onClick={() => setShowAudit(s => !s)} title="Secret audit trail"
              className={`text-xs px-2 py-1 rounded transition-colors ${showAudit ? 'bg-surface-overlay text-content-strong' : 'text-content-subtle hover:text-content-strong hover:bg-surface-raised'}`}>🕓 Audit</button>
            <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl leading-none">×</button>
          </div>
        </div>

        {/* Deployment-aware secret status */}
        {swarm ? (
          <div className="mb-3 text-xs rounded-lg border border-success-border bg-success-subtle text-success-fg px-3 py-2">
            🔒 Secrets are stored as <strong>Docker Swarm secrets</strong> — encrypted at rest and mounted in-memory at <code className="text-emerald-200">/run/secrets/&lt;KEY&gt;</code>. Their values can't be read back; use Rotate to change one.
          </div>
        ) : (
          <div className="mb-3 text-xs rounded-lg border border-warning-border/60 bg-warning-subtle/40 text-warning-fg/90 px-3 py-2">
            ⚠ Compose stores values in <strong>plaintext</strong> in <code className="text-warning-fg">.env</code> on disk. Deploy this environment with <strong>Swarm</strong> for encrypted-at-rest secrets.
          </div>
        )}

        {showAudit && <SecretAuditPanel name={name} env={env} />}

        {/* Reveal toggle */}
        <div className="flex items-center justify-between mb-3">
          <p className="text-xs text-content-subtle">Click the lock to flag a value as a secret.</p>
          <label className="flex items-center gap-2 cursor-pointer shrink-0 ml-3">
            <input type="checkbox" checked={reveal} onChange={e => { setReveal(e.target.checked); setEdits({}) }}
              className="w-3.5 h-3.5 accent-brand-500" />
            <span className="text-xs text-content-muted select-none">Show values</span>
          </label>
        </div>

        {isLoading ? <p className="text-content-subtle text-sm">Loading…</p> : (
          <div className="space-y-2 mb-4 max-h-72 overflow-y-auto pr-1">
            {Object.entries(vars || {}).map(([k, info]) => {
              const markedForDelete = deletes.has(k)
              const secret = isSecret(k)
              // Swarm secrets are write-only: their value can't be edited inline.
              const lockedValue = secret && swarm && !!info.secret
              return (
                <div key={k} className={`flex items-center gap-2 rounded pl-1.5 transition-colors ${markedForDelete ? 'opacity-40' : ''} ${secret ? 'border-l-2 border-warning/70' : 'border-l-2 border-transparent'}`}>
                  <button type="button" onClick={() => toggleFlag(k)} disabled={markedForDelete}
                    title={secret ? 'Flagged as secret — click to unflag' : 'Flag as secret'}
                    className={`shrink-0 w-6 h-6 flex items-center justify-center rounded text-xs ${secret ? 'text-warning-fg' : 'text-content-faint hover:text-content'}`}>
                    {secret ? '🔒' : '🔓'}
                  </button>
                  <span className="font-mono text-xs text-content w-36 shrink-0 truncate" title={k}>{k}</span>
                  {lockedValue ? (
                    <div className="flex-1 flex items-center gap-2">
                      <span className="flex-1 px-2 py-1 text-sm text-content-subtle italic select-none">stored in Docker secret</span>
                      <button type="button" onClick={() => { setRotateKey(k); setRotateVal('') }}
                        className="shrink-0 px-2 py-1 text-xs rounded bg-surface-overlay hover:bg-surface-overlay text-content-strong">Rotate</button>
                    </div>
                  ) : (
                    <input
                      type={reveal && !secret ? 'text' : 'password'}
                      placeholder={reveal ? (info.value || '') : '••••••••'}
                      value={markedForDelete ? '' : (edits[k] ?? (reveal && !secret ? info.value : ''))}
                      disabled={markedForDelete}
                      onChange={e => setEdits(p => ({ ...p, [k]: e.target.value }))}
                      className="flex-1 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500 disabled:opacity-40 disabled:cursor-not-allowed"
                    />
                  )}
                  <button type="button" onClick={() => toggleDelete(k)}
                    title={markedForDelete ? 'Undo delete' : 'Delete this variable'}
                    className={`shrink-0 w-6 h-6 flex items-center justify-center rounded transition-colors text-xs ${
                      markedForDelete ? 'bg-red-600 text-white hover:bg-red-700' : 'text-content-faint hover:text-danger-fg hover:bg-surface-overlay'}`}>
                    {markedForDelete ? '↩' : '×'}
                  </button>
                </div>
              )
            })}
          </div>
        )}

        {/* Rotate sub-form */}
        {rotateKey && (
          <div className="mb-3 rounded-lg border border-border-strong bg-surface-raised/60 p-3">
            <p className="text-xs text-content mb-2">Rotate secret <span className="font-mono text-warning-fg">{rotateKey}</span> — enter a new value:</p>
            <div className="flex gap-2">
              <input type="password" autoFocus value={rotateVal} onChange={e => setRotateVal(e.target.value)}
                placeholder="new value"
                className="flex-1 px-2 py-1 bg-surface border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500" />
              <button type="button" disabled={!rotateVal || rotateMut.isPending}
                onClick={() => rotateMut.mutate({ key: rotateKey, value: rotateVal })}
                className="px-3 py-1 bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white text-sm rounded">
                {rotateMut.isPending ? 'Rotating…' : 'Rotate'}</button>
              <button type="button" onClick={() => setRotateKey(null)}
                className="px-3 py-1 bg-surface-overlay hover:bg-surface-overlay text-content-strong text-sm rounded">Cancel</button>
            </div>
            {rotateMut.isError && <p className="text-danger-fg text-xs mt-2">{rotateMut.error?.response?.data?.error || 'Rotation failed'}</p>}
          </div>
        )}

        {/* Add new variable row */}
        <div className="flex gap-2 pt-3 border-t border-border">
          <button type="button" onClick={() => setNewSecret(s => !s)}
            title={newSecret ? 'New var is a secret' : 'Flag new var as secret'}
            className={`shrink-0 w-7 h-7 flex items-center justify-center rounded text-xs ${newSecret ? 'text-warning-fg bg-surface-raised' : 'text-content-faint hover:text-content'}`}>
            {newSecret ? '🔒' : '🔓'}
          </button>
          <input type="text" placeholder="NEW_KEY" value={newKey}
            onChange={e => setNewKey(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && newKey.trim() && handleSave()}
            className="w-40 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500" />
          <input type={newSecret ? 'password' : 'text'} placeholder="value" value={newVal}
            onChange={e => setNewVal(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && newKey.trim() && handleSave()}
            className="flex-1 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500" />
          <button type="button" onClick={() => { if (newKey.trim()) handleSave() }}
            disabled={!newKey.trim() || mutation.isPending}
            className="px-3 py-1 bg-surface-overlay hover:bg-surface-overlay disabled:opacity-40 text-content-strong text-sm rounded transition-colors shrink-0">Add</button>
        </div>

        {/* Refresh hint */}
        <p className="text-xs text-warning-fg/80 flex items-center gap-1.5 mt-2">
          <span>⚠</span> After saving, use <strong>Deploy ▾ → Refresh</strong> to apply changes to running containers.
        </p>

        <div className="flex items-center gap-3 mt-3">
          <button onClick={handleSave} disabled={mutation.isPending}
            className="bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-medium px-4 py-2 rounded-lg transition-colors">
            {mutation.isPending ? 'Saving…' : 'Save changes'}
          </button>
          {mutation.isSuccess && <span className="text-success-fg text-sm">Saved ✓</span>}
          {mutation.isError && <span className="text-danger-fg text-sm">{mutation.error?.response?.data?.error || 'Failed'}</span>}
          {mutation.data?.warning && <span className="text-warning-fg text-sm">⚠ {mutation.data.warning}</span>}
        </div>
      </div>
    </div>
  )
}

// SecretAuditPanel renders the recent secret read/write/rotate/delete events for
// one environment (Phase 8d).
function SecretAuditPanel({ name, env }) {
  const { workspace } = useParams()
  const { data: events, isLoading } = useQuery({
    queryKey: ['secret-events', workspace, name, env],
    queryFn: () => fetchSecretEvents(workspace, name, env),
  })
  const color = { read: 'text-sky-400', write: 'text-emerald-400', rotate: 'text-warning-fg', delete: 'text-danger-fg' }
  return (
    <div className="mb-3 rounded-lg border border-border-strong bg-canvas/60 p-3 max-h-40 overflow-y-auto">
      <p className="text-xs text-content-muted mb-2 font-medium">Secret audit trail</p>
      {isLoading ? <p className="text-xs text-content-subtle">Loading…</p> :
        (events || []).length === 0 ? <p className="text-xs text-content-faint">No secret events yet.</p> : (
          <table className="w-full text-xs">
            <tbody>
              {events.map((e, i) => (
                <tr key={i} className="text-content-muted">
                  <td className={`pr-2 font-medium ${color[e.action] || 'text-content'}`}>{e.action}</td>
                  <td className="pr-2 font-mono text-content truncate max-w-[8rem]" title={e.key}>{e.key}</td>
                  <td className="pr-2 truncate">{e.username || '—'}</td>
                  <td className="text-content-faint whitespace-nowrap">{e.created_at}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export default function ProjectPage() {
  const { workspace, name } = useParams()
  const navigate = useNavigate()
  const [actionWs, setActionWs]           = useState(null)   // current action WebSocket → ActionLog
  const [actionMeta, setActionMeta]       = useState(null)   // {cmd, env, extra, user, ts} for the run's header
  const username = useAuthStore(s => s.user?.sub) || 'unknown'
  const [configModal, setConfigModal]     = useState(null)
  const [composeModal, setComposeModal]   = useState(null)
  const [termModal, setTermModal]         = useState(null) // {env}
  const [logModal, setLogModal]           = useState(null) // {env, service}

  const { data: ws, isLoading, error } = useQuery({
    queryKey: ['workspace', workspace, name],
    queryFn: () => fetchWorkspace(workspace, name),
  })

  function runAction(cmd, env, onComplete, extra = [], services = []) {
    const socket = openActionSocket(workspace, name, cmd, env, extra, services)
    if (onComplete) socket.addEventListener('close', onComplete)
    setActionMeta({ cmd, env, extra, user: username, ts: Date.now() })
    setActionWs(socket)
  }

  if (isLoading) return <Layout><div className="p-8 text-content-subtle text-sm">Loading…</div></Layout>
  if (error)     return <Layout><div className="p-8 text-danger-fg text-sm">Failed to load project: {error.message}</div></Layout>

  const cfg = ws?.config
  const envs = ws?.envs || []
  const type = cfg?.project?.type || 'custom'
  const version = cfg?.project?.version
  const vStr = version ? `v${version.major}.${version.minor}.${version.patch}-build.${version.build}` : ''

  // Build header stack description
  const stackParts = []
  if (type === 'image') {
    ;(cfg?.images || []).forEach(img => stackParts.push(img.image?.split('/').pop()))
  } else {
    const firstEnvCfg = cfg?.environments?.[envs[0]] || {}
    if (firstEnvCfg.backend) stackParts.push(capitalize(firstEnvCfg.backend))
    if (firstEnvCfg.frontend && firstEnvCfg.frontend !== 'none') stackParts.push(capitalize(firstEnvCfg.frontend))
    if (firstEnvCfg.database && firstEnvCfg.database !== 'none') stackParts.push(capitalize(firstEnvCfg.database))
    if (firstEnvCfg.redis_enabled) stackParts.push('Redis')
  }

  return (
    <Layout>
      <div className="p-6 space-y-5">
        {/* Header */}
        <div className="flex items-start justify-between">
          <div>
            <div className="flex items-center gap-3 flex-wrap">
              <h1 className="text-2xl font-bold text-content-strong">{name}</h1>
              <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${
                type === 'image' ? 'bg-info-subtle text-info-fg' : 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300'
              }`}>{type}</span>
            </div>
            <p className="text-sm text-content-muted mt-1">
              {stackParts.join(' · ')}
              {vStr && <span className="ml-2 font-mono text-content-subtle text-xs">{vStr}</span>}
            </p>
          </div>

          {/* Global actions */}
          <div className="flex items-center gap-2 flex-wrap justify-end">
            <HeaderBtn label="Edit project" onClick={() => navigate(`/workspaces/${workspace}/projects/${name}/edit`)} />
            {type !== 'image' && <HeaderBtn label="Build ↗" onClick={() => runAction('build', envs[0])} primary />}
          </div>
        </div>

        {/* Environment cards — left-aligned 3-column proportional grid:
            each card targets one third of the row (minus the two gaps) and never
            grows past that, so widths stay uniform regardless of how many envs
            exist. A min width keeps content readable; when the row can't fit 3
            (or 2) at that minimum, cards hold the minimum and the extras wrap to
            the next row. gap-4 = 1rem → 2rem subtracted for the two inter-card gaps. */}
        <div className="flex flex-wrap gap-4">
          {envs.map(env => (
            <div key={env} className="grow-0 shrink min-w-[22rem]" style={{ flexBasis: 'calc((100% - 2rem) / 3)' }}>
              <EnvCard
                name={name}
                ws={ws}
                envName={env}
                cfg={cfg?.environments?.[env]}
                onAction={runAction}
                onConfig={() => setConfigModal({ env })}
                onCompose={() => setComposeModal({ env })}
                onTerminal={(service) => setTermModal({ env, service: typeof service === 'string' ? service : undefined })}
                onLogs={(service) => setLogModal({ env, service })}
              />
            </div>
          ))}
        </div>

        {/* Release pipeline (custom stacks only) */}
        {type !== 'image' && <ReleasePipeline ws={ws} />}

        {/* Bottom split: Action output + Logs — both fixed-height, scroll internally */}
        <div className="grid grid-cols-2 gap-5 items-start">
          <ActionLog
            key={name}
            wsName={name}
            actionWs={actionWs}
            actionMeta={actionMeta}
          />
          <LogViewer wsName={name} envs={envs} />
        </div>
      </div>

      {/* Modals / drawers */}
      {configModal && (
        <EnvVarsModal name={name} env={configModal.env} deployment={cfg?.environments?.[configModal.env]?.deployment || 'compose'} onClose={() => setConfigModal(null)} />
      )}
      {composeModal && (
        <ComposeEditor
          workspace={workspace}
          name={name}
          env={composeModal.env}
          onClose={() => setComposeModal(null)}
          onRefresh={() => runAction('refresh', composeModal.env)}
        />
      )}
      {termModal && (
        <TerminalModal workspace={workspace} wsName={name} envName={termModal.env} initialService={termModal.service}
          onClose={() => setTermModal(null)} />
      )}
      {logModal && (
        <LogModal
          wsName={name}
          envs={envs}
          initialEnv={logModal.env}
          initialContainers={logModal.service ? [logModal.service] : []}
          onClose={() => setLogModal(null)}
        />
      )}
    </Layout>
  )
}

function HeaderBtn({ label, onClick, primary }) {
  return (
    <button
      onClick={onClick}
      className={`flex items-center gap-1.5 text-sm font-medium px-3 py-1.5 rounded-lg border transition-colors ${
        primary
          ? 'bg-brand-600 hover:bg-brand-700 text-white border-brand-600'
          : 'bg-transparent hover:bg-surface-raised text-content hover:text-content-strong border-border-strong'
      }`}
    >
      <span className="text-xs opacity-60">○</span> {label}
    </button>
  )
}
