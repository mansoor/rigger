import { useState, useRef, useEffect, useCallback } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchWorkspace, fetchEnvVars, fetchEnvStatus, fetchImageUpdates, fetchContainers, fetchEnvMetrics, updateEnvVars, openActionSocket, exportTemplate } from '../lib/api'
import { useAuthStore } from '../store/auth'
import Layout from '../components/Layout'
import ComposeEditor from '../components/ComposeEditor'
import TerminalModal from '../components/TerminalModal'
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

function MetricTile({ label, value, series, stroke }) {
  return (
    <div className="bg-gray-900/40 border border-gray-800/60 rounded-lg px-3 py-2 min-w-0">
      <div className="flex items-center justify-between gap-1 mb-1">
        <span className="text-xs font-semibold text-gray-500 uppercase tracking-wider">{label}</span>
        <span className="text-xs font-mono text-gray-300 truncate">{value}</span>
      </div>
      <Sparkline values={series} stroke={stroke} height={24} />
    </div>
  )
}

// ── Env status badge ──────────────────────────────────────────────────────────

function StatusBadge({ label, color }) {
  const colors = {
    running:  'bg-green-500/20 text-green-400 border-green-500/30',
    partial:  'bg-amber-500/20 text-amber-400 border-amber-500/30',
    building: 'bg-amber-500/20 text-amber-400 border-amber-500/30',
    stopped:  'bg-red-500/15 text-red-400 border-red-500/30',
    unknown:  'bg-gray-700/40 text-gray-500 border-gray-600/30',
  }
  const dot = {
    running:  'bg-green-400',
    partial:  'bg-amber-400 animate-pulse',
    building: 'bg-amber-400 animate-pulse',
    stopped:  'bg-red-500',
    unknown:  'bg-gray-600',
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
        className={`${base} bg-gray-800/30 text-gray-600 border-gray-800 cursor-not-allowed`}>
        {children}
      </span>
    )
  }
  return (
    <a href={href} target="_blank" rel="noreferrer" title={`Open ${href}`}
      className={`${base} bg-gray-800 hover:bg-brand-900 text-gray-400 hover:text-brand-300 border-gray-700 hover:border-brand-600`}>
      {children}
    </a>
  )
}

