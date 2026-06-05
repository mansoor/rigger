import { useState, useEffect, useRef } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchConfig, putConfig, deleteWorkspace, fetchEnvVars, updateEnvVars, fetchHosts, fetchWorkspace, migrateWorkspace, setEnvHost, getMigrationJob } from '../lib/api'
import Layout from '../components/Layout'
import TrashIcon from '../components/TrashIcon'

// ── Shared primitives ─────────────────────────────────────────────────────────

function Label({ children, required }) {
  return (
    <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">
      {children}{required && <span className="text-red-400 ml-0.5">*</span>}
    </label>
  )
}

function Input({ value, onChange, placeholder, type = 'text', ...rest }) {
  return (
    <input
      type={type} value={value ?? ''} onChange={e => onChange(e.target.value)}
      placeholder={placeholder}
      className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-500 focus:outline-none focus:border-brand-500 transition-colors"
      {...rest}
    />
  )
}

function Toggle({ label, hint, checked, onChange, disabled = false }) {
  return (
    <div className={`flex items-center justify-between ${disabled ? 'opacity-50' : ''}`}>
      <div>
        <p className="text-sm text-gray-200">{label}</p>
        {hint && <p className="text-xs text-gray-500 mt-0.5">{hint}</p>}
      </div>
      <button
        type="button"
        onClick={() => !disabled && onChange(!checked)}
        disabled={disabled}
        className={`relative w-10 h-5 rounded-full transition-colors shrink-0 disabled:cursor-not-allowed ${
          checked && !disabled ? 'bg-brand-600' : checked ? 'bg-brand-800' : 'bg-gray-700'
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
      className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500">
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

const DEPLOYMENT_OPTIONS = [{ value: 'compose', label: 'Docker Compose' }, { value: 'swarm', label: 'Docker Swarm' }]
const BACKEND_OPTIONS    = [{ value: 'laravel', label: 'Laravel (PHP-FPM)' }, { value: 'nodejs', label: 'Node.js' }]
const FRONTEND_OPTIONS   = [{ value: 'none', label: 'None (API only)' }, { value: 'nextjs', label: 'Next.js' }, { value: 'react', label: 'React / Vite' }]
const DB_OPTIONS         = [{ value: 'none', label: 'None' }, { value: 'postgres', label: 'PostgreSQL' }, { value: 'mysql', label: 'MySQL' }]

// ── Helpers: port rows ↔ img fields ──────────────────────────────────────────

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
    const p = ep.split(':')
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
    <div className="flex items-center rounded overflow-hidden border border-gray-700 shrink-0 text-xs font-mono">
      {['rw', 'ro'].map(m => (
        <button
          key={m}
          type="button"
          onClick={() => onChange(m)}
          className={`px-2 py-0.5 transition-colors ${
            mode === m
              ? m === 'ro'
                ? 'bg-amber-600 text-white'
                : 'bg-gray-600 text-white'
              : 'bg-gray-900 text-gray-500 hover:text-gray-300'
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
function ServiceCard({ img, idx, allImages, onUpdate, onRemove }) {
  const [portRows,   setPortRows]   = useState(() => imgToPortRows(img))
  const [volumeRows, setVolumeRows] = useState(() => imgToVolumeRows(img))

  function syncPorts(rows) {
    setPortRows(rows)
    onUpdate(idx, { ...img, ...portRowsToFields(rows) })
  }
  function syncVolumes(rows) {
    setVolumeRows(rows)
    onUpdate(idx, { ...img, volumes: volumeRowsToArray(rows) })
  }
  function upd(field, val) {
    onUpdate(idx, { ...img, [field]: val })
  }

  const otherNames = allImages.map((m, j) => j !== idx ? m.name : null).filter(Boolean)

  const monoInput = 'px-2 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm font-mono focus:outline-none focus:border-brand-500'

  return (
    <div className="bg-gray-800/50 border border-gray-700 rounded-xl p-4 space-y-4">
      {/* Header */}
      <div className="flex items-center justify-between">
        <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">Service {idx + 1}</p>
        {allImages.length > 1 && (
          <button type="button" onClick={() => onRemove(idx)} className="text-xs text-red-400 hover:text-red-300 transition-colors">Remove</button>
        )}
      </div>

      {/* Identity */}
      <div className="grid grid-cols-3 gap-3">
        <div><Label required>Service name</Label>
          <Input value={img.name} onChange={v => upd('name', v)} placeholder="app" /></div>
        <div><Label required>Image</Label>
          <Input value={img.image} onChange={v => upd('image', v)} placeholder="nginx" /></div>
        <div><Label>Tag</Label>
          <Input value={img.tag} onChange={v => upd('tag', v)} placeholder="latest" /></div>
      </div>

      {/* Port mappings */}
      <div>
        <Label>Port mappings</Label>
        <p className="text-xs text-gray-500 mb-2">
          <code className="font-mono text-xs">HOST PORT</code> : <code className="font-mono text-xs">CONTAINER PORT</code> — leave host blank to expose internally only.
          <span className="ml-2 text-gray-600">🔗 = show as link on env card</span>
        </p>
        <div className="space-y-1.5">
          {portRows.map((row, ri) => (
            <div key={ri} className="flex items-center gap-2">
              <input type="text" value={row.host}
                onChange={e => { const r = portRows.map((x,j)=>j===ri?{...x,host:e.target.value}:x); syncPorts(r) }}
                placeholder="8080" className={`flex-1 ${monoInput}`} />
              <span className="text-gray-500 font-bold shrink-0">:</span>
              <input type="text" value={row.container}
                onChange={e => { const r = portRows.map((x,j)=>j===ri?{...x,container:e.target.value}:x); syncPorts(r) }}
                placeholder="80" className={`flex-1 ${monoInput}`} />
              {/* Link checkbox — only meaningful when a host port is set */}
              <label title="Show as clickable link on env card" className={`flex items-center gap-1 shrink-0 cursor-pointer select-none ${row.host.trim() ? 'text-gray-400 hover:text-brand-400' : 'text-gray-700 cursor-not-allowed'}`}>
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
                  className="text-gray-500 hover:text-red-400 transition-colors shrink-0 p-0.5 rounded hover:bg-red-950/30"><TrashIcon /></button>
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
        <p className="text-xs text-gray-500 mb-2">
          <code className="font-mono text-xs">SOURCE</code> : <code className="font-mono text-xs">CONTAINER PATH</code> —
          use <code className="font-mono text-xs">./volumes/name</code> for a bind mount scoped to this env, or a plain name for a Docker named volume.
        </p>
        <div className="space-y-1.5">
          {volumeRows.map((row, ri) => {
            const isBind = row.source.startsWith('./') || row.source.startsWith('/')
            return (
              <div key={ri} className="flex items-center gap-2">
                <span className={`text-xs px-1.5 py-0.5 rounded font-medium shrink-0 ${
                  isBind ? 'bg-blue-950 text-blue-300' : 'bg-purple-950 text-purple-300'
                }`}>{isBind ? 'bind' : 'named'}</span>
                <input type="text" value={row.source}
                  onChange={e => { const r = volumeRows.map((x,j)=>j===ri?{...x,source:e.target.value}:x); syncVolumes(r) }}
                  placeholder="./volumes/app_data" className={`flex-1 ${monoInput}`} />
                <span className="text-gray-500 font-bold shrink-0">:</span>
                <input type="text" value={row.path}
                  onChange={e => { const r = volumeRows.map((x,j)=>j===ri?{...x,path:e.target.value}:x); syncVolumes(r) }}
                  placeholder="/var/lib/data" className={`flex-1 ${monoInput}`} />
                <VolModeToggle mode={row.mode || 'rw'} onChange={m => { const r = volumeRows.map((x,j)=>j===ri?{...x,mode:m}:x); syncVolumes(r) }} />
                <button type="button" onClick={() => syncVolumes(volumeRows.filter((_,j)=>j!==ri))}
                  className="text-gray-500 hover:text-red-400 transition-colors shrink-0 p-0.5 rounded hover:bg-red-950/30"><TrashIcon /></button>
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
          <p className="text-xs text-gray-500 mb-2">
            Shell command Docker runs to test container health. Leave blank to disable.
            Example: <code className="font-mono text-xs">curl -sf http://localhost/health || exit 1</code>
          </p>
          <input
            type="text"
            value={img.healthcheck || ''}
            onChange={e => upd('healthcheck', e.target.value)}
            placeholder="curl -sf http://localhost/health || exit 1"
            className="w-full px-2 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm font-mono placeholder-gray-600 focus:outline-none focus:border-brand-500"
          />
        </div>
        {/* Time parameters — only shown when a command is set */}
        {img.healthcheck && (
          <div>
            <p className="text-xs text-gray-500 mb-2">
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
                    <label className="block text-xs text-gray-500 mb-1">
                      {label}{!noSuffix && <span className="text-gray-600"> (s)</span>}
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
                      className="w-full px-2 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm font-mono focus:outline-none focus:border-brand-500"
                    />
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>

      {/* depends_on */}
      {otherNames.length > 0 && (
        <div>
          <Label>Depends on</Label>
          <p className="text-xs text-gray-500 mb-2">
            This service waits for selected services before starting.
            Compose waits for healthy status if the dependency has a healthcheck.
          </p>
          <div className="flex flex-wrap gap-3">
            {otherNames.map(svcName => {
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
                    className="rounded border-gray-600 bg-gray-700 text-brand-500 focus:ring-brand-500"
                  />
                  <span className="text-sm text-gray-300 font-mono">{svcName}</span>
                </label>
              )
            })}
          </div>
        </div>
      )}

      {/* Advanced — extra_compose YAML */}
      <details className="group">
        <summary className="text-xs text-gray-500 cursor-pointer hover:text-gray-300 transition-colors select-none list-none flex items-center gap-1">
          <span className="group-open:rotate-90 transition-transform inline-block">▶</span>
          Advanced YAML overrides
        </summary>
        <div className="mt-2 space-y-1">
          <p className="text-xs text-gray-500">
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
            className="w-full px-3 py-2 bg-gray-950 border border-gray-700 rounded-lg text-green-300 text-xs font-mono placeholder-gray-600 focus:outline-none focus:border-brand-500 resize-y"
          />
        </div>
      </details>
    </div>
  )
}

function ImagesEditor({ images, onChange }) {
  return (
    <div className="space-y-3">
      {images.map((img, i) => (
        <ServiceCard
          key={i}
          img={img}
          idx={i}
          allImages={images}
          onUpdate={(idx, updated) => onChange(images.map((m, j) => j === idx ? updated : m))}
          onRemove={idx => onChange(images.filter((_, j) => j !== idx))}
        />
      ))}
      <button
        type="button"
        onClick={() => onChange([...images, {
          name: '', image: '', tag: 'latest', port: 0, host_port: '',
          volumes: [], depends_on: [], extra_ports: [],
          restart: 'unless-stopped', extra_compose: '',
        }])}
        className="w-full py-2 border border-dashed border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500 rounded-xl text-sm transition-colors"
      >
        + Add service
      </button>
    </div>
  )
}

// ── Environment editor ────────────────────────────────────────────────────────

function EnvEditor({ envName, cfg, onChange, onRename, onRemove, isNew, projectType, workspaceName, isOnlyEnv, imageNames }) {
  const upd = (k, v) => onChange({ ...cfg, [k]: v })
  const updGit = (k, v) => onChange({ ...cfg, git: { ...(cfg.git || {}), [k]: v } })
  const updReplicas = (k, v) => onChange({ ...cfg, replicas: { ...(cfg.replicas || {}), [k]: parseInt(v) || 1 } })
  const updServiceOverride = (svcName, yaml) => onChange({
    ...cfg,
    service_overrides: { ...(cfg.service_overrides || {}), [svcName]: { extra_compose: yaml } },
  })

  return (
    <div className="bg-gray-800/50 border border-gray-700 rounded-xl p-5 space-y-4">
      {/* Env name + remove */}
      <div className="flex items-center justify-between">
        <div className="flex items-center gap-3 flex-1">
          <div className="w-2 h-2 rounded-full bg-gray-500 shrink-0" />
          {/* defaultValue (uncontrolled) — React never updates this input's DOM value
              while the user is typing, so focus is never lost. onBlur fires rename. */}
          <input
            key={envName}
            defaultValue={envName}
            onBlur={e => { if (e.target.value !== envName) onRename(e.target.value) }}
            placeholder="prod"
            className="bg-transparent text-white font-semibold text-base border-b border-transparent focus:border-brand-500 focus:outline-none px-0 py-0.5 w-32"
          />
          {isNew && <span className="text-xs text-brand-400 bg-brand-950 px-2 py-0.5 rounded-full">new</span>}
        </div>
        <button
          type="button"
          onClick={onRemove}
          disabled={isOnlyEnv}
          title={isOnlyEnv ? 'Cannot remove the only environment' : undefined}
          className={`text-xs transition-colors ${isOnlyEnv ? 'text-gray-600 cursor-not-allowed' : 'text-red-400 hover:text-red-300'}`}
        >Remove</button>
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div>
          <Label>Domain</Label>
          <Input value={cfg.domain} onChange={v => upd('domain', v)} placeholder="example.com" />
        </div>
        <div>
          <Label>Deployment</Label>
          <Select value={cfg.deployment} onChange={v => upd('deployment', v)} options={DEPLOYMENT_OPTIONS} />
        </div>
        {/* HTTP port — only relevant for custom stacks without Traefik (direct Nginx binding) */}
        {projectType !== 'image' && !cfg.traefik_enabled && (
          <div>
            <Label>HTTP port</Label>
            <Input type="number" value={cfg.http_port} onChange={v => upd('http_port', parseInt(v) || 80)} />
            <p className="text-xs text-gray-500 mt-1">Host port Nginx binds to — access at <code className="font-mono text-xs">host:{cfg.http_port || 80}</code></p>
          </div>
        )}
      </div>

      <div className="space-y-3 pt-3 border-t border-gray-700/50">
        <Toggle
          label="Traefik reverse proxy"
          hint="Route via Traefik instead of direct port binding"
          checked={!!cfg.traefik_enabled}
          onChange={v => {
            upd('traefik_enabled', v)
            if (!v) onChange({ ...cfg, traefik_enabled: false, ssl_enabled: false })
          }}
        />
        {cfg.traefik_enabled && (
          <>
            <div>
              <Label>Traefik network</Label>
              <Input value={cfg.traefik_network} onChange={v => upd('traefik_network', v)} placeholder="traefik_net" />
            </div>

            {/* SSL toggle — enabled only when domain is set */}
            <div className={`pl-3 border-l-2 ${cfg.ssl_enabled ? 'border-green-700' : 'border-gray-700'}`}>
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
                <p className="text-xs text-green-400/70 mt-1">
                  🔒 Run <code className="font-mono text-xs">./run.sh refresh {'{env}'}</code> after saving to regenerate the compose file with TLS labels.
                </p>
              )}
            </div>
          </>
        )}
      </div>

      {/* Custom-stack-only fields */}
      {projectType === 'custom' && (
        <div className="space-y-4 pt-3 border-t border-gray-700/50">
          <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider">Application stack</p>
          <div className="grid grid-cols-3 gap-3">
            <div>
              <Label>Backend</Label>
              <Select value={cfg.backend} onChange={v => upd('backend', v)} options={BACKEND_OPTIONS} />
            </div>
            <div>
              <Label>Frontend</Label>
              <Select value={cfg.frontend} onChange={v => upd('frontend', v)} options={FRONTEND_OPTIONS} />
            </div>
            <div>
              <Label>Database</Label>
              <Select value={cfg.database} onChange={v => upd('database', v)} options={DB_OPTIONS} />
            </div>
          </div>
          <div className="grid grid-cols-2 gap-3">
            <Toggle label="Redis" checked={!!cfg.redis_enabled} onChange={v => upd('redis_enabled', v)} />
            <Toggle label="Garage S3" checked={!!cfg.garage_enabled} onChange={v => upd('garage_enabled', v)} />
          </div>
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Backend replicas</Label>
              <Input type="number" value={cfg.replicas?.backend ?? 1} onChange={v => updReplicas('backend', v)} />
            </div>
            <div>
              <Label>Frontend replicas</Label>
              <Input type="number" value={cfg.replicas?.frontend ?? 1} onChange={v => updReplicas('frontend', v)} />
            </div>
          </div>
        </div>
      )}

      {/* Git */}
      <div className="space-y-3 pt-3 border-t border-gray-700/50">
        <Toggle
          label="Git sync"
          hint="Enable ./run.sh sync"
          checked={!!cfg.git?.enabled}
          onChange={v => updGit('enabled', v)}
        />
        {cfg.git?.enabled && (
          <div className="grid grid-cols-2 gap-3">
            <div>
              <Label>Repository</Label>
              <Input value={cfg.git?.repo} onChange={v => updGit('repo', v)} placeholder="git@github.com:org/repo.git" />
            </div>
            <div>
              <Label>Branch</Label>
              <Input value={cfg.git?.branch} onChange={v => updGit('branch', v)} placeholder="main" />
            </div>
          </div>
        )}
      </div>

      {/* Environment variables */}
      {isNew
        ? <NewEnvVarsEditor cfg={cfg} onChange={onChange} />
        : <EnvVarsInline workspaceName={workspaceName} envName={envName} />
      }

      {/* Service overrides — image stacks only */}
      {projectType === 'image' && imageNames && imageNames.length > 0 && (
        <ServiceOverridesEditor
          imageNames={imageNames}
          overrides={cfg.service_overrides || {}}
          onChange={updServiceOverride}
        />
      )}
    </div>
  )
}

// ── Service overrides editor ───────────────────────────────────────────────────

function ServiceOverridesEditor({ imageNames, overrides, onChange }) {
  const [open, setOpen] = useState(false)
  const hasAny = imageNames.some(n => overrides[n]?.extra_compose?.trim())

  return (
    <div className="pt-3 border-t border-gray-700/50">
      <button
        type="button"
        onClick={() => setOpen(o => !o)}
        className="flex items-center justify-between w-full text-left group"
      >
        <div className="flex items-center gap-2">
          <span className="text-xs font-semibold text-gray-400 uppercase tracking-wider">Service overrides</span>
          {hasAny && (
            <span className="text-xs px-1.5 py-0.5 rounded bg-brand-900 text-brand-400 border border-brand-700">active</span>
          )}
        </div>
        <span className="text-gray-600 group-hover:text-gray-400 text-xs transition-colors">{open ? '▲' : '▼'}</span>
      </button>

      {open && (
        <div className="mt-3 space-y-4">
          <p className="text-xs text-gray-500">
            Env-specific YAML appended to each service after the base config. Use for resource limits, logging drivers, replica counts, etc.
            Keys defined here override the service-level Advanced YAML for this environment only.
          </p>
          {imageNames.map(svcName => {
            const yaml = overrides[svcName]?.extra_compose || ''
            return (
              <div key={svcName}>
                <label className="block text-xs font-medium text-gray-400 mb-1.5">
                  <span className="font-mono text-brand-400">{svcName}</span>
                  <span className="text-gray-600 ml-1">— env override</span>
                </label>
                <textarea
                  value={yaml}
                  onChange={e => onChange(svcName, e.target.value)}
                  rows={yaml.trim().split('\n').length + 2}
                  placeholder={`mem_limit: 2g\ncpus: "1.5"\nlogging:\n  driver: "none"`}
                  spellCheck={false}
                  className="w-full px-3 py-2 bg-gray-950 border border-gray-700 rounded-lg text-gray-200 text-xs font-mono placeholder-gray-700 focus:outline-none focus:border-brand-500 resize-y leading-relaxed"
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
function NewEnvVarsEditor({ cfg, onChange }) {
  const vars = cfg._initial_vars || {}
  const [open, setOpen]     = useState(false)
  const [reveal, setReveal] = useState(false)
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')

  function setVar(k, v) { onChange({ ...cfg, _initial_vars: { ...vars, [k]: v } }) }
  function removeVar(k) { const n = { ...vars }; delete n[k]; onChange({ ...cfg, _initial_vars: n }) }
  function addVar() {
    const k = newKey.trim(); if (!k) return
    setVar(k, newVal); setNewKey(''); setNewVal('')
  }

  const entries = Object.entries(vars)

  return (
    <div className="pt-3 border-t border-gray-700/50">
      <button type="button" onClick={() => setOpen(o => !o)}
        className="flex items-center gap-2 text-xs font-semibold text-gray-400 uppercase tracking-wider hover:text-gray-200 transition-colors w-full">
        <span className={`transition-transform ${open ? 'rotate-90' : ''}`}>▶</span>
        Environment Variables
        {entries.length > 0 && <span className="ml-1 text-brand-400 normal-case font-normal">{entries.length} inherited</span>}
        <span className="ml-auto text-gray-600 normal-case font-normal">.env file</span>
      </button>
      {open && (
        <div className="mt-3 space-y-3">
          <div className="flex items-center justify-between">
            <p className="text-xs text-amber-400/80 flex items-center gap-1">
              <span>⚠</span> These will be written to <code className="font-mono">.env</code> on save.
            </p>
            <label className="flex items-center gap-1.5 cursor-pointer shrink-0">
              <input type="checkbox" checked={reveal} onChange={e => setReveal(e.target.checked)}
                className="w-3 h-3 accent-brand-500" />
              <span className="text-xs text-gray-400 select-none">Show values</span>
            </label>
          </div>
          <div className="space-y-1.5">
            {entries.map(([k, v]) => (
              <div key={k} className="flex items-center gap-2">
                <span className="font-mono text-xs text-gray-300 w-44 shrink-0 truncate">{k}</span>
                <input type={reveal ? 'text' : 'password'} value={v}
                  onChange={e => setVar(k, e.target.value)}
                  className="flex-1 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm font-mono text-white focus:outline-none focus:border-brand-500" />
                <button type="button" onClick={() => removeVar(k)}
                  className="text-gray-500 hover:text-red-400 transition-colors shrink-0 p-0.5 rounded hover:bg-red-950/30"><TrashIcon /></button>
              </div>
            ))}
          </div>
          <div className="flex gap-2 pt-1">
            <input type="text" placeholder="KEY" value={newKey} onChange={e => setNewKey(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && addVar()}
              className="w-44 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm font-mono text-white focus:outline-none focus:border-brand-500" />
            <input type="text" placeholder="value" value={newVal} onChange={e => setNewVal(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && addVar()}
              className="flex-1 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm font-mono text-white focus:outline-none focus:border-brand-500" />
            <button type="button" onClick={addVar}
              className="text-xs text-brand-400 hover:text-brand-300 shrink-0 px-2">Add</button>
          </div>
        </div>
      )}
    </div>
  )
}

// ── Inline env vars editor (used inside EnvEditor) ────────────────────────────

function EnvVarsInline({ workspaceName, envName }) {
  const [open, setOpen]       = useState(false)
  const [reveal, setReveal]   = useState(false)
  const [edits, setEdits]     = useState({})
  const [deletes, setDeletes] = useState(new Set())
  const [newKey, setNewKey]   = useState('')
  const [newVal, setNewVal]   = useState('')
  const qc = useQueryClient()

  const { data: vars, isLoading } = useQuery({
    queryKey: ['envvars', workspaceName, envName, reveal],
    queryFn:  () => fetchEnvVars(workspaceName, envName, reveal),
    enabled:  open,
  })

  const saveMut = useMutation({
    mutationFn: ({ updates, dels }) => updateEnvVars(workspaceName, envName, updates, dels),
    onSuccess: () => {
      setEdits({}); setDeletes(new Set()); setNewKey(''); setNewVal('')
      qc.invalidateQueries({ queryKey: ['envvars', workspaceName, envName] })
    },
  })

  function toggleDelete(k) {
    setDeletes(prev => { const n = new Set(prev); n.has(k) ? n.delete(k) : n.add(k); return n })
    setEdits(prev => { const n = { ...prev }; delete n[k]; return n })
  }

  function handleSave() {
    const updates = { ...edits }
    if (newKey.trim()) updates[newKey.trim()] = newVal
    saveMut.mutate({ updates, dels: [...deletes] })
  }

  return (
    <div className="pt-3 border-t border-gray-700/50">
      <button
        type="button"
        onClick={() => setOpen(o => !o)}
        className="flex items-center gap-2 text-xs font-semibold text-gray-400 uppercase tracking-wider hover:text-gray-200 transition-colors w-full"
      >
        <span className={`transition-transform ${open ? 'rotate-90' : ''}`}>▶</span>
        Environment Variables
        <span className="ml-auto text-gray-600 normal-case font-normal">.env file</span>
      </button>

      {open && (
        <div className="mt-3 space-y-3">
          {/* Reveal + hint */}
          <div className="flex items-center justify-between">
            <p className="text-xs text-amber-400/80 flex items-center gap-1">
              <span>⚠</span> Use <strong>Deploy ▾ → Refresh</strong> after saving to apply.
            </p>
            <label className="flex items-center gap-1.5 cursor-pointer shrink-0">
              <input type="checkbox" checked={reveal}
                onChange={e => { setReveal(e.target.checked); setEdits({}) }}
                className="w-3 h-3 accent-brand-500" />
              <span className="text-xs text-gray-400 select-none">Show values</span>
            </label>
          </div>

          {/* Existing vars */}
          {isLoading
            ? <p className="text-xs text-gray-500">Loading…</p>
            : (
              <div className="space-y-1.5 max-h-48 overflow-y-auto pr-1">
                {Object.entries(vars || {}).map(([k, v]) => {
                  const marked = deletes.has(k)
                  return (
                    <div key={k} className={`flex items-center gap-2 ${marked ? 'opacity-40' : ''}`}>
                      <span className="font-mono text-xs text-gray-400 w-36 shrink-0 truncate" title={k}>{k}</span>
                      <input
                        type={reveal ? 'text' : 'password'}
                        placeholder={reveal ? v : '••••••••'}
                        value={marked ? '' : (edits[k] ?? (reveal ? v : ''))}
                        disabled={marked}
                        onChange={e => setEdits(p => ({ ...p, [k]: e.target.value }))}
                        className="flex-1 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-xs text-white font-mono focus:outline-none focus:border-brand-500 disabled:opacity-40"
                      />
                      <button type="button" onClick={() => toggleDelete(k)}
                        className={`shrink-0 w-5 h-5 flex items-center justify-center rounded text-xs transition-colors ${
                          marked ? 'bg-red-800 text-red-200 hover:bg-red-700' : 'text-gray-600 hover:text-red-400 hover:bg-gray-700'
                        }`}>
                        {marked ? '↩' : '×'}
                      </button>
                    </div>
                  )
                })}
              </div>
            )
          }

          {/* Add new variable */}
          <div className="flex gap-2 pt-2 border-t border-gray-700/40">
            <input type="text" placeholder="NEW_KEY" value={newKey}
              onChange={e => setNewKey(e.target.value)}
              className="w-36 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-xs text-white font-mono focus:outline-none focus:border-brand-500" />
            <input type={reveal ? 'text' : 'password'} placeholder="value" value={newVal}
              onChange={e => setNewVal(e.target.value)}
              className="flex-1 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-xs text-white font-mono focus:outline-none focus:border-brand-500" />
            <button type="button" onClick={() => newKey.trim() && handleSave()}
              disabled={!newKey.trim() || saveMut.isPending}
              className="px-2.5 py-1 bg-gray-700 hover:bg-gray-600 disabled:opacity-40 text-white text-xs rounded transition-colors shrink-0">
              Add
            </button>
          </div>

          {/* Save */}
          <div className="flex items-center gap-3">
            <button type="button" onClick={handleSave} disabled={saveMut.isPending}
              className="px-3 py-1.5 bg-brand-600 hover:bg-brand-700 disabled:opacity-50 text-white text-xs font-medium rounded-lg transition-colors">
              {saveMut.isPending ? 'Saving…' : 'Save changes'}
            </button>
            {saveMut.isSuccess && <span className="text-green-400 text-xs">Saved ✓</span>}
            {saveMut.isError   && <span className="text-red-400 text-xs">Failed</span>}
          </div>
        </div>
      )}
    </div>
  )
}

// ── Main page ─────────────────────────────────────────────────────────────────

// serializeConfig builds the same config object that Save writes, stringified —
// used to detect unsaved changes by comparing against the loaded baseline.
function serializeConfig(project, envs, images, rawConfig) {
  const cleanEnvs = {}
  for (const [k, v] of Object.entries(envs || {})) {
    const { _initial_vars, _id, ...rest } = v // eslint-disable-line no-unused-vars
    cleanEnvs[k] = rest
  }
  const updated = { ...rawConfig, project, environments: cleanEnvs, ...(project?.type === 'image' && { images }) }
  return JSON.stringify(updated)
}

export default function EditWorkspacePage() {
  const { name } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()

  const { data: rawConfig, isLoading, error } = useQuery({
    queryKey: ['config', name],
    queryFn: () => fetchConfig(name),
  })

  // Local editable state
  const [envs, setEnvs]       = useState(null)
  const [project, setProject] = useState(null)
  const [images, setImages]   = useState(null)
  const [newEnvCounter, setNewEnvCounter] = useState(0)
  const [saveError, setSaveError] = useState('')
  const [firstEnvVars, setFirstEnvVars] = useState({})
  const [baseline, setBaseline] = useState(null)        // serialized config at load
  const [confirmCancel, setConfirmCancel] = useState(false)

  useEffect(() => {
    if (rawConfig && envs === null) {
      // Inject a stable _id into every env so EnvEditor keys never change on rename
      const withIds = {}
      Object.entries(rawConfig.environments || {}).forEach(([k, v], i) => {
        withIds[k] = { ...v, _id: `env-orig-${i}` }
      })
      setEnvs(withIds)
      setProject(rawConfig.project || {})
      setImages(rawConfig.images || [])
      setBaseline(serializeConfig(rawConfig.project || {}, withIds, rawConfig.images || [], rawConfig))
      // Pre-load vars from first env for use when adding new environments
      const firstEnvName = Object.keys(rawConfig.environments || {})[0]
      if (firstEnvName) {
        fetchEnvVars(name, firstEnvName, true) // reveal=true so values are editable in new env
          .then(vars => setFirstEnvVars(vars || {}))
          .catch(() => {})
      }
    }
  }, [rawConfig])

  const mutation = useMutation({
    mutationFn: async () => {
      // Strip _initial_vars from the config before saving — it's a UI-only field
      const cleanEnvs = {}
      for (const [k, v] of Object.entries(envs || {})) {
        // eslint-disable-next-line no-unused-vars
        const { _initial_vars: _iv, _id: _id2, ...rest } = v
        cleanEnvs[k] = rest
      }
      const updated = {
        ...rawConfig,
        project,
        environments: cleanEnvs,
        ...(project?.type === 'image' && { images }),
      }
      await putConfig(name, JSON.stringify(updated, null, 2))

      // Write initial env vars for new environments.
      // UpdateEnvVars now creates the .env file if it doesn't exist.
      const newEnvNames = Object.keys(envs || {}).filter(e => !originalEnvNames.includes(e))
      for (const envName of newEnvNames) {
        const initialVars = envs[envName]?._initial_vars || {}
        if (Object.keys(initialVars).length > 0) {
          try { await updateEnvVars(name, envName, initialVars) } catch { /* non-fatal */ }
        }
      }
    },
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspace', name] })
      qc.invalidateQueries({ queryKey: ['config', name] })
      navigate(`/workspaces/${name}`)
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
    const isImage = project?.type === 'image'
    const firstEnv = Object.values(envs || {})[0] || {}
    const base = {
      domain: '',
      http_port: 8080,
      deployment: 'compose',
      traefik_enabled: false,
      traefik_network: 'traefik_net',
      ssl_enabled: false,
      git: { enabled: false, repo: '', branch: '' },
    }
    if (!isImage) {
      Object.assign(base, {
        backend: firstEnv.backend || 'laravel',
        frontend: firstEnv.frontend || 'none',
        database: firstEnv.database || 'none',
        redis_enabled: false,
        garage_enabled: false,
        replicas: { backend: 1, frontend: 1 },
      })
    }
    setEnvs(prev => ({ ...prev, [n]: { ...base, _id: `env-new-${newEnvCounter + 1}`, _initial_vars: { ...firstEnvVars } } }))
  }

  const originalEnvNames = rawConfig ? Object.keys(rawConfig.environments || {}) : []
  const currentEnvNames = envs ? Object.keys(envs) : []

  // Unsaved-changes detection: compare the current editable config to the load
  // baseline. Save is enabled only when something changed; Cancel confirms first.
  const dirty = baseline !== null && envs !== null && project !== null &&
    serializeConfig(project, envs, images, rawConfig) !== baseline

  function leave() { navigate(`/workspaces/${name}`) }
  function handleCancel() { if (dirty) setConfirmCancel(true); else leave() }

  if (isLoading) return <Layout><div className="p-8 text-gray-500 text-sm">Loading…</div></Layout>
  if (error)     return <Layout><div className="p-8 text-red-400 text-sm">{error.message}</div></Layout>

  return (
    <Layout>
      <div className="p-6 max-w-3xl">
        {/* Header */}
        <div className="flex items-center justify-between mb-6">
          <div>
            <h1 className="text-xl font-bold text-white">Edit workspace</h1>
            <p className="text-sm text-gray-400 mt-0.5">{name}</p>
          </div>
          <div className="flex items-center gap-3">
            <button
              onClick={handleCancel}
              className="text-sm font-medium px-4 py-2 rounded-lg border border-amber-700/60 bg-amber-900/30 hover:bg-amber-800/50 text-amber-300 transition-colors"
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
          <div className="mb-5 px-4 py-3 bg-red-950 border border-red-800 text-red-300 rounded-lg text-sm">{saveError}</div>
        )}

        {/* Project settings */}
        <section className="mb-6">
          <h2 className="text-sm font-semibold text-gray-300 mb-3">Project</h2>
          <div className="bg-gray-900 border border-gray-800 rounded-xl p-5 grid grid-cols-2 gap-4">
            <div>
              <Label>Project name</Label>
              <Input value={project?.name} onChange={v => setProject(p => ({ ...p, name: v }))} />
            </div>
            <div>
              <Label>Registry</Label>
              <Input value={project?.registry} onChange={v => setProject(p => ({ ...p, registry: v }))} />
            </div>
          </div>
        </section>

        {/* Images (image stacks only) */}
        {project?.type === 'image' && (
          <section className="mb-6">
            <h2 className="text-sm font-semibold text-gray-300 mb-3">Services</h2>
            <ImagesEditor images={images || []} onChange={setImages} />
            <p className="text-xs text-gray-500 mt-2">After saving, redeploy each environment to pick up image changes.</p>
          </section>
        )}

        {/* Environments */}
        <section className="mb-6">
          <div className="flex items-center justify-between mb-3">
            <h2 className="text-sm font-semibold text-gray-300">Environments</h2>
            <p className="text-xs text-gray-500">
              {currentEnvNames.length} environment{currentEnvNames.length !== 1 ? 's' : ''}
              {currentEnvNames.some(e => !originalEnvNames.includes(e)) && (
                <span className="ml-2 text-brand-400">· new environments will need bootstrapping after save</span>
              )}
            </p>
          </div>

          <div className="space-y-4">
            {Object.entries(envs || {}).map(([envName, cfg]) => (
              <EnvEditor
                key={cfg._id || envName}
                envName={envName}
                cfg={cfg}
                onChange={(updated) => updateEnv(envName, updated)}
                onRename={(newName) => renameEnv(envName, newName)}
                onRemove={() => removeEnv(envName)}
                isNew={!originalEnvNames.includes(envName)}
                isOnlyEnv={Object.keys(envs || {}).length === 1}
                projectType={project?.type || 'custom'}
                workspaceName={name}
                imageNames={(images || []).map(img => img.name).filter(Boolean)}
              />
            ))}
          </div>

          <button
            type="button" onClick={addEnv}
            className="mt-4 w-full py-2.5 border border-dashed border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500 rounded-xl text-sm transition-colors"
          >
            + Add environment
          </button>
        </section>

        {/* After-save hint for new envs */}
        {currentEnvNames.some(e => !originalEnvNames.includes(e)) && (
          <div className="bg-amber-950/40 border border-amber-800/50 rounded-xl px-4 py-3 text-sm text-amber-300">
            After saving, go to the workspace and click <strong>Init</strong> for each new environment to generate its compose file and .env.
          </div>
        )}

        {/* Per-environment hosts (Phase 7) */}
        <EnvHostsSection name={name} />

        {/* Move the whole workspace to another host (Phase 7) */}
        <MigrateSection name={name} />

        {/* Danger zone */}
        <DangerZone name={name} />
      </div>

      {/* Discard-changes confirmation */}
      {confirmCancel && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm" onClick={() => setConfirmCancel(false)}>
          <div className="bg-gray-900 border border-amber-900/60 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <div className="flex items-center gap-3">
              <span className="text-2xl">⚠️</span>
              <h3 className="font-semibold text-white">Discard unsaved changes?</h3>
            </div>
            <p className="text-sm text-gray-400">You have unsaved changes to <strong className="text-gray-300">{name}</strong>. Leaving now will discard them.</p>
            <div className="flex gap-3">
              <button onClick={() => { setConfirmCancel(false); leave() }} className="flex-1 bg-amber-700 hover:bg-amber-600 text-white text-sm font-semibold py-2 rounded-lg transition-colors">Discard &amp; leave</button>
              <button onClick={() => setConfirmCancel(false)} className="px-4 py-2 bg-gray-800 hover:bg-gray-700 text-gray-300 text-sm rounded-lg transition-colors">Keep editing</button>
            </div>
          </div>
        </div>
      )}
    </Layout>
  )
}

// MigrateWarning is the confirmation shown before any host move. It spells out the
// downtime and the data/secrets left behind on the source host.
function MigrateWarning({ what, from, to, onConfirm, onCancel }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm" onClick={onCancel}>
      <div className="bg-gray-900 border border-amber-900/60 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center gap-3">
          <span className="text-2xl">⚠️</span>
          <h3 className="font-semibold text-white">Move {what}?</h3>
        </div>
        <p className="text-sm text-gray-300">
          Moving <strong className="text-white">{what}</strong> from <strong className="text-white">{from}</strong> to <strong className="text-white">{to}</strong>.
        </p>
        <ul className="text-xs text-gray-400 space-y-2 list-disc pl-5">
          <li>
            <strong className="text-amber-300">Downtime:</strong> if it's currently running, it goes down the
            moment the source stops and stays down until it's back up and restored on the target. This runs in
            the background — you'll get a notification when it's done, so you can leave this page.
          </li>
          <li>
            <strong className="text-amber-300">Data left on the source:</strong> {from} keeps the stopped
            containers, volumes (your data) and files (including <code className="font-mono">.env</code> secrets) —
            they are <strong>not</strong> deleted. If you plan to decommission {from}, wipe them afterward in{' '}
            <a href="/housekeeping" className="text-brand-400 underline">Housekeeping → Migration leftovers</a>.
          </li>
        </ul>
        <div className="flex gap-3 pt-1">
          <button onClick={onConfirm} className="flex-1 bg-amber-700 hover:bg-amber-600 text-white text-sm font-semibold py-2 rounded-lg transition-colors">Move</button>
          <button onClick={onCancel} className="px-4 py-2 bg-gray-800 hover:bg-gray-700 text-gray-300 text-sm rounded-lg transition-colors">Cancel</button>
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
  const cls = job.status === 'running' ? 'text-blue-400' : job.status === 'completed' ? 'text-green-400' : 'text-red-400'
  return (
    <div className="space-y-2">
      <p className={`text-sm ${cls}`}>{banner}</p>
      {job.log && (
        <pre className="max-h-72 overflow-auto bg-gray-950 border border-gray-800 rounded-lg p-3 text-xs text-gray-300 whitespace-pre-wrap">{job.log}</pre>
      )}
    </div>
  )
}

// EnvHostsSection shows each environment's host and lets you change it. Changing
// a deployed env's host migrates its data; an undeployed env just repoints.
function EnvHostsSection({ name }) {
  const qc = useQueryClient()
  const { data: hosts = [] } = useQuery({ queryKey: ['hosts'], queryFn: fetchHosts })
  const { data: ws } = useQuery({ queryKey: ['workspace', name], queryFn: () => fetchWorkspace(name) })

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
      const job = await setEnvHost(name, env, targetId)
      setJobId(job.id)
    } catch (e) {
      setErr(e.response?.data?.error || e.message || 'failed to start')
      setRunning(false)
    }
  }

  function onJobDone() {
    setRunning(false)
    qc.invalidateQueries({ queryKey: ['workspace', name] })
    qc.invalidateQueries({ queryKey: ['workspaces'] })
  }

  if (envs.length === 0) return null

  return (
    <section className="mt-8">
      <div className="border border-gray-800 rounded-xl overflow-hidden">
        <div className="px-5 py-3 bg-gray-900/60 border-b border-gray-800">
          <h2 className="text-sm font-semibold text-gray-200">Environment hosts</h2>
          <p className="text-xs text-gray-500 mt-0.5">
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
                  <p className="text-sm text-gray-200">{env}</p>
                  <p className="text-xs text-gray-500">on {hostName(curId)}</p>
                </div>
                <select
                  value={target[env] ?? ''}
                  onChange={e => setTarget(t => ({ ...t, [env]: e.target.value }))}
                  disabled={running}
                  className="flex-1 px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-blue-500"
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
          {err && <p className="text-sm text-red-400">✗ {err}</p>}
          <MigrationProgress jobId={jobId} onDone={onJobDone} />
        </div>
      </div>

      {pending && (
        <MigrateWarning
          what={`${name} / ${pending.env}`}
          from={hostName(envHosts[pending.env]?.host_id || 0)}
          to={hostName(pending.targetId)}
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
  const qc = useQueryClient()
  const { data: hosts = [] } = useQuery({ queryKey: ['hosts'], queryFn: fetchHosts })
  const { data: ws } = useQuery({ queryKey: ['workspace', name], queryFn: () => fetchWorkspace(name) })

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
      const job = await migrateWorkspace(name, Number(target))
      setJobId(job.id)
    } catch (e) {
      setErr(e.response?.data?.error || e.message || 'failed to start')
      setRunning(false)
    }
  }

  function onJobDone() {
    setRunning(false)
    qc.invalidateQueries({ queryKey: ['workspaces'] })
    qc.invalidateQueries({ queryKey: ['workspace', name] })
  }

  const currentLabel = currentHostId === 0
    ? 'local control plane'
    : (hosts.find(h => h.id === currentHostId)?.name || `host #${currentHostId}`)

  return (
    <section className="mt-8">
      <div className="border border-gray-800 rounded-xl overflow-hidden">
        <div className="px-5 py-3 bg-gray-900/60 border-b border-gray-800">
          <h2 className="text-sm font-semibold text-gray-200">Move the whole workspace</h2>
          <p className="text-xs text-gray-500 mt-0.5">
            {mixed
              ? 'Environments are on different hosts — move them individually above.'
              : <>Currently on <strong className="text-gray-400">{currentLabel}</strong>. Moves every environment together (back up → ship → restore). Source data is left intact.</>}
          </p>
        </div>
        {!mixed && (
          <div className="px-5 py-4 space-y-3">
            <div className="flex items-center gap-3">
              <select
                value={target}
                onChange={e => setTarget(e.target.value)}
                disabled={running}
                className="flex-1 px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-blue-500"
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
            {err && <p className="text-sm text-red-400">✗ {err}</p>}
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

function DangerZone({ name }) {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [open, setOpen]       = useState(false)
  const [confirm, setConfirm] = useState('')
  const [error, setError]     = useState('')

  const mutation = useMutation({
    mutationFn: () => deleteWorkspace(name),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['workspaces'] })
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
      <div className="border border-red-900/50 rounded-xl overflow-hidden">
        <div className="px-5 py-3 bg-red-950/30 border-b border-red-900/50 flex items-center justify-between">
          <div>
            <h2 className="text-sm font-semibold text-red-400">Danger zone</h2>
            <p className="text-xs text-red-400/70 mt-0.5">Irreversible actions — proceed with caution</p>
          </div>
        </div>
        <div className="px-5 py-4 flex items-center justify-between">
          <div>
            <p className="text-sm text-gray-200">Delete this workspace</p>
            <p className="text-xs text-gray-500 mt-0.5">Permanently removes all files, configs, and backups for <strong className="text-gray-400">{name}</strong>. Running containers are not stopped automatically.</p>
          </div>
          <button
            onClick={() => { setOpen(true); setConfirm(''); setError('') }}
            className="ml-6 shrink-0 px-4 py-2 bg-red-900/60 hover:bg-red-800/80 text-red-300 hover:text-red-200 text-sm font-medium rounded-lg border border-red-800/50 transition-colors"
          >
            Delete workspace
          </button>
        </div>
      </div>

      {/* Confirmation modal */}
      {open && (
        <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm" onClick={() => setOpen(false)}>
          <div className="bg-gray-900 border border-red-900/60 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
            <div className="flex items-center gap-3">
              <span className="text-2xl">⚠️</span>
              <h3 className="font-semibold text-white">Delete <span className="text-red-400">{name}</span>?</h3>
            </div>
            <p className="text-sm text-gray-400">
              This will permanently delete the workspace directory and all its contents including configs, environment files, and backups.
              <strong className="text-gray-300 block mt-1">This cannot be undone.</strong>
            </p>
            {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}
            <div>
              <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1.5">
                Type <span className="text-red-400 font-mono">{name}</span> to confirm
              </label>
              <input
                type="text"
                value={confirm}
                onChange={e => { setConfirm(e.target.value); setError('') }}
                onKeyDown={e => e.key === 'Enter' && handleDelete()}
                placeholder={name}
                autoFocus
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm font-mono focus:outline-none focus:border-red-500 transition-colors"
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
                className="px-4 py-2 bg-gray-800 hover:bg-gray-700 text-gray-300 text-sm rounded-lg transition-colors"
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
