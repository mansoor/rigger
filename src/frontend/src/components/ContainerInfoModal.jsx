import { useState, useEffect } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  fetchContainerInspect, fetchContainerStats, fetchContainerTop, fetchContainerHistory,
} from '../lib/api'
import Sparkline from './Sparkline'

const TABS = ['Overview', 'Resources', 'Network', 'Mounts', 'Processes', 'Environment', 'Labels', 'Health', 'Security', 'Layers', 'JSON']

// docker top columns that are noise for most users.
const TOP_DROP = new Set(['PPID', 'C', 'TTY'])

// ── small helpers ─────────────────────────────────────────────────────────────
function fmtBytes(n) {
  if (!n || n <= 0) return '0 B'
  const u = ['B', 'KB', 'MB', 'GB', 'TB']
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), u.length - 1)
  return `${(n / Math.pow(1024, i)).toFixed(i === 0 ? 0 : 1)} ${u[i]}`
}
function fmtDate(s) { return s && !s.startsWith('0001') ? s.replace('T', ' ').replace(/\..*$/, '') : '—' }
function errText(e) { return e?.response?.data?.error || e?.message || 'request failed' }
function fmtRate(bps) { return !bps || bps <= 0 ? '0 B/s' : `${fmtBytes(bps)}/s` }
function parsePercent(s) { return parseFloat(String(s || '').replace('%', '')) || 0 }
// parseSize handles docker stats units: B, kB/MB/GB (decimal) and KiB/MiB/GiB (binary).
function parseSize(s) {
  const m = String(s || '').trim().match(/^([\d.]+)\s*([a-zA-Z]*)$/)
  if (!m) return 0
  const map = { b: 1, kb: 1e3, mb: 1e6, gb: 1e9, tb: 1e12, kib: 1024, mib: 1024 ** 2, gib: 1024 ** 3, tib: 1024 ** 4 }
  return parseFloat(m[1]) * (map[m[2].toLowerCase()] ?? 1)
}
// ioSum parses a docker "a / b" pair (NetIO, BlockIO) into total bytes.
function ioSum(s) { const [a, b] = String(s || '').split('/'); return parseSize(a) + parseSize(b) }
// fmtDuration formats a docker healthcheck duration (nanoseconds) as e.g. "10s" / "1m 30s".
function fmtDuration(ns) {
  if (!ns || ns <= 0) return '—'
  const s = ns / 1e9
  if (s < 60) return `${Number.isInteger(s) ? s : s.toFixed(1)}s`
  const m = Math.floor(s / 60), rem = Math.round(s % 60)
  return rem ? `${m}m ${rem}s` : `${m}m`
}

