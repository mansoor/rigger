import { useState, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { fetchTemplates, fetchTemplate, recordTemplateUse, openCreateSocket, fetchRegistries, fetchBackupTargets, fetchWorkspaces, fetchHosts } from '../lib/api'
import TrashIcon from '../components/TrashIcon'
import PortWarnings from '../components/PortWarnings'
import { portConflicts, hostPortsFromMappings } from '../lib/ports'
import { usePortConflicts } from '../hooks/usePortConflicts'

// ── Shared UI primitives ──────────────────────────────────────────────────────

function Label({ children, required }) {
  return (
    <label className="block text-sm font-medium text-gray-300 mb-1">
      {children}{required && <span className="text-red-400 ml-0.5">*</span>}
    </label>
  )
}

function Input({ value, onChange, placeholder, type = 'text', error, ...rest }) {
  return (
    <>
      <input
        type={type} value={value} onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        className={`w-full px-3 py-2 bg-gray-800 border rounded-lg text-white placeholder-gray-500 text-sm focus:outline-none transition-colors ${
          error ? 'border-red-500 focus:border-red-400' : 'border-gray-700 focus:border-brand-500'
        }`}
        {...rest}
      />
      {error && <p className="text-red-400 text-xs mt-1">{error}</p>}
    </>
  )
}

function Select({ value, onChange, options }) {
  return (
    <select
      value={value} onChange={e => onChange(e.target.value)}
      className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500"
    >
      {options.map(o => (
        <option key={o.value} value={o.value}>{o.label}</option>
      ))}
    </select>
  )
}

function Toggle({ label, checked, onChange, hint }) {
  return (
    <div className="flex items-center justify-between">
      <div>
        <p className="text-sm text-gray-200">{label}</p>
        {hint && <p className="text-xs text-gray-500">{hint}</p>}
      </div>
      <button
        type="button" onClick={() => onChange(!checked)}
        className={`relative w-10 h-5 rounded-full transition-colors ${checked ? 'bg-brand-600' : 'bg-gray-700'}`}
      >
        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-5' : ''}`} />
      </button>
    </div>
  )
}

function StepHeader({ step, title, subtitle }) {
  return (
    <div className="mb-6">
      <p className="text-xs font-semibold text-brand-400 uppercase tracking-wider mb-1">Step {step}</p>
      <h2 className="text-xl font-semibold text-white">{title}</h2>
      {subtitle && <p className="text-sm text-gray-400 mt-0.5">{subtitle}</p>}
    </div>
  )
}

// ── Step 1: Project ───────────────────────────────────────────────────────────

const CUSTOM_REGISTRY = '__custom__'

function Step1({ data, onChange, errors, onConflict }) {
  // Registered remote hosts (Phase 7) — for the default-host selector.
  const { data: hosts = [] } = useQuery({ queryKey: ['hosts'], queryFn: fetchHosts })

  // Uniqueness check — fetch existing workspace names once and compare
  const { data: existingWorkspaces = [] } = useQuery({
    queryKey: ['workspaces'],
    queryFn: fetchWorkspaces,
    staleTime: 30_000,
  })
  const existingNames = existingWorkspaces.map(w => w.name)
  const nameConflict = data.name.trim() && existingNames.includes(data.name.trim())
    ? `A workspace named "${data.name.trim()}" already exists`
    : null
  // Propagate conflict to parent so validate() can block Continue
  useEffect(() => { onConflict(nameConflict) }, [nameConflict]) // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <div className="space-y-5">
      <StepHeader step={1} title="Project" subtitle="Name your project and pick a default host." />

      <div>
        <Label required>Project name</Label>
        <Input
          value={data.name} onChange={v => onChange('name', v)}
          placeholder="my-app" error={errors.name || nameConflict}
        />
        {!errors.name && !nameConflict && (
          <p className="text-xs text-gray-500 mt-1">Lowercase letters, numbers, hyphens. Becomes the Docker resource prefix.</p>
        )}
      </div>

      {/* Default host (Phase 7) — pre-fills each environment's host; overridable per env */}
      <div>
        <Label>Default host</Label>
        <select
          value={String(data.default_host_id || 0)}
          onChange={e => onChange('default_host_id', Number(e.target.value))}
          className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500 transition-colors"
        >
          <option value="0">Local control plane</option>
          {hosts.map(h => <option key={h.id} value={String(h.id)}>{h.name} — {h.address}</option>)}
        </select>
        <p className="text-xs text-gray-500 mt-1">
          Where environments run by default — override per environment on the next steps. Files are pushed
          and the stack starts on the host the first time you deploy that environment.
        </p>
      </div>
    </div>
  )
}

// ── Step 2: Stack ─────────────────────────────────────────────────────────────

const STACK_TYPES = [
  { id: 'prebuilt', label: 'Pre-built template',  desc: 'Pick from curated stacks — NPM, WordPress, Vaultwarden, Uptime Kuma…' },
  { id: 'image',    label: 'Image stack',          desc: 'Deploy any Docker images — specify your own image names, tags, and ports.' },
  { id: 'custom',   label: 'Custom application',  desc: 'Your own code — Laravel, Node.js, Next.js, React with a database.' },
]

const BACKEND_OPTIONS  = [{ value: 'laravel', label: 'Laravel (PHP-FPM)' }, { value: 'nodejs', label: 'Node.js (Express / Fastify)' }]
const FRONTEND_OPTIONS = [{ value: 'none', label: 'None (API only)' }, { value: 'nextjs', label: 'Next.js' }, { value: 'react', label: 'React / Vite SPA' }]
const DB_OPTIONS       = [{ value: 'none', label: 'None' }, { value: 'postgres', label: 'PostgreSQL' }, { value: 'mysql', label: 'MySQL' }]

function TemplateCard({ tmpl, selected, onClick }) {
  const tagColors = ['bg-blue-950 text-blue-300', 'bg-purple-950 text-purple-300', 'bg-green-950 text-green-300']
  return (
    <button
      type="button" onClick={onClick}
      className={`text-left w-full p-4 rounded-xl border transition-all ${
        selected
          ? 'border-brand-500 bg-brand-950/30'
          : 'border-gray-700 bg-gray-800/40 hover:border-gray-500'
      }`}
    >
      <div className="flex items-start justify-between gap-2 mb-1">
        <p className="font-medium text-white text-sm">{tmpl.label}</p>
        <span className="text-xs text-gray-500 shrink-0">{tmpl.image_count} container{tmpl.image_count !== 1 ? 's' : ''}</span>
      </div>
      <p className="text-xs text-gray-400 mb-2">{tmpl.description}</p>
      <div className="flex flex-wrap gap-1">
        {(tmpl.tags || []).slice(0, 3).map((tag, i) => (
          <span key={tag} className={`text-xs px-1.5 py-0.5 rounded ${tagColors[i % tagColors.length]}`}>{tag}</span>
        ))}
      </div>
    </button>
  )
}

const DEFAULT_IMAGE = {
  name: '', image: '', tag: 'latest',
  portMappings: [{ host: '', container: '' }],
  volumes: [],
  healthcheck: '',
  healthcheck_config: { interval: '30', timeout: '10', retries: '3', start_period: '30' },
}

function ImageEditor({ images, onChange }) {
  function update(idx, field, val) {
    const next = images.map((img, i) => i === idx ? { ...img, [field]: val } : img)
    onChange(next)
  }
  function add() { onChange([...images, { ...DEFAULT_IMAGE }]) }
  function remove(idx) { onChange(images.filter((_, i) => i !== idx)) }

  return (
    <div className="space-y-3">
      {images.map((img, i) => (
        <div key={i} className="bg-gray-800/50 border border-gray-700 rounded-xl p-4 space-y-3">
          <div className="flex items-center justify-between">
            <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">Service {i + 1}</p>
            {images.length > 1 && (
              <button type="button" onClick={() => remove(i)} className="text-xs text-red-400 hover:text-red-300">Remove</button>
            )}
          </div>
          <div className="grid grid-cols-3 gap-3">
            <div>
              <Label required>Service name</Label>
              <Input value={img.name} onChange={v => update(i, 'name', v)} placeholder="app" />
            </div>
            <div>
              <Label required>Image</Label>
              <Input value={img.image} onChange={v => update(i, 'image', v)} placeholder="nginx" />
            </div>
            <div>
              <Label>Tag</Label>
              <Input value={img.tag} onChange={v => update(i, 'tag', v)} placeholder="latest" />
            </div>
          </div>
          <p className="text-xs text-gray-600">Port mappings, volumes and healthcheck configured in Step 4.</p>
        </div>
      ))}
      <button
        type="button" onClick={add}
        className="w-full py-2 border border-dashed border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500 rounded-xl text-sm transition-colors"
      >
        + Add service
      </button>
    </div>
  )
}

