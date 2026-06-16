import { useState, useEffect, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchConfig, putConfig, deleteWorkspace, fetchEnvVars, updateEnvVars, fetchWorkspaceHosts, fetchWorkspace, migrateWorkspace, setEnvHost, getMigrationJob, fetchWorkspaceBackupTargets, fetchBackupServices, scanRepo, fetchWorkspaceSettings, copyEnvironment, replaceProjectSource, seedDatabase } from '../lib/api'
import DropZone from '../components/DropZone'
import { resolveEnvRoute } from '../lib/envRoute'
import VerticalTabs from '../components/VerticalTabs'
import PipelinesTab from '../components/PipelinesTab'
import RegistryPicker from '../components/RegistryPicker'
import DatabaseSelect from '../components/DatabaseSelect'
import ManagedServices, { enabledDependsOnTargets } from '../components/ManagedServices'
import EnvReorderModal from '../components/EnvReorderModal'
import { BackupScheduleEditor } from '../components/BackupSchedules'
import Layout from '../components/Layout'
import TrashIcon from '../components/TrashIcon'
import PortWarnings from '../components/PortWarnings'
import { portConflicts, hostPortsFromConfig } from '../lib/ports'
import { usePortConflicts } from '../hooks/usePortConflicts'
import { useConfirm } from '../context/ConfirmContext'

// ── Shared primitives ─────────────────────────────────────────────────────────

function Label({ children, required }) {
  return (
    <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
      {children}{required && <span className="text-danger-fg ml-0.5">*</span>}
    </label>
  )
}

function Input({ value, onChange, placeholder, type = 'text', ...rest }) {
  return (
    <input
      type={type} value={value ?? ''} onChange={e => onChange(e.target.value)}
      placeholder={placeholder}
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors"
      {...rest}
    />
  )
}

// Toggle: label + switch. Default spreads them (justify-between) for full-width
// setting rows; `inline` packs the switch right after the label (compact, for
// grid/aligned layouts).
function Toggle({ label, hint, checked, onChange, disabled = false, inline = false }) {
  return (
    <div className={`flex items-center ${inline ? 'gap-2.5' : 'justify-between'} ${disabled ? 'opacity-50' : ''}`}>
      <div>
        <p className="text-sm text-content">{label}</p>
        {hint && <p className="text-xs text-content-subtle mt-0.5">{hint}</p>}
      </div>
      <button
        type="button"
        onClick={() => !disabled && onChange(!checked)}
        disabled={disabled}
        className={`relative w-10 h-5 rounded-full transition-colors shrink-0 disabled:cursor-not-allowed ${
          checked && !disabled ? 'bg-brand-600' : checked ? 'bg-brand-800' : 'bg-surface-overlay'
        }`}
      >
        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-5' : ''}`} />
      </button>
    </div>
  )
}