// parseLayer turns a `docker history` CreatedBy string into the underlying
// Dockerfile instruction + a cleaned command, stripping the /bin/sh wrappers and
// buildkit markers that make the raw output noisy.
const LAYER_INSTRUCTIONS = ['ADD', 'COPY', 'RUN', 'CMD', 'ENV', 'ENTRYPOINT', 'EXPOSE', 'LABEL', 'USER', 'WORKDIR', 'VOLUME', 'ARG', 'HEALTHCHECK', 'STOPSIGNAL', 'ONBUILD', 'SHELL', 'MAINTAINER']
const LAYER_COLORS = {
  ADD: 'bg-pink-600 text-white', COPY: 'bg-pink-600 text-white',
  RUN: 'bg-emerald-600 text-white',
  CMD: 'bg-red-600 text-white', ENTRYPOINT: 'bg-red-600 text-white',
  ENV: 'bg-amber-500 text-black', ARG: 'bg-amber-500 text-black',
  LABEL: 'bg-sky-600 text-white', EXPOSE: 'bg-indigo-600 text-white',
  WORKDIR: 'bg-purple-600 text-white', USER: 'bg-purple-600 text-white',
  VOLUME: 'bg-cyan-600 text-white', HEALTHCHECK: 'bg-teal-600 text-white',
}
function parseLayer(createdBy) {
  let cmd = (createdBy || '').trim()
    .replace(/^\/bin\/sh -c #\(nop\)\s+/, '') // legacy meta instruction (CMD/ENV/ADD…)
    .replace(/^\/bin\/sh -c\s+/, 'RUN ')      // legacy shell form → RUN
    .replace(/\s*#\s*buildkit\s*$/, '')        // buildkit marker
    .trim()
  let instr = (cmd.split(/\s+/)[0] || '').toUpperCase()
  if (LAYER_INSTRUCTIONS.includes(instr)) {
    cmd = cmd.slice(instr.length).trim()
  } else {
    instr = 'RUN' // unrecognised → almost always a shell layer
  }
  if (instr === 'RUN') cmd = cmd.replace(/^\/bin\/sh -c\s+/, '').trim() // buildkit RUN wrapper
  return { instr, command: cmd || '—' }
}

function KV({ k, v, mono = true }) {
  return (
    <div className="flex gap-3 py-1 border-b border-border/40">
      <span className="text-content-subtle w-44 shrink-0">{k}</span>
      <span className={`text-content break-all ${mono ? 'font-mono' : ''}`}>{v === undefined || v === null || v === '' ? '—' : v}</span>
    </div>
  )
}
function Section({ title, children }) {
  return (
    <div className="mb-4">
      {title && <div className="text-[11px] font-semibold text-content-subtle uppercase tracking-wider mb-1.5">{title}</div>}
      {children}
    </div>
  )
}

// ── tabs ──────────────────────────────────────────────────────────────────────
function Overview({ c, charts }) {
  const st = c.State || {}
  const cfg = c.Config || {}
  const ports = c.NetworkSettings?.Ports || {}
  const published = Object.entries(ports)
    .filter(([, v]) => v)
    .map(([p, binds]) => `${binds.map(b => `${b.HostIp || '0.0.0.0'}:${b.HostPort}`).join(', ')} → ${p}`)
  return (
    <div className="text-xs">
      {charts && charts.length > 0 && (
        <Section title="Live metrics">
          <div className="grid grid-cols-2 gap-2">
            {charts.map(ch => (
              <div key={ch.label} className="bg-surface/40 border border-border/60 rounded-lg px-3 py-2 min-w-0">
                <div className="flex items-center justify-between gap-1 mb-1">
                  <span className="text-[11px] font-semibold text-content-subtle uppercase tracking-wider">{ch.label}</span>
                  <span className="text-[11px] font-mono text-content truncate">{ch.value}</span>
                </div>
                <Sparkline values={ch.series} stroke={ch.stroke} height={24} />
              </div>
            ))}
          </div>
        </Section>
      )}
      <Section>
        <KV k="Name" v={(c.Name || '').replace(/^\//, '')} />
        <KV k="Image" v={cfg.Image} />
        <KV k="Image ID" v={(c.Image || '').replace('sha256:', '').slice(0, 12)} />
        <KV k="State" v={st.Status} />
        <KV k="Created" v={fmtDate(c.Created)} />
        <KV k="Started" v={fmtDate(st.StartedAt)} />
        {st.Status !== 'running' && <KV k="Finished" v={fmtDate(st.FinishedAt)} />}
        <KV k="Exit code" v={st.ExitCode} />
        <KV k="Restarts" v={c.RestartCount} />
      </Section>
      <Section title="Command">
        <KV k="Entrypoint" v={(cfg.Entrypoint || []).join(' ')} />
        <KV k="Cmd" v={(cfg.Cmd || []).join(' ')} />
        <KV k="Working dir" v={cfg.WorkingDir} />
        <KV k="User" v={cfg.User || 'root'} />
      </Section>
      <Section title="Published ports">
        {published.length ? published.map((p, i) => <div key={i} className="font-mono text-content py-0.5">{p}</div>)
          : <div className="text-content-faint">None published</div>}
      </Section>
    </div>
  )
}

function Resources({ c, stats }) {
  const hc = c.HostConfig || {}
  const s = stats.data && !Array.isArray(stats.data) ? stats.data : null
  return (
    <div className="text-xs">
      <Section title="Live usage">
        {stats.isLoading && <div className="text-content-subtle">Loading…</div>}
        {stats.error && <div className="text-warning-fg">{errText(stats.error)} (container may be stopped)</div>}
        {s && <>
          <KV k="CPU" v={s.CPUPerc} />
          <KV k="Memory" v={`${s.MemUsage}  (${s.MemPerc})`} />
          <KV k="Network I/O" v={s.NetIO} />
          <KV k="Block I/O" v={s.BlockIO} />
          <KV k="PIDs" v={s.PIDs} />
        </>}
      </Section>
      <Section title="Limits (configured)">
        <KV k="Memory limit" v={hc.Memory ? fmtBytes(hc.Memory) : 'unlimited'} />
        <KV k="CPU limit" v={hc.NanoCpus ? `${(hc.NanoCpus / 1e9).toFixed(2)} CPUs` : 'unlimited'} />
        <KV k="Restart policy" v={hc.RestartPolicy?.Name || 'no'} />
      </Section>
    </div>
  )
}

function Network({ c }) {
  const nets = c.NetworkSettings?.Networks || {}
  const hc = c.HostConfig || {}
  return (
    <div className="text-xs">
      <Section><KV k="Network mode" v={hc.NetworkMode} /></Section>
      {Object.entries(nets).map(([name, n]) => (
        <Section key={name} title={name}>
          <KV k="IP address" v={n.IPAddress} />
          <KV k="Gateway" v={n.Gateway} />
          <KV k="MAC" v={n.MacAddress} />
          <KV k="Aliases" v={(n.Aliases || []).join(', ')} />
        </Section>
      ))}
      {!Object.keys(nets).length && <div className="text-content-faint">No networks.</div>}
    </div>
  )
}

function Mounts({ c }) {
  const mounts = c.Mounts || []
  if (!mounts.length) return <div className="text-content-faint text-xs">No mounts.</div>
  return (
    <div className="text-xs space-y-2">
      {mounts.map((m, i) => (
        <div key={i} className="border border-border rounded-lg p-2.5">
          <div className="flex items-center gap-2 mb-1">
            <span className="px-1.5 py-0 rounded bg-surface-raised text-content-muted text-[10px] uppercase">{m.Type}</span>
            <span className={`text-[10px] ${m.RW ? 'text-success-fg' : 'text-warning-fg'}`}>{m.RW ? 'rw' : 'ro'}</span>
          </div>
          <KV k="Source" v={m.Source || m.Name} />
          <KV k="Destination" v={m.Destination} />
        </div>
      ))}
    </div>
  )
}

function Environment({ c }) {
  const [reveal, setReveal] = useState(false)
  const env = c.Config?.Env || []
  return (
    <div className="text-xs">
      <label className="flex items-center gap-1.5 mb-2 text-content-muted cursor-pointer select-none">
        <input type="checkbox" checked={reveal} onChange={e => setReveal(e.target.checked)} className="accent-brand-500" />
        Show values
      </label>
      {env.map((e, i) => {
        const eq = e.indexOf('=')
        const k = eq >= 0 ? e.slice(0, eq) : e
        const v = eq >= 0 ? e.slice(eq + 1) : ''
        return <KV key={i} k={k} v={reveal ? v : '••••••••'} />
      })}
      {!env.length && <div className="text-content-faint">No environment variables.</div>}
    </div>
  )
}

function Labels({ c }) {
  const labels = Object.entries(c.Config?.Labels || {})
  if (!labels.length) return <div className="text-content-faint text-xs">No labels.</div>
  return <div className="text-xs">{labels.map(([k, v]) => <KV key={k} k={k} v={v} />)}</div>
}

function Health({ c }) {
  const cfg = c.Config?.Healthcheck
  const h = c.State?.Health
  // A healthcheck can be configured (image/compose) even before any run has produced
  // State.Health, so render the configuration whenever it exists.
  if (!cfg && !h) return <div className="text-content-faint text-xs">No healthcheck configured.</div>
  const test = cfg?.Test || []
  // Test is ["CMD-SHELL", "cmd…"] or ["CMD", "exe", "arg"…]; "NONE" disables it.
  const disabled = test[0] === 'NONE'
  const cmd = test.length > 1 ? test.slice(1).join(' ') : (test[0] && test[0] !== 'NONE' ? test[0] : '')
  return (
    <div className="text-xs">
      {cfg && !disabled && (
        <Section title="Configuration">
          <KV k="Command" v={cmd} />
          <div className="grid grid-cols-2 gap-x-6">
            <KV k="Interval" v={fmtDuration(cfg.Interval)} />
            <KV k="Timeout" v={fmtDuration(cfg.Timeout)} />
            <KV k="Retries" v={cfg.Retries ?? '—'} />
            <KV k="Start period" v={fmtDuration(cfg.StartPeriod)} />
          </div>
        </Section>
      )}
      {cfg && disabled && (
        <Section title="Configuration">
          <div className="text-content-faint">Healthcheck explicitly disabled (NONE).</div>
        </Section>
      )}
      <Section title="Status">
        <KV k="Current status" v={h?.Status || 'no checks recorded yet'} />
        <KV k="Failing streak" v={h?.FailingStreak ?? '—'} />
      </Section>
      {h?.Log?.length > 0 && (
        <Section title="Recent checks">
          {h.Log.slice(-5).reverse().map((l, i) => (
            <div key={i} className="border-b border-border/40 py-1.5">
              <div className="flex gap-3 text-content-subtle">
                <span>{fmtDate(l.Start)}</span>
                <span className={l.ExitCode === 0 ? 'text-success-fg' : 'text-danger-fg'}>exit {l.ExitCode}</span>
              </div>
              {l.Output?.trim() && <div className="text-content-muted font-mono whitespace-pre-wrap break-all mt-0.5">{l.Output.trim().slice(0, 500)}</div>}
            </div>
          ))}
        </Section>
      )}
    </div>
  )
}

function Security({ c }) {
  const hc = c.HostConfig || {}
  return (
    <div className="text-xs">
      <KV k="User" v={c.Config?.User || 'root'} />
      <KV k="Privileged" v={hc.Privileged ? 'yes' : 'no'} />
      <KV k="Read-only rootfs" v={hc.ReadonlyRootfs ? 'yes' : 'no'} />
      <KV k="Cap add" v={(hc.CapAdd || []).join(', ')} />
      <KV k="Cap drop" v={(hc.CapDrop || []).join(', ')} />
      <KV k="Security opt" v={(hc.SecurityOpt || []).join(', ')} />
    </div>
  )
}

// Layers — Dockhand-style image history: a summary header (layer count + total
// size) over a collapsible, numbered layer stack (base → top). Each row shows the
// Dockerfile instruction + size; expanding reveals the full command and metadata.
function Layers({ layers }) {
  const [openIdx, setOpenIdx] = useState(null)
  if (!layers || !layers.length) return <div className="text-content-faint text-xs">No layer history available.</div>
  // docker history returns newest-first; reverse to read like a Dockerfile (base first).
  const ordered = [...layers].reverse().map((l, i) => {
    const { instr, command } = parseLayer(l.createdBy ?? l.created_by)
    const bytes = parseSize(l.size ?? l.Size)
    return { ...l, n: i + 1, instr, command, bytes }
  })
  const total = ordered.reduce((s, l) => s + l.bytes, 0)
  const big = total > 0 ? total * 0.1 : 50 * 1e6 // a layer is "large" if >10% of the image (min 50MB)
  return (
    <div className="text-xs">
      <div className="flex items-center justify-between bg-surface/50 border border-border rounded-lg px-3 py-2 mb-3">
        <div className="flex gap-5">
          <span className="text-content-subtle">Total layers <span className="text-content-strong font-semibold ml-1">{ordered.length}</span></span>
          <span className="text-content-subtle">Total size <span className="text-content-strong font-semibold ml-1">{fmtBytes(total)}</span></span>
        </div>
      </div>
      <div className="text-[11px] text-content-subtle mb-2">Layer stack (base → top) — click a row to expand</div>
      <div className="space-y-1">
        {ordered.map((l, i) => {
          const open = openIdx === i
          const large = l.bytes >= big && l.bytes > 0
          const missing = !l.id || (l.id ?? l.ID) === '<missing>'
          return (
            <div key={i} className="border border-border rounded-lg overflow-hidden">
              <button type="button" onClick={() => setOpenIdx(open ? null : i)}
                className="w-full flex items-center gap-2 px-2.5 py-1.5 hover:bg-surface-raised/50 transition-colors text-left">
                <span className="text-content-faint shrink-0 w-4">{open ? '▾' : '▸'}</span>
                <span className="text-content-subtle font-mono shrink-0 w-7 text-right">#{l.n}</span>
                <span className={`shrink-0 px-1.5 py-0.5 rounded text-[10px] font-bold font-mono ${LAYER_COLORS[l.instr] || 'bg-surface-overlay text-content'}`}>{l.instr}</span>
                <span className="flex-1 min-w-0 truncate font-mono text-content">{l.command}</span>
                <span className="shrink-0 text-content-subtle font-mono">{fmtBytes(l.bytes)}</span>
                {large && <span className="shrink-0 px-1.5 py-0.5 rounded-full text-[10px] bg-warning-subtle/60 text-warning-fg">Large</span>}
              </button>
              {open && (
                <div className="px-3 py-2 border-t border-border bg-canvas/40 space-y-2">
                  <div className="grid grid-cols-2 gap-x-6">
                    <KV k="Instruction" v={l.instr} />
                    <KV k="Size" v={fmtBytes(l.bytes)} />
                    <KV k="Created" v={fmtDate(l.createdAt ?? l.created_at)} />
                    {!missing && <KV k="Layer ID" v={String(l.id ?? l.ID).replace('sha256:', '').slice(0, 19)} />}
                  </div>
                  <div>
                    <div className="text-[11px] font-semibold text-content-subtle uppercase tracking-wider mb-1">Command</div>
                    <pre className="text-[11px] text-content font-mono whitespace-pre-wrap break-all bg-canvas/60 rounded-lg p-2 border border-border">{l.command}</pre>
                  </div>
                  {(l.comment ?? l.Comment) && <KV k="Comment" v={l.comment ?? l.Comment} />}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}

// parseTop splits `docker top` output into headers + rows. The last column (CMD)
// can contain spaces, so anything past the header count is rejoined into it.
function parseTop(text) {
  const lines = (text || '').trim().split('\n').filter(l => l.trim())
  if (!lines.length) return { headers: [], rows: [] }
  const headers = lines[0].trim().split(/\s+/)
  const n = headers.length
  const rows = lines.slice(1).map(line => {
    const parts = line.trim().split(/\s+/)
    return parts.length <= n ? parts : [...parts.slice(0, n - 1), parts.slice(n - 1).join(' ')]
  })
  return { headers, rows }
}

function Processes({ top }) {
  if (top.isLoading) return <div className="text-content-subtle text-xs">Loading…</div>
  if (top.error) return <div className="text-warning-fg text-xs">{errText(top.error)} (container may be stopped)</div>
  const { headers, rows } = parseTop(top.data?.output)
  if (!headers.length) return <div className="text-content-faint text-xs">No processes.</div>
  const keep = headers.map((_, i) => i).filter(i => !TOP_DROP.has(headers[i]))
  return (
    <div className="overflow-x-auto">
      <table className="text-[11px] font-mono w-full">
        <thead>
          <tr className="text-content-subtle text-left border-b border-border">
            {keep.map(i => <th key={i} className="py-1 pr-4 font-semibold uppercase tracking-wider whitespace-nowrap">{headers[i]}</th>)}
          </tr>
        </thead>
        <tbody>
          {rows.map((r, ri) => (
            <tr key={ri} className="border-b border-border/30 text-content">
              {keep.map(i => <td key={i} className={`py-1 pr-4 align-top ${headers[i] === 'CMD' ? 'break-all' : 'whitespace-nowrap'}`}>{r[i] ?? ''}</td>)}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function CopyBtn({ text }) {
  const [copied, setCopied] = useState(false)
  return (
    <button type="button"
      onClick={async () => { try { await navigator.clipboard.writeText(text); setCopied(true); setTimeout(() => setCopied(false), 1500) } catch { /* ignore */ } }}
      className="text-xs px-2 py-1 rounded bg-surface-raised hover:bg-surface-overlay text-content transition-colors">
      {copied ? 'Copied ✓' : 'Copy'}
    </button>
  )
}

function RawJson({ data }) {
  const json = JSON.stringify(data, null, 2)
  return (
    <div>
      <div className="flex justify-end mb-2"><CopyBtn text={json} /></div>
      <pre className="text-[11px] text-content font-mono whitespace-pre overflow-x-auto bg-canvas/60 rounded-lg p-3 border border-border">{json}</pre>
    </div>
  )
}

// ── modal ─────────────────────────────────────────────────────────────────────
export default function ContainerInfoModal({ workspace, wsName, env, service, short, onClose }) {
  const [tab, setTab] = useState('Overview')
  const [statsHist, setStatsHist] = useState([])

  const insp = useQuery({
    queryKey: ['cinspect', workspace, wsName, env, service],
    queryFn: () => fetchContainerInspect(workspace, wsName, env, service),
    retry: false,
  })
  const c = Array.isArray(insp.data) ? insp.data[0] : null

  // Poll this container's docker stats while Overview/Resources is showing, so the
  // Overview sparklines are true per-container live metrics (built up in-memory).
  const liveTab = tab === 'Overview' || tab === 'Resources'
  const stats = useQuery({
    queryKey: ['cstats', workspace, wsName, env, service],
    queryFn: () => fetchContainerStats(workspace, wsName, env, service),
    enabled: liveTab, retry: false,
    refetchInterval: liveTab ? 2500 : false,
  })

  useEffect(() => {
    const s = stats.data
    if (!s || Array.isArray(s) || typeof s !== 'object') return
    setStatsHist(h => [...h, {
      cpu: parsePercent(s.CPUPerc),
      mem: parseSize((s.MemUsage || '').split('/')[0]),
      net: ioSum(s.NetIO),
      blk: ioSum(s.BlockIO),
    }].slice(-40))
  }, [stats.dataUpdatedAt]) // eslint-disable-line react-hooks/exhaustive-deps

  // Derive per-container chart series from the live history (rates from deltas).
  const rate = (arr, i) => (i === 0 ? 0 : Math.max(0, (arr[i] - arr[i - 1]) / 2.5))
  const netArr = statsHist.map(s => s.net)
  const blkArr = statsHist.map(s => s.blk)
  const netRate = statsHist.map((_, i) => rate(netArr, i))
  const blkRate = statsHist.map((_, i) => rate(blkArr, i))
  const last = statsHist[statsHist.length - 1]
  const liveCharts = last ? [
    { label: 'CPU',      value: `${last.cpu.toFixed(1)}%`,             series: statsHist.map(s => s.cpu), stroke: '#22d3ee' },
    { label: 'Memory',   value: fmtBytes(last.mem),                   series: statsHist.map(s => s.mem), stroke: '#a78bfa' },
    { label: 'Disk I/O', value: fmtRate(blkRate[blkRate.length - 1]), series: blkRate,                   stroke: '#34d399' },
    { label: 'Network',  value: fmtRate(netRate[netRate.length - 1]), series: netRate,                   stroke: '#fbbf24' },
  ] : []
  const top = useQuery({
    queryKey: ['ctop', workspace, wsName, env, service],
    queryFn: () => fetchContainerTop(workspace, wsName, env, service),
    enabled: tab === 'Processes', retry: false,
  })
  const hist = useQuery({
    queryKey: ['chist', workspace, wsName, env, service],
    queryFn: () => fetchContainerHistory(workspace, wsName, env, service),
    enabled: tab === 'Layers', retry: false,
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 p-4" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-4xl h-[600px] max-h-[88vh] flex flex-col shadow-2xl"
        onClick={e => e.stopPropagation()}>
        {/* Header */}
        <div className="flex items-center justify-between px-4 py-3 border-b border-border">
          <div className="flex items-baseline gap-2 min-w-0">
            <h3 className="text-sm font-semibold text-content-strong">{short}</h3>
            <span className="text-xs text-content-subtle font-mono truncate">{service}</span>
          </div>
          <button onClick={onClose} className="text-content-subtle hover:text-content text-lg leading-none px-1">×</button>
        </div>
        {/* Tabs */}
        <div className="flex gap-1 px-3 pt-2 border-b border-border overflow-x-auto shrink-0">
          {TABS.map(t => (
            <button key={t} onClick={() => setTab(t)}
              className={`px-2.5 py-1.5 text-xs rounded-t-lg whitespace-nowrap transition-colors ${
                tab === t ? 'text-content-strong bg-surface-raised' : 'text-content-subtle hover:text-content'}`}>
              {t}
            </button>
          ))}
        </div>
        {/* Body — fixed height, scrolls internally so the modal never resizes per tab */}
        <div className="flex-1 min-h-0 overflow-y-auto p-4">
          {insp.isLoading && <div className="text-content-subtle text-xs">Loading…</div>}
          {insp.error && <div className="text-danger-fg text-xs">{errText(insp.error)}</div>}
          {c && (
            <div key={tab} className="tab-fade">
              {tab === 'Overview' && <Overview c={c} charts={liveCharts} />}
              {tab === 'Resources' && <Resources c={c} stats={stats} />}
              {tab === 'Network' && <Network c={c} />}
              {tab === 'Mounts' && <Mounts c={c} />}
              {tab === 'Processes' && <Processes top={top} />}
              {tab === 'Environment' && <Environment c={c} />}
              {tab === 'Labels' && <Labels c={c} />}
              {tab === 'Health' && <Health c={c} />}
              {tab === 'Security' && <Security c={c} />}
              {tab === 'Layers' && (hist.isLoading ? <div className="text-content-subtle text-xs">Loading…</div>
                : hist.error ? <div className="text-warning-fg text-xs">{errText(hist.error)}</div>
                  : <Layers layers={hist.data?.layers} />)}
              {tab === 'JSON' && <RawJson data={insp.data} />}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