function EnvVarEditor({ envVars, secretKeys = [], onChange, onSecretKeysChange, deployment }) {
  const [newKey, setNewKey] = useState('')
  const [newVal, setNewVal] = useState('')
  const [newSecret, setNewSecret] = useState(false)
  const entries = Object.entries(envVars)
  const secretSet = new Set(secretKeys)
  const swarm = deployment === 'swarm'

  function update(k, v) { onChange({ ...envVars, [k]: v }) }
  function remove(k) {
    const next = { ...envVars }; delete next[k]; onChange(next)
    if (secretSet.has(k)) onSecretKeysChange(secretKeys.filter(x => x !== k))
  }
  function toggleSecret(k) {
    onSecretKeysChange(secretSet.has(k) ? secretKeys.filter(x => x !== k) : [...secretKeys, k])
  }
  function add() {
    const k = newKey.trim()
    if (!k) return
    onChange({ ...envVars, [k]: newVal })
    if (newSecret && !secretSet.has(k)) onSecretKeysChange([...secretKeys, k])
    setNewKey(''); setNewVal(''); setNewSecret(false)
  }

  return (
    <div className="space-y-2">
      {swarm
        ? <p className="text-xs text-emerald-400/80">🔒 Secret-flagged values become Docker Swarm secrets (encrypted at rest) when this environment is created.</p>
        : <p className="text-xs text-amber-400/70">⚠ Compose keeps values plaintext in .env — flag secrets and deploy with Swarm for encryption at rest.</p>}
      {entries.map(([k, v]) => {
        const secret = secretSet.has(k)
        return (
          <div key={k} className={`flex items-center gap-2 pl-1.5 border-l-2 ${secret ? 'border-amber-500/70' : 'border-transparent'}`}>
            <button type="button" onClick={() => toggleSecret(k)} title={secret ? 'Secret — click to unflag' : 'Flag as secret'}
              className={`shrink-0 w-6 h-6 flex items-center justify-center rounded text-xs ${secret ? 'text-amber-400' : 'text-gray-600 hover:text-gray-300'}`}>
              {secret ? '🔒' : '🔓'}
            </button>
            <span className="font-mono text-xs text-gray-300 w-40 shrink-0 truncate">{k}</span>
            <input
              type={secret ? 'password' : 'text'} value={v} onChange={e => update(k, e.target.value)}
              className="flex-1 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm text-white font-mono focus:outline-none focus:border-brand-500"
            />
            <button type="button" onClick={() => remove(k)} className="text-gray-500 hover:text-red-400 transition-colors shrink-0 p-0.5 rounded hover:bg-red-950/30"><TrashIcon /></button>
          </div>
        )
      })}
      <div className="flex gap-2 pt-1">
        <button type="button" onClick={() => setNewSecret(s => !s)} title={newSecret ? 'New var is a secret' : 'Flag new var as secret'}
          className={`shrink-0 w-7 h-7 flex items-center justify-center rounded text-xs ${newSecret ? 'text-amber-400 bg-gray-800' : 'text-gray-600 hover:text-gray-300'}`}>
          {newSecret ? '🔒' : '🔓'}
        </button>
        <input
          type="text" placeholder="KEY" value={newKey} onChange={e => setNewKey(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && add()}
          className="w-40 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm text-white font-mono focus:outline-none focus:border-brand-500"
        />
        <input
          type={newSecret ? 'password' : 'text'} placeholder="value" value={newVal} onChange={e => setNewVal(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && add()}
          className="flex-1 px-2 py-1 bg-gray-800 border border-gray-700 rounded text-sm text-white font-mono focus:outline-none focus:border-brand-500"
        />
        <button type="button" onClick={add} className="text-xs text-brand-400 hover:text-brand-300 shrink-0 px-2">Add</button>
      </div>
    </div>
  )
}

// ── Template picker section (popular + recently used + Browse all modal) ──────

function TemplatePickerSection({ templates, selected, onSelect }) {
  const [modalOpen, setModalOpen] = useState(false)
  const [search, setSearch] = useState('')

  // Popular: templates with popular=true, sorted by popular_rank
  const popular = templates
    .filter(t => t.popular)
    .sort((a, b) => a.popular_rank - b.popular_rank)
    .slice(0, 4)

  // Recently used: templates with last_used_at, sorted newest first, excluding popular ones
  const popularNames = new Set(popular.map(t => t.name))
  const recentlyUsed = templates
    .filter(t => t.last_used_at && !popularNames.has(t.name))
    .sort((a, b) => new Date(b.last_used_at) - new Date(a.last_used_at))
    .slice(0, 4)

  // All templates for modal, filtered by search
  const filtered = templates.filter(t =>
    !search ||
    t.label.toLowerCase().includes(search.toLowerCase()) ||
    t.name.toLowerCase().includes(search.toLowerCase()) ||
    (t.tags || []).some(tag => tag.toLowerCase().includes(search.toLowerCase()))
  )

  const selectedTmpl = templates.find(t => t.name === selected)

  function handleSelect(tmpl) {
    onSelect(tmpl)
    setModalOpen(false)
  }

  return (
    <div className="space-y-4">
      {/* Popular */}
      <div>
        <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">Popular</p>
        <div className="grid grid-cols-2 gap-3">
          {popular.map(tmpl => (
            <TemplateCard key={tmpl.name} tmpl={tmpl} selected={selected === tmpl.name} onClick={() => handleSelect(tmpl)} />
          ))}
        </div>
      </div>

      {/* Recently used — only shown when there's history */}
      {recentlyUsed.length > 0 && (
        <div>
          <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider mb-2">Recently used</p>
          <div className="grid grid-cols-2 gap-3">
            {recentlyUsed.map(tmpl => (
              <TemplateCard key={tmpl.name} tmpl={tmpl} selected={selected === tmpl.name} onClick={() => handleSelect(tmpl)} />
            ))}
          </div>
        </div>
      )}

      {/* Browse all button */}
      <button
        type="button"
        onClick={() => { setSearch(''); setModalOpen(true) }}
        className="w-full py-2.5 border border-dashed border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500 rounded-xl text-sm transition-colors"
      >
        Browse all templates ({templates.length}) →
      </button>

      {/* Selected template confirmation */}
      {selectedTmpl && (
        <p className="text-xs text-gray-500 flex items-center gap-1.5">
          <span className="text-brand-400">✓</span>
          <strong className="text-gray-300">{selectedTmpl.label}</strong> selected —
          env vars and volumes pre-filled in Step 4. Review secrets before creating.
        </p>
      )}

      {/* Browse all modal */}
      {modalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/70">
          <div className="bg-gray-900 border border-gray-700 rounded-2xl w-full max-w-2xl max-h-[80vh] flex flex-col shadow-2xl">
            {/* Modal header */}
            <div className="flex items-center justify-between px-5 py-4 border-b border-gray-800">
              <h3 className="text-base font-semibold text-white">All templates</h3>
              <button
                type="button"
                onClick={() => setModalOpen(false)}
                className="text-gray-500 hover:text-white transition-colors text-xl leading-none"
              >×</button>
            </div>
            {/* Search */}
            <div className="px-5 py-3 border-b border-gray-800">
              <input
                type="text"
                value={search}
                onChange={e => setSearch(e.target.value)}
                placeholder="Search templates…"
                autoFocus
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-500 focus:outline-none focus:border-brand-500"
              />
            </div>
            {/* Template grid */}
            <div className="overflow-y-auto p-5">
              {filtered.length === 0 ? (
                <p className="text-sm text-gray-500 text-center py-8">No templates match "{search}"</p>
              ) : (
                <div className="grid grid-cols-2 gap-3">
                  {filtered.map(tmpl => (
                    <TemplateCard
                      key={tmpl.name}
                      tmpl={tmpl}
                      selected={selected === tmpl.name}
                      onClick={() => handleSelect(tmpl)}
                    />
                  ))}
                </div>
              )}
            </div>
          </div>
        </div>
      )}
    </div>
  )
}

// RegistryField — saved-registries dropdown + manual entry. Shown only for
// custom (build-type) stacks: those tag & push built images to the registry so
// remote hosts can pull them without rebuilding. Image/prebuilt stacks pull
// their images directly (registry embedded in each image ref), so it's hidden.
function RegistryField({ data, onChange, errors }) {
  const { data: registries = [], isLoading } = useQuery({
    queryKey: ['registries'],
    queryFn: fetchRegistries,
  })

  // Default to the first saved registry once loaded (if none chosen yet).
  useEffect(() => {
    if (!isLoading && registries.length > 0 && !data.registry) {
      onChange('registry', registries[0].url)
    }
  }, [isLoading, registries.length]) // eslint-disable-line react-hooks/exhaustive-deps

  const hasRegistries = !isLoading && registries.length > 0
  const isCustom = !isLoading && registries.length > 0 && !registries.some(r => r.url === data.registry)
  const selectValue = isCustom ? CUSTOM_REGISTRY : (data.registry || '')
  const handleSelectChange = val => onChange('registry', val === CUSTOM_REGISTRY ? '' : val)

  return (
    <div>
      <Label required>Container registry</Label>
      <p className="text-xs text-gray-500 mb-2">Built images are tagged and pushed here so remote hosts can pull them without rebuilding.</p>

      {!isLoading && !hasRegistries && (
        <div className="mb-3 flex items-start gap-2 px-3 py-2.5 bg-amber-950/40 border border-amber-800/50 rounded-lg">
          <span className="text-amber-400 mt-0.5 shrink-0">⚠</span>
          <p className="text-xs text-amber-300">
            No registries configured.{' '}
            <a href="/settings" target="_blank" rel="noreferrer"
              className="underline underline-offset-2 hover:text-amber-200 transition-colors">
              Add one in Settings
            </a>{' '}
            to reuse credentials across workspaces.
          </p>
        </div>
      )}

      {hasRegistries && (
        <select
          value={selectValue}
          onChange={e => handleSelectChange(e.target.value)}
          className={`w-full px-3 py-2 bg-gray-800 border rounded-lg text-white text-sm focus:outline-none focus:border-brand-500 transition-colors ${
            errors.registry ? 'border-red-500' : 'border-gray-700'
          }`}
        >
          {registries.map(r => (
            <option key={r.id} value={r.url}>{r.name} — {r.url}</option>
          ))}
          <option value={CUSTOM_REGISTRY}>Other (enter manually)…</option>
        </select>
      )}

      {(!hasRegistries || isCustom || selectValue === CUSTOM_REGISTRY) && (
        <div className={hasRegistries ? 'mt-2' : ''}>
          <Input
            value={data.registry} onChange={v => onChange('registry', v)}
            placeholder="registry.example.com"
            error={errors.registry}
          />
          {hasRegistries && (
            <p className="text-xs text-gray-500 mt-1">
              To save this registry for reuse,{' '}
              <a href="/settings" target="_blank" rel="noreferrer"
                className="text-brand-400 hover:text-brand-300 underline underline-offset-2 transition-colors">
                add it in Settings
              </a>{' '}
              first.
            </p>
          )}
        </div>
      )}

      {errors.registry && hasRegistries && !isCustom && selectValue !== CUSTOM_REGISTRY && (
        <p className="text-red-400 text-xs mt-1">{errors.registry}</p>
      )}
    </div>
  )
}

function Step2({ data, onChange, errors }) {
  const { data: templates } = useQuery({ queryKey: ['templates'], queryFn: fetchTemplates })

  return (
    <div className="space-y-5">
      <StepHeader step={2} title="Application stack" subtitle="Choose how you want to configure your containers." />

      {/* Type selector */}
      <div className="grid grid-cols-3 gap-3">
        {STACK_TYPES.map(t => (
          <button
            key={t.id} type="button" onClick={() => onChange('stackType', t.id)}
            className={`text-left p-4 rounded-xl border transition-all ${
              data.stackType === t.id
                ? 'border-brand-500 bg-brand-950/30'
                : 'border-gray-700 bg-gray-800/40 hover:border-gray-500'
            }`}
          >
            <p className="font-medium text-white text-sm mb-1">{t.label}</p>
            <p className="text-xs text-gray-400">{t.desc}</p>
          </button>
        ))}
      </div>

      {/* Container registry — custom (build) stacks only; image/prebuilt pull directly */}
      {data.stackType === 'custom' && (
        <RegistryField data={data} onChange={onChange} errors={errors} />
      )}

      {/* Image stack: custom image list + env vars */}
      {data.stackType === 'image' && (
        <div className="space-y-5">
          <div>
            <Label>Services</Label>
            <p className="text-xs text-gray-500 mb-2">Add each Docker image you want to deploy.</p>
            <ImageEditor images={data.images} onChange={v => onChange('images', v)} />
          </div>
          <div>
            <Label>Environment variables</Label>
            <p className="text-xs text-gray-500 mb-2">These will be written to <code className="font-mono text-xs">.env</code>. Secrets can be set now or edited after creation.</p>
            <EnvVarEditor envVars={data.customEnvVars} onChange={v => onChange('customEnvVars', v)} />
          </div>
        </div>
      )}

      {/* Pre-built template picker */}
      {data.stackType === 'prebuilt' && (
        <TemplatePickerSection
          templates={templates || []}
          selected={data.template}
          onSelect={async (tmpl) => {
            onChange('template', tmpl.name)
            try {
              recordTemplateUse(tmpl.name).catch(() => {})
              const detail = await fetchTemplate(tmpl.name)
              // Distribute env vars to ALL environments in Step 3 (per-env)
              const envs = detail?.default_envs || detail?.default_env_vars || {}
              if (Object.keys(envs).length > 0) onChange('_distributeVars', envs)
              // Pre-populate services with full config from template (ports/volumes/healthcheck in Step 4)
              if (detail?.images?.length > 0) {
                onChange('images', detail.images.map(img => ({
                  ...DEFAULT_IMAGE,
                  name:  img.name  || '',
                  image: img.image || '',
                  tag:   img.tag   || 'latest',
                  // Port mappings from template (port = container port, host_port = host)
                  portMappings: img.port
                    ? [{ host: img.host_port || '', container: String(img.port) }]
                    : DEFAULT_IMAGE.portMappings,
                  volumes: img.volumes || [],
                  healthcheck: img.healthcheck || '',
                  healthcheck_config: img.healthcheck_config || DEFAULT_IMAGE.healthcheck_config,
                })))
                // Build templateVolumes display list for Step 4
                const seen = new Set()
                const templateVols = []
                for (const img of detail.images) {
                  for (const v of (img.volumes || [])) {
                    const c = v.indexOf(':')
                    if (c < 0) continue
                    const source = v.slice(0, c)
                    const mountPath = v.slice(c + 1).split(':')[0]
                    if (!seen.has(source)) { seen.add(source); templateVols.push({ source, mountPath }) }
                  }
                }
                onChange('templateVolumes', templateVols)
              }
            } catch (e) { /* non-fatal */ }
          }}
        />
      )}

      {/* Custom stack options */}
      {data.stackType === 'custom' && (
        <div className="space-y-4">
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label>Backend</Label>
              <Select value={data.backend} onChange={v => onChange('backend', v)} options={BACKEND_OPTIONS} />
            </div>
            <div>
              <Label>Frontend</Label>
              <Select value={data.frontend} onChange={v => onChange('frontend', v)} options={FRONTEND_OPTIONS} />
            </div>
            <div>
              <Label>Database</Label>
              <Select value={data.database} onChange={v => onChange('database', v)} options={DB_OPTIONS} />
            </div>
          </div>
          <div className="space-y-3 pt-2 border-t border-gray-800">
            <Toggle label="Redis cache" hint="redis:7-alpine" checked={data.redis} onChange={v => onChange('redis', v)} />
            <Toggle label="Garage S3" hint="Self-hosted S3-compatible object storage" checked={data.garage} onChange={v => onChange('garage', v)} />
          </div>
        </div>
      )}
    </div>
  )
}

// ── Environments (wizard step 4 — Step3 component) ────────────────────────────

const DEFAULT_ENV = { name: '', domain: '', http_port: 8080, traefik: false, traefik_network: 'traefik_net', ssl_enabled: false, deployment: 'compose', backend_replicas: 1, frontend_replicas: 1, git_enabled: false, git_repo: '', git_branch: '', vars: {}, secret_keys: [] }
const DEPLOYMENT_OPTIONS = [{ value: 'compose', label: 'Docker Compose' }, { value: 'swarm', label: 'Docker Swarm' }]

function EnvForm({ env, idx, onChange, onRemove, canRemove, stackType, hosts = [], defaultHostId = 0 }) {
  const upd = (k, v) => onChange(idx, { ...env, [k]: v })
  const hostOptions = [{ value: '0', label: 'Local control plane' },
    ...hosts.map(h => ({ value: String(h.id), label: h.name }))]
  return (
    <div className="bg-gray-800/50 border border-gray-700 rounded-xl p-4 space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm font-semibold text-white">Environment {idx + 1}</p>
        {canRemove && (
          <button type="button" onClick={() => onRemove(idx)} className="text-xs text-red-400 hover:text-red-300">Remove</button>
        )}
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <Label required>Name</Label>
          <Input value={env.name} onChange={v => upd('name', v)} placeholder="dev" />
        </div>
        <div>
          <Label>Domain</Label>
          <Input value={env.domain} onChange={v => upd('domain', v)} placeholder="example.com" />
        </div>
        {/* HTTP port — only relevant for custom stacks without Traefik (direct Nginx binding) */}
        {!env.traefik && stackType !== 'image' && (
          <div>
            <Label>HTTP port</Label>
            <Input type="number" value={env.http_port} onChange={v => upd('http_port', parseInt(v) || 8080)} placeholder="8080" />
            <p className="text-xs text-gray-500 mt-1">Host port Nginx binds to — access your app at <code className="font-mono text-xs">host:{env.http_port || 8080}</code></p>
          </div>
        )}
        {env.traefik && (
          <div className="col-span-2">
            <p className="text-xs text-gray-500 flex items-center gap-1.5 px-3 py-2 bg-gray-800/60 rounded-lg border border-gray-700/60">
              <span>ℹ</span> Traefik handles ports 80 / 443 — set a domain above for routing.
            </p>
          </div>
        )}
        <div>
          <Label>Deployment</Label>
          <Select value={env.deployment} onChange={v => upd('deployment', v)} options={DEPLOYMENT_OPTIONS} />
        </div>
        <div>
          <Label>Host</Label>
          <Select
            value={String(env.host_id ?? defaultHostId)}
            onChange={v => upd('host_id', Number(v))}
            options={hostOptions}
          />
          <p className="text-xs text-gray-500 mt-1">Where this environment runs.</p>
        </div>
      </div>

      <div className="space-y-3 pt-2 border-t border-gray-700/60">
        <Toggle
          label="Traefik reverse proxy"
          hint="Route traffic via Traefik instead of direct port binding"
          checked={env.traefik}
          onChange={v => {
            upd('traefik', v)
            // Clear SSL when Traefik is disabled
            if (!v) upd('ssl_enabled', false)
          }}
        />

        {/* SSL checkbox — only shown when Traefik is on AND a domain is entered */}
        {env.traefik && (
          <div className={`pl-4 border-l-2 ${env.ssl_enabled ? 'border-green-700' : 'border-gray-700'}`}>
            <div className="flex items-start justify-between">
              <div>
                <p className="text-sm text-gray-200">Request SSL certificate</p>
                <p className="text-xs text-gray-500 mt-0.5">
                  {!env.domain
                    ? 'Enter a domain above to enable SSL'
                    : 'Traefik will issue a Let\'s Encrypt cert for this domain'}
                </p>
              </div>
              <button
                type="button"
                disabled={!env.domain}
                onClick={() => upd('ssl_enabled', !env.ssl_enabled)}
                className={`relative w-10 h-5 rounded-full transition-colors shrink-0 ml-4 ${
                  env.ssl_enabled && env.domain ? 'bg-green-600' : 'bg-gray-700'
                } disabled:opacity-40`}
              >
                <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${
                  env.ssl_enabled && env.domain ? 'translate-x-5' : ''
                }`} />
              </button>
            </div>

            {env.ssl_enabled && env.domain && (
              <div className="mt-2 flex items-start gap-2 px-3 py-2 bg-green-950/40 border border-green-800/50 rounded-lg">
                <span className="text-green-400 shrink-0 mt-0.5">🔒</span>
                <div className="text-xs text-green-300 space-y-0.5">
                  <p>SSL will be active for <strong>{env.domain}</strong></p>
                  <p className="text-green-400/70">
                    Port 80 must be publicly reachable for the Let's Encrypt HTTP-01 challenge.
                    Set <code className="font-mono text-xs">ACME_EMAIL</code> in{' '}
                    <code className="font-mono text-xs">src/.env</code> before deploying.
                  </p>
                </div>
              </div>
            )}
          </div>
        )}
        <Toggle
          label="Git sync"
          hint="Enable ./run.sh sync for this environment"
          checked={env.git_enabled}
          onChange={v => upd('git_enabled', v)}
        />
        {env.git_enabled && (
          <div className="grid grid-cols-2 gap-3 pl-1">
            <div>
              <Label>Git repo</Label>
              <Input value={env.git_repo} onChange={v => upd('git_repo', v)} placeholder="git@github.com:org/repo.git" />
            </div>
            <div>
              <Label>Branch</Label>
              <Input value={env.git_branch} onChange={v => upd('git_branch', v)} placeholder="main" />
            </div>
          </div>
        )}
      </div>

      {/* Per-environment variables */}
      <div className="pt-3 border-t border-gray-700/60">
        <EnvVarsSection vars={env.vars || {}} secretKeys={env.secret_keys || []} deployment={env.deployment}
          onChange={v => upd('vars', v)} onSecretKeysChange={s => upd('secret_keys', s)} />
      </div>
    </div>
  )
}

// Collapsible per-env vars section inside EnvForm
function EnvVarsSection({ vars, secretKeys = [], onChange, onSecretKeysChange, deployment }) {
  const [open, setOpen] = useState(false)
  const count = Object.keys(vars).length
  const secretCount = secretKeys.length
  return (
    <div>
      <button type="button" onClick={() => setOpen(o => !o)}
        className="flex items-center gap-2 text-xs font-semibold text-gray-400 uppercase tracking-wider hover:text-gray-200 transition-colors w-full">
        <span className={`transition-transform ${open ? 'rotate-90' : ''}`}>▶</span>
        Environment Variables
        {count > 0 && <span className="ml-1 text-brand-400 normal-case font-normal">{count} set</span>}
        {secretCount > 0 && <span className="ml-1 text-amber-400 normal-case font-normal">· {secretCount} 🔒</span>}
        <span className="ml-auto text-gray-600 normal-case font-normal">per-environment .env</span>
      </button>
      {open && <div className="mt-3"><EnvVarEditor envVars={vars} secretKeys={secretKeys} onChange={onChange} onSecretKeysChange={onSecretKeysChange} deployment={deployment} /></div>}
    </div>
  )
}

function Step3({ data, onChange }) {
  const { data: hosts = [] } = useQuery({ queryKey: ['hosts'], queryFn: fetchHosts })
  function updateEnv(idx, updated) {
    const envs = [...data.environments]
    envs[idx] = updated
    onChange('environments', envs)
  }
  function addEnv() {
    // Inherit vars + secret flags from the first environment so all envs start aligned
    const inheritedVars = data.environments.length > 0 ? { ...(data.environments[0].vars || {}) } : {}
    const inheritedSecrets = data.environments.length > 0 ? [...(data.environments[0].secret_keys || [])] : []
    onChange('environments', [...data.environments, { ...DEFAULT_ENV, vars: inheritedVars, secret_keys: inheritedSecrets }])
  }
  function removeEnv(idx) {
    onChange('environments', data.environments.filter((_, i) => i !== idx))
  }

  return (
    <div className="space-y-4">
      <StepHeader step={4} title="Environments" subtitle="Configure the environments for this workspace." />
      {data.environments.map((env, i) => (
        <EnvForm
          key={i} idx={i} env={env}
          onChange={updateEnv}
          onRemove={removeEnv}
          canRemove={data.environments.length > 1}
          stackType={data.stackType}
          hosts={hosts}
          defaultHostId={data.default_host_id || 0}
        />
      ))}
      <button
        type="button" onClick={addEnv}
        className="w-full py-2 border border-dashed border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500 rounded-xl text-sm transition-colors"
      >
        + Add environment
      </button>
    </div>
  )
}

// ── Services / volumes (wizard step 3 — Step4 component) ──────────────────────

const DEFAULT_VOLUME = { name: '', mountPath: '' }

// Returns 'bind' if source starts with . or /, otherwise 'named'
function volType(source) {
  return (source.startsWith('./') || source.startsWith('/')) ? 'bind' : 'named'
}

function VolTypeBadge({ source }) {
  const t = volType(source)
  return (
    <span className={`text-xs px-1.5 py-0.5 rounded font-medium shrink-0 ${
      t === 'bind' ? 'bg-blue-950 text-blue-300' : 'bg-purple-950 text-purple-300'
    }`}>{t === 'bind' ? 'bind' : 'named'}</span>
  )
}

function VolumeEditor({ volumes, onChange }) {
  function update(idx, field, val) {
    onChange(volumes.map((v, i) => i === idx ? { ...v, [field]: val } : v))
  }
  function add() { onChange([...volumes, { name: './volumes/', mountPath: '' }]) }
  function remove(idx) { onChange(volumes.filter((_, i) => i !== idx)) }

  return (
    <div className="space-y-2">
      {volumes.map((vol, i) => (
        <div key={i} className="flex items-center gap-2">
          <VolTypeBadge source={vol.name || ''} />
          <input
            type="text" value={vol.name} onChange={e => update(i, 'name', e.target.value)}
            placeholder="./volumes/db_data or db_data"
            className="flex-1 px-2 py-1.5 bg-gray-800 border border-gray-700 rounded text-sm text-white font-mono focus:outline-none focus:border-brand-500"
          />
          <span className="text-gray-600 text-xs shrink-0">→</span>
          <input
            type="text" value={vol.mountPath} onChange={e => update(i, 'mountPath', e.target.value)}
            placeholder="/var/lib/mysql"
            className="flex-1 px-2 py-1.5 bg-gray-800 border border-gray-700 rounded text-sm text-white font-mono focus:outline-none focus:border-brand-500"
          />
          <button type="button" onClick={() => remove(i)} className="text-gray-500 hover:text-red-400 transition-colors shrink-0 p-0.5 rounded hover:bg-red-950/30"><TrashIcon /></button>
        </div>
      ))}
      <p className="text-xs text-gray-600">
        Paths starting with <code className="font-mono">./</code> or <code className="font-mono">/</code> = bind mount (scoped to workspace). Plain names = Docker named volume.
      </p>
      <button
        type="button" onClick={add}
        className="text-xs text-brand-400 hover:text-brand-300 transition-colors"
      >
        + Add volume
      </button>
    </div>
  )
}

const monoInput = 'px-2 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm font-mono focus:outline-none focus:border-brand-500'

const RESTART_OPTIONS_WIZ = [
  { value: 'unless-stopped', label: 'Unless stopped (recommended)' },
  { value: 'always',         label: 'Always' },
  { value: 'on-failure',     label: 'On failure' },
  { value: 'no',             label: 'No (never restart)' },
]

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

function VolBadge({ src }) {
  const isBind = src.startsWith('./') || src.startsWith('/')
  return (
    <span className={`text-xs px-1.5 py-0.5 rounded font-medium shrink-0 ${isBind ? 'bg-blue-950 text-blue-300' : 'bg-purple-950 text-purple-300'}`}>
      {isBind ? 'bind' : 'named'}
    </span>
  )
}

// Full service card matching Edit Workspace ServiceCard appearance
function ServiceConfigCard({ img, idx, allImages, onChange }) {
  const [portRows, setPortRows] = useState(() => {
    const rows = (img.portMappings || [])
    return rows.length ? rows : [{ host: '', container: '' }]
  })
  const [volRows, setVolRows] = useState(() => {
    const rows = (img.volumes || []).map(v => {
      if (typeof v !== 'string') return { source: v.source||'', path: v.path||'', mode: 'rw' }
      const parts = v.split(':')
      const last = parts[parts.length - 1]
      if ((last === 'ro' || last === 'rw') && parts.length >= 3) {
        return { source: parts[0], path: parts.slice(1, -1).join(':'), mode: last }
      }
      const c = v.indexOf(':')
      return c >= 0 ? { source: v.slice(0, c), path: v.slice(c + 1), mode: 'rw' } : { source: v, path: '', mode: 'rw' }
    })
    return rows.length ? rows : []
  })

  function syncPorts(rows) {
    setPortRows(rows)
    onChange(idx, { ...img, portMappings: rows })
  }
  function syncVols(rows) {
    setVolRows(rows)
    const vols = rows
      .filter(r => r.source.trim() || r.path.trim())
      .map(r => {
        const src  = r.source.trim()
        const path = r.path.trim()
        if (!src && !path) return null
        if (!src || !path) return src || path
        return r.mode === 'ro' ? `${src}:${path}:ro` : `${src}:${path}`
      })
      .filter(Boolean)
    onChange(idx, { ...img, volumes: vols })
  }
  function upd(field, val) { onChange(idx, { ...img, [field]: val }) }

  const hcConfig = img.healthcheck_config || {}
  const otherNames = (allImages || []).map((m, j) => j !== idx ? m.name : null).filter(Boolean)

  return (
    <div className="bg-gray-800/50 border border-gray-700 rounded-xl p-4 space-y-4">
      <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">
        {img.name || `Service ${idx + 1}`}
        <span className="ml-2 text-gray-600 font-mono font-normal normal-case">{img.image}:{img.tag || 'latest'}</span>
      </p>

      {/* Port mappings */}
      <div>
        <Label>Port mappings</Label>
        <p className="text-xs text-gray-500 mb-2">
          HOST : CONTAINER — leave host blank to expose internally only.
          <span className="ml-2 text-gray-600">🔗 = show as link on env card</span>
        </p>
        <div className="space-y-1.5">
          {portRows.map((row, ri) => (
            <div key={ri} className="flex items-center gap-2">
              <input type="text" value={row.host} placeholder="8080"
                onChange={e => syncPorts(portRows.map((x,j) => j===ri?{...x,host:e.target.value}:x))}
                className={`flex-1 ${monoInput}`} />
              <span className="text-gray-500 font-bold shrink-0">:</span>
              <input type="text" value={row.container} placeholder="80"
                onChange={e => syncPorts(portRows.map((x,j) => j===ri?{...x,container:e.target.value}:x))}
                className={`flex-1 ${monoInput}`} />
              <label title="Show as clickable link on env card" className={`flex items-center gap-1 shrink-0 cursor-pointer select-none ${row.host.trim() ? 'text-gray-400 hover:text-brand-400' : 'text-gray-700 cursor-not-allowed'}`}>
                <input
                  type="checkbox"
                  checked={!!row.link}
                  disabled={!row.host.trim()}
                  onChange={e => syncPorts(portRows.map((x,j) => j===ri?{...x,link:e.target.checked}:x))}
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
          <button type="button" onClick={() => setPortRows(r => [...r, {host:'',container:'',link:false}])}
            className="text-xs text-brand-400 hover:text-brand-300 transition-colors flex items-center gap-1 mt-1">
            <span className="text-base leading-none">＋</span> Add port mapping
          </button>
        </div>
      </div>

      {/* Volume mappings */}
      <div>
        <Label>Volume mappings</Label>
        <p className="text-xs text-gray-500 mb-2">SOURCE (named vol or path) : CONTAINER PATH</p>
        <div className="space-y-1.5">
          {volRows.map((row, ri) => (
            <div key={ri} className="flex items-center gap-2">
              <VolBadge src={row.source || ''} />
              <input type="text" value={row.source} placeholder="./volumes/app_data"
                onChange={e => syncVols(volRows.map((x,j) => j===ri?{...x,source:e.target.value}:x))}
                className={`flex-1 ${monoInput}`} />
              <span className="text-gray-500 font-bold shrink-0">:</span>
              <input type="text" value={row.path} placeholder="/var/lib/data"
                onChange={e => syncVols(volRows.map((x,j) => j===ri?{...x,path:e.target.value}:x))}
                className={`flex-1 ${monoInput}`} />
              <VolModeToggle mode={row.mode || 'rw'} onChange={m => syncVols(volRows.map((x,j) => j===ri?{...x,mode:m}:x))} />
              <button type="button" onClick={() => syncVols(volRows.filter((_,j)=>j!==ri))}
                className="text-gray-500 hover:text-red-400 transition-colors shrink-0 p-0.5 rounded hover:bg-red-950/30"><TrashIcon /></button>
            </div>
          ))}
          <button type="button" onClick={() => setVolRows(r => [...r, {source:'./volumes/',path:'',mode:'rw'}])}
            className="text-xs text-brand-400 hover:text-brand-300 transition-colors flex items-center gap-1 mt-1">
            <span className="text-base leading-none">＋</span> Add volume
          </button>
        </div>
      </div>

      {/* Restart policy */}
      <div className="w-1/2">
        <Label>Restart policy</Label>
        <select value={img.restart || 'unless-stopped'} onChange={e => upd('restart', e.target.value)}
          className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500">
          {RESTART_OPTIONS_WIZ.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
      </div>

      {/* Healthcheck */}
      <div className="space-y-2">
        <div>
          <Label>Healthcheck command</Label>
          <p className="text-xs text-gray-500 mb-1">Shell command to test container health. Leave blank to disable.</p>
          <input type="text" value={img.healthcheck || ''} placeholder="curl -sf http://localhost/health || exit 1"
            onChange={e => upd('healthcheck', e.target.value)} className={`w-full ${monoInput}`} />
        </div>
        {img.healthcheck && (
          <div className="grid grid-cols-5 gap-2">
            {[['interval','30'],['timeout','10'],['retries','3',true],['start_period','30'],['start_interval','5']].map(([k,ph,noSuffix]) => (
              <div key={k}>
                <label className="block text-xs text-gray-500 mb-1">{k.replace(/_/g,' ')}{!noSuffix && ' (s)'}</label>
                <input type="number" min="1" value={(hcConfig[k]||'').replace(/s$/,'')} placeholder={ph}
                  onChange={e => {
                    const v = e.target.value.replace(/\D/g,'')
                    upd('healthcheck_config', {...hcConfig, [k]: v ? (noSuffix ? v : `${v}s`) : ''})
                  }}
                  className={`w-full ${monoInput}`} />
              </div>
            ))}
          </div>
        )}
      </div>

      {/* depends_on */}
      {otherNames.length > 0 && (
        <div>
          <Label>Depends on</Label>
          <div className="flex flex-wrap gap-3 mt-1">
            {otherNames.map(svcName => (
              <label key={svcName} className="flex items-center gap-1.5 cursor-pointer select-none">
                <input type="checkbox"
                  checked={(img.depends_on || []).includes(svcName)}
                  onChange={e => {
                    const deps = img.depends_on || []
                    upd('depends_on', e.target.checked ? [...deps, svcName] : deps.filter(d => d !== svcName))
                  }}
                  className="rounded border-gray-600 bg-gray-700 text-brand-500 focus:ring-brand-500" />
                <span className="text-sm text-gray-300 font-mono">{svcName}</span>
              </label>
            ))}
          </div>
        </div>
      )}
    </div>
  )
}

// Simple named-volume-only editor for the extra volumes section
function NamedVolumeEditor({ volumes, onChange }) {
  const [newName, setNewName] = useState('')
  function add() {
    const n = newName.trim()
    if (!n || n.startsWith('./') || n.startsWith('/')) return
    onChange([...volumes, { name: n, mountPath: '' }])
    setNewName('')
  }
  return (
    <div className="space-y-2">
      {volumes.map((v, i) => (
        <div key={i} className="flex items-center gap-2">
          <span className="px-1.5 py-0.5 rounded text-xs bg-purple-950 text-purple-300 shrink-0">named</span>
          <span className="text-xs font-mono text-gray-300 flex-1">{v.name}</span>
          <button type="button" onClick={() => onChange(volumes.filter((_,j)=>j!==i))}
            className="text-gray-500 hover:text-red-400 transition-colors shrink-0 p-0.5 rounded hover:bg-red-950/30"><TrashIcon /></button>
        </div>
      ))}
      <div className="flex gap-2">
        <input type="text" value={newName} onChange={e => setNewName(e.target.value.replace(/[./\\]/g,''))}
          onKeyDown={e => e.key === 'Enter' && add()}
          placeholder="volume_name (no paths)"
          className="flex-1 px-2 py-1.5 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm font-mono focus:outline-none focus:border-brand-500" />
        <button type="button" onClick={add}
          className="text-xs text-brand-400 hover:text-brand-300 shrink-0 px-3 transition-colors">Add</button>
      </div>
    </div>
  )
}

function Step4({ data, onChange }) {
  // updateImage uses data.images indices (not filtered activeImages indices)
  function updateImage(idx, updated) {
    onChange('images', data.images.map((img, i) => i === idx ? updated : img))
  }
  // For image stacks, idx in ServiceConfigCard maps to data.images directly
  // For prebuilt, same — images array is populated from template
  const showServices = data.stackType === 'image' || data.stackType === 'prebuilt'
  const serviceImages = data.images.filter(i => i.name && i.image)

  // Within-workspace duplicate host ports (cheap, local).
  const dupWarnings = portConflicts(serviceImages.map(img => ({ name: img.name, ports: hostPortsFromMappings(img) })))
  // Host-aware conflicts (C+D): each env's target host × each image's host ports.
  const hostChecks = []
  for (const env of data.environments || []) {
    const hostId = env.host_id ?? data.default_host_id ?? 0
    for (const img of serviceImages) {
      for (const p of hostPortsFromMappings(img)) hostChecks.push({ host_id: hostId, port: Number(p), service: img.name })
    }
  }
  const hostWarnings = usePortConflicts(hostChecks)

  return (
    <div className="space-y-6">
      <StepHeader step={3} title="Services" subtitle="Configure ports, volumes, restart policy and healthchecks per service." />

      {/* Per-service config — image and prebuilt stacks */}
      {showServices && serviceImages.length > 0 && (
        <div className="space-y-4">
          {data.stackType === 'prebuilt' && (
            <p className="text-xs text-amber-400/80 flex items-center gap-1.5">
              <span>ℹ</span> Values pre-filled from template — adjust host ports or leave as-is.
            </p>
          )}
          {data.images.map((img, i) =>
            img.name && img.image ? (
              <ServiceConfigCard key={i} img={img} idx={i}
                allImages={serviceImages}
                onChange={updateImage} />
            ) : null
          )}
          <PortWarnings warnings={[...dupWarnings, ...hostWarnings]} />
        </div>
      )}

      {/* Custom stacks: no per-service config (handled by compose-gen.sh) */}
      {data.stackType === 'custom' && (
        <div className="px-4 py-3 bg-gray-800/40 border border-gray-700/50 rounded-xl">
          <p className="text-sm text-gray-300 font-medium mb-1">Custom application stack</p>
          <p className="text-xs text-gray-500">
            Port mappings, volumes and healthchecks for custom stacks are defined by the compose templates.
            After creation, use <strong>Edit Workspace</strong> to adjust service configuration.
          </p>
        </div>
      )}

      {/* Extra named volumes — only named volumes, not bind mounts */}
      <details className="group">
        <summary className="text-xs text-gray-500 cursor-pointer hover:text-gray-300 transition-colors select-none list-none flex items-center gap-1">
          <span className="group-open:rotate-90 transition-transform inline-block">▶</span>
          Extra named volumes
          <span className="ml-2 text-gray-600 font-normal">shared Docker volumes across services</span>
        </summary>
        <div className="mt-3">
          <p className="text-xs text-gray-500 mb-3">
            Only for named Docker volumes that need to be shared between multiple services
            and aren't already declared in a service's volume list above.
            Bind mounts are defined per-service and don't need declaring here.
          </p>
          <NamedVolumeEditor volumes={data.volumes} onChange={v => onChange('volumes', v)} />
        </div>
      </details>
    </div>
  )
}

// ── Step 5: Backup Configuration ──────────────────────────────────────────────

const SCHEDULE_OPTIONS = [
  { value: 'daily',  label: 'Daily' },
  { value: 'weekly', label: 'Weekly' },
  { value: 'manual', label: 'Manual only' },
]

const RETENTION_OPTIONS = [
  { value: 3,  label: '3 backups' },
  { value: 7,  label: '7 backups' },
  { value: 14, label: '14 backups' },
  { value: 30, label: '30 backups' },
]

function Step5({ data, onChange }) {
  const { data: targets = [], isLoading } = useQuery({
    queryKey: ['backup-targets'],
    queryFn: fetchBackupTargets,
  })

  const backup = data.backup
  function upd(key, val) { onChange('backup', { ...backup, [key]: val }) }

  // When a target is selected, store both id and name
  function handleTargetChange(val) {
    if (val === 'local') {
      upd('targetId', null)
      onChange('backup', { ...backup, targetId: null, targetName: 'local' })
    } else {
      const t = targets.find(t => String(t.id) === val)
      onChange('backup', { ...backup, targetId: t?.id ?? null, targetName: t?.name ?? val })
    }
  }

  const selectedTargetVal = backup.targetId ? String(backup.targetId) : 'local'

  return (
    <div className="space-y-6">
      <StepHeader step={5} title="Backup Configuration" subtitle="Configure where and how often this workspace is backed up." />

      {/* Enable toggle */}
      <Toggle
        label="Enable backups"
        hint="Backup all environment databases and volumes"
        checked={backup.enabled}
        onChange={v => upd('enabled', v)}
      />

      {backup.enabled && (
        <div className="space-y-5 pt-2">
          {/* Backup destination */}
          <div>
            <Label required>Backup destination</Label>
            {isLoading ? (
              <p className="text-sm text-gray-500">Loading targets…</p>
            ) : (
              <>
                <select
                  value={selectedTargetVal}
                  onChange={e => handleTargetChange(e.target.value)}
                  className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500"
                >
                  <option value="local">Local filesystem (default)</option>
                  {targets.map(t => (
                    <option key={t.id} value={String(t.id)}>
                      {t.name} ({t.type.toUpperCase()})
                    </option>
                  ))}
                </select>
                {targets.length === 0 && (
                  <p className="text-xs text-gray-500 mt-1.5">
                    Only local backups available.{' '}
                    <a href="/settings" target="_blank" rel="noreferrer"
                      className="text-brand-400 hover:text-brand-300 underline underline-offset-2">
                      Add an S3 or SFTP target in Settings
                    </a>{' '}
                    to enable remote backups.
                  </p>
                )}
                {selectedTargetVal === 'local' && (
                  <p className="text-xs text-gray-500 mt-1.5">
                    Stored in <code className="font-mono text-xs">workspaces/{data.name || '<name>'}/backups/</code>
                  </p>
                )}
              </>
            )}
          </div>

          {/* Schedule */}
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label>Schedule</Label>
              <Select
                value={backup.schedule}
                onChange={v => upd('schedule', v)}
                options={SCHEDULE_OPTIONS}
              />
            </div>
            <div>
              <Label>Retention</Label>
              <Select
                value={backup.retention}
                onChange={v => upd('retention', parseInt(v))}
                options={RETENTION_OPTIONS}
              />
              <p className="text-xs text-gray-500 mt-1">Older backups are pruned automatically.</p>
            </div>
          </div>
        </div>
      )}

      {!backup.enabled && (
        <div className="px-4 py-3 bg-gray-800/60 border border-gray-700/60 rounded-lg">
          <p className="text-sm text-gray-400">Backups disabled — you can enable them later from the workspace settings.</p>
        </div>
      )}
    </div>
  )
}

// ── Step 6: Review ────────────────────────────────────────────────────────────

function ReviewRow({ label, value }) {
  return (
    <div className="flex items-start justify-between py-2 border-b border-gray-800 last:border-0">
      <span className="text-sm text-gray-400">{label}</span>
      <span className="text-sm text-white font-medium text-right max-w-[60%]">{value || '—'}</span>
    </div>
  )
}

function Step6({ data }) {
  const stackDesc = data.stackType === 'prebuilt'
    ? `Pre-built: ${data.template || '(none selected)'}`
    : data.stackType === 'image'
    ? `Image stack: ${data.images.filter(i => i.name).map(i => `${i.name} (${i.image}:${i.tag || 'latest'})`).join(', ') || '(no services)'}`
    : `Custom: ${[data.backend, data.frontend !== 'none' && data.frontend, data.database !== 'none' && data.database].filter(Boolean).join(' · ')}`

  const reviewImages = data.images.filter(i => i.name && i.image)
  const dupWarnings = data.stackType === 'custom' ? [] :
    portConflicts(reviewImages.map(img => ({ name: img.name, ports: hostPortsFromMappings(img) })))
  const hostChecks = []
  if (data.stackType !== 'custom') {
    for (const env of data.environments || []) {
      const hostId = env.host_id ?? data.default_host_id ?? 0
      for (const img of reviewImages) {
        for (const p of hostPortsFromMappings(img)) hostChecks.push({ host_id: hostId, port: Number(p), service: img.name })
      }
    }
  }
  const hostWarnings = usePortConflicts(hostChecks)

  return (
    <div className="space-y-5">
      <StepHeader step={6} title="Review" subtitle="Confirm your configuration before creating the workspace." />

      <div className="bg-gray-900 border border-gray-800 rounded-xl p-4 space-y-0">
        <ReviewRow label="Project name" value={data.name} />
        <ReviewRow label="Registry" value={data.registry} />
        <ReviewRow label="Stack" value={stackDesc} />
        <ReviewRow label="Environments" value={data.environments.map(e => e.name || '(unnamed)').join(', ')} />
        {data.environments.map((e, i) => e.domain && (
          <ReviewRow key={i} label={`  ${e.name} domain`} value={e.domain} />
        ))}
        {data.redis  && <ReviewRow label="Redis" value="Enabled" />}
        {data.garage && <ReviewRow label="Garage S3" value="Enabled" />}
        {data.environments.filter(e => Object.keys(e.vars || {}).length > 0).map((e, i) => (
          <ReviewRow key={i} label={`  ${e.name} vars`} value={`${Object.keys(e.vars).length} variable(s)`} />
        ))}
        {data.volumes.filter(v => v.name).length > 0 && (
          <ReviewRow label="Named volumes" value={data.volumes.filter(v => v.name).map(v => v.name).join(', ')} />
        )}
        <ReviewRow label="Backup" value={
          !data.backup.enabled ? 'Disabled' :
          `${data.backup.targetName === 'local' ? 'Local' : data.backup.targetName} · ${data.backup.schedule} · keep ${data.backup.retention}`
        } />
      </div>

      <PortWarnings warnings={[...dupWarnings, ...hostWarnings]} />

      <div className="bg-amber-950/40 border border-amber-800/50 rounded-xl px-4 py-3">
        <p className="text-sm text-amber-300">
          After creation, edit <code className="font-mono text-xs bg-amber-900/40 px-1 py-0.5 rounded">envs/&#123;env&#125;/.env</code> to fill in secrets before starting the stack.
        </p>
      </div>
    </div>
  )
}

// ── Step 7: Creating (live terminal) ─────────────────────────────────────────

function Step7({ workspace, onDone, onResult, onGoBack }) {
  const termRef      = useRef(null)
  const containerRef = useRef(null)
  const [status, setStatus] = useState(null) // null | 'success' | 'failure'

  useEffect(() => {
    const term = new Terminal({
      theme: { background: '#030712', foreground: '#f3f4f6', cursor: '#6366f1', selectionBackground: '#374151' },
      fontFamily: "'JetBrains Mono', 'Fira Code', monospace",
      fontSize: 13, lineHeight: 1.5, convertEol: true, scrollback: 2000,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    fit.fit()
    termRef.current = term

    let resolved = false
    function resolve(result) {
      if (resolved) return
      resolved = true
      setStatus(result)
      onResult(result)
    }

    const ws = openCreateSocket(workspace)
    ws.addEventListener('message', e => {
      term.write(e.data)
      const text = e.data
      // Success markers written by bootstrap.sh
      if (text.includes('is ready!') || text.includes('[OK]') && text.includes('workspace ready')) {
        resolve('success')
      }
      // Failure markers
      if (text.includes('[ERROR]') || text.includes('✗') || text.includes('failed')) {
        resolve('failure')
      }
    })
    ws.addEventListener('error', () => {
      term.write('\r\n\x1b[31m[connection error]\x1b[0m\r\n')
      resolve('failure')
    })
    ws.addEventListener('close', () => {
      // If closed without an explicit result, check terminal output for success
      if (!resolved) resolve('success') // bootstrap finishing = success unless error was already flagged
    })

    return () => { term.dispose(); ws.close() }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const isSuccess = status === 'success'
  const isFailure = status === 'failure'
  const isDone    = status !== null

  return (
    <div className="space-y-4">
      <StepHeader step={7} title="Result" subtitle={
        !isDone ? 'Bootstrap in progress — this takes a few seconds.' :
        isSuccess ? 'Workspace created successfully.' :
        'Workspace creation failed — review the output above.'
      } />

      <div ref={containerRef} className="rounded-xl overflow-hidden" style={{ height: 320 }} />

      {isDone && (
        <div className={`flex items-center gap-3 px-4 py-3 rounded-xl border ${
          isSuccess
            ? 'bg-green-950/40 border-green-700/40 text-green-300'
            : 'bg-red-950/40 border-red-700/40 text-red-300'
        }`}>
          <span className="text-lg">{isSuccess ? '✓' : '✗'}</span>
          <span className="text-sm font-medium">
            {isSuccess ? 'Workspace created successfully.' : 'Creation failed. Review output above for details.'}
          </span>
        </div>
      )}

      {isDone && (
        <div className="flex gap-3">
          {isFailure && (
            <button
              onClick={onGoBack}
              className="flex-1 py-2.5 border border-gray-600 hover:border-gray-400 text-gray-300 hover:text-white font-medium rounded-lg transition-colors"
            >
              ← Go back &amp; fix
            </button>
          )}
          <button
            onClick={onDone}
            disabled={!isSuccess}
            className={`flex-1 py-2.5 font-medium rounded-lg transition-colors ${
              isSuccess
                ? 'bg-brand-600 hover:bg-brand-700 text-white'
                : 'bg-gray-800 text-gray-600 cursor-not-allowed border border-gray-700'
            }`}
          >
            Open workspace →
          </button>
        </div>
      )}
    </div>
  )
}

// ── Stepper nav ───────────────────────────────────────────────────────────────

const STEPS = ['Project', 'Stack', 'Services', 'Environments', 'Backup', 'Review', 'Result']

function Stepper({ current, maxVisited, onStepClick }) {
  return (
    <div className="flex items-center gap-0 mb-8">
      {STEPS.map((label, i) => {
        const n = i + 1
        const state    = n < current ? 'done' : n === current ? 'active' : 'pending'
        // Step 7 (Result) is never clickable — can't skip back to it
        const clickable = n <= maxVisited && n !== current && n < 7
        return (
          <div key={label} className="flex items-center flex-1 last:flex-none">
            <div className="flex flex-col items-center gap-1">
              <button
                type="button"
                onClick={() => clickable && onStepClick(n)}
                disabled={!clickable}
                className={`w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold transition-colors ${
                  state === 'done'   ? 'bg-brand-600 text-white' :
                  state === 'active' ? 'bg-brand-600 text-white ring-2 ring-brand-400 ring-offset-2 ring-offset-gray-950' :
                  'bg-gray-800 text-gray-500 border border-gray-700'
                } ${clickable ? 'cursor-pointer hover:ring-2 hover:ring-brand-400 hover:ring-offset-1 hover:ring-offset-gray-950' : 'cursor-default'}`}
                title={clickable ? `Go to step ${n}: ${label}` : undefined}
              >
                {state === 'done' ? '✓' : n}
              </button>
              <span className={`text-xs ${state === 'active' ? 'text-white' : clickable ? 'text-gray-400' : 'text-gray-500'}`}>{label}</span>
            </div>
            {i < STEPS.length - 1 && (
              <div className={`flex-1 h-px mx-2 mb-4 ${n < current ? 'bg-brand-600' : 'bg-gray-700'}`} />
            )}
          </div>
        )
      })}
    </div>
  )
}

// ── Main wizard page ──────────────────────────────────────────────────────────

const DEFAULT_DATA = {
  name: '', registry: '',
  stackType: 'prebuilt', template: '', images: [{ ...DEFAULT_IMAGE }], customEnvVars: {},
  backend: 'laravel', frontend: 'none', database: 'postgres', redis: false, garage: false,
  default_host_id: 0, // Phase 7: default host for environments (0 = local)
  environments: [{ ...DEFAULT_ENV, name: 'dev' }],
  volumes: [],
  templateVolumes: [], // read-only display list populated from selected prebuilt template
  backup: { enabled: true, targetId: null, targetName: 'local', schedule: 'daily', retention: 7 },
}

export default function NewWorkspacePage() {
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [step, setStep]           = useState(1)
  const [data, setData]           = useState(DEFAULT_DATA)
  const [errors, setErrors]       = useState({})
  const [nameConflict, setNameConflict] = useState(null)
  const [maxVisited, setMaxVisited]     = useState(1) // highest step reached — enables stepper navigation
  const [createResult, setCreateResult] = useState(null) // null | 'success' | 'failure'

  function update(key, value) {
    if (key === '_distributeVars') {
      // Distribute template default vars to all current environments
      setData(prev => ({
        ...prev,
        environments: prev.environments.map(e => ({ ...e, vars: { ...value, ...(e.vars || {}) } })),
      }))
      return
    }
    setData(prev => ({ ...prev, [key]: value }))
    setErrors(prev => ({ ...prev, [key]: undefined }))
  }

  function validate() {
    const e = {}
    if (!data.name.trim()) e.name = 'Required'
    else if (!/^[a-z0-9][a-z0-9\-]{0,62}$/.test(data.name)) e.name = 'Lowercase letters, numbers, hyphens only'
    else if (nameConflict) e.name = nameConflict // uniqueness check propagated from Step1
    // Registry only matters for custom (build) stacks — and lives on step 2 now.
    if (step === 2 && data.stackType === 'custom' && !data.registry.trim()) e.registry = 'Required'
    if (step === 2 && data.stackType === 'prebuilt' && !data.template) e.template = 'Select a template'
    if (step === 2 && data.stackType === 'image' && data.images.every(img => !img.name || !img.image)) e.images = 'Add at least one service with a name and image'
    // Steps 3/4 are Services then Environments (swapped to match Edit workspace).
    if (step === 3) {
      const badVols = data.volumes.filter(v => v.name && !v.mountPath)
      if (badVols.length > 0) e.volumes = 'Each volume needs a mount path'
    }
    if (step === 4 && data.environments.some(e => !e.name.trim())) e.envs = 'All environments need a name'
    setErrors(e)
    return Object.keys(e).length === 0
  }

  function next() {
    if (!validate()) return
    setStep(s => { const n = s + 1; setMaxVisited(m => Math.max(m, n)); return n })
  }

  function buildPayload() {
    const isImage = data.stackType === 'prebuilt' || data.stackType === 'image'
    return {
      name: data.name.trim(),
      // Registry is only used to tag/push built images (custom stacks). Image and
      // prebuilt stacks pull images directly, so send empty to avoid storing a
      // value that's never read.
      registry: data.stackType === 'custom' ? data.registry.trim() : '',
      type: isImage ? 'image' : 'custom',
      template: data.stackType === 'prebuilt' ? data.template : '',
      images: data.stackType === 'image'
        ? data.images.filter(img => img.name && img.image).map(img => {
            const ports = (img.portMappings || []).filter(p => p.container)
            return {
              name: img.name, image: img.image, tag: img.tag || 'latest',
              port: parseInt((ports[0] || {}).container) || 0,
              host_port: (ports[0] || {}).host || '',
              extra_ports: ports.slice(1).filter(p => p.host && p.container).map(p => `${p.host}:${p.container}`),
              link_ports: ports.filter(p => p.link && p.host).map(p => p.host),
              volumes: (img.volumes || []).filter(v => typeof v === 'string' ? v.includes(':') : false),
              depends_on: [],
              healthcheck: img.healthcheck || '',
              healthcheck_config: img.healthcheck_config || {},
            }
          })
        : [],
      custom_env_vars: data.stackType === 'image' ? data.customEnvVars : {},
      initial_env_vars: {}, // vars now per-environment via environments[].vars
      named_volumes: data.volumes.filter(v => v.name && v.mountPath),
      backup: {
        enabled: data.backup.enabled,
        target_id: data.backup.targetId,
        target_name: data.backup.targetName,
        schedule: data.backup.schedule,
        retention: data.backup.retention,
      },
      backend: isImage ? '' : data.backend,
      frontend: isImage ? 'none' : data.frontend,
      database: isImage ? 'none' : data.database,
      redis: isImage ? false : data.redis,
      garage: isImage ? false : data.garage,
      environments: data.environments.filter(e => e.name).map(e => ({
        ...e,
        ssl_enabled: e.traefik && !!e.domain && !!e.ssl_enabled,
        vars: e.vars || {},
        host_id: e.host_id ?? (data.default_host_id || 0),
      })),
      versions: {},
    }
  }

  function handleDone() {
    qc.invalidateQueries({ queryKey: ['workspaces'] })
    navigate(`/workspaces/${data.name}`)
  }

  return (
    <div className="min-h-screen bg-gray-950 flex flex-col">
      {/* Nav bar (same style as Layout) */}
      <nav className="border-b border-gray-800 bg-gray-900 shrink-0">
        <div className="px-6 h-12 flex items-center justify-between">
          <div className="flex items-center gap-2.5">
            <img src="/rigger-icon.png" alt="Rigger" className="w-8 h-8 rounded-lg" />
            <span className="text-gray-400 text-sm">New workspace</span>
          </div>
          {step < 7
            ? <button onClick={() => navigate(-1)} className="text-sm font-medium px-4 py-1.5 rounded-lg border border-amber-700/60 bg-amber-900/30 hover:bg-amber-800/50 text-amber-300 transition-colors">Cancel</button>
            : <button onClick={() => navigate(-1)} className="text-sm font-medium px-4 py-1.5 rounded-lg border border-gray-700 bg-gray-800 hover:bg-gray-700 text-gray-300 transition-colors">Close</button>
          }
        </div>
      </nav>

      {/* Wizard body */}
      <div className="flex-1 flex items-start justify-center p-8">
        <div className="w-full max-w-2xl">
          <Stepper current={step} maxVisited={maxVisited} onStepClick={n => setStep(n)} />

          <div className="bg-gray-900 border border-gray-800 rounded-2xl p-8">
            {step === 1 && <Step1 data={data} onChange={update} errors={errors} onConflict={setNameConflict} />}
            {step === 2 && <Step2 data={data} onChange={update} errors={errors} />}
            {/* Services (3) then Environments (4) — define the stack shape before
                its environments. Step4=Services component, Step3=Environments. */}
            {step === 3 && <Step4 data={data} onChange={update} errors={errors} />}
            {step === 4 && <Step3 data={data} onChange={update} />}
            {step === 5 && <Step5 data={data} onChange={update} />}
            {step === 6 && <Step6 data={data} />}
            {step === 7 && (
              <Step7
                workspace={buildPayload()}
                onDone={handleDone}
                onResult={result => setCreateResult(result)}
                onGoBack={() => { setCreateResult(null); setStep(6) }}
              />
            )}

            {/* Navigation buttons (hidden on step 7) */}
            {step < 7 && (
              <div className="flex items-center justify-between mt-8 pt-6 border-t border-gray-800">
                <button
                  type="button"
                  onClick={() => step > 1 ? setStep(s => s - 1) : navigate(-1)}
                  className={`text-sm font-medium px-4 py-2 rounded-lg border transition-colors ${
                    step === 1
                      ? 'border-amber-700/60 bg-amber-900/30 hover:bg-amber-800/50 text-amber-300'
                      : 'border-gray-700 bg-gray-800 hover:bg-gray-700 text-gray-300'
                  }`}
                >
                  {step === 1 ? 'Cancel' : '← Back'}
                </button>
                <button
                  type="button"
                  onClick={step === 6 ? () => { if (validate()) { setMaxVisited(7); setStep(7) } } : next}
                  disabled={step === 1 && !!nameConflict}
                  className={`text-white text-sm font-semibold px-6 py-2 rounded-lg transition-colors ${
                    step === 1 && nameConflict
                      ? 'bg-brand-800 text-brand-400 cursor-not-allowed'
                      : 'bg-brand-600 hover:bg-brand-700'
                  }`}
                >
                  {step === 6 ? 'Create workspace' : 'Continue →'}
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