function EnvCard({ name, ws, envName, cfg, onAction, onConfig, onCompose, onTerminal, onActionDone }) {
  const qc         = useQueryClient()
  // Use server-resolved domain (${VAR} already substituted) for display
  const domain     = ws?.env_access?.[envName]?.domain || cfg?.domain || '—'
  const gitBranch  = cfg?.git?.branch || ''
  const deployment = cfg?.deployment || 'compose'
  const isImage    = ws?.config?.project?.type === 'image'
  const { url, port, links, viaTraefik, domainUrl } = envAccess(cfg, ws, envName)

  // Poll container status every 15 seconds, refresh immediately after actions
  const { data: statusData, refetch: refetchStatus } = useQuery({
    queryKey: ['envstatus', name, envName],
    queryFn: () => fetchEnvStatus(name, envName),
    refetchInterval: 30_000,  // SSE invalidates immediately; polling is the fallback
    retry: false,
  })
  const containerStatus = statusData?.status || 'unknown'

  // Per-container health details — shared query key with LogViewer (React Query deduplicates)
  const { data: containers = [] } = useQuery({
    queryKey: ['containers', name, envName],
    queryFn: () => fetchContainers(name, envName),
    refetchInterval: 30_000,
    retry: false,
  })

  // Metrics history (Phase 6d) — per-env CPU/memory/disk/network for sparklines.
  // Poll faster until the first snapshot exists (e.g. a just-deployed env) so the
  // strip fills in on its own; settle to 60s once populated.
  const { data: metrics = [] } = useQuery({
    queryKey: ['metrics', name, envName],
    queryFn: () => fetchEnvMetrics(name, envName, 24),
    refetchInterval: (q) => ((q.state.data?.length ?? 0) === 0 ? 20_000 : 60_000),
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

  // Derive short service names for display
  const stackPrefix = `${name}_${envName}_`
  const containerDetails = containers.map(c => ({
    ...c,
    short: c.Service.startsWith(stackPrefix) ? c.Service.slice(stackPrefix.length) : c.Service,
  }))

  // URLs are only clickable when the env is actually reachable: running and with
  // no unhealthy container.
  const reachable = containerStatus === 'running' && !containerDetails.some(c => c.Health === 'unhealthy')

  // Image update check — results come from hourly background cache; poll every 10 min
  const { data: imgUpdates } = useQuery({
    queryKey: ['imageupdates', name, envName],
    queryFn: () => fetchImageUpdates(name, envName),
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

  function handleAction(cmd) {
    onAction(cmd, envName, () => {
      // Refresh env status, container details, image-update and metric state
      // after any action (deploy/refresh/etc.) so the card reflects reality.
      setTimeout(() => {
        refetchStatus()
        qc.invalidateQueries({ queryKey: ['containers', name, envName] })
        qc.invalidateQueries({ queryKey: ['metrics', name, envName] })
        if (isImage) qc.invalidateQueries({ queryKey: ['imageupdates', name, envName] })
      }, 2000)
      // After update: backend invalidates its cache and runs a fresh check (~3-5s).
      // Wait 8s then refetch so the UI reflects the post-update digest comparison.
      if (cmd === 'update') {
        setTimeout(() => {
          qc.invalidateQueries({ queryKey: ['imageupdates', name, envName] })
        }, 8000)
      }
    })
  }

  const [stopOpen, setStopOpen]           = useState(false)
  const [deployOpen, setDeployOpen]       = useState(false)
  const [noUpdateMsg, setNoUpdateMsg]     = useState(false)
  const [containersOpen, setContainersOpen] = useState(true)

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
  const stopRef   = useRef(null)
  const deployRef = useRef(null)

  // Close dropdowns when clicking outside
  useEffect(() => {
    if (!stopOpen) return
    function handler(e) { if (stopRef.current && !stopRef.current.contains(e.target)) setStopOpen(false) }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [stopOpen])

  useEffect(() => {
    if (!deployOpen) return
    function handler(e) { if (deployRef.current && !deployRef.current.contains(e.target)) setDeployOpen(false) }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [deployOpen])

  return (
    <div className="w-full bg-gray-900 border border-gray-800 rounded-xl p-5 flex flex-col gap-4">
      {/* Card header */}
      <div className="flex items-start justify-between gap-2">
        <div className="flex items-center gap-2 flex-wrap min-w-0">
          <h3 className="font-semibold text-white text-base shrink-0">{envName}</h3>

          {/* Domain badge — Traefik ON: primary access URL */}
          {viaTraefik && url && (
            <UrlBadge href={url} reachable={reachable}>{domain} ↗</UrlBadge>
          )}

          {/* Domain badge — Traefik OFF but domain is set (informational, alongside port links) */}
          {!viaTraefik && domainUrl && (
            <UrlBadge href={domainUrl} reachable={reachable}>{domain} ↗</UrlBadge>
          )}

          {/* Port link badges — image stack (Traefik off): one badge per linked port */}
          {!viaTraefik && links && links.map((lnk, li) => (
            <UrlBadge key={li} href={lnk.url} reachable={reachable} mono>{lnk.label} ↗</UrlBadge>
          ))}

          {/* Port badge — custom stack (Traefik off) via http_port */}
          {!viaTraefik && (!links || links.length === 0) && port && url && (
            <UrlBadge href={url} reachable={reachable} mono>:{port} ↗</UrlBadge>
          )}

          {/* Update badges moved to container panel rows */}
        </div>
        <div className="flex items-center gap-2 shrink-0">
          {/* > bash terminal button */}
          <button
            onClick={onTerminal}
            title="Open terminal"
            className="font-mono text-xs px-2 py-0.5 rounded bg-gray-800 hover:bg-gray-700 text-gray-400 hover:text-green-400 border border-gray-700 hover:border-green-700 transition-colors"
          >
            &gt; bash
          </button>
          {ws?.env_hosts?.[envName]?.host_name && (
            <span
              title={`Runs on remote host ${ws.env_hosts[envName].host_name}`}
              className="text-xs px-2 py-0.5 rounded bg-indigo-950/60 text-indigo-300 border border-indigo-800/50"
            >
              🖥 {ws.env_hosts[envName].host_name}
            </span>
          )}
          <StatusBadge label={containerStatus} color={containerStatus} />
        </div>
      </div>

      {/* Details */}
      <div className="space-y-1.5 text-sm text-gray-400">
        {/* Only show domain/url row if neither badge above applies */}
        {!cfg?.domain && !port && <DetailRow icon="○" value="no url configured" />}
        {gitBranch && <DetailRow icon="○" value={gitBranch} />}
      </div>

      {/* Actions — 2×2 grid: [Deploy][Update] / [Restart][Stop▾] */}
      <div className="grid grid-cols-2 gap-2 mt-auto">

        {/* Row 1 col 1: Deploy ▾ split button */}
        <div ref={deployRef} className="relative flex">
          <button
            onClick={() => handleAction('start')}
            className="flex-1 text-sm font-medium px-3 py-1.5 rounded-l-lg transition-colors flex items-center justify-center gap-1.5 bg-brand-600 hover:bg-brand-700 text-white"
          >
            <span className="text-xs opacity-60">○</span> Deploy
          </button>
          <button
            onClick={() => setDeployOpen(o => !o)}
            className="px-1.5 py-1.5 rounded-r-lg border-l border-brand-700 bg-brand-600 hover:bg-brand-700 text-white transition-colors"
            title="More deploy options"
          >
            ▾
          </button>
          {deployOpen && (
            <div className="absolute left-0 top-full mt-1 z-20 bg-gray-800 border border-gray-700 rounded-lg shadow-xl min-w-[200px] py-1">
              <button
                onClick={() => { setDeployOpen(false); handleAction('start') }}
                className="w-full text-left px-3 py-2 text-sm text-gray-200 hover:bg-gray-700 transition-colors"
              >
                Deploy
                <p className="text-xs text-gray-500 mt-0.5">Start containers (uses current compose)</p>
              </button>
              <div className="border-t border-gray-700 my-1" />
              <button
                onClick={() => { setDeployOpen(false); handleAction('refresh') }}
                className="w-full text-left px-3 py-2 text-sm text-brand-300 hover:bg-gray-700 transition-colors"
              >
                Refresh
                <p className="text-xs text-gray-500 mt-0.5">Regenerate compose from config + deploy</p>
              </button>
            </div>
          )}
        </div>

        {/* Row 1 col 2: Update (image stacks) or empty slot (custom) */}
        {isImage ? (() => {
          const checked  = imgUpdates && !imgUpdates.pending
          const upToDate = checked && !hasImageUpdate

          function handleUpdate() {
            if (upToDate) {
              setNoUpdateMsg(true)
              setTimeout(() => setNoUpdateMsg(false), 3000)
              return
            }
            handleAction('update')
          }

          return (
            <div className="relative">
              <button
                onClick={handleUpdate}
                className={`w-full text-sm font-medium px-3 py-1.5 rounded-lg transition-colors flex items-center justify-center gap-1.5 border ${
                  upToDate
                    ? 'bg-gray-800/60 text-gray-500 border-gray-700 cursor-default'
                    : 'bg-amber-500/15 hover:bg-amber-500/25 text-amber-300 hover:text-amber-200 border-amber-500/20'
                }`}
                title={upToDate ? 'All images are up to date' : 'Pull latest images and recreate containers'}
              >
                <span className="text-xs">{upToDate ? '✓' : '↑'}</span>
                {upToDate ? 'Up to date' : 'Update'}
                {hasImageUpdate && <span className="w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />}
              </button>
              {noUpdateMsg && (
                <div className="absolute left-0 right-0 -bottom-7 text-center text-xs text-gray-400 bg-gray-800 border border-gray-700 rounded px-2 py-1 z-10 pointer-events-none">
                  Already up to date
                </div>
              )}
            </div>
          )
        })() : <div />}

        {/* Row 2 col 1: Restart */}
        <button
          onClick={() => handleAction('restart')}
          className="text-sm font-medium px-3 py-1.5 rounded-lg transition-colors flex items-center justify-center gap-1.5 bg-gray-800 hover:bg-gray-700 text-gray-300 hover:text-white"
        >
          <span className="text-xs opacity-60">○</span> Restart
        </button>

        {/* Row 2 col 2: Stop / Down split button */}
        <div ref={stopRef} className="relative flex">
          <button
            onClick={() => handleAction('stop')}
            className="flex-1 text-sm font-medium px-3 py-1.5 rounded-l-lg transition-colors flex items-center justify-center gap-1.5 bg-red-900/60 hover:bg-red-800/80 text-red-300 hover:text-red-200"
          >
            <span className="text-xs opacity-60">○</span> Stop
          </button>
          <button
            onClick={() => setStopOpen(o => !o)}
            className="px-1.5 py-1.5 rounded-r-lg border-l border-red-900 bg-red-900/60 hover:bg-red-800/80 text-red-300 hover:text-red-200 transition-colors"
            title="More stop options"
          >
            ▾
          </button>
          {stopOpen && (
            <div className="absolute right-0 top-full mt-1 z-20 bg-gray-800 border border-gray-700 rounded-lg shadow-xl min-w-[160px] py-1">
              <button
                onClick={() => { setStopOpen(false); handleAction('stop') }}
                className="w-full text-left px-3 py-2 text-sm text-gray-200 hover:bg-gray-700 transition-colors"
              >
                Stop
                <p className="text-xs text-gray-500 mt-0.5">Pause containers (keep state)</p>
              </button>
              <button
                onClick={() => { setStopOpen(false); handleAction('down') }}
                className="w-full text-left px-3 py-2 text-sm text-red-300 hover:bg-gray-700 transition-colors"
              >
                Inactivate
                <p className="text-xs text-gray-500 mt-0.5">Remove containers (keep volumes)</p>
              </button>
            </div>
          )}
        </div>

      </div>{/* end grid */}

      {/* Resource history sparklines (Phase 6d). Shown whenever the env is up so a
          freshly-deployed env looks consistent (— placeholders) instead of missing
          the strip until the collector's first snapshot arrives. */}
      {(metrics.length > 0 || containerStatus === 'running' || containerStatus === 'partial') && (
        <div className="border-t border-gray-800/60 pt-3 grid grid-cols-2 2xl:grid-cols-4 gap-3">
          <MetricTile label="CPU"     value={lastMetric ? `${lastMetric.cpu_pct.toFixed(1)}%` : '—'} series={cpuSeries}     stroke="#22d3ee" />
          <MetricTile label="Memory"  value={lastMetric ? fmtBytes(lastMetric.memory_bytes) : '—'}   series={memSeries}     stroke="#a78bfa" />
          <MetricTile label="Disk"    value={lastMetric ? fmtBytes(lastMetric.disk_bytes) : '—'}     series={diskSeries}    stroke="#34d399" />
          <MetricTile label="Network" value={lastMetric ? fmtRate(lastNetRate) : '—'}                series={netRateSeries} stroke="#fbbf24" />
        </div>
      )}

      {/* Container health panel — collapsible */}
      {serviceRows.length > 0 && (
        <div className="border-t border-gray-800/60 pt-3">
          {/* Panel header / toggle */}
          <button
            type="button"
            onClick={() => setContainersOpen(o => !o)}
            className="flex items-center justify-between w-full group mb-2"
          >
            <span className="text-xs font-semibold text-gray-500 uppercase tracking-wider">
              Services
              <span className="ml-1.5 font-normal normal-case text-gray-700">
                ({serviceRows.filter(c => c.State === 'running').length}/{serviceRows.length})
              </span>
            </span>
            <span className="text-gray-700 group-hover:text-gray-400 text-xs transition-colors">
              {containersOpen ? '▲' : '▼'}
            </span>
          </button>

          {containersOpen && (
            <div className="space-y-1.5">
              {serviceRows.map(c => {
                const dotCls    = c.State ? containerDotClass(c) : 'bg-gray-700'
                const txtCls    = c.State ? containerTxtClass(c) : 'text-gray-600'
                const label     = c.State ? containerStatusLabel(c) : 'Not started'
                const isNeutral = !c.State || (c.State === 'running' && (c.Health === 'healthy' || c.Health === ''))
                const upd       = updateByService[c.short]
                return (
                  <div key={c.short} className="flex items-center justify-between gap-2">
                    <div className="flex items-center gap-1.5 min-w-0">
                      <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${dotCls}`} />
                      <span className="text-xs text-gray-400 font-mono truncate">{c.short}</span>
                    </div>
                    <div className="flex items-center gap-1.5 shrink-0">
                      {/* Per-service update badge */}
                      {upd?.has_update && (
                        <span title={`Update available: ${upd.newer_tag}`}
                          className="text-xs px-1.5 py-0 rounded bg-amber-500/20 text-amber-300 border border-amber-500/30 animate-pulse leading-5">
                          ↑
                        </span>
                      )}
                      {upd?.indeterminate && !upd?.has_update && (
                        <span title="Cannot compare digest — run Update to pull latest"
                          className="text-xs px-1.5 py-0 rounded bg-gray-700/40 text-gray-500 border border-gray-600/30 leading-5">
                          ?
                        </span>
                      )}
                      <span className={`text-xs ${isNeutral ? 'text-gray-600' : txtCls}`}>
                        {c.State === 'running' && c.Health ? `${c.State} · ${label}` : label}
                      </span>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
        </div>
      )}

      {/* File editors + Backup */}
      <div className="flex gap-2 pt-3 border-t border-gray-800">
        <button onClick={onConfig}
          className="flex-1 text-xs text-gray-400 hover:text-gray-200 py-1.5 rounded-lg hover:bg-gray-800 transition-colors">
          Env Vars
        </button>
        <button onClick={onCompose}
          className="flex-1 text-xs text-gray-400 hover:text-gray-200 py-1.5 rounded-lg hover:bg-gray-800 transition-colors">
          Compose
        </button>
        <button onClick={() => handleAction('backup')}
          className="flex-1 text-xs text-gray-400 hover:text-gray-200 py-1.5 rounded-lg hover:bg-gray-800 transition-colors">
          Backup
        </button>
      </div>
    </div>
  )
}

function DetailRow({ icon, value }) {
  return (
    <div className="flex items-center gap-2">
      <span className="text-xs text-gray-600">{icon}</span>
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
    done:    'bg-green-500 border-green-500 text-green-900',
    active:  'bg-amber-400 border-amber-400 text-amber-900 animate-pulse',
    pending: 'bg-gray-800 border-gray-700 text-gray-500',
  }

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl p-5">
      <h2 className="text-sm font-semibold text-gray-300 mb-5 flex items-center gap-2">
        <span className="text-xs">○</span> Release pipeline
      </h2>

      <div className="flex items-center gap-0 mb-5 overflow-x-auto pb-2">
        {steps.map((step, i) => (
          <div key={step.label} className="flex items-center">
            <div className="flex flex-col items-center gap-1.5 min-w-[90px]">
              <div className={`w-9 h-9 rounded-full border-2 flex items-center justify-center text-xs font-bold ${stepStyle[step.status]}`}>
                {step.status === 'done' ? '✓' : step.status === 'active' ? '◎' : '○'}
              </div>
              <span className={`text-xs text-center leading-tight ${step.status === 'pending' ? 'text-gray-600' : 'text-gray-300'}`}>
                {step.label}
              </span>
              {step.version && (
                <span className="text-xs text-gray-500 font-mono">{step.version}</span>
              )}
              {step.status === 'active' && (
                <span className="text-xs text-amber-400">in progress</span>
              )}
              {step.status === 'pending' && (
                <span className="text-xs text-gray-600">—</span>
              )}
            </div>
            {i < steps.length - 1 && (
              <div className={`h-0.5 w-8 shrink-0 mx-1 ${i < 2 ? 'bg-green-500' : 'bg-gray-700'}`} />
            )}
          </div>
        ))}
      </div>

      <div className="flex items-center justify-between bg-gray-800/60 rounded-lg px-4 py-3">
        <p className="text-sm text-gray-300">
          Ready to promote? <span className="font-mono text-white">{vStr}</span> will be retagged and deployed to prod — no rebuild.
        </p>
        <button className="ml-4 shrink-0 bg-gray-700 hover:bg-gray-600 text-gray-300 hover:text-white text-sm font-medium px-4 py-2 rounded-lg transition-colors flex items-center gap-1.5">
          <span className="text-xs">○</span> Promote to prod
        </button>
      </div>
    </div>
  )
}

// ── Inline action log — streams output from Deploy/Stop/Restart/Backup etc. ──

function ActionLog({ actionWs, actionTitle, onClear }) {
  const [lines, setLines] = useState([])
  const [running, setRunning] = useState(false)
  const scrollRef = useRef(null)
  const wsRef = useRef(null)

  useEffect(() => {
    if (!actionWs) {
      setLines([])      // Clear when parent sets actionWs → null (Clear button)
      setRunning(false)
      return
    }
    wsRef.current = actionWs
    setLines([])
    setRunning(true)

    actionWs.addEventListener('message', e => {
      const text = String(e.data || '')
      setLines(prev => {
        const newLines = text.split(/\r?\n/).filter(l => l !== '')
        const next = [...prev, ...newLines]
        return next.length > 2000 ? next.slice(-2000) : next
      })
    })
    actionWs.addEventListener('close', () => setRunning(false))
    actionWs.addEventListener('error', () => setRunning(false))
  }, [actionWs])

  // Scroll the output container only — not the page (scrollIntoView would).
  useEffect(() => {
    const el = scrollRef.current
    if (el) el.scrollTop = el.scrollHeight
  }, [lines])

  return (
    <div className="bg-gray-900 border border-gray-800 rounded-xl flex flex-col overflow-hidden" style={{ height: 380 }}>
      <div className="flex items-center justify-between px-4 py-3 border-b border-gray-800 shrink-0">
        <div className="flex items-center gap-2">
          <span className="text-sm font-semibold text-gray-300">Action output</span>
          {actionTitle && (
            <span className="text-xs text-gray-500 bg-gray-800 px-2 py-0.5 rounded font-mono">{actionTitle}</span>
          )}
          {running && <span className="w-1.5 h-1.5 rounded-full bg-green-400 animate-pulse" />}
        </div>
        {lines.length > 0 && (
          <button onClick={onClear} className="text-xs text-gray-600 hover:text-gray-400 transition-colors">Clear</button>
        )}
      </div>

      <div ref={scrollRef} className="flex-1 overflow-y-auto p-3 font-mono text-xs leading-relaxed bg-gray-950/60 min-h-0">
        {lines.length === 0 ? (
          <p className="text-gray-700 pt-2">
            Run Deploy, Stop, Restart, Backup, Update or other actions — output will stream here.
          </p>
        ) : (
          lines.map((line, i) => (
            <div key={i} dangerouslySetInnerHTML={{ __html: ansiToHtml(line) }} />
          ))
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
function useLogStream({ wsName, activeEnv, activeContainers = [], token, maxLines = 2000 }) {
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
    const ws = new WebSocket(`${proto}://${window.location.host}/api/workspaces/${wsName}/action`)
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
  }, [wsName, activeEnv, activeContainers.join(','), token, maxLines]) // eslint-disable-line react-hooks/exhaustive-deps

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
    case 'created':    return 'bg-gray-500'
    default:           return 'bg-gray-600'
  }
}

function containerTxtClass(c) {
  const health = (c.Health || '').toLowerCase()
  if (c.State === 'running') {
    if (health === 'unhealthy') return 'text-red-400'
    if (health === 'starting')  return 'text-amber-400'
    return 'text-green-400'
  }
  if (c.State === 'restarting' || c.State === 'paused') return 'text-amber-400'
  if (c.State === 'exited' || c.State === 'dead')       return 'text-red-400'
  return 'text-gray-400'
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

function ContainerSelector({ containers, wsName, activeEnv, activeContainers, onSelect, colorMap }) {
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
    const prefix = `${wsName}_${activeEnv}_`
    const short = c.Service.startsWith(prefix) ? c.Service.slice(prefix.length) : c.Service
    // Full service name as it appears in compose logs (used for color lookup)
    return { c, short, fullSvc: c.Service }
  })

  return (
    <div className="flex items-center gap-3 px-3 py-2 border-b border-gray-800/40 shrink-0 overflow-x-auto">
      {/* All checkbox */}
      <label className="flex items-center gap-1.5 cursor-pointer shrink-0 select-none">
        <input type="checkbox" checked={allSelected} onChange={toggleAll}
          className="accent-brand-500 w-3 h-3" />
        <span className={`text-xs ${allSelected ? 'text-white' : 'text-gray-500'}`}>all</span>
      </label>
      <span className="w-px h-3 bg-gray-700 shrink-0" />
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
    <div ref={scrollRef} className={`flex-1 overflow-y-auto p-3 font-mono text-xs leading-relaxed bg-gray-950/60 ${wrap ? 'break-all' : 'overflow-x-auto whitespace-nowrap'}`}>
      {displayed.map((line, i) => {
        const { svc, content } = parseLogLine(line)
        const color = svc ? serviceColorFallback(svc, colorMap) : null
        return (
          <div key={rowOffset + i} className="flex items-start gap-0">
            {showRowNumbers && (
              <span className="text-gray-700 select-none shrink-0 w-10 text-right mr-2">{rowOffset + i + 1}</span>
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
  const token = useAuthStore(s => s.token)
  const [activeEnv, setActiveEnv]           = useState(initialEnv)
  const [activeContainers, setContainers]   = useState(initialContainers || [])
  const [filter, setFilter]                 = useState('')
  const [wrap, setWrap]                     = useState(false)
  const [autoScroll, setAutoScroll]         = useState(true)
  const [rowLimit, setRowLimit]             = useState(0)
  const [showRowNumbers, setShowRowNumbers] = useState(false)

  const { data: containers } = useQuery({
    queryKey: ['containers', wsName, activeEnv, 'modal'],
    queryFn:  () => fetchContainers(wsName, activeEnv),
    enabled:  !!activeEnv, refetchInterval: 15_000, retry: false,
  })

  const { lines, setLines, connect, paused, setPaused } =
    useLogStream({ wsName, activeEnv, activeContainers, token, maxLines: 10000 })

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
      className={`text-xs px-2 py-1 rounded border transition-colors shrink-0 ${active ? 'border-brand-600 text-brand-400 bg-brand-950' : 'border-gray-700 text-gray-500 hover:text-gray-300'}`}>
      {label}
    </button>
  )

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-gray-950" style={{ fontFamily: 'inherit' }}>
      {/* ── Top bar ── */}
      <div className="flex items-center gap-2 px-4 py-2 border-b border-gray-800 bg-gray-900 shrink-0 flex-wrap">
        <h2 className="text-sm font-semibold text-gray-200 shrink-0">Logs</h2>

        {/* Env tabs */}
        <div className="flex items-center gap-1 overflow-x-auto shrink-0">
          {envs.map(env => (
            <button key={env} onClick={() => switchEnv(env)}
              className={`px-3 py-1 text-xs font-medium rounded-md transition-colors shrink-0 ${activeEnv === env ? 'bg-brand-600 text-white' : 'text-gray-400 hover:text-white hover:bg-gray-800'}`}
            >{env}</button>
          ))}
        </div>

        <span className="w-px h-4 bg-gray-700 shrink-0" />

        {/* Filter */}
        <div className="relative shrink-0">
          <input type="text" value={filter} onChange={e => setFilter(e.target.value)}
            placeholder="Filter lines…"
            className="w-44 px-3 py-1 text-xs bg-gray-800 border border-gray-700 rounded-lg text-white placeholder-gray-500 focus:outline-none focus:border-brand-500 font-mono" />
          {filter && <button onClick={() => setFilter('')} className="absolute right-2 top-1/2 -translate-y-1/2 text-gray-500 hover:text-gray-300 text-xs">×</button>}
        </div>

        {/* Line count */}
        <span className="text-xs text-gray-600 shrink-0">
          {filter.trim() || rowLimit > 0
            ? `${displayedCount} / ${lines.length}`
            : `${lines.length}`} lines
        </span>

        <span className="w-px h-4 bg-gray-700 shrink-0" />

        {/* Row limit */}
        <div className="flex items-center gap-1 shrink-0">
          <span className="text-xs text-gray-600">show</span>
          <select value={rowLimit} onChange={e => setRowLimit(Number(e.target.value))}
            className="text-xs bg-gray-800 border border-gray-700 rounded px-1.5 py-1 text-gray-300 focus:outline-none focus:border-brand-500">
            {ROW_LIMIT_OPTIONS.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </div>

        <span className="w-px h-4 bg-gray-700 shrink-0" />

        {/* Toggle buttons */}
        {toggleBtn(wrap,           () => setWrap(v => !v),           'wrap',       'Toggle line wrap')}
        {toggleBtn(autoScroll,     () => setAutoScroll(v => !v),     '↓ auto',     'Toggle auto-scroll')}
        {toggleBtn(paused,         () => setPaused(v => !v),         paused ? '▶ resume' : '⏸ pause', 'Pause / resume stream')}
        {toggleBtn(showRowNumbers, () => setShowRowNumbers(v => !v), '# rows',     'Toggle row numbers')}

        <span className="w-px h-4 bg-gray-700 shrink-0" />

        {/* Action buttons */}
        <button onClick={connect}       title="Reconnect"          className="text-xs text-gray-500 hover:text-gray-300 transition-colors shrink-0">↺</button>
        <button onClick={() => setLines([])} title="Clear buffer"  className="text-xs text-gray-500 hover:text-red-400 transition-colors shrink-0">clear</button>
        <button onClick={copyAll}       title="Copy visible log"   className="text-xs text-gray-500 hover:text-gray-300 transition-colors shrink-0">⎘ copy</button>
        <button onClick={download}      title="Download as .txt"   className="text-xs text-gray-500 hover:text-gray-300 transition-colors shrink-0">⬇ download</button>

        <div className="flex-1" />
        <button onClick={onClose} title="Close (Esc)" className="text-gray-500 hover:text-white transition-colors text-lg leading-none shrink-0">✕</button>
      </div>

      {/* Container multi-selector */}
      <ContainerSelector containers={containers} wsName={wsName} activeEnv={activeEnv}
        activeContainers={activeContainers} onSelect={setContainers} colorMap={colorMap} />

      {/* Pause banner */}
      {paused && (
        <div className="bg-amber-950/60 border-b border-amber-700/40 px-4 py-1.5 shrink-0 flex items-center gap-2">
          <span className="text-xs text-amber-400 font-medium">⏸ Stream paused — new log lines are being discarded</span>
          <button onClick={() => setPaused(false)} className="text-xs text-amber-300 hover:text-white underline">Resume</button>
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
  const token = useAuthStore(s => s.token)
  const [activeEnv, setActiveEnv]         = useState(envs[0] || '')
  const [activeContainers, setContainers] = useState([])
  const [maximized, setMaximized]         = useState(false)
  const [filter, setFilter]               = useState('')
  const [autoScroll, setAutoScroll]       = useState(true)

  const { data: containers } = useQuery({
    queryKey: ['containers', wsName, activeEnv],
    queryFn:  () => fetchContainers(wsName, activeEnv),
    enabled:  !!activeEnv, refetchInterval: 15_000, retry: false,
  })

  const { lines, connect, paused, setPaused } =
    useLogStream({ wsName, activeEnv, activeContainers, token })

  function switchEnv(env) { setActiveEnv(env); setContainers([]) }

  const colorMap = buildColorMap(containers, wsName, activeEnv)

  return (
    <>
      <div className="bg-gray-900 border border-gray-800 rounded-xl flex flex-col overflow-hidden" style={{ height: 380 }}>
        {/* Header */}
        <div className="flex items-center justify-between px-3 py-2.5 border-b border-gray-800 shrink-0 gap-2 flex-wrap">
          <h2 className="text-sm font-semibold text-gray-300 shrink-0">Logs</h2>

          <div className="flex items-center gap-2 flex-wrap">
            {/* Filter */}
            <div className="relative">
              <input type="text" value={filter} onChange={e => setFilter(e.target.value)}
                placeholder="filter…"
                className="w-28 px-2 py-0.5 text-xs bg-gray-800 border border-gray-700 rounded text-white placeholder-gray-600 focus:outline-none focus:border-brand-500 font-mono" />
              {filter && <button onClick={() => setFilter('')} className="absolute right-1.5 top-1/2 -translate-y-1/2 text-gray-500 hover:text-gray-300 text-xs">×</button>}
            </div>

            {/* Auto-scroll checkbox */}
            <label className="flex items-center gap-1 cursor-pointer select-none shrink-0">
              <input type="checkbox" checked={autoScroll} onChange={e => setAutoScroll(e.target.checked)}
                className="accent-brand-500 w-3 h-3" />
              <span className="text-xs text-gray-500">auto</span>
            </label>

            {/* Pause */}
            <button onClick={() => setPaused(v => !v)} title={paused ? 'Resume stream' : 'Pause stream'}
              className={`text-xs transition-colors shrink-0 ${paused ? 'text-amber-400 hover:text-amber-300' : 'text-gray-500 hover:text-gray-300'}`}>
              {paused ? '▶' : '⏸'}
            </button>

            <button onClick={connect} title="Reconnect" className="text-xs text-gray-500 hover:text-gray-300 transition-colors shrink-0">↺</button>
            <button onClick={() => setMaximized(true)} title="Maximize" className="text-xs text-gray-500 hover:text-gray-200 transition-colors shrink-0">⛶</button>
          </div>
        </div>

        {/* Env tabs */}
        <div className="flex items-center gap-1 px-3 py-2 border-b border-gray-800/60 shrink-0 overflow-x-auto">
          {envs.map(env => (
            <button key={env} onClick={() => switchEnv(env)}
              className={`px-3 py-1 text-xs font-medium rounded-md transition-colors shrink-0 ${activeEnv === env ? 'bg-brand-600 text-white' : 'text-gray-400 hover:text-white hover:bg-gray-800'}`}
            >{env}</button>
          ))}
        </div>

        {/* Container multi-selector */}
        <ContainerSelector containers={containers} wsName={wsName} activeEnv={activeEnv}
          activeContainers={activeContainers} onSelect={setContainers} colorMap={colorMap} />

        {/* Pause banner */}
        {paused && (
          <div className="bg-amber-950/50 px-3 py-1 shrink-0 flex items-center gap-2 border-b border-amber-800/30">
            <span className="text-xs text-amber-500">⏸ paused</span>
            <button onClick={() => setPaused(false)} className="text-xs text-amber-400 hover:text-amber-200 underline">resume</button>
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

// ── Export as template modal ──────────────────────────────────────────────────

function ExportTemplateModal({ name, envs, onClose }) {
  const [label, setLabel]       = useState(name)
  const [desc, setDesc]         = useState('')
  const [tags, setTags]         = useState('')
  const [env, setEnv]           = useState(envs[0] || '')
  const [done, setDone]         = useState(false)
  const [error, setError]       = useState('')

  const mutation = useMutation({
    mutationFn: () => exportTemplate(name, {
      label,
      description: desc,
      tags: tags.split(',').map(t => t.trim()).filter(Boolean),
      env,
    }),
    onSuccess: () => setDone(true),
    onError: (e) => setError(e.response?.data?.error || 'Export failed'),
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-white">Export as prebuilt template</h3>
          <button onClick={onClose} className="text-gray-500 hover:text-white text-xl">×</button>
        </div>

        {done ? (
          <div className="space-y-3">
            <p className="text-sm text-green-400">✓ Template saved as <code className="font-mono text-xs bg-gray-800 px-1 py-0.5 rounded">{name}.json</code> in <code className="font-mono text-xs bg-gray-800 px-1 py-0.5 rounded">templates/stacks/</code>.</p>
            <p className="text-xs text-gray-500">It will appear in the "Pre-built template" picker when creating a new workspace.</p>
            <button onClick={onClose} className="w-full bg-brand-600 hover:bg-brand-700 text-white text-sm font-semibold py-2 rounded-lg transition-colors">Close</button>
          </div>
        ) : (
          <>
            {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}

            <div>
              <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Template label</label>
              <input value={label} onChange={e => setLabel(e.target.value)}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500" />
            </div>
            <div>
              <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Description</label>
              <textarea value={desc} onChange={e => setDesc(e.target.value)} rows={2}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500 resize-none" />
            </div>
            <div>
              <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Tags <span className="normal-case font-normal text-gray-500">(comma-separated)</span></label>
              <input value={tags} onChange={e => setTags(e.target.value)} placeholder="nginx, proxy, ssl"
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500" />
            </div>
            {envs.length > 1 && (
              <div>
                <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Read env vars from</label>
                <select value={env} onChange={e => setEnv(e.target.value)}
                  className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500">
                  {envs.map(e => <option key={e} value={e}>{e}</option>)}
                </select>
              </div>
            )}
            <p className="text-xs text-gray-500">Secret values will be replaced with <code className="font-mono">CHANGE_ME</code> placeholders in the template.</p>
            <button
              onClick={() => mutation.mutate()}
              disabled={mutation.isPending || !label}
              className="w-full bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-semibold py-2 rounded-lg transition-colors"
            >
              {mutation.isPending ? 'Exporting…' : 'Export template'}
            </button>
          </>
        )}
      </div>
    </div>
  )
}

// ── Env vars editor (modal) ───────────────────────────────────────────────────

function EnvVarsModal({ name, env, onClose }) {
  const qc = useQueryClient()
  const [reveal, setReveal] = useState(false)
  const [edits, setEdits]   = useState({})
  const [deletes, setDeletes] = useState(new Set())
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')

  const { data: vars, isLoading } = useQuery({
    queryKey: ['envvars', name, env, reveal],
    queryFn: () => fetchEnvVars(name, env, reveal),
  })

  const mutation = useMutation({
    mutationFn: ({ updates, dels }) => updateEnvVars(name, env, updates, dels),
    onSuccess: () => {
      setEdits({})
      setDeletes(new Set())
      setNewKey('')
      setNewVal('')
      qc.invalidateQueries({ queryKey: ['envvars', name, env] })
    },
  })

  function toggleDelete(k) {
    setDeletes(prev => {
      const next = new Set(prev)
      next.has(k) ? next.delete(k) : next.add(k)
      return next
    })
    // Clear any pending edit for a key being deleted
    setEdits(prev => { const n = { ...prev }; delete n[k]; return n })
  }

  function handleSave() {
    const updates = { ...edits }
    if (newKey.trim()) updates[newKey.trim()] = newVal
    mutation.mutate({ updates, dels: [...deletes] })
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-4">
          <h3 className="font-semibold text-white">Env vars — {env}</h3>
          <button onClick={onClose} className="text-gray-500 hover:text-white text-xl">×</button>
        </div>

        {/* Reveal toggle */}
        <div className="flex items-center justify-between mb-4">
          <p className="text-xs text-gray-500">
            {reveal ? 'Showing current values — edit to change.' : 'Values hidden. Edit inputs to change; leave blank to keep existing.'}
          </p>
          <label className="flex items-center gap-2 cursor-pointer shrink-0 ml-3">
            <input
              type="checkbox"
              checked={reveal}
              onChange={e => { setReveal(e.target.checked); setEdits({}) }}
              className="w-3.5 h-3.5 accent-brand-500"
            />
            <span className="text-xs text-gray-400 select-none">Show values</span>
          </label>
        </div>

        {isLoading ? <p className="text-gray-500 text-sm">Loading…</p> : (
          <div className="space-y-2 mb-4 max-h-72 overflow-y-auto pr-1">
            {Object.entries(vars || {}).map(([k, currentVal]) => {
              const markedForDelete = deletes.has(k)
              return (
                <div key={k} className={`flex items-center gap-2 rounded transition-colors ${markedForDelete ? 'opacity-40' : ''}`}>
                  <span className="font-mono text-xs text-gray-300 w-40 shrink-0 truncate" title={k}>{k}</span>
                  <input
                    type={reveal ? 'text' : 'password'}
                    placeholder={reveal ? currentVal : '••••••••'}
                    value={markedForDelete ? '' : (edits[k] ?? (reveal ? currentVal : ''))}
                    disabled={markedForDelete}
                    onChange={e => setEdits(p => ({ ...p, [k]: e.target.value }))}
                    className="flex-1 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm text-white font-mono focus:outline-none focus:border-brand-500 disabled:opacity-40 disabled:cursor-not-allowed"
                  />
                  <button
                    type="button"
                    onClick={() => toggleDelete(k)}
                    title={markedForDelete ? 'Undo delete' : 'Delete this variable'}
                    className={`shrink-0 w-6 h-6 flex items-center justify-center rounded transition-colors text-xs ${
                      markedForDelete
                        ? 'bg-red-800 text-red-200 hover:bg-red-700'
                        : 'text-gray-600 hover:text-red-400 hover:bg-gray-700'
                    }`}
                  >
                    {markedForDelete ? '↩' : '×'}
                  </button>
                </div>
              )
            })}
          </div>
        )}

        {/* Add new variable row */}
        <div className="flex gap-2 pt-3 border-t border-gray-800">
          <input type="text" placeholder="NEW_KEY" value={newKey}
            onChange={e => setNewKey(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && newKey.trim() && handleSave()}
            className="w-44 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm text-white font-mono focus:outline-none focus:border-brand-500" />
          <input type={reveal ? 'text' : 'password'} placeholder="value" value={newVal}
            onChange={e => setNewVal(e.target.value)}
            onKeyDown={e => e.key === 'Enter' && newKey.trim() && handleSave()}
            className="flex-1 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm text-white font-mono focus:outline-none focus:border-brand-500" />
          <button
            type="button"
            onClick={() => { if (newKey.trim()) handleSave() }}
            disabled={!newKey.trim() || mutation.isPending}
            className="px-3 py-1 bg-gray-700 hover:bg-gray-600 disabled:opacity-40 text-white text-sm rounded transition-colors shrink-0"
          >
            Add
          </button>
        </div>

        {/* Refresh hint */}
        <p className="text-xs text-amber-400/80 flex items-center gap-1.5 mt-2">
          <span>⚠</span> After saving, use <strong>Deploy ▾ → Refresh</strong> to apply changes to running containers.
        </p>

        <div className="flex items-center gap-3 mt-3">
          <button onClick={handleSave} disabled={mutation.isPending}
            className="bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-sm font-medium px-4 py-2 rounded-lg transition-colors">
            {mutation.isPending ? 'Saving…' : 'Save changes'}
          </button>
          {mutation.isSuccess && <span className="text-green-400 text-sm">Saved ✓</span>}
          {mutation.isError && <span className="text-red-400 text-sm">Failed</span>}
        </div>
      </div>
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

export default function WorkspacePage() {
  const { name } = useParams()
  const navigate = useNavigate()
  const [actionWs, setActionWs]           = useState(null)   // current action WebSocket → ActionLog
  const [actionTitle, setActionTitle]     = useState('')
  const [configModal, setConfigModal]     = useState(null)
  const [composeModal, setComposeModal]   = useState(null)
  const [exportModal, setExportModal]     = useState(false)
  const [termModal, setTermModal]         = useState(null) // {env}

  const { data: ws, isLoading, error } = useQuery({
    queryKey: ['workspace', name],
    queryFn: () => fetchWorkspace(name),
  })

  function runAction(cmd, env, onComplete) {
    const socket = openActionSocket(name, cmd, env)
    if (onComplete) socket.addEventListener('close', onComplete)
    setActionWs(socket)
    setActionTitle(`${cmd} ${env}`)
  }

  if (isLoading) return <Layout><div className="p-8 text-gray-500 text-sm">Loading…</div></Layout>
  if (error)     return <Layout><div className="p-8 text-red-400 text-sm">Failed to load workspace: {error.message}</div></Layout>

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
              <h1 className="text-2xl font-bold text-white">{name}</h1>
              <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${
                type === 'image' ? 'bg-blue-950 text-blue-300' : 'bg-purple-950 text-purple-300'
              }`}>{type}</span>
              {(() => {
                const dep = cfg?.environments?.[envs[0]]?.deployment || 'compose'
                return <span className="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-gray-400">{dep}</span>
              })()}
            </div>
            <p className="text-sm text-gray-400 mt-1">
              {stackParts.join(' · ')}
              {vStr && <span className="ml-2 font-mono text-gray-500 text-xs">{vStr}</span>}
            </p>
          </div>

          {/* Global actions */}
          <div className="flex items-center gap-2 flex-wrap justify-end">
            {type === 'image' && <HeaderBtn label="Export as template" onClick={() => setExportModal(true)} />}
            <HeaderBtn label="Edit workspace" onClick={() => navigate(`/workspaces/${name}/edit`)} />
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
                onTerminal={() => setTermModal({ env })}
              />
            </div>
          ))}
        </div>

        {/* Release pipeline (custom stacks only) */}
        {type !== 'image' && <ReleasePipeline ws={ws} />}

        {/* Bottom split: Action output + Logs — both fixed-height, scroll internally */}
        <div className="grid grid-cols-2 gap-5 items-start">
          <ActionLog
            actionWs={actionWs}
            actionTitle={actionTitle}
            onClear={() => { setActionWs(null); setActionTitle('') }}
          />
          <LogViewer wsName={name} envs={envs} />
        </div>
      </div>

      {/* Modals / drawers */}
      {configModal && (
        <EnvVarsModal name={name} env={configModal.env} onClose={() => setConfigModal(null)} />
      )}
      {composeModal && (
        <ComposeEditor
          name={name}
          env={composeModal.env}
          onClose={() => setComposeModal(null)}
          onRefresh={() => runAction('refresh', composeModal.env)}
        />
      )}
      {exportModal && (
        <ExportTemplateModal name={name} envs={envs} onClose={() => setExportModal(false)} />
      )}
      {termModal && (
        <TerminalModal wsName={name} envName={termModal.env} onClose={() => setTermModal(null)} />
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
          : 'bg-transparent hover:bg-gray-800 text-gray-300 hover:text-white border-gray-700'
      }`}
    >
      <span className="text-xs opacity-60">○</span> {label}
    </button>
  )
}