function Select({ value, onChange, options }) {
  return (
    <select value={value ?? ''} onChange={e => onChange(e.target.value)}
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500">
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

const DEPLOYMENT_OPTIONS = [{ value: 'compose', label: 'Docker Compose' }, { value: 'swarm', label: 'Docker Swarm' }]

// Unified service model (Phase 2a): a service is built, pulled, or reuses another
// service's image (a worker). Build services scaffold a Dockerfile from a template.
const SERVICE_SOURCE = [
  { value: 'image', label: 'Pull image' },
  { value: 'build', label: 'Build from source' },
  { value: 'image_from', label: 'Reuse a service’s image (worker)' },
]
const BUILD_TEMPLATES = [
  { value: '', label: 'Custom Dockerfile (no scaffold)' },
  { value: 'laravel', label: 'Laravel (PHP-FPM)' },
  { value: 'nodejs', label: 'Node.js' },
  { value: 'nextjs', label: 'Next.js' },
  { value: 'react', label: 'React / Vite' },
]
// Managed-dependency service names a service may depend_on.
const MANAGED_DEPS = ['postgres', 'mysql', 'mariadb', 'redis', 'minio']

function serviceSource(s) {
  if (s.build) return 'build'
  if (s.image_from) return 'image_from'
  return 'image'
}

// ── Helpers: port rows ↔ img fields ──────────────────────────────────────────

// splitPortSpec splits a "host:container" port spec on ':' OUTSIDE ${...}, so an
// env-var default like ${APP_PORT:-80} (whose inner colon is not a field break) isn't
// mangled. Mirrors the backend detector's splitColonOutsideBraces.
function splitPortSpec(s) {
  const parts = []
  let depth = 0, start = 0
  for (let i = 0; i < s.length; i++) {
    const c = s[i]
    if (c === '{') depth++
    else if (c === '}') { if (depth > 0) depth-- }
    else if (c === ':' && depth === 0) { parts.push(s.slice(start, i)); start = i + 1 }
  }
  parts.push(s.slice(start))
  return parts
}

function imgToPortRows(img) {
  const rows = []
  const linkSet = new Set((img.link_ports || []).map(String))
  // Default: if no link_ports defined, treat the primary host_port as linked
  const useDefault = linkSet.size === 0
  if (img.host_port || img.port) {
    const h = String(img.host_port || '')
    rows.push({ host: h, container: String(img.port || ''), link: useDefault ? !!h : linkSet.has(h) })
  }
  for (const ep of (img.extra_ports || [])) {
    const p = splitPortSpec(String(ep))
    const h = p.length === 2 ? p[0] : ''
    rows.push(p.length === 2
      ? { host: h, container: p[1], link: linkSet.has(h) }
      : { host: '',  container: p[0], link: false })
  }
  return rows.length ? rows : [{ host: '', container: '', link: false }]
}

function portRowsToFields(rows) {
  // Only rows with a container port contribute to config
  const valid = rows.filter(r => r.container.trim())
  if (!valid.length) return { port: 0, host_port: '', extra_ports: [], link_ports: [] }
  const [first, ...rest] = valid
  return {
    port:        parseInt(first.container) || 0,
    host_port:   first.host.trim(),
    extra_ports: rest.map(r =>
      r.host.trim() ? `${r.host.trim()}:${r.container.trim()}` : r.container.trim()),
    link_ports:  valid.filter(r => r.link && r.host.trim()).map(r => r.host.trim()),
  }
}

// ── RW/RO segmented control ───────────────────────────────────────────────────

function VolModeToggle({ mode, onChange }) {
  return (
    <div className="flex items-center rounded overflow-hidden border border-border-strong shrink-0 text-xs font-mono">
      {['rw', 'ro'].map(m => (
        <button
          key={m}
          type="button"
          onClick={() => onChange(m)}
          className={`px-2 py-0.5 transition-colors ${
            mode === m
              ? m === 'ro'
                ? 'bg-amber-600 text-white'
                : 'bg-surface-overlay text-content-strong'
              : 'bg-surface text-content-subtle hover:text-content'
          }`}
        >{m.toUpperCase()}</button>
      ))}
    </div>
  )
}

// ── Helpers: volume rows ↔ volumes array ──────────────────────────────────────

// Volume string format: "source:path" | "source:path:ro" | "source:path:rw"
// We parse the last segment as mode when it is exactly "ro" or "rw".
function parseVolumeString(v) {
  const parts = v.split(':')
  const last = parts[parts.length - 1]
  if ((last === 'ro' || last === 'rw') && parts.length >= 3) {
    return { source: parts[0], path: parts.slice(1, -1).join(':'), mode: last }
  }
  const c = v.indexOf(':')
  return c === -1
    ? { source: v, path: '', mode: 'rw' }
    : { source: v.slice(0, c), path: v.slice(c + 1), mode: 'rw' }
}

function serializeVolumeRow(r) {
  const src  = r.source.trim()
  const path = r.path.trim()
  if (!src && !path) return null
  if (!src || !path) return src || path
  return r.mode === 'ro' ? `${src}:${path}:ro` : `${src}:${path}`
}

function imgToVolumeRows(img) {
  const vols = img.volumes || []
  if (!vols.length) return [{ source: '', path: '', mode: 'rw' }]
  return vols.map(parseVolumeString)
}

function volumeRowsToArray(rows) {
  return rows.map(serializeVolumeRow).filter(Boolean)
}

// Build args round-trip between the build.args object and editable key/value rows.
// One trailing blank row is kept so the + behaviour matches ports/volumes.
function imgToArgRows(img) {
  const args = img.build?.args || {}
  const rows = Object.keys(args).map(k => ({ key: k, val: args[k] }))
  if (!rows.length) return [{ key: '', val: '' }]
  return rows
}

function argRowsToObject(rows) {
  const out = {}
  for (const r of rows) {
    const k = (r.key || '').trim()
    if (k) out[k] = r.val ?? ''
  }
  return Object.keys(out).length ? out : undefined
}

// Service links round-trip between the links[] array and editable rows. A link
// declares an env var pointing at another service's in-network URL; Rigger emits
// {env_var}={scheme}://{prefix}_{service}:{port}{path}. One trailing blank row is
// kept so + behaves like ports/args.
function imgToLinkRows(img) {
  const rows = (img.links || []).map(l => ({
    env_var: l.env_var || '', service: l.service || '',
    port: l.port || '', path: l.path || '', scheme: l.scheme || '',
  }))
  if (!rows.length) return [{ env_var: '', service: '', port: '', path: '', scheme: '' }]
  return rows
}

function linkRowsToArray(rows) {
  const out = []
  for (const r of rows) {
    const ev = (r.env_var || '').trim()
    const svc = (r.service || '').trim()
    if (!ev || !svc) continue
    const link = { service: svc, env_var: ev }
    if ((r.port || '').trim()) link.port = r.port.trim()
    if ((r.path || '').trim()) link.path = r.path.trim()
    if ((r.scheme || '').trim() && r.scheme.trim() !== 'http') link.scheme = r.scheme.trim()
    out.push(link)
  }
  return out.length ? out : undefined
}

// Standard in-network ports for managed-dependency link targets (no service entry).
const MANAGED_LINK_PORT = { postgres: '5432', mysql: '3306', mariadb: '3306', redis: '6379', minio: '9000', adminer: '8080' }

// ── Image stack editor ────────────────────────────────────────────────────────

const RESTART_OPTIONS = [
  { value: 'unless-stopped', label: 'Unless stopped (recommended)' },
  { value: 'always',         label: 'Always' },
  { value: 'on-failure',     label: 'On failure' },
  { value: 'no',             label: 'No (never restart)' },
]

// ServiceCard keeps local row state so empty rows added by + buttons survive
// until the user types into them. Without local state, portRowsToFields() would
// immediately filter out the empty new row and Add would appear broken.
function ServiceCard({ img, idx, allImages, onUpdate, onRemove, managedDeps = [] }) {
  const confirm = useConfirm()
  const [open, setOpen] = useState(idx === 0) // collapsible — first service open
  const [portRows,   setPortRows]   = useState(() => imgToPortRows(img))
  const [volumeRows, setVolumeRows] = useState(() => imgToVolumeRows(img))
  const [argRows,    setArgRows]    = useState(() => imgToArgRows(img))
  const [linkRows,   setLinkRows]   = useState(() => imgToLinkRows(img))
  // Explicit "override default command" toggle. Kept as local UI state so the input
  // stays revealed while the field is momentarily empty (before the user types).
  const [cmdOverride, setCmdOverride] = useState(() => !!img.command)

  function syncPorts(rows) {
    setPortRows(rows)
    onUpdate(idx, { ...img, ...portRowsToFields(rows) })
  }
  function syncVolumes(rows) {
    setVolumeRows(rows)
    onUpdate(idx, { ...img, volumes: volumeRowsToArray(rows) })
  }
  function syncArgs(rows) {
    setArgRows(rows)
    onUpdate(idx, { ...img, build: { ...(img.build || {}), args: argRowsToObject(rows) } })
  }
  function syncLinks(rows) {
    setLinkRows(rows)
    onUpdate(idx, { ...img, links: linkRowsToArray(rows) })
  }
  function upd(field, val) {
    onUpdate(idx, { ...img, [field]: val })
  }
  // Switch a service's source, clearing the other source fields.
  function setSource(s) {
    const base = { ...img, build: undefined, image: undefined, tag: undefined, image_from: undefined }
    if (s === 'build') onUpdate(idx, { ...base, build: img.build || { template: '' }, env_file: true })
    else if (s === 'image_from') onUpdate(idx, { ...base, image_from: img.image_from || (otherNames[0] || ''), env_file: true })
    else onUpdate(idx, { ...base, image: img.image || '', tag: img.tag || 'latest' })
  }

  const otherNames = allImages.map((m, j) => j !== idx ? m.name : null).filter(Boolean)
  // Only offer the managed services actually enabled on this project (not the full
  // static list) so depends_on can't reference a dependency that won't exist.
  const depOptions = [...otherNames, ...managedDeps]

  const monoInput = 'px-2 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-brand-500'

  return (
    <div className="bg-surface-raised/50 border border-border-strong rounded-xl overflow-hidden">
      {/* Collapsible header */}
      <div className="flex items-center gap-3 p-4 cursor-pointer select-none" onClick={() => setOpen(o => !o)}>
        <span className={`text-content-subtle text-xs transition-transform ${open ? 'rotate-90' : ''}`}>▸</span>
        <span className="text-sm font-semibold text-content-strong">{img.name || `Service ${idx + 1}`}</span>
        {(img.image || img.tag) && <span className="text-xs text-content-subtle font-mono truncate">{img.image}{img.tag ? `:${img.tag}` : ''}</span>}
        <div className="ml-auto flex items-center gap-3">
          {hostPortsFromConfig(img).length > 0 && <span className="text-[11px] px-2 py-0.5 rounded bg-surface-overlay/40 text-content-muted">{hostPortsFromConfig(img).join(', ')}</span>}
          {allImages.length > 1 && (
            <button type="button"
              onClick={async (e) => {
                e.stopPropagation()
                if (await confirm({
                  title: 'Remove service?',
                  message: `Remove "${img.name || `Service ${idx + 1}`}" from this project? It will be deleted when you save changes.`,
                  confirmLabel: 'Remove',
                })) onRemove(idx)
              }}
              className="text-xs text-danger-fg hover:text-danger-fg transition-colors">Remove</button>
          )}
        </div>
      </div>
      {open && (<div className="px-4 pb-4 space-y-4 border-t border-border-strong pt-4">

      {/* Identity + source */}
      <div className="grid grid-cols-2 gap-3">
        <div><Label required>Service name</Label>
          <Input value={img.name} onChange={v => upd('name', v)} placeholder="api" /></div>
        <div><Label>Source</Label>
          <Select value={serviceSource(img)} onChange={setSource} options={SERVICE_SOURCE} /></div>
      </div>

      {serviceSource(img) === 'image' && (
        <div className="grid grid-cols-2 gap-3">
          <div><Label required>Image</Label>
            <Input value={img.image} onChange={v => upd('image', v)} placeholder="nginx" /></div>
          <div><Label>Tag</Label>
            <Input value={img.tag} onChange={v => upd('tag', v)} placeholder="latest" /></div>
        </div>
      )}
      {serviceSource(img) === 'build' && (
        <>
        <div className="grid grid-cols-2 gap-3">
          <div><Label>Dockerfile template</Label>
            <Select value={img.build?.template || ''} onChange={v => upd('build', { ...(img.build || {}), template: v })} options={BUILD_TEMPLATES} />
            <p className="text-xs text-content-subtle mt-1">Scaffolds a starter Dockerfile; replace with your own via repo sync.</p></div>
          <div><Label>Build context</Label>
            <Input value={img.build?.context || ''} onChange={v => upd('build', { ...(img.build || {}), context: v })} placeholder={img.name || 'service dir'} /></div>
        </div>

        {/* Build args — passed as --build-arg KEY=VALUE at image build time. */}
        <div>
          <Label>Build args</Label>
          <p className="text-xs text-content-subtle mb-2">
            Passed to <code className="font-mono text-xs">docker build --build-arg</code> (for values baked at build time, e.g. a Next.js
            <code className="font-mono text-xs"> next.config</code> rewrite target). Values may use{' '}
            <code className="font-mono text-xs">${'{ENV}'}</code>, <code className="font-mono text-xs">${'{VERSION}'}</code>, and{' '}
            <code className="font-mono text-xs">${'{ROUTE_URL}'}</code> (the env's public URL).
          </p>
          <div className="space-y-1.5">
            {argRows.map((row, ri) => (
              <div key={ri} className="flex items-center gap-2">
                <input type="text" value={row.key}
                  onChange={e => { const r = argRows.map((x,j)=>j===ri?{...x,key:e.target.value}:x); syncArgs(r) }}
                  placeholder="NEXT_PUBLIC_API_URL" className={`flex-1 ${monoInput}`} />
                <span className="text-content-subtle font-bold shrink-0">=</span>
                <input type="text" value={row.val}
                  onChange={e => { const r = argRows.map((x,j)=>j===ri?{...x,val:e.target.value}:x); syncArgs(r) }}
                  placeholder="${ROUTE_URL}/api" className={`flex-[2] ${monoInput}`} />
                <button type="button" title="Remove build arg"
                  onClick={() => { const r = argRows.filter((_,j)=>j!==ri); syncArgs(r.length ? r : [{ key:'', val:'' }]) }}
                  className="shrink-0 text-content-faint hover:text-danger-fg transition-colors px-1">✕</button>
              </div>
            ))}
          </div>
          <button type="button" onClick={() => syncArgs([...argRows, { key:'', val:'' }])}
            className="mt-2 text-xs text-brand-400 hover:text-brand-300 transition-colors">+ Add build arg</button>
        </div>
        </>
      )}
      {serviceSource(img) === 'image_from' && (
        <div>
          <Label required>Reuse image of</Label>
          <Select value={img.image_from || ''} onChange={v => upd('image_from', v)}
            options={[{ value: '', label: '— pick a service —' }, ...otherNames.map(n => ({ value: n, label: n }))]} />
          <p className="text-xs text-content-subtle mt-1">A worker/scheduler that runs another service's built image with a custom command.</p>
        </div>
      )}

      {/* Command override — explicit toggle reveals the input; off clears it so the
          image's own default CMD (or the build's) is used. */}
      {/* Service options — toggles in one aligned column (switch hugs its label),
          the reveal input in the right column when the toggle is on. */}
      <div className="grid grid-cols-[max-content_1fr] gap-x-5 gap-y-3 items-center">
        <Toggle inline label="Override default command"
          checked={cmdOverride}
          onChange={v => { setCmdOverride(v); if (!v && img.command) upd('command', '') }} />
        {cmdOverride
          ? <Input value={img.command} onChange={v => upd('command', v)} placeholder="php artisan queue:work" />
          : <div />}

        <Toggle inline label="Mount .env (env_file)"
          checked={img.env_file !== false && serviceSource(img) !== 'image'}
          onChange={v => upd('env_file', v)} />
        {serviceSource(img) !== 'image'
          ? <Input value={img.env_file_mount} onChange={v => upd('env_file_mount', v)} placeholder="/var/www/html/.env — also mount as file (optional)" />
          : <div />}

        {serviceSource(img) !== 'image' && (img.env_file_mount || '').trim()
          ? <>
              <Toggle inline label="App owns .env (writable)"
                checked={!!img.env_file_writable}
                onChange={v => upd('env_file_writable', v)} />
              <p className="text-xs text-content-subtle self-center">
                Bind the .env writable (not read-only) and skip process-env injection, so an app
                that writes its own <code className="font-mono text-xs">.env</code> at runtime — a
                CodeCanyon installer setting <code className="font-mono text-xs">INSTALLED=true</code> —
                persists. Rigger still re-asserts managed DB/Redis/storage keys on redeploy.
              </p>
            </>
          : null}

        <Toggle inline label="Web entry (route traffic here)" checked={!!img.web_routed} onChange={async v => {
          if (v) {
            const sub = (img.subdomain || '').trim()
            const clash = allImages.find((m, j) => j !== idx && m.web_routed && (m.subdomain || '').trim() === sub)
            if (clash) {
              const host = sub ? `the "${sub}" subdomain` : 'the apex domain'
              const ok = await confirm({
                title: 'Another service already routes here',
                message: `"${clash.name || 'another service'}" is already the web entry on ${host}. Two services on the same host collide in Traefik — give one a distinct subdomain to run both. Enable anyway?`,
                confirmLabel: 'Enable anyway',
              })
              if (!ok) return
            }
          }
          upd('web_routed', v)
        }} />
        {img.web_routed
          ? <Input value={img.subdomain} onChange={v => upd('subdomain', v)} placeholder="subdomain (blank = apex domain)" />
          : <div />}
      </div>
      <p className="text-xs text-content-subtle">
        <strong className="text-content-muted">Override command</strong> replaces the image/build default. <strong className="text-content-muted">Mount .env as a file</strong> also writes the env to disk for apps that read a physical <code className="font-mono text-xs">.env</code> (e.g. Laravel <code className="font-mono text-xs">php artisan serve</code>).
      </p>

      {/* Port mappings */}
      <div>
        <Label>Port mappings</Label>
        <p className="text-xs text-content-subtle mb-2">
          <code className="font-mono text-xs">HOST PORT</code> : <code className="font-mono text-xs">CONTAINER PORT</code> — leave host blank to expose internally only.
          <span className="ml-2 text-content-faint">🔗 = show as link on env card</span>
        </p>
        <div className="space-y-1.5">
          {portRows.map((row, ri) => (
            <div key={ri} className="flex items-center gap-2">
              <input type="text" value={row.host}
                onChange={e => { const r = portRows.map((x,j)=>j===ri?{...x,host:e.target.value}:x); syncPorts(r) }}
                placeholder="8080" className={`flex-1 ${monoInput}`} />
              <span className="text-content-subtle font-bold shrink-0">:</span>
              <input type="text" value={row.container}
                onChange={e => { const r = portRows.map((x,j)=>j===ri?{...x,container:e.target.value}:x); syncPorts(r) }}
                placeholder="80" className={`flex-1 ${monoInput}`} />
              {/* Link checkbox — only meaningful when a host port is set */}
              <label title="Show as clickable link on env card" className={`flex items-center gap-1 shrink-0 cursor-pointer select-none ${row.host.trim() ? 'text-content-muted hover:text-brand-400' : 'text-content-faint cursor-not-allowed'}`}>
                <input
                  type="checkbox"
                  checked={!!row.link}
                  disabled={!row.host.trim()}
                  onChange={e => { const r = portRows.map((x,j)=>j===ri?{...x,link:e.target.checked}:x); syncPorts(r) }}
                  className="accent-brand-500 w-3.5 h-3.5"
                />
                <span className="text-sm">🔗</span>
              </label>
              {portRows.length > 1 && (
                <button type="button" onClick={() => syncPorts(portRows.filter((_,j)=>j!==ri))}
                  className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
              )}
            </div>
          ))}
          <button type="button" onClick={() => setPortRows(r => [...r, { host: '', container: '', link: false }])}
            className="text-xs text-brand-400 hover:text-brand-300 transition-colors flex items-center gap-1 mt-1">
            <span className="text-base leading-none">＋</span> Add port mapping
          </button>
        </div>
      </div>

      {/* Volume mappings */}
      <div>
        <Label>Volume mappings</Label>
        <p className="text-xs text-content-subtle mb-2">
          <code className="font-mono text-xs">SOURCE</code> : <code className="font-mono text-xs">CONTAINER PATH</code> —
          use <code className="font-mono text-xs">./volumes/name</code> for a bind mount scoped to this env, or a plain name for a Docker named volume.
        </p>
        <div className="space-y-1.5">
          {volumeRows.map((row, ri) => {
            const isBind = row.source.startsWith('./') || row.source.startsWith('/')
            return (
              <div key={ri} className="flex items-center gap-2">
                <span className={`text-xs px-1.5 py-0.5 rounded font-medium shrink-0 ${
                  isBind ? 'bg-info-subtle text-info-fg' : 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300'
                }`}>{isBind ? 'bind' : 'named'}</span>
                <input type="text" value={row.source}
                  onChange={e => { const r = volumeRows.map((x,j)=>j===ri?{...x,source:e.target.value}:x); syncVolumes(r) }}
                  placeholder="./volumes/app_data" className={`flex-1 ${monoInput}`} />
                <span className="text-content-subtle font-bold shrink-0">:</span>
                <input type="text" value={row.path}
                  onChange={e => { const r = volumeRows.map((x,j)=>j===ri?{...x,path:e.target.value}:x); syncVolumes(r) }}
                  placeholder="/var/lib/data" className={`flex-1 ${monoInput}`} />
                <VolModeToggle mode={row.mode || 'rw'} onChange={m => { const r = volumeRows.map((x,j)=>j===ri?{...x,mode:m}:x); syncVolumes(r) }} />
                <button type="button" onClick={() => syncVolumes(volumeRows.filter((_,j)=>j!==ri))}
                  className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
              </div>
            )
          })}
          <button type="button" onClick={() => setVolumeRows(r => [...r, { source: './volumes/', path: '', mode: 'rw' }])}
            className="text-xs text-brand-400 hover:text-brand-300 transition-colors flex items-center gap-1 mt-1">
            <span className="text-base leading-none">＋</span> Add volume
          </button>
        </div>
      </div>

      {/* Restart policy — half width */}
      <div className="w-1/2">
        <Label>Restart policy</Label>
        <Select value={img.restart || 'unless-stopped'} onChange={v => upd('restart', v)} options={RESTART_OPTIONS} />
      </div>

      {/* Healthcheck */}
      <div className="space-y-3">
        <div>
          <Label>Healthcheck command</Label>
          <p className="text-xs text-content-subtle mb-2">
            Shell command Docker runs to test container health. Leave blank to disable.
            Example: <code className="font-mono text-xs">curl -sf http://localhost/health || exit 1</code>
          </p>
          <input
            type="text"
            value={img.healthcheck || ''}
            onChange={e => upd('healthcheck', e.target.value)}
            placeholder="curl -sf http://localhost/health || exit 1"
            className="w-full px-2 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono placeholder-content-faint focus:outline-none focus:border-brand-500"
          />
        </div>
        {/* Time parameters — only shown when a command is set */}
        {img.healthcheck && (
          <div>
            <p className="text-xs text-content-subtle mb-2">
              Timing parameters — enter seconds only (numbers). <code className="font-mono text-xs">start_interval</code> requires Docker Engine 25+.
            </p>
            <div className="grid grid-cols-5 gap-2">
              {[
                { key: 'interval',       label: 'Interval',        placeholder: '30' },
                { key: 'timeout',        label: 'Timeout',         placeholder: '10' },
                { key: 'retries',        label: 'Retries',         placeholder: '3',  noSuffix: true },
                { key: 'start_period',   label: 'Start period',    placeholder: '30' },
                { key: 'start_interval', label: 'Start interval',  placeholder: '5'  },
              ].map(({ key, label, placeholder, noSuffix }) => {
                const raw = (img.healthcheck_config || {})[key] || ''
                // Strip trailing 's' for display; store with 's' (except retries)
                const display = raw.replace(/s$/, '')
                return (
                  <div key={key}>
                    <label className="block text-xs text-content-subtle mb-1">
                      {label}{!noSuffix && <span className="text-content-faint"> (s)</span>}
                    </label>
                    <input
                      type="number"
                      min="1"
                      value={display}
                      placeholder={placeholder}
                      onChange={e => {
                        const v = e.target.value.replace(/[^0-9]/g, '')
                        const stored = v ? (noSuffix ? v : `${v}s`) : ''
                        upd('healthcheck_config', {
                          ...(img.healthcheck_config || {}),
                          [key]: stored,
                        })
                      }}
                      className="w-full px-2 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-brand-500"
                    />
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>

      {/* depends_on */}
      {depOptions.length > 0 && (
        <div>
          <Label>Depends on</Label>
          <p className="text-xs text-content-subtle mb-2">
            This service waits for selected services (and enabled managed dependencies)
            before starting. Compose waits for healthy status when available.
          </p>
          <div className="flex flex-wrap gap-3">
            {depOptions.map(svcName => {
              const checked = (img.depends_on || []).includes(svcName)
              return (
                <label key={svcName} className="flex items-center gap-1.5 cursor-pointer select-none">
                  <input type="checkbox" checked={checked}
                    onChange={e => {
                      const deps = img.depends_on || []
                      upd('depends_on', e.target.checked
                        ? [...deps, svcName]
                        : deps.filter(d => d !== svcName))
                    }}
                    className="rounded border-border-strong bg-surface-overlay text-brand-500 focus:ring-brand-500"
                  />
                  <span className="text-sm text-content font-mono">{svcName}</span>
                </label>
              )
            })}
          </div>
        </div>
      )}

      {/* Service links — wire an env var to another service's in-network URL. */}
      <div>
        <Label>Service links</Label>
        <p className="text-xs text-content-subtle mb-2">
          Inject another service's in-network URL as an environment variable, e.g. a
          frontend reaching its API. Rigger emits{' '}
          <code className="font-mono text-xs">ENV_VAR=http://{'{service}'}:{'{port}'}{'{path}'}</code>{' '}
          (the value overrides any matching <code className="font-mono text-xs">.env</code> key). Optional — you can also set the URL by hand as an env var.
        </p>
        {depOptions.length === 0
          ? <p className="text-xs text-content-faint">Add another service or managed dependency first to link to it.</p>
          : (<div className="space-y-1.5">
          {linkRows.map((row, ri) => {
            const targetPort = (allImages.find(m => m.name === row.service)?.port) || MANAGED_LINK_PORT[row.service] || ''
            return (
              <div key={ri} className="flex items-center gap-2">
                <input type="text" value={row.env_var}
                  onChange={e => { const r = linkRows.map((x,j)=>j===ri?{...x,env_var:e.target.value}:x); syncLinks(r) }}
                  placeholder="API_URL" className={`flex-1 ${monoInput}`} />
                <span className="text-content-subtle font-bold shrink-0">=</span>
                <select value={row.service}
                  onChange={e => { const r = linkRows.map((x,j)=>j===ri?{...x,service:e.target.value}:x); syncLinks(r) }}
                  className={`shrink-0 ${monoInput}`}>
                  <option value="">— service —</option>
                  {depOptions.map(n => <option key={n} value={n}>{n}</option>)}
                </select>
                <span className="text-content-subtle shrink-0">:</span>
                <input type="text" value={row.port}
                  onChange={e => { const r = linkRows.map((x,j)=>j===ri?{...x,port:e.target.value}:x); syncLinks(r) }}
                  placeholder={targetPort || 'port'} className={`w-16 ${monoInput}`} />
                <input type="text" value={row.path}
                  onChange={e => { const r = linkRows.map((x,j)=>j===ri?{...x,path:e.target.value}:x); syncLinks(r) }}
                  placeholder="/api" className={`w-20 ${monoInput}`} />
                <button type="button" title="Remove link"
                  onClick={() => { const r = linkRows.filter((_,j)=>j!==ri); syncLinks(r.length ? r : [{ env_var:'', service:'', port:'', path:'', scheme:'' }]) }}
                  className="shrink-0 text-content-faint hover:text-danger-fg transition-colors px-1">✕</button>
              </div>
            )
          })}
          <button type="button" onClick={() => syncLinks([...linkRows, { env_var:'', service:'', port:'', path:'', scheme:'' }])}
            className="mt-1 text-xs text-brand-400 hover:text-brand-300 transition-colors">+ Add service link</button>
        </div>)}
      </div>

      {/* Advanced — extra_compose YAML */}
      <details className="group">
        <summary className="text-xs text-content-subtle cursor-pointer hover:text-content transition-colors select-none list-none flex items-center gap-1">
          <span className="group-open:rotate-90 transition-transform inline-block">▶</span>
          Advanced YAML overrides
        </summary>
        <div className="mt-2 space-y-1">
          <p className="text-xs text-content-subtle">
            Raw YAML appended to this service in the generated compose file.
            Use for: <code className="font-mono text-xs">mem_limit</code>,{' '}
            <code className="font-mono text-xs">cpus</code>,{' '}
            <code className="font-mono text-xs">logging</code>,{' '}
            <code className="font-mono text-xs">command</code>, etc.
            Run <strong>Refresh</strong> after saving to apply.
          </p>
          <textarea
            value={img.extra_compose || ''}
            onChange={e => upd('extra_compose', e.target.value)}
            rows={4}
            placeholder={"mem_limit: 512m\ncpus: '0.5'\nlogging:\n  driver: json-file"}
            spellCheck={false}
            className="w-full px-3 py-2 bg-canvas border border-border-strong rounded-lg text-success-fg text-xs font-mono placeholder-content-faint focus:outline-none focus:border-brand-500 resize-y"
          />
        </div>
      </details>
      </div>)}
    </div>
  )
}

// ── Re-scan repo (2b-4): advisory diff/merge against the live service graph ────

// Service fields that define the graph (excludes UI-only/derived keys). Used to
// decide whether a detected service differs from the current one.
const SVC_DIFF_FIELDS = [
  'build', 'image', 'image_from', 'tag', 'command', 'port', 'host_port',
  'extra_ports', 'web_routed', 'subdomain', 'healthcheck', 'env_file',
  'env_file_mount', 'env_file_writable', 'depends_on', 'volumes', 'restart', 'config_template', 'env_vars', 'links',
]

// stable serialises a value with object keys sorted at every depth, so two
// equivalent services compare equal regardless of key order.
function stable(v) {
  if (Array.isArray(v)) return '[' + v.map(stable).join(',') + ']'
  if (v && typeof v === 'object') {
    return '{' + Object.keys(v).sort().map(k => JSON.stringify(k) + ':' + stable(v[k])).join(',') + '}'
  }
  return JSON.stringify(v === undefined ? null : v)
}

// changedFields returns the diff-field names whose value differs between two
// services (treating absent / empty-string / 0 as the same "unset").
function changedFields(a, b) {
  const norm = (x) => (x === undefined || x === '' || x === 0 ? null : x)
  return SVC_DIFF_FIELDS.filter(k => stable(norm(a?.[k])) !== stable(norm(b?.[k])))
}

// ReplaceSourceCard lets an upload-source project's owner replace the stored archive.
// The next Build wipes _src and re-extracts it, so updating is: replace, then build.
function ReplaceSourceCard({ workspace, name }) {
  const [busy, setBusy] = useState(false)
  const [msg, setMsg] = useState(null) // { ok, text }
  async function upload(file) {
    if (!file) return
    setBusy(true); setMsg(null)
    try {
      const fd = new FormData(); fd.append('archive', file)
      await replaceProjectSource(workspace, name, fd)
      setMsg({ ok: true, text: `${file.name} uploaded — Build each environment to apply.` })
    } catch (e) {
      setMsg({ ok: false, text: e?.response?.data?.error || 'Upload failed' })
    } finally { setBusy(false) }
  }
  return (
    <div className="mb-5 rounded-xl border border-border bg-surface-raised/40 p-4 space-y-2">
      <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider">Application source (uploaded)</p>
      <p className="text-xs text-content-subtle">
        This project builds from an uploaded archive. Replace it with a new
        <code className="font-mono text-xs"> .zip</code>/<code className="font-mono text-xs">.tar.gz</code>, then <strong>Build</strong> each
        environment to apply.
      </p>
      <DropZone onFile={upload} accept=".zip,.tar,.tar.gz,.tgz,.gz" busy={busy}
        busyLabel="Uploading & validating…"
        hint="↑ Drop a new source archive here, or click to browse" />
      {msg && <p className={`text-xs ${msg.ok ? 'text-success-fg' : 'text-danger-fg'}`}>{msg.text}</p>}
    </div>
  )
}

// SeedDatabaseCard surfaces a project's bundled SQL dump: toggle auto-import (saved
// with the project config) and import it on demand per environment. Import refuses a
// non-empty DB (409) unless the user confirms an overwrite. Shown for upload-source
// projects that have a configured seed + a managed database.
function SeedDatabaseCard({ workspace, name, seed, database, envNames, onToggleAuto }) {
  const confirm = useConfirm()
  const [busyEnv, setBusyEnv] = useState('')
  const [msg, setMsg] = useState(null) // { ok, text }
  async function runImport(env, force) {
    setBusyEnv(env); setMsg(null)
    try {
      const res = await seedDatabase(workspace, name, env, { force })
      setMsg({ ok: true, text: `Imported into ${env} — ${res.tables} table(s).` })
    } catch (e) {
      if (e?.response?.status === 409 && !force) {
        const ok = await confirm({
          title: 'Database is not empty',
          message: `${env}'s database already has ${e?.response?.data?.tables ?? 'some'} table(s). Re-importing the dump may overwrite or duplicate data. Import anyway?`,
          confirmLabel: 'Import anyway', danger: true,
        })
        if (ok) { return runImport(env, true) }
        setBusyEnv(''); return
      }
      setMsg({ ok: false, text: e?.response?.data?.error || 'Import failed' })
    } finally { setBusyEnv('') }
  }
  return (
    <div className="mb-5 rounded-xl border border-border bg-surface-raised/40 p-4 space-y-3">
      <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider">Database seed</p>
      <p className="text-xs text-content-subtle">
        A bundled SQL dump (<code className="font-mono text-xs">{seed?.file || 'seed.sql'}</code>) imports into the managed {database}.
        Import only runs into an empty database unless you confirm an overwrite; the dump is loaded as-is.
      </p>
      <label className="flex items-center gap-2 text-xs cursor-pointer">
        <input type="checkbox" checked={!!seed?.auto} onChange={e => onToggleAuto(e.target.checked)}
          className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
        <span>Auto-import on first deploy <span className="text-content-faint">(when the database is empty — save to apply)</span></span>
      </label>
      <div className="flex flex-wrap gap-2">
        {envNames.map(env => (
          <button key={env} type="button" disabled={!!busyEnv}
            onClick={() => runImport(env, false)}
            className="px-2.5 py-1 text-xs rounded-md border border-border hover:bg-surface-hover disabled:opacity-50">
            {busyEnv === env ? 'Importing…' : `Import now → ${env}`}
          </button>
        ))}
      </div>
      {msg && <p className={`text-xs ${msg.ok ? 'text-success-fg' : 'text-danger-fg'}`}>{msg.text}</p>}
    </div>
  )
}

// diffServices buckets detected services against the current graph by name.
function diffServices(current, detected) {
  const byName = new Map((current || []).map(s => [s.name, s]))
  const added = [], changed = [], unchanged = []
  for (const d of (detected || [])) {
    const c = byName.get(d.name)
    if (!c) added.push(d)
    else if (changedFields(c, d).length) changed.push({ name: d.name, current: c, detected: d, fields: changedFields(c, d) })
    else unchanged.push(d.name)
  }
  const detNames = new Set((detected || []).map(s => s.name))
  const onlyLocal = (current || []).filter(s => s.name && !detNames.has(s.name)).map(s => s.name)
  return { added, changed, unchanged, onlyLocal }
}

// ScanRepoModal re-scans the project's git repo and proposes changes to the live
// service graph. It is ADVISORY: nothing is applied until the user picks items
// and confirms. New services default to checked; changed services default to
// UNCHECKED so a re-scan never silently overwrites a service the user has tuned.
function ScanRepoModal({ gitRepo, gitBranch, images, onApply, onClose }) {
  const [busy, setBusy] = useState(true)
  const [err, setErr] = useState('')
  const [draft, setDraft] = useState(null)
  const [picked, setPicked] = useState(() => new Set())

  useEffect(() => {
    let alive = true
    ;(async () => {
      try {
        const d = await scanRepo((gitRepo || '').trim(), (gitBranch || '').trim())
        if (!alive) return
        setDraft(d)
        // Pre-check the additive (safe) proposals only.
        setPicked(new Set(diffServices(images, d.services || []).added.map(s => s.name)))
      } catch (e) {
        if (alive) setErr(e?.response?.data?.error || 'Scan failed')
      } finally {
        if (alive) setBusy(false)
      }
    })()
    return () => { alive = false }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const diff = draft ? diffServices(images, draft.services || []) : null
  const toggle = (name) => setPicked(p => { const n = new Set(p); n.has(name) ? n.delete(name) : n.add(name); return n })

  function apply() {
    if (!draft) return
    const detByName = new Map((draft.services || []).map(s => [s.name, s]))
    let next = [...images]
    // Replace changed (picked), keyed by name.
    next = next.map(s => (picked.has(s.name) && detByName.has(s.name)) ? detByName.get(s.name) : s)
    // Append added (picked).
    for (const a of diff.added) if (picked.has(a.name)) next.push(a)
    onApply(next)
    onClose()
  }

  const pickedCount = picked.size

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface-raised border border-border rounded-xl w-full max-w-2xl max-h-[85vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="px-5 py-4 border-b border-border">
          <h3 className="text-base font-semibold text-content-strong">Re-scan repository</h3>
          <p className="text-xs text-content-subtle mt-0.5 font-mono truncate">{gitRepo || '(no repo set)'}{gitBranch ? ` @ ${gitBranch}` : ''}</p>
        </div>

        <div className="px-5 py-4 overflow-y-auto space-y-4 text-sm">
          {busy && <p className="text-content-subtle">Cloning &amp; detecting…</p>}
          {err && <p className="text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{err}</p>}

          {diff && (
            <>
              {diff.added.length === 0 && diff.changed.length === 0 && (
                <p className="text-success-fg">No changes — the detected graph matches your services.</p>
              )}

              {diff.added.length > 0 && (
                <div className="space-y-1.5">
                  <p className="text-xs font-semibold uppercase tracking-wide text-content-muted">New services</p>
                  {diff.added.map(s => (
                    <label key={s.name} className="flex items-start gap-2 px-3 py-2 rounded-lg bg-surface border border-border cursor-pointer">
                      <input type="checkbox" className="mt-0.5" checked={picked.has(s.name)} onChange={() => toggle(s.name)} />
                      <span className="text-xs">
                        <span className="font-mono text-content">{s.name}</span>{' '}
                        <span className="text-content-faint">{svcKindLabel(s)}</span>
                        {s.port ? <span className="text-content-subtle"> :{s.port}</span> : null}
                      </span>
                    </label>
                  ))}
                </div>
              )}

              {diff.changed.length > 0 && (
                <div className="space-y-1.5">
                  <p className="text-xs font-semibold uppercase tracking-wide text-content-muted">Changed services <span className="normal-case font-normal text-content-faint">— check to overwrite your version</span></p>
                  {diff.changed.map(c => (
                    <label key={c.name} className="flex items-start gap-2 px-3 py-2 rounded-lg bg-surface border border-amber-600/40 cursor-pointer">
                      <input type="checkbox" className="mt-0.5" checked={picked.has(c.name)} onChange={() => toggle(c.name)} />
                      <span className="text-xs">
                        <span className="font-mono text-content">{c.name}</span>{' '}
                        <span className="text-content-faint">differs in: {c.fields.join(', ')}</span>
                      </span>
                    </label>
                  ))}
                </div>
              )}

              {diff.unchanged.length > 0 && (
                <p className="text-xs text-content-subtle">{diff.unchanged.length} service{diff.unchanged.length !== 1 ? 's' : ''} unchanged.</p>
              )}
              {diff.onlyLocal.length > 0 && (
                <p className="text-xs text-content-subtle">Kept (not in scan): <span className="font-mono">{diff.onlyLocal.join(', ')}</span></p>
              )}

              {(draft.database !== 'none' || draft.redis || (draft.object_storage && draft.object_storage !== 'none')) && (
                <p className="text-xs text-content-muted">Detected managed deps: {[draft.database !== 'none' && draft.database, draft.redis && 'redis', draft.object_storage && draft.object_storage !== 'none' && draft.object_storage].filter(Boolean).join(', ')} — toggle these per environment below if needed.</p>
              )}
              {(draft.notes || []).length > 0 && (
                <ul className="text-xs text-content-subtle list-disc pl-4 space-y-0.5">
                  {draft.notes.map((n, i) => <li key={i}>{n}</li>)}
                </ul>
              )}
            </>
          )}
        </div>

        <div className="px-5 py-3 border-t border-border flex items-center justify-end gap-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 rounded-lg text-sm text-content-subtle hover:text-content">Cancel</button>
          <button type="button" onClick={apply} disabled={busy || !!err || pickedCount === 0}
            className="px-4 py-1.5 rounded-lg text-sm font-semibold bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white">
            Merge {pickedCount > 0 ? pickedCount : ''} selected
          </button>
        </div>
      </div>
    </div>
  )
}

// svcKindLabel summarises a service's source for compact lists.
function svcKindLabel(s) {
  if (s.build) return `build${s.build.template ? ` · ${s.build.template}` : ''}${s.build.context && s.build.context !== '.' ? ` (${s.build.context})` : ''}`
  if (s.image_from) return `worker → ${s.image_from}`
  return `image ${s.image || ''}${s.tag ? `:${s.tag}` : ''}`
}

function ImagesEditor({ images, onChange, gitRepo, gitBranch, managedDeps = [] }) {
  const [scanning, setScanning] = useState(false)
  const addService = () => onChange([...images, {
    name: '', image: '', tag: 'latest', port: 0, host_port: '',
    volumes: [], depends_on: [], extra_ports: [],
    restart: 'unless-stopped', extra_compose: '',
  }])
  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-content-subtle">Containers that make up the stack — click one to expand.</p>
        <div className="flex items-center gap-2 shrink-0">
          {gitRepo && (
            <button type="button" onClick={() => setScanning(true)} title="Re-detect the stack from the source repository"
              className="px-3 py-1.5 rounded-lg text-xs font-semibold border border-border-strong text-content-subtle hover:text-content hover:border-brand-600 transition-colors">
              ⟳ Scan repo
            </button>
          )}
          <button type="button" onClick={addService}
            className="px-3 py-1.5 rounded-lg text-xs font-semibold bg-brand-600 hover:bg-brand-700 text-white transition-colors">
            + Add service
          </button>
        </div>
      </div>
      {scanning && (
        <ScanRepoModal gitRepo={gitRepo} gitBranch={gitBranch} images={images}
          onApply={onChange} onClose={() => setScanning(false)} />
      )}
      {images.map((img, i) => (
        <ServiceCard
          key={i}
          img={img}
          idx={i}
          allImages={images}
          managedDeps={managedDeps}
          onUpdate={(idx, updated) => onChange(images.map((m, j) => j === idx ? updated : m))}
          onRemove={idx => onChange(images.filter((_, j) => j !== idx))}
        />
      ))}
      <PortWarnings warnings={portConflicts(
        images.filter(i => i.name || i.image).map(img => ({ name: img.name, ports: hostPortsFromConfig(img) }))
      )} />
    </div>
  )
}

// ── Environment editor ────────────────────────────────────────────────────────

const SWARM_RESTART_COND = [{ value: 'on-failure', label: 'on-failure' }, { value: 'any', label: 'any' }, { value: 'none', label: 'none' }]
const SWARM_ORDER = [{ value: '', label: 'order: default' }, { value: 'stop-first', label: 'stop-first' }, { value: 'start-first', label: 'start-first' }]
const SWARM_FAILURE = [{ value: 'rollback', label: 'rollback' }, { value: 'pause', label: 'pause' }, { value: 'continue', label: 'continue' }]

// SwarmSettings edits cfg.swarm: per-service replicas + placement, plus the
// env-level restart/update/rollback policy. Numeric fields are stored as strings
// (flexStr-tolerant); empty means "use Rigger's default" on generation.
function SwarmSettings({ cfg, onChange, projectType, imageNames = [], managedDeps = [] }) {
  const sw = cfg.swarm || {}
  const updSwarm = (patch) => onChange({ ...cfg, swarm: { ...sw, ...patch } })
  const updSvc = (svc, patch) => updSwarm({ services: { ...(sw.services || {}), [svc]: { ...((sw.services || {})[svc] || {}), ...patch } } })
  const updPolicy = (key, patch) => updSwarm({ [key]: { ...(sw[key] || {}), ...patch } })
  const [advOpen, setAdvOpen] = useState(false)

  // Managed deps (DB/Redis/Garage) are project-level now — passed in.
  const services = [...imageNames, ...managedDeps]

  const svcReplicas = (svc) => {
    const o = sw.services?.[svc]
    if (o?.replicas != null && o.replicas !== '') return o.replicas
    return 1
  }
  const svcPlacement = (svc) => (sw.services?.[svc]?.placement || []).join(', ')
  const setPlacement = (svc, str) => updSvc(svc, { placement: str.split(',').map(s => s.trim()).filter(Boolean) })

  const rp = sw.restart_policy || {}, uc = sw.update_config || {}, rc = sw.rollback_config || {}

  return (
    <div className="space-y-4 pt-3 border-t border-border-strong/50">
      <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider flex items-center gap-2">
        ⚓ Swarm settings <span className="text-content-faint normal-case font-normal tracking-normal">applied on next deploy</span>
      </p>

      <div className="space-y-1.5">
        {services.length === 0 && <p className="text-xs text-content-subtle">No services to configure.</p>}
        {services.map(svc => (
          <div key={svc} className="grid grid-cols-[110px_80px_1fr] gap-2 items-center">
            <span className="font-mono text-xs text-content-muted truncate" title={svc}>{svc}</span>
            <Input type="number" value={svcReplicas(svc)} onChange={v => updSvc(svc, { replicas: v })} />
            <Input value={svcPlacement(svc)} onChange={v => setPlacement(svc, v)} placeholder="placement, e.g. node.role==manager, node.labels.zone==a" />
          </div>
        ))}
        <p className="text-[11px] text-content-faint">replicas · placement constraints (comma-separated). Pin stateful services (db) to a node; scale stateless ones.</p>
      </div>

      <div>
        <Label>Restart policy</Label>
        <div className="grid grid-cols-4 gap-2">
          <Select value={rp.condition || 'on-failure'} onChange={v => updPolicy('restart_policy', { condition: v })} options={SWARM_RESTART_COND} />
          <Input value={rp.delay || ''} onChange={v => updPolicy('restart_policy', { delay: v })} placeholder="delay 5s" />
          <Input value={rp.max_attempts ?? ''} onChange={v => updPolicy('restart_policy', { max_attempts: v })} placeholder="attempts 3" />
          <Input value={rp.window || ''} onChange={v => updPolicy('restart_policy', { window: v })} placeholder="window" />
        </div>
      </div>

      <div>
        <Label>Update config (rolling deploy)</Label>
        <div className="grid grid-cols-4 gap-2">
          <Input value={uc.parallelism ?? ''} onChange={v => updPolicy('update_config', { parallelism: v })} placeholder="parallel 1" />
          <Input value={uc.delay || ''} onChange={v => updPolicy('update_config', { delay: v })} placeholder="delay 10s" />
          <Select value={uc.order || ''} onChange={v => updPolicy('update_config', { order: v })} options={SWARM_ORDER} />
          <Select value={uc.failure_action || 'rollback'} onChange={v => updPolicy('update_config', { failure_action: v })} options={SWARM_FAILURE} />
        </div>
      </div>

      <button type="button" onClick={() => setAdvOpen(o => !o)} className="text-xs text-content-subtle hover:text-content transition-colors">
        {advOpen ? '▾' : '▸'} Rollback config (advanced)
      </button>
      {advOpen && (
        <div className="grid grid-cols-3 gap-2">
          <Input value={rc.parallelism ?? ''} onChange={v => updPolicy('rollback_config', { parallelism: v })} placeholder="parallel 1" />
          <Input value={rc.delay || ''} onChange={v => updPolicy('rollback_config', { delay: v })} placeholder="delay 0s" />
          <Select value={rc.order || ''} onChange={v => updPolicy('rollback_config', { order: v })} options={SWARM_ORDER} />
        </div>
      )}
    </div>
  )
}

// ProcessesSettings edits the custom-app extra processes (queue workers,
// scheduler, job processors). Each reuses the built backend (or frontend) image
// with a custom command — no ports, not web-routed. Replicas show only on swarm
// (matching the backend/frontend replica behaviour). cfg here is one env.
function ProcessesSettings({ cfg, onChange }) {
  const procs = cfg.processes || []
  const swarm = cfg.deployment === 'swarm'
  const frontendEnabled = cfg.frontend && cfg.frontend !== 'none'
  const sourceOpts = [
    { value: 'backend', label: 'backend image' },
    ...(frontendEnabled ? [{ value: 'frontend', label: 'frontend image' }] : []),
  ]
  const setProcs = (next) => onChange({ ...cfg, processes: next })
  const updProc = (i, patch) => setProcs(procs.map((p, idx) => idx === i ? { ...p, ...patch } : p))
  const addProc = (preset) => setProcs([...procs, { name: '', command: '', source: 'backend', ...preset }])
  const removeProc = (i) => setProcs(procs.filter((_, idx) => idx !== i))

  // Backend-aware quick-add presets for the common worker/scheduler cases.
  const presets = cfg.backend === 'nodejs'
    ? [{ label: '+ worker', value: { name: 'worker', command: 'node worker.js' } }]
    : [
        { label: '+ queue worker', value: { name: 'queue', command: 'php artisan queue:work --tries=3' } },
        { label: '+ scheduler', value: { name: 'scheduler', command: 'php artisan schedule:work' } },
      ]

  const names = procs.map(p => (p.name || '').trim())
  const dup = (i) => names[i] && names.indexOf(names[i]) !== i

  return (
    <div className="space-y-3 pt-3 border-t border-border-strong/50">
      <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider flex items-center gap-2">
        ⚙ Workers / processes <span className="text-content-faint normal-case font-normal tracking-normal">extra containers from your app image</span>
      </p>

      {procs.length === 0 && (
        <p className="text-xs text-content-subtle">
          No extra processes. Add a queue worker, scheduler, or job processor — each runs your built image with a different command (no web port).
        </p>
      )}

      {procs.map((p, i) => (
        <div key={i} className={`grid ${swarm ? 'grid-cols-[130px_1fr_120px_70px_28px]' : 'grid-cols-[130px_1fr_120px_28px]'} gap-2 items-center`}>
          <Input value={p.name || ''} onChange={v => updProc(i, { name: v })} placeholder="name" />
          <Input value={p.command || ''} onChange={v => updProc(i, { command: v })} placeholder="command, e.g. php artisan queue:work" />
          <Select value={p.source || 'backend'} onChange={v => updProc(i, { source: v })} options={sourceOpts} />
          {swarm && <Input type="number" value={p.replicas ?? 1} onChange={v => updProc(i, { replicas: v })} title="replicas" />}
          <button type="button" onClick={() => removeProc(i)} title="Remove process"
            className="text-content-faint hover:text-danger-fg text-sm">✕</button>
        </div>
      ))}
      {procs.some((_, i) => dup(i)) && <p className="text-[11px] text-danger-fg">Process names must be unique.</p>}

      <div className="flex items-center gap-2 flex-wrap">
        {presets.map(pr => (
          <button key={pr.label} type="button" onClick={() => addProc(pr.value)}
            className="text-xs px-2 py-1 rounded-lg border border-border-strong text-content-muted hover:text-content hover:bg-surface-raised transition-colors">
            {pr.label}
          </button>
        ))}
        <button type="button" onClick={() => addProc()}
          className="text-xs px-2 py-1 rounded-lg border border-border-strong text-content-muted hover:text-content hover:bg-surface-raised transition-colors">
          + custom
        </button>
      </div>
    </div>
  )
}

// CopyEnvModal clones an existing environment into a new one (Phase 1: config +
// regenerated .env/compose + bind config; fresh empty volumes; source untouched).
function CopyEnvModal({ workspace, project, srcEnv, existingNames = [], onClose, onCopied }) {
  const [newEnv, setNewEnv] = useState('')
  const [regen, setRegen]   = useState(true)
  const [err, setErr]       = useState('')
  const [busy, setBusy]     = useState(false)
  const nm = newEnv.trim().toLowerCase()
  const valid = /^[a-z][a-z0-9-]{0,29}$/.test(nm) && !existingNames.includes(nm)
  async function go() {
    if (!valid) return
    setBusy(true); setErr('')
    try {
      await copyEnvironment(workspace, project, srcEnv, nm, regen)
      onCopied(nm)
    } catch (e) {
      setErr(e?.response?.data?.error || e.message)
      setBusy(false)
    }
  }
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">Copy environment “{srcEnv}”</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>
        <p className="text-sm text-content-subtle">
          Creates a new environment from <strong className="text-content">{srcEnv}</strong>’s configuration and env vars. Volume <strong className="text-content">data is not copied</strong> — the new env starts with fresh, empty volumes. The source is left untouched.
        </p>
        <div>
          <Label required>New environment name</Label>
          <Input value={newEnv} onChange={setNewEnv} placeholder="prod" />
          {nm && !valid && <p className="text-danger-fg text-xs mt-1">{existingNames.includes(nm) ? 'That environment already exists.' : 'Lowercase letters, digits, hyphens; must start with a letter.'}</p>}
        </div>
        <Toggle label="Regenerate secrets" hint="Fresh DB passwords / tokens for the new env (recommended)" checked={regen} onChange={setRegen} />
        {err && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2 whitespace-pre-wrap">{err}</p>}
        <button onClick={go} disabled={!valid || busy}
          className="w-full bg-brand-600 hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed text-white text-sm font-semibold py-2 rounded-lg transition-colors">
          {busy ? 'Copying…' : 'Copy environment'}
        </button>
      </div>
    </div>
  )
}

function EnvEditor({ envName, cfg, onChange, onRename, onRemove, isNew, projectType, workspaceName, isOnlyEnv, imageNames, defaultOpen, hosts = [], resourcePrefix = '', baseDomain = '', autoUrlMode = '', appHost = '', localTLS = false, projectDatabase = '', projectRedis = false, projectObjectStorage = '', projectWebSql = false, projectStorageUi = false, gitRepo = '', gitBranch = '', dirty = false, onCopied, existingNames = [] }) {
  const { workspace } = useParams()
  const confirm = useConfirm()
  const [open, setOpen] = useState(defaultOpen || isNew) // collapsible — first/new env open
  const [copyOpen, setCopyOpen] = useState(false)
  const upd = (k, v) => onChange({ ...cfg, [k]: v })
  const updGit = (k, v) => onChange({ ...cfg, git: { ...(cfg.git || {}), [k]: v } })
  const updReplicas = (k, v) => onChange({ ...cfg, replicas: { ...(cfg.replicas || {}), [k]: parseInt(v) || 1 } })
  const updServiceOverride = (svcName, yaml) => onChange({
    ...cfg,
    service_overrides: { ...(cfg.service_overrides || {}), [svcName]: { extra_compose: yaml } },
  })

  return (
    <div className="bg-surface-raised/50 border border-border-strong rounded-xl overflow-hidden">
      {/* Collapsible header: chevron toggles; name input + remove are isolated */}
      <div className="flex items-center justify-between gap-3 p-5">
        <div className="flex items-center gap-3 flex-1 min-w-0">
          <button type="button" onClick={() => setOpen(o => !o)}
            className={`text-content-subtle text-xs transition-transform shrink-0 ${open ? 'rotate-90' : ''}`} title={open ? 'Collapse' : 'Expand'}>▸</button>
          <div className="w-2 h-2 rounded-full bg-surface-overlay shrink-0" />
          {/* The name identifies the env's folder + Docker resources ({prefix}_{env});
              renaming an existing one can't be done safely in place (it would orphan
              its volumes/data), so it's editable only while the env is NEW (unsaved).
              Existing envs show a read-only name — use "Copy environment" to clone, or
              Remove to delete. */}
          {isNew ? (
            <input
              key={envName}
              defaultValue={envName}
              onBlur={e => { if (e.target.value !== envName) onRename(e.target.value) }}
              placeholder="prod"
              className="bg-transparent text-content-strong font-semibold text-base border-b border-transparent focus:border-brand-500 focus:outline-none px-0 py-0.5 w-32 shrink-0"
            />
          ) : (
            <span className="text-content-strong font-semibold text-base px-0 py-0.5 shrink-0" title="Environment name is fixed after creation">{envName}</span>
          )}
          {isNew && <span className="text-xs text-brand-400 bg-brand-950 px-2 py-0.5 rounded-full shrink-0">new</span>}
          {!open && (
            <span className="text-xs text-content-subtle truncate cursor-pointer" onClick={() => setOpen(true)}>
              {cfg.deployment || 'compose'}{cfg.domain ? ` · ${cfg.domain}` : ''}
            </span>
          )}
        </div>
        <div className="flex items-center gap-3 shrink-0">
          {/* Copy: clone this env into a new one. Disabled while the page has unsaved
              edits (the copy reads the last SAVED config) and for brand-new envs. */}
          {!isNew && (
            <button
              type="button"
              onClick={() => setCopyOpen(true)}
              disabled={dirty}
              title={dirty ? 'Save changes before copying' : 'Copy this environment to a new one'}
              className={`text-xs transition-colors ${dirty ? 'text-content-faint cursor-not-allowed' : 'text-content-subtle hover:text-brand-400'}`}
            >Copy</button>
          )}
          <button
            type="button"
            onClick={async () => {
              if (await confirm({
                title: 'Remove environment?',
                message: `Remove the "${envName}" environment from this project? When you save, its containers are stopped and removed and its files are deleted. This can't be undone.`,
                confirmLabel: 'Remove',
              })) onRemove()
            }}
            disabled={isOnlyEnv}
            title={isOnlyEnv ? 'Cannot remove the only environment' : undefined}
            className={`text-xs transition-colors ${isOnlyEnv ? 'text-content-faint cursor-not-allowed' : 'text-danger-fg hover:text-danger-fg'}`}
          >Remove</button>
        </div>
      </div>
      {copyOpen && (
        <CopyEnvModal
          workspace={workspace}
          project={workspaceName}
          srcEnv={envName}
          existingNames={existingNames}
          onClose={() => setCopyOpen(false)}
          onCopied={() => { setCopyOpen(false); if (onCopied) onCopied() }}
        />
      )}

      {open && (<div className="px-5 pb-5 space-y-4 border-t border-border-strong/50 pt-4">
      <div className="grid grid-cols-2 gap-4">
        <div>
          <Label>Deployment</Label>
          <Select value={cfg.deployment} onChange={v => upd('deployment', v)} options={DEPLOYMENT_OPTIONS} />
        </div>
        {/* Host selection — only for a NEW env (matches the New Project wizard).
            Existing envs change host in the Host tab. Bound on save (bind-only). */}
        {isNew && (
          <div>
            <Label>Host</Label>
            <Select
              value={String(cfg._host_id || 0)}
              onChange={v => upd('_host_id', Number(v))}
              options={[{ value: '0', label: 'Local Docker' }, ...hosts.map(h => ({ value: String(h.id), label: `${h.name} — ${h.address}` }))]}
            />
            <p className="text-xs text-content-subtle mt-1">The stack starts here the first time you deploy this environment.</p>
          </div>
        )}
        {/* HTTP port — only relevant for custom stacks without Traefik (direct Nginx binding) */}
        {projectType !== 'image' && !cfg.traefik_enabled && (
          <div>
            <Label>HTTP port</Label>
            <Input type="number" value={cfg.http_port} onChange={v => upd('http_port', parseInt(v) || 80)} />
            <p className="text-xs text-content-subtle mt-1">Host port Nginx binds to — access at <code className="font-mono text-xs">host:{cfg.http_port || 80}</code></p>
          </div>
        )}
      </div>

      <div className="space-y-3 pt-3 border-t border-border-strong/50">
        <Toggle
          label="Expose via domain (Traefik)"
          hint="Route through the shared Traefik proxy by hostname instead of binding a host port (avoids port conflicts; gives the env a URL)."
          checked={!!cfg.traefik_enabled}
          onChange={v => {
            upd('traefik_enabled', v)
            if (!v) onChange({ ...cfg, traefik_enabled: false, ssl_enabled: false })
          }}
        />
        {(() => {
          const route = resolveEnvRoute(cfg, resourcePrefix, envName, baseDomain, localTLS, autoUrlMode, appHost)
          return route ? (
            <p className="text-xs text-content-subtle">
              Reachable at <a href={route.url} target="_blank" rel="noreferrer" className="font-mono text-brand-600 hover:underline">{route.url}</a>
              {route.auto && <span className="text-content-faint"> (auto{route.ssl ? ' · TLS' : ''})</span>}
            </p>
          ) : null
        })()}
        {cfg.traefik_enabled && (
          <>
            <div>
              <Label>Domain <span className="font-normal normal-case text-content-faint">(optional)</span></Label>
              <Input value={cfg.domain} onChange={v => upd('domain', v)} placeholder="leave blank for an automatic URL" />
              {!cfg.domain && (
                <p className="text-xs text-content-subtle mt-1">
                  Blank → an automatic URL: {baseDomain
                    ? <>the base domain (<code className="font-mono text-xs">{resourcePrefix?.replace(/_/g, '-')}-{envName}.{baseDomain}</code>)</>
                    : <>the admin auto-URL (sslip/nip via the App host, else <code className="font-mono text-xs">*.localhost</code>)</>}.
                  Set a value only to use your own custom domain (point a CNAME at this server).
                </p>
              )}
            </div>

            {/* SSL toggle — enabled only when domain is set */}
            <div className={`pl-3 border-l-2 ${cfg.ssl_enabled ? 'border-success-border' : 'border-border-strong'}`}>
              <Toggle
                label="SSL certificate (Let's Encrypt)"
                hint={cfg.domain
                  ? `Traefik will request a cert for ${cfg.domain}`
                  : 'Set a domain above to enable SSL'}
                checked={!!cfg.ssl_enabled && !!cfg.domain}
                onChange={v => upd('ssl_enabled', v)}
                disabled={!cfg.domain}
              />
              {cfg.ssl_enabled && cfg.domain && (
                <p className="text-xs text-success-fg/70 mt-1">
                  🔒 After saving, click <strong>Refresh</strong> on the environment card (or redeploy) to regenerate the compose with TLS labels.
                </p>
              )}
            </div>

            {/* Traefik network — advanced; changing it breaks routing unless the
                proxy actually listens on the new network. Hidden by default. */}
            <details className="text-xs">
              <summary className="cursor-pointer text-content-faint hover:text-content-subtle select-none">Advanced</summary>
              <div className="mt-2 pl-3 border-l-2 border-border-strong">
                <Label>Traefik network</Label>
                <Input value={cfg.traefik_network} onChange={v => upd('traefik_network', v)} placeholder="traefik_net" />
                <p className="text-xs text-content-subtle mt-1">
                  The shared proxy network. Leave as <code className="font-mono text-xs">traefik_net</code> unless you run a
                  differently-named Traefik — a mismatch means the proxy can't reach this env and routing 404s.
                </p>
              </div>
            </details>
          </>
        )}
      </div>

      {/* Database access — per-environment external-port exposure. The DB engine /
          version themselves are project-level (Services tab → Project dependencies). */}
      {projectDatabase && projectDatabase !== 'none' && (
        <div className="space-y-2 pt-3 border-t border-border-strong/50">
          <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider">Database access</p>
          <Toggle
            label="Expose database on a host port"
            hint={`Publish ${projectDatabase} on this environment's host so external clients can connect — typically dev only; keep prod private. Override the port via DB_EXTERNAL_PORT in env vars.`}
            checked={!!cfg.db_external}
            onChange={v => upd('db_external', v)}
          />
          <p className="text-xs text-content-subtle">
            The managed {projectDatabase} (and any Redis / object storage) is provisioned project-wide — configure it in the{' '}
            <strong>Services</strong> tab → <em>Project dependencies</em>.
          </p>
        </div>
      )}

      {/* Security — per-env protection for the admin sidecars (Adminer / MinIO console).
          Only meaningful when this env routes through Traefik (basic-auth is a Traefik
          edge middleware) and the project actually has an admin UI. */}
      {(projectWebSql || projectStorageUi) && (
        <div className="space-y-2 pt-3 border-t border-border-strong/50">
          <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider">Security</p>
          <Toggle
            label="Protect admin UIs (Adminer / MinIO console)"
            hint={cfg.traefik_enabled
              ? "Require HTTP basic-auth at the Traefik edge before reaching Adminer / the MinIO console. Recommended for prod. Credentials are generated on deploy — view them in this env's Env Vars (ADMIN_UI_USER / ADMIN_UI_PASSWORD)."
              : "Needs domain routing — enable Traefik above to protect the admin UIs (basic-auth is a Traefik middleware; host-port mode can't enforce it)."}
            checked={!!cfg.protect_admin_uis}
            disabled={!cfg.traefik_enabled}
            onChange={v => upd('protect_admin_uis', v)}
          />
        </div>
      )}

      {/* Swarm scheduling — per-service replicas/placement + rolling-update policy. */}
      {cfg.deployment === 'swarm' && (
        <SwarmSettings cfg={cfg} onChange={onChange} projectType={projectType} imageNames={imageNames}
          managedDeps={projectDatabase && projectDatabase !== 'none'
            ? [projectDatabase, ...(projectRedis ? ['redis'] : []), ...(projectObjectStorage === 'minio' ? ['minio'] : [])]
            : [...(projectRedis ? ['redis'] : []), ...(projectObjectStorage === 'minio' ? ['minio'] : [])]} />
      )}

      {/* Build source (Git) — only for projects that build from a repo. The repo is
          set once at the project level (one repo per project); each environment may
          override just the branch it builds from (blank = inherit the project default). */}
      {gitRepo ? (
        <div className="space-y-2 pt-3 border-t border-border-strong/50">
          <p className="text-xs font-semibold text-content-muted uppercase tracking-wider">Build source (Git)</p>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Repository <span className="font-normal normal-case text-content-faint">(from Project tab)</span></Label>
              <div className="px-3 py-2 bg-surface border border-border-strong rounded-lg text-sm text-content-muted font-mono truncate" title={gitRepo}>{gitRepo}</div>
            </div>
            <div>
              <Label>Branch override <span className="font-normal normal-case text-content-faint">(blank = inherit)</span></Label>
              <Input value={cfg.git?.branch} onChange={v => updGit('branch', v)} placeholder={gitBranch || 'main'} />
            </div>
          </div>
          <p className="text-xs text-content-subtle">
            This environment builds from the project repo at branch <code className="font-mono text-xs">{cfg.git?.branch || gitBranch || 'main'}</code>. Leave blank to inherit the project default (<code className="font-mono text-xs">{gitBranch || 'main'}</code>).
          </p>
        </div>
      ) : null}

      {/* Environment variables */}
      {isNew
        ? <NewEnvVarsEditor cfg={cfg} onChange={onChange} />
        : <EnvVarsInline workspaceName={workspaceName} envName={envName} deployment={cfg.deployment} />
      }

      {/* Service overrides — image stacks only */}
      {projectType === 'image' && imageNames && imageNames.length > 0 && (
        <ServiceOverridesEditor
          imageNames={imageNames}
          overrides={cfg.service_overrides || {}}
          onChange={updServiceOverride}
        />
      )}
      </div>)}
    </div>
  )
}

// ── Service overrides editor ───────────────────────────────────────────────────

function ServiceOverridesEditor({ imageNames, overrides, onChange }) {
  const [open, setOpen] = useState(false)
  const hasAny = imageNames.some(n => overrides[n]?.extra_compose?.trim())

  return (
    <div className="pt-3 border-t border-border-strong/50">
      <button
        type="button"
        onClick={() => setOpen(o => !o)}
        className="flex items-center justify-between w-full text-left group"
      >
        <div className="flex items-center gap-2">
          <span className="text-xs font-semibold text-content-muted uppercase tracking-wider">Service overrides</span>
          {hasAny && (
            <span className="text-xs px-1.5 py-0.5 rounded bg-brand-900 text-brand-400 border border-brand-700">active</span>
          )}
        </div>
        <span className="text-content-faint group-hover:text-content-muted text-xs transition-colors">{open ? '▲' : '▼'}</span>
      </button>

      {open && (
        <div className="mt-3 space-y-4">
          <p className="text-xs text-content-subtle">
            Env-specific YAML appended to each service after the base config. Use for resource limits, logging drivers, replica counts, etc.
            Keys defined here override the service-level Advanced YAML for this environment only.
          </p>
          {imageNames.map(svcName => {
            const yaml = overrides[svcName]?.extra_compose || ''
            return (
              <div key={svcName}>
                <label className="block text-xs font-medium text-content-muted mb-1.5">
                  <span className="font-mono text-brand-400">{svcName}</span>
                  <span className="text-content-faint ml-1">— env override</span>
                </label>
                <textarea
                  value={yaml}
                  onChange={e => onChange(svcName, e.target.value)}
                  rows={yaml.trim().split('\n').length + 2}
                  placeholder={`mem_limit: 2g\ncpus: "1.5"\nlogging:\n  driver: "none"`}
                  spellCheck={false}
                  className="w-full px-3 py-2 bg-canvas border border-border-strong rounded-lg text-content text-xs font-mono placeholder-content-faint focus:outline-none focus:border-brand-500 resize-y leading-relaxed"
                />
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

// ── New env vars editor — same look as EnvVarsInline for new (unsaved) envs ───
// Stores vars in cfg._initial_vars (written to .env after save).
// Matches EnvVarsInline appearance: collapsible, show/hide values toggle.
// isSystemVar classifies an env var as Rigger-managed ("system") vs application-
// defined, so the editors can group them. System = the keys Rigger generates: a
// managed dependency's connection vars (POSTGRES_*/MYSQL_*/MARIADB_*/REDIS_*/
// GARAGE_*), per-service image pointers (*_IMAGE), and a few platform vars.
// Everything else is the app's own configuration.
const SYS_VAR_PREFIXES = ['POSTGRES_', 'MYSQL_', 'MARIADB_', 'REDIS_', 'GARAGE_']
const SYS_VAR_EXACT = new Set(['ADMINER_LOGIN_SECRET', 'PROJECT_NAME', 'RESOURCE_PREFIX', 'REGISTRY', 'COMPOSE_PROJECT_NAME', 'MAIL_HOST'])
function isSystemVar(k) {
  if (SYS_VAR_EXACT.has(k)) return true
  if (k.endsWith('_IMAGE')) return true
  return SYS_VAR_PREFIXES.some(p => k.startsWith(p))
}

// EnvVarGroupLabel is the small subheader shown above the Application / System
// groups when both are present.
function EnvVarGroupLabel({ children }) {
  return <p className="text-[10px] font-semibold uppercase tracking-wider text-content-faint pt-1 first:pt-0">{children}</p>
}

function NewEnvVarsEditor({ cfg, onChange }) {
  const vars = cfg._initial_vars || {}
  const secretKeys = cfg._secret_keys || []
  const secretSet = new Set(secretKeys)
  const swarm = cfg.deployment === 'swarm'
  const [open, setOpen]     = useState(false)
  const [reveal, setReveal] = useState(false)
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [newSecret, setNewSecret] = useState(false)

  function setVar(k, v) { onChange({ ...cfg, _initial_vars: { ...vars, [k]: v } }) }
  function removeVar(k) {
    const n = { ...vars }; delete n[k]
    onChange({ ...cfg, _initial_vars: n, _secret_keys: secretKeys.filter(x => x !== k) })
  }
  function toggleSecret(k) {
    onChange({ ...cfg, _secret_keys: secretSet.has(k) ? secretKeys.filter(x => x !== k) : [...secretKeys, k] })
  }
  function addVar() {
    const k = newKey.trim(); if (!k) return
    const nextSecret = newSecret && !secretSet.has(k) ? [...secretKeys, k] : secretKeys
    onChange({ ...cfg, _initial_vars: { ...vars, [k]: newVal }, _secret_keys: nextSecret })
    setNewKey(''); setNewVal(''); setNewSecret(false)
  }

  const entries = Object.entries(vars)
  const appEntries = entries.filter(([k]) => !isSystemVar(k))
  const sysEntries = entries.filter(([k]) => isSystemVar(k))
  const grouped = appEntries.length > 0 && sysEntries.length > 0

  const renderRow = ([k, v]) => {
    const secret = secretSet.has(k)
    return (
      <div key={k} className={`flex items-center gap-2 pl-1.5 border-l-2 ${secret ? 'border-warning/70' : 'border-transparent'}`}>
        <button type="button" onClick={() => toggleSecret(k)} title={secret ? 'Secret — click to unflag' : 'Flag as secret'}
          className={`shrink-0 w-6 h-6 flex items-center justify-center rounded text-xs ${secret ? 'text-warning-fg' : 'text-content-faint hover:text-content'}`}>
          {secret ? '🔒' : '🔓'}
        </button>
        <span className="font-mono text-xs text-content w-40 shrink-0 truncate">{k}</span>
        <input type={secret && !reveal ? 'password' : 'text'} value={v}
          onChange={e => setVar(k, e.target.value)}
          className="flex-1 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm font-mono text-content-strong focus:outline-none focus:border-brand-500" />
        <button type="button" onClick={() => removeVar(k)}
          className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
      </div>
    )
  }

  return (
    <div className="pt-3 border-t border-border-strong/50">
      <button type="button" onClick={() => setOpen(o => !o)}
        className="flex items-center gap-2 text-xs font-semibold text-content-muted uppercase tracking-wider hover:text-content transition-colors w-full">
        <span className={`transition-transform ${open ? 'rotate-90' : ''}`}>▶</span>
        Environment Variables
        {entries.length > 0 && <span className="ml-1 text-brand-400 normal-case font-normal">{entries.length} inherited{secretKeys.length > 0 ? ` · ${secretKeys.length} 🔒` : ''}</span>}
        <span className="ml-auto text-content-faint normal-case font-normal">.env file</span>
      </button>
      {open && (
        <div className="mt-3 space-y-3">
          <div className="flex items-center justify-between gap-3">
            {swarm
              ? <p className="text-xs text-emerald-400/80">🔒 Secret-flagged values become Docker Swarm secrets (encrypted at rest) when this environment is deployed.</p>
              : <p className="text-xs text-warning-fg/70">⚠ Compose keeps values plaintext in <code className="font-mono">.env</code> — flag secrets and deploy with Swarm for encryption at rest.</p>}
            <label className="flex items-center gap-1.5 cursor-pointer shrink-0">
              <input type="checkbox" checked={reveal} onChange={e => setReveal(e.target.checked)}
                className="w-3 h-3 accent-brand-500" />
              <span className="text-xs text-content-muted select-none">Show values</span>
            </label>
          </div>
          <div className="space-y-1.5">
            {grouped ? (
              <>
                <EnvVarGroupLabel>Application</EnvVarGroupLabel>
                {appEntries.map(renderRow)}
                <EnvVarGroupLabel>System · managed by Rigger</EnvVarGroupLabel>
                {sysEntries.map(renderRow)}
              </>
            ) : entries.map(renderRow)}
          </div>
          <div className="flex gap-2 pt-1">
            <button type="button" onClick={() => setNewSecret(s => !s)} title={newSecret ? 'New var is a secret' : 'Flag new var as secret'}
              className={`shrink-0 w-7 h-7 flex items-center justify-center rounded text-xs ${newSecret ? 'text-warning-fg' : 'text-content-faint hover:text-content'}`}>
              {newSecret ? '🔒' : '🔓'}
            </button>
            <input type="text" placeholder="KEY" value={newKey} onChange={e => setNewKey(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && addVar()}
              className="w-40 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm font-mono text-content-strong focus:outline-none focus:border-brand-500" />
            <input type="text" placeholder="value" value={newVal} onChange={e => setNewVal(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && addVar()}
              className="flex-1 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm font-mono text-content-strong focus:outline-none focus:border-brand-500" />
            <button type="button" onClick={addVar}
              className="text-xs text-brand-400 hover:text-brand-300 shrink-0 px-2">Add</button>
          </div>
        </div>
      )}
    </div>
  )
}

// ── Inline env vars editor (used inside EnvEditor) ────────────────────────────

function EnvVarsInline({ workspaceName, envName, deployment }) {
  const { workspace } = useParams()
  const swarm = deployment === 'swarm'
  const [open, setOpen]       = useState(false)
  const [reveal, setReveal]   = useState(false)
  const [edits, setEdits]     = useState({})
  const [deletes, setDeletes] = useState(new Set())
  const [flags, setFlags]     = useState({}) // explicit secret-flag overrides: key → bool
  const [newKey, setNewKey]   = useState('')
  const [newVal, setNewVal]   = useState('')
  const [newSecret, setNewSecret] = useState(false)
  const qc = useQueryClient()

  const { data: vars, isLoading } = useQuery({
    queryKey: ['envvars', workspace, workspaceName, envName, reveal],
    queryFn:  () => fetchEnvVars(workspace, workspaceName, envName, reveal),
    enabled:  open,
  })

  // Effective secret flag: a pending toggle wins, else the server's stored value.
  const isSecret = (k) => (k in flags ? flags[k] : !!vars?.[k]?.secret)

  const saveMut = useMutation({
    mutationFn: ({ updates, dels, secretKeys }) => updateEnvVars(workspace, workspaceName, envName, updates, dels, secretKeys),
    onSuccess: () => {
      setEdits({}); setDeletes(new Set()); setFlags({}); setNewKey(''); setNewVal(''); setNewSecret(false)
      qc.invalidateQueries({ queryKey: ['envvars', workspace, workspaceName, envName] })
    },
  })

  function toggleDelete(k) {
    setDeletes(prev => { const n = new Set(prev); n.has(k) ? n.delete(k) : n.add(k); return n })
    setEdits(prev => { const n = { ...prev }; delete n[k]; return n })
  }
  function toggleSecret(k) {
    setFlags(prev => ({ ...prev, [k]: !isSecret(k) }))
  }

  function handleSave() {
    const updates = { ...edits }
    const nk = newKey.trim()
    if (nk) updates[nk] = newVal
    // secret_keys is the full desired set of secret-flagged keys for this env.
    const secretKeys = Object.keys(vars || {}).filter(k => !deletes.has(k) && isSecret(k))
    if (nk && newSecret) secretKeys.push(nk)
    saveMut.mutate({ updates, dels: [...deletes], secretKeys })
  }

  // One var row; reused for the Application and System groups.
  const renderRow = ([k, v]) => {
    const marked = deletes.has(k)
    // Values arrive as { value, secret } objects; tolerate a bare string too.
    const val    = typeof v === 'string' ? v : (v?.value ?? '')
    const secret = isSecret(k)
    const show   = reveal && !secret
    return (
      <div key={k} className={`flex items-center gap-2 pl-1.5 border-l-2 ${secret ? 'border-warning/70' : 'border-transparent'} ${marked ? 'opacity-40' : ''}`}>
        <button type="button" onClick={() => toggleSecret(k)} disabled={marked}
          title={secret ? 'Secret — click to unflag' : 'Flag as secret'}
          className={`shrink-0 w-5 h-5 flex items-center justify-center rounded text-xs ${secret ? 'text-warning-fg' : 'text-content-faint hover:text-content'}`}>
          {secret ? '🔒' : '🔓'}
        </button>
        <span className="font-mono text-xs text-content-muted w-32 shrink-0 truncate" title={k}>{k}</span>
        <input
          type={show ? 'text' : 'password'}
          placeholder={show ? val : '••••••••'}
          value={marked ? '' : (edits[k] ?? (show ? val : ''))}
          disabled={marked}
          onChange={e => setEdits(p => ({ ...p, [k]: e.target.value }))}
          className="flex-1 px-2 py-1 bg-surface-raised border border-border-strong rounded text-xs text-content-strong font-mono focus:outline-none focus:border-brand-500 disabled:opacity-40"
        />
        <button type="button" onClick={() => toggleDelete(k)}
          className={`shrink-0 w-5 h-5 flex items-center justify-center rounded text-xs transition-colors ${
            marked ? 'bg-red-600 text-white hover:bg-red-700' : 'text-content-faint hover:text-danger-fg hover:bg-surface-overlay'
          }`}>
          {marked ? '↩' : '×'}
        </button>
      </div>
    )
  }

  return (
    <div className="pt-3 border-t border-border-strong/50">
      <button
        type="button"
        onClick={() => setOpen(o => !o)}
        className="flex items-center gap-2 text-xs font-semibold text-content-muted uppercase tracking-wider hover:text-content transition-colors w-full"
      >
        <span className={`transition-transform ${open ? 'rotate-90' : ''}`}>▶</span>
        Environment Variables
        <span className="ml-auto text-content-faint normal-case font-normal">.env file</span>
      </button>

      {open && (
        <div className="mt-3 space-y-3">
          {/* Reveal + hint */}
          <div className="flex items-center justify-between gap-3">
            <p className="text-xs text-content-subtle">
              After saving, <strong className="text-content-muted">Refresh</strong> the environment from its card to apply.
            </p>
            <label className="flex items-center gap-1.5 cursor-pointer shrink-0">
              <input type="checkbox" checked={reveal}
                onChange={e => { setReveal(e.target.checked); setEdits({}) }}
                className="w-3 h-3 accent-brand-500" />
              <span className="text-xs text-content-muted select-none">Show values</span>
            </label>
          </div>
          {swarm
            ? <p className="text-[11px] text-emerald-400/80">🔒 Secret-flagged values become Docker Swarm secrets (encrypted at rest) on the next deploy.</p>
            : <p className="text-[11px] text-content-faint">🔒 Flag secrets here; deploy with Swarm to store them as encrypted Docker secrets (Compose keeps them in <code className="font-mono">.env</code>).</p>}

          {/* Existing vars — grouped Application vs System (Rigger-managed) when both present */}
          {isLoading
            ? <p className="text-xs text-content-subtle">Loading…</p>
            : (() => {
                const entries = Object.entries(vars || {})
                const appEntries = entries.filter(([k]) => !isSystemVar(k))
                const sysEntries = entries.filter(([k]) => isSystemVar(k))
                const grouped = appEntries.length > 0 && sysEntries.length > 0
                return (
                  <div className="space-y-1.5 max-h-60 overflow-y-auto pr-1">
                    {grouped ? (
                      <>
                        <EnvVarGroupLabel>Application</EnvVarGroupLabel>
                        {appEntries.map(renderRow)}
                        <EnvVarGroupLabel>System · managed by Rigger</EnvVarGroupLabel>
                        {sysEntries.map(renderRow)}
                      </>
                    ) : entries.map(renderRow)}
                  </div>
                )
              })()
          }

          {/* Add new variable */}
          <div className="flex gap-2 pt-2 border-t border-border-strong/40">
            <button type="button" onClick={() => setNewSecret(s => !s)}
              title={newSecret ? 'New var is a secret' : 'Flag new var as secret'}
              className={`shrink-0 w-6 h-6 flex items-center justify-center rounded text-xs ${newSecret ? 'text-warning-fg' : 'text-content-faint hover:text-content'}`}>
              {newSecret ? '🔒' : '🔓'}
            </button>
            <input type="text" placeholder="NEW_KEY" value={newKey}
              onChange={e => setNewKey(e.target.value)}
              className="w-32 px-2 py-1 bg-surface-raised border border-border-strong rounded text-xs text-content-strong font-mono focus:outline-none focus:border-brand-500" />
            <input type={newSecret ? 'password' : (reveal ? 'text' : 'password')} placeholder="value" value={newVal}
              onChange={e => setNewVal(e.target.value)}
              className="flex-1 px-2 py-1 bg-surface-raised border border-border-strong rounded text-xs text-content-strong font-mono focus:outline-none focus:border-brand-500" />
            <button type="button" onClick={() => newKey.trim() && handleSave()}
              disabled={!newKey.trim() || saveMut.isPending}
              className="px-2.5 py-1 bg-surface-overlay hover:bg-surface-overlay disabled:opacity-40 text-content-strong text-xs rounded transition-colors shrink-0">
              Add
            </button>
          </div>

          {/* Save */}
          <div className="flex items-center gap-3">
            <button type="button" onClick={handleSave} disabled={saveMut.isPending}
              className="px-3 py-1.5 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors">
              {saveMut.isPending ? 'Saving…' : 'Save changes'}
            </button>
            {saveMut.isSuccess && <span className="text-success-fg text-xs">Saved ✓</span>}
            {saveMut.isError   && <span className="text-danger-fg text-xs">Failed</span>}
          </div>
        </div>
      )}
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

// buildConfigObject assembles the config.json object that Save writes. Both the
// Save mutation AND the unsaved-changes check go through this single builder so
// they can never drift — a past bug wrote `services` in the dirty-check but not
// in the actual save, so service edits (ports, etc.) silently reverted on reload.
function buildConfigObject(project, envs, images, rawConfig) {
  const cleanEnvs = {}
  for (const [k, v] of Object.entries(envs || {})) {
    const { _initial_vars, _id, ...rest } = v // eslint-disable-line no-unused-vars
    cleanEnvs[k] = rest
  }
  const updated = { ...rawConfig, project, environments: cleanEnvs, services: images }
  delete updated.images // legacy field, fully replaced by services[]
  return updated
}

// serializeConfig stringifies the built config (compact) for baseline comparison.
function serializeConfig(project, envs, images, rawConfig) {
  return JSON.stringify(buildConfigObject(project, envs, images, rawConfig))
}

export default function EditProjectPage() {
  const { workspace, name } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const { data: rawConfig, isLoading, error } = useQuery({
    queryKey: ['config', workspace, name],
    queryFn: () => fetchConfig(workspace, name),
  })
  // Project (for env→host bindings) — used by the host-aware port check.
  const { data: ws } = useQuery({ queryKey: ['workspace', workspace, name], queryFn: () => fetchWorkspace(workspace, name) })
  // Remote hosts available to this workspace — for host selection on a NEW env.
  const { data: wsHosts = [] } = useQuery({ queryKey: ['ws-hosts', workspace], queryFn: () => fetchWorkspaceHosts(workspace), enabled: !!workspace })
  // Workspace apps base domain — drives env auto-routing URLs ({proj}-{env}.{base}).
  const { data: wsSettings } = useQuery({ queryKey: ['ws-settings', workspace], queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace })
  // Effective base domain: workspace override → global apps base domain (mirrors
  // the backend's EffectiveBaseDomain so the route preview matches the deploy).
  const baseDomain = (wsSettings?.domain || ws?.apps_base_domain || '').trim()

  // Local editable state
  const [envs, setEnvs]       = useState(null)
  const [project, setProject] = useState(null)
  const [images, setImages]   = useState(null)
  const [newEnvCounter, setNewEnvCounter] = useState(0)
  const [saveError, setSaveError] = useState('')
  const [reorderOpen, setReorderOpen] = useState(false)
  const [firstEnvVars, setFirstEnvVars] = useState({})
  const [baseline, setBaseline] = useState(null)        // serialized config at load
  const [confirmCancel, setConfirmCancel] = useState(false)
  const [tab, setTab] = useState('project')             // tabbed layout (Prototype A)

  useEffect(() => {
    if (rawConfig && envs === null) {
      // Inject a stable _id into every env so EnvEditor keys never change on rename
      const withIds = {}
      Object.entries(rawConfig.environments || {}).forEach(([k, v], i) => {
        withIds[k] = { ...v, _id: `env-orig-${i}` }
      })
      setEnvs(withIds)
      setProject(rawConfig.project || {})
      setImages(rawConfig.services || [])
      setBaseline(serializeConfig(rawConfig.project || {}, withIds, rawConfig.services || [], rawConfig))
      // Pre-load vars from first env for use when adding new environments
      const firstEnvName = Object.keys(rawConfig.environments || {})[0]
      if (firstEnvName) {
        fetchEnvVars(workspace, name, firstEnvName, true) // reveal=true so values are editable in new env
          .then(vars => setFirstEnvVars(vars || {}))
          .catch(() => {})
      }
    }
  }, [rawConfig])

  const mutation = useMutation({
    mutationFn: async () => {
      // Use the shared builder so the saved payload always matches the
      // unsaved-changes check — including the edited services[] (ports, env,
      // sources). Previously this wrote `images` only for image-type projects,
      // dropping every service edit on custom projects.
      const updated = buildConfigObject(project, envs, images, rawConfig)
      await putConfig(workspace, name, JSON.stringify(updated, null, 2))

      // Write initial env vars for new environments.
      // UpdateEnvVars now creates the .env file if it doesn't exist.
      const newEnvNames = Object.keys(envs || {}).filter(e => !originalEnvNames.includes(e))
      for (const envName of newEnvNames) {
        const e = envs[envName] || {}
        const initialVars = e._initial_vars || {}
        const secretKeys = e._secret_keys || []
        if (Object.keys(initialVars).length > 0 || secretKeys.length > 0) {
          // Pass secret keys so swarm-flagged vars become Docker secrets (parity
          // with the New Project wizard).
          try { await updateEnvVars(workspace, name, envName, initialVars, [], secretKeys) } catch { /* non-fatal */ }
        }
        // Bind the chosen host (bind-only — nothing is deployed yet).
        if (e._host_id) {
          try { await setEnvHost(workspace, name, envName, Number(e._host_id), true) } catch { /* non-fatal */ }
        }
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspace', workspace, name] })
      qc.invalidateQueries({ queryKey: ['config', workspace, name] })
      navigate(`/workspaces/${workspace}/projects/${name}`)
    },
    onError: (err) => setSaveError(err.response?.data?.error || err.message),
  })

  function updateEnv(envName, cfg) {
    setEnvs(prev => ({ ...prev, [envName]: cfg }))
  }

  function renameEnv(oldName, newName) {
    if (!newName || newName === oldName) return
    setEnvs(prev => {
      const next = {}
      for (const [k, v] of Object.entries(prev)) {
        next[k === oldName ? newName : k] = v
      }
      return next
    })
  }

  function removeEnv(envName) {
    setEnvs(prev => {
      const next = { ...prev }
      delete next[envName]
      return next
    })
  }

  function addEnv() {
    const n = `new-env-${newEnvCounter + 1}`
    setNewEnvCounter(c => c + 1)
    const firstEnv = Object.values(envs || {})[0] || {}
    const base = {
      domain: '',
      http_port: 8080,
      deployment: 'compose',
      traefik_enabled: false,
      traefik_network: 'traefik_net',
      ssl_enabled: false,
      git: { enabled: false, repo: '', branch: '' },
      // Managed-dependency toggles inherit from the first env (services are
      // project-level and shared across envs).
      database: firstEnv.database || 'none',
      redis_enabled: !!firstEnv.redis_enabled,
    }
    // firstEnvVars is the API shape { KEY: { value, secret } }; flatten it to the
    // plain { KEY: value } map _initial_vars expects, and carry over secret flags.
    const seedVars = {}, seedSecrets = []
    for (const [k, v] of Object.entries(firstEnvVars)) {
      seedVars[k] = typeof v === 'string' ? v : (v?.value ?? '')
      if (typeof v === 'object' && v?.secret) seedSecrets.push(k)
    }
    setEnvs(prev => ({ ...prev, [n]: { ...base, _id: `env-new-${newEnvCounter + 1}`, _initial_vars: seedVars, _secret_keys: seedSecrets } }))
  }

  const originalEnvNames = rawConfig ? Object.keys(rawConfig.environments || {}) : []
  const currentEnvNames = envs ? Object.keys(envs) : []

  // Unsaved-changes detection: compare the current editable config to the load
  // baseline. Save is enabled only when something changed; Cancel confirms first.
  const dirty = baseline !== null && envs !== null && project !== null &&
    serializeConfig(project, envs, images, rawConfig) !== baseline

  function leave() { navigate(`/workspaces/${workspace}/projects/${name}`) }
  function handleCancel() { if (dirty) setConfirmCancel(true); else leave() }

  // Host-aware port conflicts (C+D): each env's target host × each image's host
  // ports, excluding this workspace's own containers/config.
  const envHostsMap = ws?.env_hosts || {}
  const hostChecks = []
  if (project?.type === 'image' && images && envs) {
    for (const env of Object.keys(envs)) {
      const hostId = envHostsMap[env]?.host_id || 0
      for (const img of images) {
        if (!img.name) continue
        for (const p of hostPortsFromConfig(img)) hostChecks.push({ host_id: hostId, port: Number(p), service: img.name })
      }
    }
  }
  const hostWarnings = usePortConflicts(hostChecks, `${workspace}_${name}`)

  if (isLoading) return <Layout><div className="p-8 text-content-subtle text-sm">Loading…</div></Layout>
  if (error)     return <Layout><div className="p-8 text-danger-fg text-sm">{error.message}</div></Layout>

  return (
    <Layout>
      <div className="max-w-7xl mx-auto px-6 py-8">
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div>
            <p className="text-xs font-medium uppercase tracking-wider text-content-subtle">Edit project · {workspace}</p>
            <div className="flex items-center gap-2.5 mt-0.5">
              <h1 className="text-2xl font-bold text-content-strong">{project?.name || name}</h1>
              <span className="text-xs font-mono text-content-faint px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong" title="Resource prefix (folder / URL / Docker identifier)">{workspace}_{name}</span>
              <span className={`text-xs font-medium px-2 py-0.5 rounded-full ${
                project?.type === 'image' ? 'bg-info-subtle text-info-fg' : 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300'
              }`}>
                {project?.type === 'image' ? 'image' : 'custom'}
              </span>
            </div>
          </div>
          <div className="flex items-center gap-3">
            <button
              onClick={handleCancel}
              className="text-sm font-medium px-4 py-2 rounded-lg border border-warning-border/60 bg-warning-subtle/30 hover:bg-warning/20 text-warning-fg transition-colors"
            >
              Cancel
            </button>
            <button
              onClick={() => { setSaveError(''); mutation.mutate() }}
              disabled={mutation.isPending || !dirty}
              title={!dirty ? 'No changes to save' : undefined}
              className="bg-brand-600 hover:bg-brand-700 disabled:opacity-40 disabled:cursor-not-allowed text-white text-sm font-semibold px-5 py-2 rounded-lg transition-colors"
            >
              {mutation.isPending ? 'Saving…' : 'Save changes'}
            </button>
          </div>
        </div>

        {saveError && (
          <div className="mb-5 px-4 py-3 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">{saveError}</div>
        )}

        {/* Vertical tab rail (Prototype A — combined view per tab) */}
        <VerticalTabs
          tabs={[
            { id: 'project', label: 'Project', icon: '📋' },
            { id: 'services', label: 'Services', icon: '🧱', count: (images || []).length },
            { id: 'envs', label: 'Environments', icon: '🌱', count: currentEnvNames.length },
            { id: 'host', label: 'Host', icon: '🖥' },
            { id: 'backup', label: 'Backup', icon: '💾' },
            { id: 'pipelines', label: 'Pipelines', icon: '🚀' },
            { group: 'Project' },
            { id: 'danger', label: 'Danger Zone', icon: '⚠', danger: true },
          ]}
          active={tab}
          onChange={setTab}
        >

        {/* Project settings */}
        {tab === 'project' && (
        <section className="mb-6">
          <h2 className="text-sm font-semibold text-content mb-3">Project</h2>
          <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
            <div className="grid sm:grid-cols-5 gap-4">
              <div className="sm:col-span-4">
                <Label>Project name</Label>
                {/* Editable display label — reusable across workspaces. The key and
                    resource prefix (below) are the immutable identity. */}
                <Input value={project?.name} onChange={v => setProject(p => ({ ...p, name: v }))} />
                <p className="text-xs text-content-subtle mt-1">A display label — editable; may repeat across workspaces.</p>
              </div>
              <div className="sm:col-span-1">
                <Label>Key <span className="font-normal normal-case text-content-faint">(fixed)</span></Label>
                <div
                  title="Fixed after creation — the project's folder / URL identity"
                  className="w-full px-3 py-2 bg-surface-raised/60 border border-border-strong rounded-lg text-content-muted text-sm font-mono cursor-not-allowed select-all truncate"
                >
                  {name}
                </div>
                <p className="text-xs text-content-subtle mt-1">Fixed identity.</p>
              </div>
            </div>
            {/* Registry only applies to stacks that BUILD images. Pull-only stacks
                (image/prebuilt) and database-hosting stacks don't push, so hide it. */}
            {(images || []).some(s => s.build) && (
              <div className="sm:max-w-[60%]">
                <Label>Registry</Label>
                <p className="text-xs text-content-subtle mb-2">Built images are tagged and pushed here. Pick a saved registry or add one with credentials so the build can authenticate.</p>
                <RegistryPicker
                  workspace={workspace}
                  value={project?.registry}
                  onChange={v => setProject(p => ({ ...p, registry: v }))}
                />
              </div>
            )}

            {/* Source repository — one repo per project; build services build from
                a subdir of it (cloned into the build context before build). */}
            <div className="grid sm:grid-cols-[1fr_auto] gap-4">
              <div>
                <Label>Source repository <span className="font-normal normal-case text-content-faint">(for build services)</span></Label>
                <Input value={project?.git_repo} onChange={v => setProject(p => ({ ...p, git_repo: v }))} placeholder="https://github.com/org/repo.git" />
                <p className="text-xs text-content-subtle mt-1">One repo per project; each build service's context is a subdirectory. Cloned/pulled before each build. Public HTTPS or token URL.</p>
              </div>
              <div className="sm:w-40">
                <Label>Default branch</Label>
                <Input value={project?.git_branch} onChange={v => setProject(p => ({ ...p, git_branch: v }))} placeholder="main" />
              </div>
            </div>

            {/* Local HTTPS for domain-routed envs without a workspace base domain. */}
            <Toggle
              label="Local HTTPS (self-signed)"
              hint={baseDomain
                ? `Not used — this workspace has a base domain (${baseDomain}); domain-routed envs use Let's Encrypt.`
                : "Serve domain-routed *.localhost envs over HTTPS with Traefik's self-signed cert (for apps that require HTTPS, e.g. Vaultwarden). Default is plain HTTP."}
              checked={!!project?.local_tls}
              onChange={v => setProject(p => ({ ...p, local_tls: v }))}
            />

            {/* Resource prefix — immutable Docker name prefix ({workspace}_{project}). */}
            <div>
              <Label>Resource prefix</Label>
              <div
                title="Fixed after creation — the Docker stack / container / volume / network name prefix"
                className="w-full px-3 py-2 bg-surface-raised/40 border border-border-strong/60 rounded-lg text-content-muted text-sm font-mono cursor-not-allowed select-all truncate"
              >
                {project?.resource_prefix || `${workspace}_${name}`}
              </div>
              <p className="text-xs text-content-subtle mt-1">Fixed after creation — the Docker stack, container, volume and network name prefix.</p>
            </div>

            {/* Workspace folder — read-only. Prefer the host-side path (the bind-
                mount source); fall back to the in-container path if unresolved. */}
            <div>
              <Label>Project folder</Label>
              <div
                title={ws?.host_path || ws?.path || ''}
                className="w-full px-3 py-2 bg-surface-raised/40 border border-border-strong/60 rounded-lg text-content-muted text-sm font-mono cursor-not-allowed select-all truncate"
              >
                {ws?.host_path || ws?.path || '—'}
              </div>
              <p className="text-xs text-content-subtle mt-1">
                {ws?.host_path
                  ? 'Location on the host — holds config, compose files and bind-mounted volumes.'
                  : 'Path inside the Rigger container. Set HOST_WORKSPACES_DIR to show the host path.'}
              </p>
            </div>
          </div>
        </section>
        )}

        {/* Services — the unified service graph (build / pull / worker) +
            project-level managed dependencies (DB / Redis / object storage). */}
        {tab === 'services' && (
          <section className="mb-6">
            {/* Managed services (project-level). Hidden for image / pre-built stacks
                — they bring their own data services as images (matches the wizard). */}
            {project?.type !== 'image' && (
              <div className="mb-5">
                <ManagedServices
                  value={{ database: project?.database, dbVersion: project?.db_version, redis: project?.redis_enabled, objectStorage: project?.object_storage, storageBucket: project?.storage_bucket, storagePath: project?.storage_path, storageUi: project?.storage_ui, webSql: project?.web_sql }}
                  onChange={v => setProject(p => ({ ...p, database: v.database, db_version: v.dbVersion, redis_enabled: !!v.redis, object_storage: v.objectStorage || 'none', storage_bucket: v.storageBucket || '', storage_path: v.storagePath || '', storage_ui: (v.objectStorage === 'minio' && !!v.storageUi), web_sql: !!v.webSql }))}
                  showWebSql={project?.type === 'database'}
                  resourcePrefix={project?.resource_prefix || `${workspace}_${project?.key || name}`}
                />
              </div>
            )}

            {project?.source_kind === 'upload' && (
              <ReplaceSourceCard workspace={workspace} name={name} />
            )}

            {project?.source_kind === 'upload' && project?.db_seed && project?.database && project?.database !== 'none' && (
              <SeedDatabaseCard workspace={workspace} name={name} seed={project.db_seed} database={project.database}
                envNames={Object.keys(project?.environments || {})}
                onToggleAuto={on => setProject(p => ({ ...p, db_seed: { ...(p.db_seed || { file: 'seed.sql' }), auto: on } }))} />
            )}

            <h2 className="text-sm font-semibold text-content mb-3">Services</h2>
            <ImagesEditor images={images || []} onChange={setImages}
              gitRepo={project?.git_repo} gitBranch={project?.git_branch}
              managedDeps={enabledDependsOnTargets({ database: project?.database, redis: project?.redis_enabled, objectStorage: project?.object_storage })} />
            <PortWarnings warnings={hostWarnings} />
            <p className="text-xs text-content-subtle mt-2">After saving, <strong>Refresh</strong> then redeploy each environment to apply service changes.</p>
          </section>
        )}

        {/* Environments */}
        {tab === 'envs' && (<>
        <section className="mb-6">
          <div className="flex items-center justify-between gap-3 mb-3">
            <h2 className="text-sm font-semibold text-content">Environments</h2>
            <div className="flex items-center gap-2 shrink-0">
              {currentEnvNames.length > 1 && (
                <button type="button" onClick={() => setReorderOpen(true)}
                  className="px-3 py-1.5 rounded-lg text-xs font-semibold border border-border-strong text-content hover:bg-surface-raised transition-colors">
                  ⇅ Reorder
                </button>
              )}
              <button type="button" onClick={addEnv}
                className="px-3 py-1.5 rounded-lg text-xs font-semibold bg-brand-600 hover:bg-brand-700 text-white transition-colors">
                + Add environment
              </button>
            </div>
          </div>
          {reorderOpen && (
            <EnvReorderModal
              workspace={workspace} name={name}
              envNames={ws?.envs || currentEnvNames}
              onClose={() => setReorderOpen(false)}
              onSaved={() => { qc.invalidateQueries({ queryKey: ['workspace', workspace, name] }); qc.invalidateQueries({ queryKey: ['config', workspace, name] }) }}
            />
          )}
          <p className="text-xs text-content-subtle mb-3">
            {currentEnvNames.length} environment{currentEnvNames.length !== 1 ? 's' : ''} — click one to expand
            {currentEnvNames.some(e => !originalEnvNames.includes(e)) && (
              <span className="ml-2 text-brand-400">· new environments need bootstrapping after save</span>
            )}
          </p>

          <div className="space-y-3">
            {Object.entries(envs || {}).map(([envName, cfg], i) => (
              <EnvEditor
                key={cfg._id || envName}
                envName={envName}
                cfg={cfg}
                defaultOpen={i === 0}
                onChange={(updated) => updateEnv(envName, updated)}
                onRename={(newName) => renameEnv(envName, newName)}
                onRemove={() => removeEnv(envName)}
                isNew={!originalEnvNames.includes(envName)}
                isOnlyEnv={Object.keys(envs || {}).length === 1}
                projectType={project?.type || 'custom'}
                workspaceName={name}
                hosts={wsHosts}
                imageNames={(images || []).map(img => img.name).filter(Boolean)}
                resourcePrefix={project?.resource_prefix || `${workspace}_${project?.key || name}`}
                baseDomain={baseDomain}
                autoUrlMode={ws?.auto_url_mode || ''}
                appHost={ws?.app_host || ''}
                localTLS={!!project?.local_tls}
                projectDatabase={project?.database || ''}
                projectRedis={!!project?.redis_enabled}
                projectObjectStorage={project?.object_storage || ''}
                projectWebSql={!!project?.web_sql}
                projectStorageUi={!!project?.storage_ui}
                gitRepo={project?.git_repo || ''}
                gitBranch={project?.git_branch || ''}
                dirty={dirty}
                existingNames={currentEnvNames}
                onCopied={() => window.location.reload()}
              />
            ))}
          </div>
        </section>

        {/* After-save hint for new envs */}
        {currentEnvNames.some(e => !originalEnvNames.includes(e)) && (
          <div className="bg-warning-subtle/40 border border-warning-border/50 rounded-xl px-4 py-3 text-sm text-warning-fg">
            After saving, go to the project and click <strong>Init</strong> for each new environment to generate its compose file and .env.
          </div>
        )}
        </>)}

        {/* Host — per-environment binding + whole-project migrate (Phase 7) */}
        {tab === 'host' && (<>
          <EnvHostsSection name={name} />
          <MigrateSection name={name} />
        </>)}

        {/* Backup schedules — per environment (Phase 11) */}
        {tab === 'backup' && envs && <BackupSection workspaceName={name} envs={envs} updateEnv={updateEnv} />}

        {/* Pipelines (Phase 9) */}
        {tab === 'pipelines' && <PipelinesTab workspace={workspace} name={name} envNames={currentEnvNames} serviceNames={(images || []).map(img => img.name).filter(Boolean)} />}

        {/* Danger zone */}
        {tab === 'danger' && <DangerZone name={name} />}
        </VerticalTabs>
      </div>

      {/* Discard-changes confirmation */}
      {confirmCancel && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm" onClick={() => setConfirmCancel(false)}>
          <div className="bg-surface border border-warning-border/60 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <div className="flex items-center gap-3">
              <span className="text-2xl">⚠️</span>
              <h3 className="font-semibold text-content-strong">Discard unsaved changes?</h3>
            </div>
            <p className="text-sm text-content-muted">You have unsaved changes to <strong className="text-content">{name}</strong>. Leaving now will discard them.</p>
            <div className="flex gap-3">
              <button onClick={() => { setConfirmCancel(false); leave() }} className="flex-1 bg-amber-700 hover:bg-amber-600 text-white text-sm font-semibold py-2 rounded-lg transition-colors">Discard &amp; leave</button>
              <button onClick={() => setConfirmCancel(false)} className="px-4 py-2 bg-surface-raised hover:bg-surface-overlay text-content text-sm rounded-lg transition-colors">Keep editing</button>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}

// MigrateWarning is the confirmation shown before any host move. It spells out the
// downtime and the data/secrets left behind on the source host.
function MigrateWarning({ what, from, to, warnings, onConfirm, onCancel }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm" onClick={onCancel}>
      <div className="bg-surface border border-warning-border/60 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center gap-3">
          <span className="text-2xl">⚠️</span>
          <h3 className="font-semibold text-content-strong">Move {what}?</h3>
        </div>
        <p className="text-sm text-content">
          Moving <strong className="text-content-strong">{what}</strong> from <strong className="text-content-strong">{from}</strong> to <strong className="text-content-strong">{to}</strong>.
        </p>
        <ul className="text-xs text-content-muted space-y-2 list-disc pl-5">
          <li>
            <strong className="text-warning-fg">Downtime:</strong> if it's currently running, it goes down the
            moment the source stops and stays down until it's back up and restored on the target. This runs in
            the background — you'll get a notification when it's done, so you can leave this page.
          </li>
          <li>
            <strong className="text-warning-fg">Data left on the source:</strong> {from} keeps the stopped
            containers, volumes (your data) and files (including <code className="font-mono">.env</code> secrets) —
            they are <strong>not</strong> deleted. If you plan to decommission {from}, wipe them afterward in{' '}
            <a href="/housekeeping" className="text-brand-400 underline">Housekeeping → Migration leftovers</a>.
          </li>
        </ul>
        <PortWarnings warnings={warnings} />
        <div className="flex gap-3 pt-1">
          <button onClick={onConfirm} className="flex-1 bg-amber-700 hover:bg-amber-600 text-white text-sm font-semibold py-2 rounded-lg transition-colors">Move</button>
          <button onClick={onCancel} className="px-4 py-2 bg-surface-raised hover:bg-surface-overlay text-content text-sm rounded-lg transition-colors">Cancel</button>
        </div>
      </div>
    </div>
  )
}

// MigrationProgress polls a background migration job and shows its live log +
// status. The job also notifies via the alert bell, so leaving the page is fine.
function MigrationProgress({ jobId, onDone }) {
  const doneRef = useRef(false)
  const { data: job } = useQuery({
    queryKey: ['migration-job', jobId],
    queryFn: () => getMigrationJob(jobId),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && q.state.data.status !== 'running' ? false : 1500),
  })
  useEffect(() => {
    if (job && job.status !== 'running' && !doneRef.current) {
      doneRef.current = true
      onDone?.(job)
    }
  }, [job, onDone])

  if (!jobId || !job) return null
  const banner = job.status === 'running'
    ? '▶ Running in the background — you can safely leave this page; you\'ll be notified when it completes.'
    : job.status === 'completed' ? '✓ Migration completed.' : `✗ Migration failed${job.error ? ': ' + job.error : '.'}`
  const cls = job.status === 'running' ? 'text-info-fg' : job.status === 'completed' ? 'text-success-fg' : 'text-danger-fg'
  return (
    <div className="space-y-2">
      <p className={`text-sm ${cls}`}>{banner}</p>
      {job.log && (
        <pre className="max-h-72 overflow-auto bg-canvas border border-border rounded-lg p-3 text-xs text-content whitespace-pre-wrap">{job.log}</pre>
      )}
    </div>
  )
}

// EnvHostsSection shows each environment's host and lets you change it. Changing
// a deployed env's host migrates its data; an undeployed env just repoints.
function EnvHostsSection({ name }) {
  const { workspace } = useParams()
  const qc = useQueryClient()
  const { data: hosts = [] } = useQuery({ queryKey: ['ws-hosts', workspace], queryFn: () => fetchWorkspaceHosts(workspace), enabled: !!workspace })
  const { data: ws } = useQuery({ queryKey: ['workspace', workspace, name], queryFn: () => fetchWorkspace(workspace, name) })

  const [target, setTarget] = useState({})   // env -> selected target id (string)
  const [pending, setPending] = useState(null) // { env, targetId } awaiting confirmation
  const [jobId, setJobId] = useState(null)      // active background migration job
  const [running, setRunning] = useState(false)
  const [err, setErr] = useState('')

  const envs = ws?.envs || []
  const envHosts = ws?.env_hosts || {}
  const hostName = (id) => id === 0 ? 'Local control plane' : (hosts.find(h => h.id === id)?.name || `host #${id}`)

  function requestChange(env) {
    const t = target[env]
    if (t === undefined || t === '') return
    setPending({ env, targetId: Number(t) })
  }

  async function confirmChange() {
    const { env, targetId } = pending
    setPending(null); setRunning(true); setErr(''); setJobId(null)
    try {
      const job = await setEnvHost(workspace, name, env, targetId)
      setJobId(job.id)
    } catch (e) {
      setErr(e.response?.data?.error || e.message || 'failed to start')
      setRunning(false)
    }
  }

  function onJobDone() {
    setRunning(false)
    qc.invalidateQueries({ queryKey: ['workspace', workspace, name] })
    qc.invalidateQueries({ queryKey: ['projects', workspace] })
  }

  // Check the pending move's resolved host ports against the destination host
  // (uses env_access so ${VAR} ports resolve to real numbers).
  const destChecks = []
  if (pending) {
    const access = ws?.env_access?.[pending.env]
    for (const img of (access?.images || [])) {
      for (const p of [img.host_port, ...(img.link_ports || [])]) {
        if (/^\d+$/.test(String(p))) destChecks.push({ host_id: pending.targetId, port: Number(p), service: img.name })
      }
    }
  }
  const destWarnings = usePortConflicts(destChecks)

  if (envs.length === 0) return null

  return (
    <section className="mt-8">
      <div className="border border-border rounded-xl overflow-hidden">
        <div className="px-5 py-3 bg-surface/60 border-b border-border">
          <h2 className="text-sm font-semibold text-content">Environment hosts</h2>
          <p className="text-xs text-content-subtle mt-0.5">
            Run each environment on a different host. Changing a <strong>deployed</strong> environment's
            host migrates its data (and stops the old copy, keeping its data); an undeployed one just
            repoints and provisions on next deploy.
          </p>
        </div>
        <div className="px-5 py-4 space-y-2">
          {envs.map(env => {
            const curId = envHosts[env]?.host_id || 0
            const opts = [{ id: 0, label: 'Local control plane' },
              ...hosts.map(h => ({ id: h.id, label: `${h.name} (${h.address})` }))]
              .filter(o => o.id !== curId)
            return (
              <div key={env} className="flex items-center gap-3">
                <div className="w-40 shrink-0">
                  <p className="text-sm text-content">{env}</p>
                  <p className="text-xs text-content-subtle">on {hostName(curId)}</p>
                </div>
                <select
                  value={target[env] ?? ''}
                  onChange={e => setTarget(t => ({ ...t, [env]: e.target.value }))}
                  disabled={running}
                  className="flex-1 px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-info"
                >
                  <option value="">Move to…</option>
                  {opts.map(o => <option key={o.id} value={String(o.id)}>{o.label}</option>)}
                </select>
                <button
                  onClick={() => requestChange(env)}
                  disabled={running || (target[env] ?? '') === ''}
                  className="shrink-0 px-3 py-2 bg-blue-700 hover:bg-blue-600 disabled:opacity-40 disabled:cursor-not-allowed text-white text-sm font-medium rounded-lg transition-colors"
                >
                  Change
                </button>
              </div>
            )
          })}
          {err && <p className="text-sm text-danger-fg">✗ {err}</p>}
          <MigrationProgress jobId={jobId} onDone={onJobDone} />
        </div>
      </div>

      {pending && (
        <MigrateWarning
          what={`${name} / ${pending.env}`}
          from={hostName(envHosts[pending.env]?.host_id || 0)}
          to={hostName(pending.targetId)}
          warnings={destWarnings}
          onConfirm={confirmChange}
          onCancel={() => setPending(null)}
        />
      )}
    </section>
  )
}

// MigrateSection moves the whole workspace to another host (or back to local),
// streaming progress. Only available when every environment is on the same host.
function MigrateSection({ name }) {
  const { workspace } = useParams()
  const qc = useQueryClient()
  const { data: hosts = [] } = useQuery({ queryKey: ['ws-hosts', workspace], queryFn: () => fetchWorkspaceHosts(workspace), enabled: !!workspace })
  const { data: ws } = useQuery({ queryKey: ['workspace', workspace, name], queryFn: () => fetchWorkspace(workspace, name) })

  const envs = ws?.envs || []
  const envHosts = ws?.env_hosts || {}
  const distinctHosts = [...new Set(envs.map(e => envHosts[e]?.host_id || 0))]
  const mixed = distinctHosts.length > 1
  const currentHostId = mixed ? -1 : (distinctHosts[0] ?? 0)

  const [target, setTarget] = useState('')          // selected target id ('' = none, '0' = local)
  const [running, setRunning] = useState(false)
  const [jobId, setJobId] = useState(null)
  const [err, setErr] = useState('')
  const [confirming, setConfirming] = useState(false)

  // Build target options: local + every host, excluding the current location.
  const options = [{ id: 0, label: 'Local control plane' },
    ...hosts.map(h => ({ id: h.id, label: `${h.name} (${h.address})` }))]
    .filter(o => o.id !== currentHostId)

  async function run() {
    setConfirming(false)
    setRunning(true); setErr(''); setJobId(null)
    try {
      const job = await migrateWorkspace(workspace, name, Number(target))
      setJobId(job.id)
    } catch (e) {
      setErr(e.response?.data?.error || e.message || 'failed to start')
      setRunning(false)
    }
  }

  function onJobDone() {
    setRunning(false)
    qc.invalidateQueries({ queryKey: ['projects', workspace] })
    qc.invalidateQueries({ queryKey: ['workspace', workspace, name] })
  }

  const currentLabel = currentHostId === 0
    ? 'local control plane'
    : (hosts.find(h => h.id === currentHostId)?.name || `host #${currentHostId}`)

  return (
    <section className="mt-8">
      <div className="border border-border rounded-xl overflow-hidden">
        <div className="px-5 py-3 bg-surface/60 border-b border-border">
          <h2 className="text-sm font-semibold text-content">Move the whole project</h2>
          <p className="text-xs text-content-subtle mt-0.5">
            {mixed
              ? 'Environments are on different hosts — move them individually above.'
              : <>Currently on <strong className="text-content-muted">{currentLabel}</strong>. Moves every environment together (back up → ship → restore). Source data is left intact.</>}
          </p>
        </div>
        {!mixed && (
          <div className="px-5 py-4 space-y-3">
            <div className="flex items-center gap-3">
              <select
                value={target}
                onChange={e => setTarget(e.target.value)}
                disabled={running}
                className="flex-1 px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-info"
              >
                <option value="">Select a target…</option>
                {options.map(o => <option key={o.id} value={String(o.id)}>{o.label}</option>)}
              </select>
              <button
                onClick={() => setConfirming(true)}
                disabled={running || target === ''}
                className="shrink-0 px-4 py-2 bg-blue-700 hover:bg-blue-600 disabled:opacity-40 disabled:cursor-not-allowed text-white text-sm font-medium rounded-lg transition-colors"
              >
                {running ? 'Migrating…' : 'Migrate'}
              </button>
            </div>
            {err && <p className="text-sm text-danger-fg">✗ {err}</p>}
            <MigrationProgress jobId={jobId} onDone={onJobDone} />
          </div>
        )}
      </div>

      {confirming && (
        <MigrateWarning
          what={`all of ${name}`}
          from={currentLabel}
          to={options.find(o => o.id === Number(target))?.label || 'target'}
          onConfirm={run}
          onCancel={() => setConfirming(false)}
        />
      )}
    </section>
  )
}

// ── Backup schedules — per environment (Phase 11) ─────────────────────────────

// EnvBackupSchedules wraps the reusable editor for one env, fetching that env's
// data-bearing services so the picker can guide the user.
function EnvBackupSchedules({ workspaceName, env, cfg, updateEnv, targets }) {
  const { workspace } = useParams()
  const { data: services = [] } = useQuery({
    queryKey: ['backup-services', workspace, workspaceName, env],
    queryFn: () => fetchBackupServices(workspace, workspaceName, env),
    retry: false,
  })
  return (
    <div className="bg-surface border border-border rounded-xl p-4">
      <h3 className="text-sm font-semibold text-content-strong mb-2">{env}</h3>
      <BackupScheduleEditor
        schedules={cfg.backup_schedules || []}
        onChange={(list) => updateEnv(env, { ...cfg, backup_schedules: list })}
        services={services}
        targets={targets}
      />
    </div>
  )
}

function BackupSection({ workspaceName, envs, updateEnv }) {
  const { data: targets = [] } = useQuery({ queryKey: ['ws-backup-targets', workspaceName], queryFn: () => fetchWorkspaceBackupTargets(workspaceName), enabled: !!workspaceName })
  const envNames = Object.keys(envs || {})
  return (
    <div className="space-y-3">
      <div>
        <h2 className="text-sm font-semibold text-content-strong">Backups</h2>
        <p className="text-xs text-content-subtle mt-0.5">
          Each environment can have its own schedules — back up specific services more or less often,
          to local or remote storage. Snapshots run on the interval; older ones beyond a schedule's keep
          count are pruned. Changes are saved with the project.
        </p>
      </div>
      {envNames.map(env => (
        <EnvBackupSchedules
          key={env} workspaceName={workspaceName} env={env}
          cfg={envs[env]} updateEnv={updateEnv} targets={targets}
        />
      ))}
    </div>
  )
}

function DangerZone({ name }) {
  const { workspace } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [open, setOpen]       = useState(false)
  const [confirm, setConfirm] = useState('')
  const [error, setError]     = useState('')

  const mutation = useMutation({
    mutationFn: () => deleteWorkspace(workspace, name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects', workspace] })
      navigate('/', { replace: true })
    },
    onError: (e) => setError(e.response?.data?.error || 'Delete failed'),
  })

  function handleDelete() {
    if (confirm !== name) {
      setError(`Type "${name}" exactly to confirm`)
      return
    }
    mutation.mutate()
  }

  return (
    <section className="mt-8">
      <div className="border border-danger-border/50 rounded-xl overflow-hidden">
        <div className="px-5 py-3 bg-danger-subtle/30 border-b border-danger-border/50 flex items-center justify-between">
          <div>
            <h2 className="text-sm font-semibold text-danger-fg">Danger zone</h2>
            <p className="text-xs text-danger-fg/70 mt-0.5">Irreversible actions — proceed with caution</p>
          </div>
        </div>
        <div className="px-5 py-4 flex items-center justify-between">
          <div>
            <p className="text-sm text-content">Delete this project</p>
            <p className="text-xs text-content-subtle mt-0.5">Permanently removes all files, configs, and backups for <strong className="text-content-muted">{name}</strong>. Running containers are not stopped automatically.</p>
          </div>
          <button
            onClick={() => { setOpen(true); setConfirm(''); setError('') }}
            className="ml-6 shrink-0 px-4 py-2 bg-danger-subtle/60 hover:bg-danger/20 text-danger-fg hover:text-danger-fg text-sm font-medium rounded-lg border border-danger-border/50 transition-colors"
          >
            Delete project
          </button>
        </div>
      </div>

      {/* Confirmation modal */}
      {open && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm" onClick={() => setOpen(false)}>
          <div className="bg-surface border border-danger-border/60 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <div className="flex items-center gap-3">
              <span className="text-2xl">⚠️</span>
              <h3 className="font-semibold text-content-strong">Delete <span className="text-danger-fg">{name}</span>?</h3>
            </div>
            <p className="text-sm text-content-muted">
              This will permanently delete the project directory and all its contents including configs, environment files, and backups.
              <strong className="text-content block mt-1">This cannot be undone.</strong>
            </p>
            {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
            <div>
              <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1.5">
                Type <span className="text-danger-fg font-mono">{name}</span> to confirm
              </label>
              <input
                type="text"
                value={confirm}
                onChange={e => { setConfirm(e.target.value); setError('') }}
                onKeyDown={e => e.key === 'Enter' && handleDelete()}
                placeholder={name}
                autoFocus
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-danger transition-colors"
              />
            </div>
            <div className="flex gap-3">
              <button
                onClick={handleDelete}
                disabled={mutation.isPending || confirm !== name}
                className="flex-1 bg-red-700 hover:bg-red-600 disabled:opacity-40 disabled:cursor-not-allowed text-white text-sm font-semibold py-2 rounded-lg transition-colors"
              >
                {mutation.isPending ? 'Deleting…' : 'Delete permanently'}
              </button>
              <button
                onClick={() => setOpen(false)}
                className="px-4 py-2 bg-surface-raised hover:bg-surface-overlay text-content text-sm rounded-lg transition-colors"
              >
                Cancel
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}
