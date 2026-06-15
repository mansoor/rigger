import { useState, useRef } from 'react'
import Layout from '../components/Layout'
import VerticalTabs from '../components/VerticalTabs'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  saveToolTemplate, fetchTemplates, fetchTemplateDraft, fetchTemplateRaw,
  startWorkspaceBackup, getBackupJob,
  listWorkspaceArchives, deleteWorkspaceArchive, syncWorkspaceArchive,
  restoreWorkspaceFromArchive, uploadWorkspaceArchive, fetchProjects,
  createWorkspaceSnapshot, fetchWorkspaceSnapshots, deleteWorkspaceSnapshot, rollbackWorkspaceSnapshot, uploadWorkspaceSnapshot,
  fetchConfig, migrateEnvData,
} from '../lib/api'
import { useAuthStore } from '../store/auth'
import { useWorkspaceStore } from '../store/workspace'
import { useConfirm } from '../context/ConfirmContext'

// ── docker-compose → Rigger template converter ──────────────────────────────────
//
// Parses a docker-compose.yml (services block) into a Rigger template JSON.
// Handles: image/tag, ports, volumes (named→bind), environment (list+map),
// depends_on (list+map), healthcheck.test, command, restart.

function unquote(s) {
  s = s.trim()
  if ((s.startsWith('"') && s.endsWith('"')) ||
      (s.startsWith("'") && s.endsWith("'"))) {
    return s.slice(1, -1)
  }
  return s
}

// Split "source:path" or "source:path:ro" correctly (source may contain colons on Windows)
function splitVolume(v) {
  const parts = v.split(':')
  if (parts.length >= 2) {
    const last = parts[parts.length - 1]
    const mode = (last === 'ro' || last === 'rw') ? last : 'rw'
    const pathParts = mode !== 'rw' ? parts.slice(1, -1) : parts.slice(1)
    return { source: parts[0], path: pathParts.join(':'), mode }
  }
  return { source: v, path: '', mode: 'rw' }
}

// Convert a named volume source to a bind mount path
function toBindMount(source, path, mode) {
  const isNamed = !source.startsWith('./') && !source.startsWith('/') && !source.startsWith('$')
  const mountSrc = isNamed ? `./volumes/${source}` : source
  return mode === 'ro' ? `${mountSrc}:${path}:ro` : `${mountSrc}:${path}`
}

// Split image ref into image + tag
function splitImage(ref) {
  ref = unquote(ref)
  // Handle registry/image:tag (don't split on registry colon with port)
  const lastSlash = ref.lastIndexOf('/')
  const afterSlash = ref.slice(lastSlash + 1)
  const colonIdx = afterSlash.lastIndexOf(':')
  if (colonIdx > 0) {
    const base = ref.slice(0, lastSlash + 1) + afterSlash.slice(0, colonIdx)
    const tag  = afterSlash.slice(colonIdx + 1)
    return { image: base, tag }
  }
  return { image: ref, tag: 'latest' }
}

// Extract CMD-SHELL string from healthcheck test formats:
//   test: ["CMD-SHELL", "command"]
//   test: ["CMD", "curl", "-f", "http://localhost"]
//   test: CMD-SHELL command
function extractHealthcheck(val) {
  val = val.trim()
  // Flow sequence: ["CMD-SHELL", "..."]
  const seqMatch = val.match(/^\["CMD(?:-SHELL)?",\s*"(.+)"\]$/)
  if (seqMatch) return seqMatch[1]
  // Simple string
  if (val.startsWith('CMD-SHELL ')) return val.slice(10)
  if (val.startsWith('CMD ')) return val.slice(4)
  return val
}

// Parse the services block from docker-compose YAML.
// Returns array of raw service objects.
function parseServices(text) {
  const lines = text.split('\n')
  const services = []
  let inServices = false
  let cur = null  // current service object

  // We process line by line, tracking indentation.
  // Services block: indent-2 keys are service names, indent-4 keys are properties,
  // indent-6+ are list items or nested map entries.

  let curBlock = null    // 'ports' | 'volumes' | 'environment' | 'depends_on' | 'healthcheck' | null
  let hcBlock  = null    // 'test' | other healthcheck sub-key

  for (let li = 0; li < lines.length; li++) {
    const raw = lines[li]
    const stripped = raw.trimEnd()
    if (!stripped || /^\s*#/.test(stripped)) continue

    const indent  = raw.search(/\S/)
    const content = stripped.trimStart()

    // Detect top-level blocks
    if (indent === 0) {
      inServices = content === 'services:'
      curBlock = null
      if (!inServices) cur = null
      continue
    }

    if (!inServices) continue

    // Service name (indent 2)
    if (indent === 2 && content.endsWith(':') && !content.startsWith('-')) {
      cur = {
        name: content.slice(0, -1),
        image: '', tag: 'latest',
        ports: [], volumes: [], environment: {},
        depends_on: [], healthcheck: '',
        command: '', restart: 'unless-stopped',
      }
      services.push(cur)
      curBlock = null
      continue
    }

    if (!cur) continue

    // Service property (indent 4)
    if (indent === 4 && !content.startsWith('-')) {
      curBlock = null
      hcBlock  = null
      const colon = content.indexOf(':')
      if (colon === -1) continue
      const key = content.slice(0, colon).trim()
      const val = content.slice(colon + 1).trim()

      switch (key) {
        case 'image':   { const p = splitImage(val); cur.image = p.image; cur.tag = p.tag; break }
        case 'command': cur.command = val; break
        case 'restart': cur.restart = val; break
        case 'ports':       curBlock = 'ports';       break
        case 'volumes':     curBlock = 'volumes';     break
        case 'environment': curBlock = 'environment'; break
        case 'depends_on':  curBlock = 'depends_on';  break
        case 'healthcheck': curBlock = 'healthcheck'; break
        default: break
      }
      continue
    }

    // Healthcheck sub-keys (indent 6 inside healthcheck block)
    if (curBlock === 'healthcheck' && indent === 6 && !content.startsWith('-')) {
      const colon = content.indexOf(':')
      if (colon !== -1) {
        const key = content.slice(0, colon).trim()
        const val = content.slice(colon + 1).trim()
        if (key === 'test') {
          cur.healthcheck = extractHealthcheck(val)
          hcBlock = 'test'
        }
      }
      continue
    }

    // List-form healthcheck test continuation (e.g. multi-line flow sequence)
    if (curBlock === 'healthcheck' && hcBlock === 'test' && indent >= 8) continue

    // Block items (indent 6, starting with "- " or map entries for depends_on/environment)
    if (indent === 6) {
      if (content.startsWith('- ')) {
        const item = unquote(content.slice(2).trim())

        if (curBlock === 'ports') {
          cur.ports.push(item)
        } else if (curBlock === 'volumes') {
          cur.volumes.push(item)
        } else if (curBlock === 'environment') {
          // List form: KEY=value or KEY=
          const eqIdx = item.indexOf('=')
          if (eqIdx !== -1) {
            cur.environment[item.slice(0, eqIdx)] = item.slice(eqIdx + 1)
          } else {
            cur.environment[item] = ''
          }
        } else if (curBlock === 'depends_on') {
          cur.depends_on.push(item)
        }
      } else if (!content.startsWith('-')) {
        // Map entry (e.g. environment: KEY: value, depends_on: svc: {condition: …})
        const colon = content.indexOf(':')
        if (colon !== -1) {
          const key = content.slice(0, colon).trim()
          const val = content.slice(colon + 1).trim()

          if (curBlock === 'environment') {
            cur.environment[key] = unquote(val)
          } else if (curBlock === 'depends_on') {
            // Map-form depends_on: service names are the keys
            if (!cur.depends_on.includes(key)) cur.depends_on.push(key)
          }
        }
      }
    }
  }

  return services
}

// Convert parsed services → Rigger template
function buildTemplate(services, templateName) {
  const allEnvVars = {}   // collected from all services: key → default value

  const images = services.map(svc => {
    // Ports: first mapping → port/host_port, rest → extra_ports
    const portObjs = svc.ports.map(p => {
      p = unquote(p)
      const parts = p.split(':')
      if (parts.length >= 2) {
        return { host: parts[parts.length - 2], container: parts[parts.length - 1] }
      }
      return { host: '', container: parts[0] }
    })
    const firstPort   = portObjs[0] || { host: '', container: '' }
    const extraPorts  = portObjs.slice(1).map(p =>
      p.host ? `${p.host}:${p.container}` : p.container)

    // Volumes: convert named volumes to bind mounts
    const volumes = svc.volumes
      .map(v => {
        v = unquote(v)
        if (!v.includes(':')) return v  // bare volume name, keep as-is
        const { source, path, mode } = splitVolume(v)
        return toBindMount(source, path, mode)
      })
      .filter(Boolean)

    // Environment: build env_vars with ${VAR} refs; collect defaults
    const envVars = {}
    for (const [k, v] of Object.entries(svc.environment)) {
      // If value looks like it's already a variable ref, keep it
      if (v.startsWith('${') || v.startsWith('$')) {
        envVars[k] = v
      } else {
        envVars[k] = `\${${k}}`
        allEnvVars[k] = v  // original value becomes the default
      }
    }

    // Parse host_port — may be a var ref like ${GHOST_PORT}
    const rawHostPort = firstPort.host
    const hostPortRef = rawHostPort
      ? (rawHostPort.startsWith('$') ? rawHostPort : rawHostPort)
      : ''

    // If host_port was a bare number, keep it; if it's a ${VAR}, keep as env var ref
    return {
      name:      svc.name,
      image:     svc.image,
      tag:       svc.tag,
      port:      parseInt(firstPort.container) || 0,
      host_port: hostPortRef,
      volumes,
      env_vars:  envVars,
      depends_on: svc.depends_on,
      extra_ports: extraPorts,
      healthcheck: svc.healthcheck,
      healthcheck_config: svc.healthcheck ? {
        interval: '30s', timeout: '10s', retries: '3', start_period: '30s',
      } : {},
      restart: svc.restart || 'unless-stopped',
      command: svc.command,
    }
  })

  const name = templateName.trim().toLowerCase().replace(/[^a-z0-9-]/g, '-') || 'my-stack'

  return {
    name,
    label: templateName.trim() || 'My Stack',
    description: '',
    tags: [],
    images,
    default_env_vars: allEnvVars,
  }
}

function convertCompose(yamlText, templateName) {
  try {
    const services = parseServices(yamlText)
    if (!services.length) return { error: 'No services found. Make sure the compose file has a "services:" block.' }
    return { result: buildTemplate(services, templateName) }
  } catch (e) {
    return { error: `Parse error: ${e.message}` }
  }
}

// ── UI ────────────────────────────────────────────────────────────────────────

const PLACEHOLDER = `services:
  app:
    image: ghost:5-alpine
    ports:
      - "\${GHOST_PORT}:2368"
    environment:
      url: \${GHOST_URL}
    volumes:
      - ghost_data:/var/lib/ghost/content
    depends_on:
      - db
    healthcheck:
      test: ["CMD-SHELL", "curl -sf http://localhost:2368/ -o /dev/null || exit 1"]

  db:
    image: mysql:8.0
    environment:
      MYSQL_ROOT_PASSWORD: changeme
      MYSQL_DATABASE: ghost
      MYSQL_USER: ghost_user
      MYSQL_PASSWORD: changeme
    volumes:
      - db_data:/var/lib/mysql`

// ── Color-coded JSON editor ─────────────────────────────────────────────────────
// A transparent <textarea> layered over a syntax-highlighted <pre>, plus a line-
// number gutter. All three share the exact font/padding/line-height, so the
// (invisible) caret, the colored text and the line numbers stay aligned. The
// editor has a fixed height and scrolls internally; the textarea is the scroller
// and its onScroll syncs the highlight <pre> and the gutter. No external deps.

function escapeHtml(s) {
  return s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
}

// Token classes: keys (sky), string values (emerald), numbers (amber),
// booleans/null (purple), punctuation (gray).
const JSON_TOKEN_RE = /("(?:\\.|[^"\\])*")(\s*:)?|(-?\d+\.?\d*(?:[eE][+-]?\d+)?)|\b(true|false|null)\b|([{}[\],:])/g

function highlightJson(code) {
  let out = '', last = 0, m
  JSON_TOKEN_RE.lastIndex = 0
  while ((m = JSON_TOKEN_RE.exec(code))) {
    out += escapeHtml(code.slice(last, m.index))
    if (m[1] && m[2] !== undefined) {            // "key":
      out += `<span class="text-sky-300">${escapeHtml(m[1])}</span><span class="text-content-subtle">${escapeHtml(m[2])}</span>`
    } else if (m[1]) {                           // "string value"
      out += `<span class="text-emerald-300">${escapeHtml(m[1])}</span>`
    } else if (m[3]) {                           // number
      out += `<span class="text-warning-fg">${escapeHtml(m[3])}</span>`
    } else if (m[4]) {                           // true | false | null
      out += `<span class="text-purple-300">${escapeHtml(m[4])}</span>`
    } else if (m[5]) {                           // punctuation
      out += `<span class="text-content-subtle">${escapeHtml(m[5])}</span>`
    }
    last = m.index + m[0].length
  }
  out += escapeHtml(code.slice(last))
  return out
}

function JsonEditor({ value, onChange, valid, height = '20rem' }) {
  const taRef = useRef(null)
  const preRef = useRef(null)
  const gutterRef = useRef(null)

  const lineCount = value ? value.split('\n').length : 1
  const gutter = Array.from({ length: lineCount }, (_, i) => i + 1).join('\n')

  function sync() {
    const ta = taRef.current
    if (!ta) return
    if (preRef.current)   { preRef.current.scrollTop = ta.scrollTop; preRef.current.scrollLeft = ta.scrollLeft }
    if (gutterRef.current) gutterRef.current.scrollTop = ta.scrollTop
  }

  const shared = 'm-0 px-3 py-3 text-xs font-mono leading-relaxed whitespace-pre'
  return (
    <div
      className={`relative w-full flex rounded-xl bg-canvas border overflow-hidden ${valid ? 'border-border-strong focus-within:border-brand-500' : 'border-danger-border/60 focus-within:border-danger'}`}
      style={{ height }}
    >
      {/* Line-number gutter (scrolls in sync, no scrollbar of its own) */}
      <pre ref={gutterRef} aria-hidden="true"
        className="m-0 py-3 pl-3 pr-2 text-xs font-mono leading-relaxed text-right text-content-faint select-none overflow-hidden whitespace-pre border-r border-border/80 bg-canvas"
        style={{ minWidth: '2.75rem' }}
      >{gutter}</pre>

      {/* Code area: highlighted <pre> behind, transparent <textarea> on top */}
      <div className="relative flex-1 overflow-hidden">
        <pre ref={preRef} aria-hidden="true"
          className={`${shared} absolute inset-0 overflow-hidden text-content pointer-events-none`}
          dangerouslySetInnerHTML={{ __html: highlightJson(value) + '\n' }} />
        <textarea
          ref={taRef}
          value={value}
          onChange={e => onChange(e.target.value)}
          onScroll={sync}
          spellCheck={false}
          wrap="off"
          className={`${shared} absolute inset-0 w-full h-full resize-none overflow-auto bg-transparent text-transparent caret-white focus:outline-none`}
        />
      </div>
    </div>
  )
}

// ── Select-image-workspace modal (Template Manager source) ──────────────────────
function SelectWorkspaceModal({ workspaces, busy, error, onLoad, onClose }) {
  const [ws, setWs]   = useState(workspaces[0]?.name || '')
  const [env, setEnv] = useState('')
  const envs = workspaces.find(w => w.name === ws)?.envs || []
  const chosenEnv = env || envs[0] || ''

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">Select an image project</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>
        <p className="text-sm text-content-subtle">
          Pulls the stack's images and environment-variable defaults (secrets masked) into the editor as a draft template.
        </p>

        {workspaces.length === 0 ? (
          <p className="text-sm text-content-muted bg-surface-raised/50 border border-border-strong/60 rounded-lg px-3 py-3">
            No image workspaces found. Only image stacks can become templates.
          </p>
        ) : (
          <>
            <div>
              <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Project</label>
              <select value={ws} onChange={e => { setWs(e.target.value); setEnv('') }}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500">
                {workspaces.map(w => <option key={w.name} value={w.name}>{w.name}</option>)}
              </select>
            </div>
            <div>
              <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
                Environment <span className="normal-case font-normal text-content-subtle">(for env-var defaults)</span>
              </label>
              <select value={chosenEnv} onChange={e => setEnv(e.target.value)}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500">
                {envs.map(en => <option key={en} value={en}>{en}</option>)}
              </select>
            </div>
            {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
            <button onClick={() => onLoad(ws, chosenEnv)} disabled={!ws || busy}
              className="w-full bg-brand-600 hover:bg-brand-700 disabled:opacity-50 disabled:cursor-not-allowed text-white text-sm font-semibold py-2 rounded-lg transition-colors">
              {busy ? 'Loading…' : 'Load into editor'}
            </button>
          </>
        )}
      </div>
    </div>
  )
}

// ── Open-existing-template modal (Template Manager source) ──────────────────────
// Pick an already-saved template and load its full JSON back into the editor so it
// can be revised. Saving with the same name overwrites it (force); renaming saves
// a copy.
function EditTemplateModal({ busy, error, onLoad, onClose }) {
  const { data: templates = [], isLoading } = useQuery({
    queryKey: ['templates'], queryFn: fetchTemplates,
  })
  const sorted = [...templates].sort((a, b) => (a.label || a.name).localeCompare(b.label || b.name))

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">Open an existing template</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>
        <p className="text-sm text-content-subtle">
          Loads the template's full JSON into the editor. Keeping the same <strong className="text-content">name</strong> and saving overwrites it; changing the name saves a copy.
        </p>

        {isLoading ? (
          <p className="text-sm text-content-subtle py-4 text-center">Loading templates…</p>
        ) : sorted.length === 0 ? (
          <p className="text-sm text-content-muted bg-surface-raised/50 border border-border-strong/60 rounded-lg px-3 py-3">
            No saved templates yet. Create one from a source below first.
          </p>
        ) : (
          <div className="max-h-72 overflow-y-auto space-y-1.5 -mx-1 px-1">
            {sorted.map(t => (
              <button key={t.name} onClick={() => onLoad(t.name)} disabled={busy}
                className="w-full text-left px-3 py-2 rounded-lg border border-border-strong bg-surface-raised/40 hover:border-brand-500 hover:bg-surface-raised transition-colors disabled:opacity-50 disabled:cursor-not-allowed">
                <div className="flex items-center gap-2">
                  <span className="text-sm font-semibold text-content-strong">{t.label || t.name}</span>
                  <span className="text-[11px] font-mono text-content-faint">{t.name}</span>
                  <span className="ml-auto text-[11px] text-content-subtle">{t.image_count} service{t.image_count !== 1 ? 's' : ''}</span>
                </div>
                {t.description && <p className="text-xs text-content-subtle mt-0.5 line-clamp-2">{t.description}</p>}
              </button>
            ))}
          </div>
        )}
        {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}
      </div>
    </div>
  )
}

// ── Convert-Docker-Compose modal (Template Manager source) ──────────────────────
// Paste/import a docker-compose.yml and convert it to a Rigger template, which is
// loaded into the editor. Lives in a modal so the editor can use the full width.
function ComposeModal({ onLoad, onClose }) {
  const [input, setInput] = useState('')
  const [name, setName]   = useState('')   // template-name slug derived from an imported filename
  const [error, setError] = useState('')
  const fileRef = useRef(null)
  const btnBase = 'text-xs px-2.5 py-1 rounded border transition-colors'

  async function paste() {
    try { const t = await navigator.clipboard.readText(); if (t) { setInput(t); setError('') } }
    catch { document.getElementById('compose-modal-input')?.focus() }
  }
  function importFile(e) {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = ev => {
      setInput(ev.target.result || ''); setError('')
      if (!name) setName(file.name.replace(/\.(ya?ml|txt)$/i, '').replace(/[^a-z0-9]/gi, '-').toLowerCase() || '')
    }
    reader.readAsText(file)
    e.target.value = '' // reset so the same file can be re-imported
  }
  function convert() {
    if (!input.trim()) return
    const res = convertCompose(input, name || 'my-stack')
    if (res.error) { setError(res.error); return }
    onLoad(res.result) // parent loads it into the editor and closes the modal
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border rounded-xl w-full max-w-3xl p-6 space-y-3" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-content-strong">Convert Docker Compose</h3>
          <button onClick={onClose} className="text-content-subtle hover:text-content-strong text-xl">×</button>
        </div>
        <p className="text-sm text-content-subtle">
          Paste or import a <code className="font-mono text-xs">docker-compose.yml</code>; converting turns its services
          into a Rigger template and loads it into the editor for review.
        </p>

        <div className="flex items-center justify-between">
          <label className="text-sm font-semibold text-content">docker-compose.yml</label>
          <div className="flex items-center gap-2">
            <button onClick={paste} className={`${btnBase} border-border-strong text-content-muted hover:text-content hover:border-border-strong`}>⎘ Paste</button>
            <button onClick={() => fileRef.current?.click()} className={`${btnBase} border-border-strong text-content-muted hover:text-content hover:border-border-strong`}>↑ Import file</button>
            <button onClick={() => { setInput(PLACEHOLDER); setError('') }} className="text-xs text-brand-400 hover:text-brand-300 transition-colors">Example</button>
            <input ref={fileRef} type="file" accept=".yml,.yaml,.txt" onChange={importFile} className="hidden" />
          </div>
        </div>

        <textarea
          id="compose-modal-input"
          value={input}
          onChange={e => { setInput(e.target.value); setError('') }}
          placeholder={PLACEHOLDER}
          spellCheck={false}
          className="w-full px-3 py-3 bg-canvas border border-border-strong rounded-xl text-content text-xs font-mono placeholder-content-faint focus:outline-none focus:border-brand-500 resize-y leading-relaxed"
          style={{ minHeight: '24rem' }}
        />

        {error && (
          <div className="rounded-lg bg-danger-subtle/40 border border-danger-border/40 px-3 py-2">
            <p className="text-danger-fg text-xs font-medium">Conversion failed</p>
            <p className="text-danger-fg/70 text-xs mt-0.5">{error}</p>
          </div>
        )}

        <button
          onClick={convert}
          disabled={!input.trim()}
          className={`w-full py-2 text-sm font-semibold rounded-lg transition-colors ${
            input.trim() ? 'bg-brand-600 hover:bg-brand-700 text-white' : 'bg-surface-raised text-content-faint cursor-not-allowed'
          }`}
        >Convert &amp; load into editor →</button>
      </div>
    </div>
  )
}

function ComposeToTemplate() {
  const [tplJson, setTplJson]       = useState('')      // editable template JSON — the source of truth
  const [tagsText, setTagsText]     = useState('')      // comma-separated tags helper (synced on load)
  const [validation, setValidation] = useState(null)    // null | {checking} | {ok:true,...} | {ok:false,errors:[]}
  const [copied, setCopied]         = useState(false)
  const [saveState, setSaveState]   = useState(null)    // null | 'saving' | 'saved' | { error }
  const tplFileRef                  = useRef(null)       // template upload
  const [composeModalOpen, setComposeModalOpen] = useState(false)  // docker-compose convert modal
  const qc = useQueryClient()

  // "Open existing template" source — re-open a saved template to revise it. When
  // set, saving under the same name overwrites the file (force); renaming saves a copy.
  const [editingName, setEditingName] = useState(null)
  const [editModalOpen, setEditModalOpen] = useState(false)
  const [editBusy, setEditBusy] = useState(false)
  const [editError, setEditError] = useState('')

  // "From a workspace" source — pick an image stack (in a modal) and pull it in.
  const [wsModalOpen, setWsModalOpen] = useState(false)
  const [wsBusy, setWsBusy]   = useState(false)
  const [wsError, setWsError] = useState('')
  const currentWs = useWorkspaceStore(s => s.current)
  const { data: allProjects = [] } = useQuery({
    queryKey: ['projects', currentWs], queryFn: () => fetchProjects(currentWs),
    enabled: !!currentWs, staleTime: 30_000,
  })
  // Only image stacks can become templates (project.type lives in the nested config).
  const imageWorkspaces = allProjects.filter(w => w.config?.project?.type === 'image')

  // Parse the editable JSON for the summary chips, metadata helpers and download.
  let parsed = null
  try { parsed = tplJson.trim() ? JSON.parse(tplJson) : null } catch { parsed = null }
  const hasContent = tplJson.trim().length > 0

  // Any edit to the template (textarea, a helper field, convert or upload) clears
  // the last validation result, so Save stays disabled until the user re-validates.
  function editJson(next) {
    setTplJson(next)
    setValidation(null)
    setSaveState(null)
    setCopied(false)
  }

  // Load a template object into the editor (from Convert, Upload or Workspace).
  // These are all "new template" sources, so clear any prior edit-existing target.
  function loadTemplate(tpl) {
    editJson(JSON.stringify(tpl, null, 2))
    setTagsText(Array.isArray(tpl?.tags) ? tpl.tags.join(', ') : '')
    setEditingName(null)
  }

  // Open an already-saved template back into the editor for revision.
  async function loadExistingTemplate(name) {
    if (!name) return
    setEditBusy(true); setEditError('')
    try {
      const tpl = await fetchTemplateRaw(name)
      loadTemplate(tpl)
      setEditingName(name)   // mark as editing AFTER loadTemplate (which clears it)
      setEditModalOpen(false)
    } catch (e) {
      setEditError(e?.response?.data?.error || e.message)
    } finally {
      setEditBusy(false)
    }
  }

  // Pull an image workspace's stack (images + masked env-var defaults) into the
  // editor as a draft. Name/label are seeded from the workspace as an editable
  // starting point; the user reviews, names and validates before saving.
  async function loadFromWorkspace(wsName, env) {
    if (!wsName) return
    setWsBusy(true); setWsError('')
    try {
      const draft = await fetchTemplateDraft(currentWs, wsName, env)
      const slug = wsName.toLowerCase().replace(/[^a-z0-9-]+/g, '-').replace(/^-+|-+$/g, '')
      loadTemplate({ ...draft, name: draft.name || slug, label: draft.label || wsName })
      setWsModalOpen(false)
    } catch (e) {
      setWsError(e?.response?.data?.error || e.message)
    } finally {
      setWsBusy(false)
    }
  }

  // Patch one top-level key on the parsed template and write it back to the editor.
  function patchField(key, value) {
    if (!parsed) return
    editJson(JSON.stringify({ ...parsed, [key]: value }, null, 2))
  }

  // Upload an existing template .json straight into the editor
  function uploadTemplateFile(e) {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = ev => {
      const text = ev.target.result || ''
      editJson(text)
      setEditingName(null)
      try { const obj = JSON.parse(text); setTagsText(Array.isArray(obj?.tags) ? obj.tags.join(', ') : '') } catch { setTagsText('') }
    }
    reader.readAsText(file)
    e.target.value = ''  // reset so the same file can be re-uploaded
  }

  function copyResult() {
    if (!hasContent) return
    navigator.clipboard.writeText(tplJson).then(() => {
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    })
  }

  function downloadResult() {
    if (!parsed) return
    const blob = new Blob([tplJson], { type: 'application/json' })
    const a = document.createElement('a')
    a.href = URL.createObjectURL(blob)
    a.download = `${parsed.name || 'template'}.json`
    a.click()
    URL.revokeObjectURL(a.href)
  }

  // Validate the template structure and confirm the name is unique. Success is
  // what unlocks Save.
  async function runValidate() {
    setValidation({ checking: true })
    let tpl
    try { tpl = JSON.parse(tplJson) } catch (e) {
      setValidation({ ok: false, errors: ['Invalid JSON — ' + e.message] })
      return
    }
    const errors = []
    const nm = (tpl?.name || '').toString().trim()
    if (!nm) errors.push('Missing "name".')
    else if (!/^[a-z0-9-]+$/.test(nm)) errors.push('"name" must contain only lowercase letters, digits and hyphens.')
    if (!(tpl?.label || '').toString().trim()) errors.push('Missing "label".')
    if (!Array.isArray(tpl?.images) || tpl.images.length === 0) errors.push('"images" must be a non-empty array.')
    else tpl.images.forEach((img, i) => {
      if (!img?.name)  errors.push(`images[${i}] is missing "name".`)
      if (!img?.image) errors.push(`images[${i}] is missing "image".`)
    })
    // Name uniqueness — only checked once the name itself is well-formed. When
    // re-editing a template, keeping its own name is allowed (it overwrites).
    if (nm && /^[a-z0-9-]+$/.test(nm) && nm !== editingName) {
      try {
        const existing = await fetchTemplates()
        if ((existing || []).some(t => t.name === nm)) {
          errors.push(`A template named "${nm}" already exists — choose a different "name".`)
        }
      } catch {
        errors.push('Could not verify name uniqueness (failed to load existing templates).')
      }
    }
    const overwrite = nm === editingName
    setValidation(errors.length ? { ok: false, errors } : { ok: true, name: nm, services: tpl.images.length, overwrite })
  }

  async function saveAsTemplate() {
    if (!validation?.ok || !parsed) return
    setSaveState('saving')
    try {
      // Overwrite (force) only when saving back over the template being edited.
      const force = !!editingName && parsed.name === editingName
      await saveToolTemplate(parsed.name, parsed, force)
      qc.invalidateQueries({ queryKey: ['templates'] })
      setEditingName(parsed.name)  // now editing this saved name (re-save keeps overwriting)
      setSaveState('saved')
    } catch (err) {
      setSaveState({ error: err?.response?.data?.error || err.message })
    }
  }

  const btnBase = 'text-xs px-2.5 py-1 rounded border transition-colors'

  return (
    <div className="space-y-6">
      {/* Description */}
      <div className="bg-surface-raised/50 border border-border-strong/60 rounded-xl p-4 text-sm text-content-muted leading-relaxed">
        Create or revise a reusable prebuilt template. Start from a source —{' '}
        <strong className="text-content">Convert Docker Compose</strong>, <strong className="text-content">Select image
        project</strong>, <strong className="text-content">Open existing template</strong> (to revise one you already
        saved), or <strong className="text-content">Upload template</strong> — then edit the JSON, fill in
        name / label / description / tags, and <strong className="text-content">Validate</strong> (which also checks the
        name is unique) to unlock <strong className="text-content">Save</strong>.
      </div>

      {/* ── Template editor (full width) ── */}
      <div className="space-y-2">
        {/* Editor toolbar */}
        <div className="flex items-center justify-between gap-2 flex-wrap">
          <div className="flex items-center gap-2">
            <label className="text-sm font-semibold text-content">Rigger template JSON</label>
            {editingName && (
              <span className="text-[11px] px-2 py-0.5 rounded-full bg-brand-950 text-brand-300 border border-brand-600/50">
                ✎ editing <span className="font-mono">{editingName}</span> — saving overwrites it
              </span>
            )}
          </div>
          <div className="flex items-center gap-2 flex-wrap">
            <button onClick={() => setComposeModalOpen(true)}
              className={`${btnBase} border-border-strong text-content-muted hover:text-content hover:border-border-strong`}>
              ⇄ Convert Docker Compose
            </button>
            <button onClick={() => { setWsError(''); setWsModalOpen(true) }}
              className={`${btnBase} border-border-strong text-content-muted hover:text-content hover:border-border-strong`}>
              ⊞ Select image workspace
            </button>
            <button onClick={() => { setEditError(''); setEditModalOpen(true) }}
              className={`${btnBase} border-border-strong text-content-muted hover:text-content hover:border-border-strong`}>
              ✎ Open existing template
            </button>
            <button onClick={() => tplFileRef.current?.click()}
              className={`${btnBase} border-border-strong text-content-muted hover:text-content hover:border-border-strong`}>
              ↑ Upload template
            </button>
            <input ref={tplFileRef} type="file" accept=".json,application/json"
              onChange={uploadTemplateFile} className="hidden" />
            <button onClick={copyResult} disabled={!hasContent}
              className={`${btnBase} disabled:opacity-40 disabled:cursor-not-allowed ${copied ? 'border-success bg-success-subtle text-success-fg' : 'border-border-strong text-content-muted hover:text-content'}`}>
              {copied ? '✓ Copied' : '⎘ Copy'}
            </button>
            <button onClick={downloadResult} disabled={!parsed}
              className={`${btnBase} border-border-strong text-content-muted hover:text-content disabled:opacity-40 disabled:cursor-not-allowed`}>
              ⬇ Download
            </button>
          </div>
        </div>

        {/* Summary chips — only when the JSON parses */}
              {parsed && Array.isArray(parsed.images) && (
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-xs px-2 py-0.5 rounded-full bg-success-subtle/40 text-success-fg border border-success-border/40">
                    ✓ {parsed.images.length} service{parsed.images.length !== 1 ? 's' : ''}
                  </span>
                  {parsed.default_env_vars && (
                    <span className="text-xs px-2 py-0.5 rounded-full bg-surface-raised text-content-subtle border border-border-strong">
                      {Object.keys(parsed.default_env_vars).length} env vars
                    </span>
                  )}
                  {parsed.images.map((img, i) => (
                    <span key={img.name || i} className="text-xs px-2 py-0.5 rounded-full bg-surface-raised text-content-muted border border-border-strong font-mono">
                      {img.name}: {img.image}{img.tag ? ':' + img.tag : ''}
                    </span>
                  ))}
                </div>
              )}

              {/* Quick metadata editors — patch the JSON below. Shown in the New
                  Workspace picker (card title, blurb, tag chips and search). */}
              <div className="space-y-2 rounded-xl border border-border bg-surface/40 p-3">
                <p className="text-xs font-semibold text-content-muted">
                  Template details <span className="font-normal text-content-faint">— shown in the New Project picker</span>
                </p>
                <input
                  type="text"
                  value={parsed?.name ?? ''}
                  disabled={!parsed}
                  onChange={e => patchField('name', e.target.value)}
                  placeholder="Name (id / filename — lowercase, digits, hyphens)"
                  className="w-full px-2.5 py-1.5 bg-canvas border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-faint focus:outline-none focus:border-brand-500 disabled:opacity-50 font-mono"
                />
                <input
                  type="text"
                  value={parsed?.label ?? ''}
                  disabled={!parsed}
                  onChange={e => patchField('label', e.target.value)}
                  placeholder="Label (e.g. Ghost CMS)"
                  className="w-full px-2.5 py-1.5 bg-canvas border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-faint focus:outline-none focus:border-brand-500 disabled:opacity-50"
                />
                <textarea
                  value={parsed?.description ?? ''}
                  disabled={!parsed}
                  onChange={e => patchField('description', e.target.value)}
                  rows={2}
                  placeholder="Description — a short blurb about what this stack is for"
                  className="w-full px-2.5 py-1.5 bg-canvas border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-faint focus:outline-none focus:border-brand-500 resize-y disabled:opacity-50"
                />
                <input
                  type="text"
                  value={tagsText}
                  disabled={!parsed}
                  onChange={e => { setTagsText(e.target.value); patchField('tags', e.target.value.split(',').map(t => t.trim()).filter(Boolean)) }}
                  placeholder="Tags (comma-separated, e.g. cms, blog, mysql)"
                  className="w-full px-2.5 py-1.5 bg-canvas border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-faint focus:outline-none focus:border-brand-500 disabled:opacity-50"
                />
              </div>

              {/* Editable template JSON — color-coded, line-numbered, scrolls internally */}
              <JsonEditor value={tplJson} onChange={editJson} valid={!hasContent || !!parsed} height="min(20rem, 40vh)" />
              {hasContent && !parsed && (
                <p className="text-xs text-danger-fg/80">⚠ The JSON isn't valid yet — fix it to validate and save.</p>
              )}

              {/* Validate → Save (Save unlocks only after a successful validation) */}
              <div className="flex items-center gap-2 flex-wrap pt-1">
                <button
                  onClick={runValidate}
                  disabled={!parsed || validation?.checking}
                  className={`${btnBase} disabled:opacity-40 disabled:cursor-not-allowed ${
                    validation?.ok ? 'border-success bg-success-subtle text-success-fg'
                    : 'border-brand-600 bg-brand-950 text-brand-300 hover:bg-brand-900'
                  }`}
                >
                  {validation?.checking ? 'Validating…' : validation?.ok ? '✓ Validated' : '✓ Validate'}
                </button>
                <button
                  onClick={saveAsTemplate}
                  disabled={!validation?.ok || saveState === 'saving' || saveState === 'saved'}
                  title={!validation?.ok ? 'Validate the template first' : undefined}
                  className={`${btnBase} ${
                    saveState === 'saved'   ? 'border-success bg-success-subtle text-success-fg' :
                    saveState === 'saving'  ? 'border-border-strong text-content-subtle cursor-wait' :
                    !validation?.ok         ? 'border-border text-content-faint cursor-not-allowed' :
                    'border-brand-600 bg-brand-950 text-brand-300 hover:bg-brand-900'
                  }`}
                >
                  {saveState === 'saved' ? '✓ Saved' : saveState === 'saving' ? 'Saving…' : validation?.overwrite ? '💾 Update template' : '💾 Save as template'}
                </button>
                {validation && !validation.checking && (
                  <span className="text-xs text-content-faint">
                    {validation.ok ? '' : `${validation.errors.length} issue${validation.errors.length !== 1 ? 's' : ''} to fix`}
                  </span>
                )}
              </div>

              {/* Validation results */}
              {validation && !validation.checking && !validation.ok && (
                <div className="rounded-lg bg-danger-subtle/30 border border-danger-border/30 p-3">
                  <p className="text-danger-fg text-xs font-semibold mb-1">Validation failed</p>
                  <ul className="text-danger-fg/80 text-xs list-disc list-inside space-y-0.5">
                    {validation.errors.map((er, i) => <li key={i}>{er}</li>)}
                  </ul>
                </div>
              )}
              {validation?.ok && saveState !== 'saved' && (
                <p className="text-xs text-success-fg/80">
                  ✓ Valid — {validation.overwrite
                    ? <>updating existing template <code className="font-mono">{validation.name}</code></>
                    : <>name <code className="font-mono">{validation.name}</code> is available</>
                  } ({validation.services} service{validation.services !== 1 ? 's' : ''}). Ready to save.
                </p>
              )}

              {/* Save error / success */}
              {saveState?.error && (
                <div className="px-3 py-2 rounded-lg bg-danger-subtle/30 border border-danger-border/30">
                  <p className="text-danger-fg text-xs">{saveState.error}</p>
                </div>
              )}
              {saveState === 'saved' && parsed && (
                <p className="text-xs text-success-fg/70">
                  Saved to <code className="font-mono">{parsed.name}.json</code> — available immediately in the New Project wizard (no rebuild needed).
                </p>
              )}
        </div>

      {composeModalOpen && (
        <ComposeModal
          onLoad={(tpl) => { loadTemplate(tpl); setComposeModalOpen(false) }}
          onClose={() => setComposeModalOpen(false)}
        />
      )}
      {wsModalOpen && (
        <SelectWorkspaceModal
          workspaces={imageWorkspaces}
          busy={wsBusy}
          error={wsError}
          onLoad={loadFromWorkspace}
          onClose={() => setWsModalOpen(false)}
        />
      )}
      {editModalOpen && (
        <EditTemplateModal
          busy={editBusy}
          error={editError}
          onLoad={loadExistingTemplate}
          onClose={() => setEditModalOpen(false)}
        />
      )}
    </div>
  )
}

// ── Workspace Backup & Restore ────────────────────────────────────────────────

function fmtBytes(b) {
  if (b < 1024) return `${b} B`
  if (b < 1024 * 1024) return `${(b / 1024).toFixed(1)} KB`
  if (b < 1024 * 1024 * 1024) return `${(b / 1024 / 1024).toFixed(1)} MB`
  return `${(b / 1024 / 1024 / 1024).toFixed(2)} GB`
}

function fmtDate(s) {
  return new Date(s).toLocaleString()
}

// ── Shared styling so the snapshot and full-backup panels look identical ──
const ROW_BTN         = 'text-xs px-2.5 py-1 rounded border transition-colors disabled:opacity-50 shrink-0'
const ROW_BTN_PRIMARY = `${ROW_BTN} border-warning-border/60 text-warning-fg hover:bg-warning-subtle/30`            // Roll back / Restore
const ROW_BTN_NEUTRAL = `${ROW_BTN} border-border-strong text-content-muted hover:text-content hover:border-border-strong` // Download
const ROW_BTN_DELETE  = `${ROW_BTN} border-border-strong text-content-subtle hover:text-danger-fg hover:border-danger-border/60` // Delete
const createBtnClass  = (enabled) =>
  `w-full py-2 text-sm font-semibold rounded-lg transition-colors ${
    enabled ? 'bg-brand-600 hover:bg-brand-700 text-white' : 'bg-surface-raised text-content-faint cursor-not-allowed'
  }`

// A single saved-file row (snapshot or backup) — identical layout for both.
function FileRow({ title, meta, children }) {
  return (
    <div className="flex items-center gap-3 px-3 py-2.5 bg-surface-raised/50 border border-border-strong/60 rounded-lg">
      <div className="flex-1 min-w-0">
        <p className="text-xs font-mono text-content truncate" title={title}>{title}</p>
        <p className="text-xs text-content-faint mt-0.5">{meta}</p>
      </div>
      {children}
    </div>
  )
}

// A saved-files list header with a count.
function SavedList({ label, count, children }) {
  return (
    <div>
      <div className="border-b border-border pb-2 mb-3">
        <h4 className="text-sm font-semibold text-content">
          {label} <span className="ml-1 text-xs font-normal text-content-faint">({count})</span>
        </h4>
      </div>
      {children}
    </div>
  )
}

// A small drag-and-drop zone that also opens a file dialog on click. Used to
// bring a snapshot/backup in from elsewhere (upload, then roll back / restore).
function DropZone({ onFile, accept, hint, busy, busyLabel }) {
  const ref = useRef(null)
  const [over, setOver] = useState(false)
  return (
    <div
      onClick={() => !busy && ref.current?.click()}
      onDragOver={e => { e.preventDefault(); if (!busy) setOver(true) }}
      onDragLeave={() => setOver(false)}
      onDrop={e => { e.preventDefault(); setOver(false); if (busy) return; const f = e.dataTransfer.files?.[0]; if (f) onFile(f) }}
      className={`border-2 border-dashed rounded-xl px-4 py-3 text-center transition-colors ${
        busy ? 'opacity-60 cursor-wait border-border-strong'
        : over ? 'border-brand-500 bg-brand-950/20 cursor-pointer'
        : 'border-border-strong hover:border-brand-600 cursor-pointer'
      }`}
    >
      <p className="text-xs text-content-muted">{busy ? busyLabel : hint}</p>
      <input ref={ref} type="file" accept={accept} className="hidden"
        onChange={e => { const f = e.target.files?.[0]; e.target.value = ''; if (f) onFile(f) }} />
    </div>
  )
}

function WorkspaceBackup() {
  const qc    = useQueryClient()
  const token = useAuthStore(s => s.token)
  const confirm = useConfirm()
  const currentWs = useWorkspaceStore(s => s.current)

  // Shared
  const [selectedWs, setSelectedWs]   = useState('')

  // Configuration snapshot (.rws) state
  const [snapName, setSnapName]         = useState('')
  const [snapBusy, setSnapBusy]         = useState(false)
  const [snapMsg, setSnapMsg]           = useState(null) // { ok, text }
  const [snapDeleting, setSnapDeleting] = useState({})
  const [rollingBack, setRollingBack]   = useState({})
  const [snapUploading, setSnapUploading] = useState(false)

  // Full backup (.rwb) state
  const [bkpName, setBkpName]               = useState('')    // optional custom backup filename
  const [activeJobId, setActiveJobId]       = useState(null)  // job ID string while running
  const [backupErr, setBackupErr]           = useState(null)
  const [bkpMsg, setBkpMsg]                 = useState(null)  // { ok, text } — restore/upload/delete
  const [deleting, setDeleting]             = useState({})
  const [downloading, setDownloading]       = useState({})
  const [syncingArchive, setSyncingArchive] = useState({})
  const [restoringArchive, setRestoringArchive] = useState({})
  const [archiveUploading, setArchiveUploading] = useState(false)

  // Project list for the selected workspace (the .rwb/.rws tools operate per project).
  const { data: workspaces = [] } = useQuery({
    queryKey: ['projects', currentWs],
    queryFn: () => fetchProjects(currentWs),
    enabled: !!currentWs,
    staleTime: 30_000,
  })

  // Poll active job — refetchInterval stops automatically when status !== 'running'
  const { data: activeJob } = useQuery({
    queryKey: ['backup-job', activeJobId],
    queryFn: () => getBackupJob(activeJobId),
    enabled: !!activeJobId,
    refetchInterval: (query) => {
      const status = query.state.data?.status
      return status === 'running' ? 2000 : false
    },
  })

  // Refresh archives list when job completes (scoped to the selected workspace)
  const { data: archives = [], refetch: refetchArchives } = useQuery({
    queryKey: ['workspace-archives', currentWs],
    queryFn: () => listWorkspaceArchives(currentWs),
  })

  // Config snapshots (scoped to the selected workspace)
  const { data: snapshots = [], refetch: refetchSnapshots } = useQuery({
    queryKey: ['workspace-snapshots', currentWs],
    queryFn: () => fetchWorkspaceSnapshots(currentWs),
  })

  // When job completes/fails, refresh archives
  if (activeJob?.status === 'completed' || activeJob?.status === 'failed') {
    if (activeJob.status === 'completed') refetchArchives()
  }

  async function startBackup() {
    if (!currentWs) { setBackupErr('Select a workspace (top-left) first'); return }
    if (!selectedWs) { setBackupErr('Select a project first'); return }
    setBackupErr(null)
    setActiveJobId(null)
    try {
      const job = await startWorkspaceBackup(currentWs, selectedWs, bkpName.trim())
      setActiveJobId(job.id)
      setBkpName('')
    } catch (e) {
      setBackupErr(e?.response?.data?.error || e.message)
    }
  }

  // Authenticated download — fetch with Bearer token, then blob URL
  async function downloadArchive(filename) {
    setDownloading(d => ({ ...d, [filename]: true }))
    try {
      const res = await fetch(`/api/tools/workspace-archives/${encodeURIComponent(filename)}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      if (!res.ok) throw new Error(`Server returned ${res.status}`)
      const blob = await res.blob()
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = filename
      a.click()
      URL.revokeObjectURL(url)
    } catch (e) {
      setBkpMsg({ ok: false, text: `Download failed: ${e.message}` })
    } finally {
      setDownloading(d => ({ ...d, [filename]: false }))
    }
  }

  async function deleteArchive(filename) {
    if (!(await confirm({
      title: 'Delete backup?',
      message: `Permanently delete the backup "${filename}"? This can't be undone.`,
      confirmLabel: 'Delete',
    }))) return
    setDeleting(d => ({ ...d, [filename]: true }))
    try {
      await deleteWorkspaceArchive(filename)
      refetchArchives()
      qc.removeQueries({ queryKey: ['workspace-archives'] })
    } catch (e) {
      setBkpMsg({ ok: false, text: e?.response?.data?.error || e.message })
    } finally {
      setDeleting(d => ({ ...d, [filename]: false }))
    }
  }

  // Restore directly from a backup already on the server (overwrites if it exists).
  async function restoreArchive(a) {
    const ok = await confirm({
      title: `Restore ${a.workspace || 'project'}?`,
      message: `This restores "${a.workspace || 'the project'}" from "${a.filename}", including all volume data. If a project named "${a.workspace}" already exists it will be REPLACED and its current data lost. Stop its containers first to avoid conflicts.`,
      confirmLabel: 'Restore',
    })
    if (!ok) return
    setRestoringArchive(s => ({ ...s, [a.filename]: true }))
    setBkpMsg(null)
    try {
      const res = await restoreWorkspaceFromArchive(a.filename, true)
      setBkpMsg({ ok: true, text: `Restored "${res.workspace}". Refresh its compose files and redeploy.` })
      qc.invalidateQueries({ queryKey: ['workspaces'] })
      qc.invalidateQueries({ queryKey: ['workspace', res.workspace] })
    } catch (e) {
      setBkpMsg({ ok: false, text: e?.response?.data?.error || e.message })
    } finally {
      setRestoringArchive(s => ({ ...s, [a.filename]: false }))
    }
  }

  // Push a full archive to the workspace's configured remote backup target (11a).
  async function syncArchive(a) {
    setSyncingArchive(s => ({ ...s, [a.filename]: true }))
    setBkpMsg(null)
    try {
      const res = await syncWorkspaceArchive(a.filename)
      setBkpMsg({ ok: true, text: `Synced ${a.filename} to ${res.target}.` })
      qc.invalidateQueries({ queryKey: ['workspace-archives'] })
    } catch (e) {
      setBkpMsg({ ok: false, text: `Sync failed: ${e?.response?.data?.error || e.message}` })
    } finally {
      setSyncingArchive(s => ({ ...s, [a.filename]: false }))
    }
  }

  // Upload a .rwb to the server — it joins the list, then restore it like any other.
  async function uploadArchive(f) {
    if (!f) return
    setArchiveUploading(true); setBkpMsg(null)
    try {
      const fd = new FormData()
      fd.append('archive', f)
      const a = await uploadWorkspaceArchive(fd)
      setBkpMsg({ ok: true, text: `Uploaded ${a.filename}${a.workspace ? ` (workspace: ${a.workspace})` : ''}. Restore it from the list above.` })
      refetchArchives()
    } catch (err) {
      setBkpMsg({ ok: false, text: err?.response?.data?.error || err.message })
    } finally {
      setArchiveUploading(false)
    }
  }

  // ── Config snapshot handlers ──
  async function takeSnapshot() {
    if (!currentWs) { setSnapMsg({ ok: false, text: 'Select a workspace (top-left) first' }); return }
    if (!selectedWs) { setSnapMsg({ ok: false, text: 'Select a project first' }); return }
    setSnapBusy(true); setSnapMsg(null)
    try {
      const snap = await createWorkspaceSnapshot(currentWs, selectedWs, snapName.trim())
      setSnapMsg({ ok: true, text: `Saved ${snap.filename} (${fmtBytes(snap.size_bytes)}).` })
      setSnapName('')
      refetchSnapshots()
    } catch (e) {
      setSnapMsg({ ok: false, text: e?.response?.data?.error || e.message })
    } finally {
      setSnapBusy(false)
    }
  }

  async function rollbackSnapshot(snap) {
    const ok = await confirm({
      title: `Roll back ${snap.workspace || 'project'} configuration?`,
      message: `This OVERWRITES the current config.json and every .env file for "${snap.workspace}" with the snapshot "${snap.filename}". Any configuration changed since then is lost. If a DB password, API token or other key has changed since this snapshot was taken, you must update it to the new value afterwards and redeploy. Data volumes are NOT touched.`,
      confirmLabel: 'Roll back',
    })
    if (!ok) return
    setRollingBack(s => ({ ...s, [snap.filename]: true }))
    try {
      const res = await rollbackWorkspaceSnapshot(snap.filename)
      setSnapMsg({ ok: true, text: `Rolled "${res.workspace}" back to ${snap.filename}. Review secrets in Edit Project, then redeploy.` })
      qc.invalidateQueries({ queryKey: ['workspace', res.workspace] })
    } catch (e) {
      setSnapMsg({ ok: false, text: e?.response?.data?.error || e.message })
    } finally {
      setRollingBack(s => ({ ...s, [snap.filename]: false }))
    }
  }

  async function removeSnapshot(filename) {
    if (!(await confirm({
      title: 'Delete snapshot?',
      message: `Permanently delete the snapshot "${filename}"? This can't be undone.`,
      confirmLabel: 'Delete',
    }))) return
    setSnapDeleting(s => ({ ...s, [filename]: true }))
    try {
      await deleteWorkspaceSnapshot(filename)
      refetchSnapshots()
    } catch (e) {
      setSnapMsg({ ok: false, text: e?.response?.data?.error || e.message })
    } finally {
      setSnapDeleting(s => ({ ...s, [filename]: false }))
    }
  }

  async function downloadSnapshot(filename) {
    try {
      const res = await fetch(`/api/tools/workspace-snapshots/${encodeURIComponent(filename)}`, {
        headers: { Authorization: `Bearer ${token}` },
      })
      if (!res.ok) throw new Error('download failed')
      const blob = await res.blob()
      const a = document.createElement('a')
      a.href = URL.createObjectURL(blob); a.download = filename; a.click()
      URL.revokeObjectURL(a.href)
    } catch (e) { setSnapMsg({ ok: false, text: e.message }) }
  }

  async function uploadSnapshot(f) {
    if (!f) return
    setSnapUploading(true); setSnapMsg(null)
    try {
      const fd = new FormData()
      fd.append('snapshot', f)
      const snap = await uploadWorkspaceSnapshot(fd)
      setSnapMsg({ ok: true, text: `Uploaded ${snap.filename}${snap.workspace ? ` (workspace: ${snap.workspace})` : ''}. Roll back to it from the list above.` })
      refetchSnapshots()
    } catch (err) {
      setSnapMsg({ ok: false, text: err?.response?.data?.error || err.message })
    } finally {
      setSnapUploading(false)
    }
  }

  const isRunning = activeJob?.status === 'running' || (activeJobId && !activeJob)

  return (
    <div className="space-y-6">
      {/* Intro */}
      <div className="bg-surface-raised/50 border border-border-strong/60 rounded-xl p-4 text-sm text-content-muted leading-relaxed">
        Pick a workspace, then take a lightweight <strong className="text-content">configuration snapshot</strong>{' '}
        (<code className="font-mono text-xs">.rws</code> — <code className="font-mono text-xs">config.json</code> + each{' '}
        <code className="font-mono text-xs">.env</code>, no data) or a <strong className="text-content">full backup</strong>{' '}
        (<code className="font-mono text-xs">.rwb</code> — config, env files, and the latest backup snapshot per env). Both can be downloaded, uploaded
        and restored on the server.
      </div>

      {/* Shared project selector */}
      <div className="bg-surface border border-border rounded-xl p-4">
        <label className="block text-xs font-medium text-content-muted mb-1.5">Project</label>
        <select
          value={selectedWs}
          onChange={e => { setSelectedWs(e.target.value); setSnapMsg(null); setBkpMsg(null); setBackupErr(null); setActiveJobId(null) }}
          className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500"
        >
          <option value="">— select project —</option>
          {workspaces.map(ws => <option key={ws.name} value={ws.name}>{ws.name}</option>)}
        </select>
        <p className="text-xs text-content-faint mt-2">Applies to both <strong className="text-content-subtle">Take snapshot</strong> and <strong className="text-content-subtle">Start backup</strong> below.</p>
      </div>

      {/* Two consistent panels */}
      <div className="grid grid-cols-2 gap-6 items-start">
        {/* ── Configuration snapshot (.rws) ── */}
        <section className="space-y-3">
          <div>
            <h3 className="text-base font-semibold text-content-strong">Configuration snapshot <span className="text-xs font-normal text-content-faint">.rws</span></h3>
            <p className="text-sm text-content-subtle mt-1">
              Just <code className="font-mono text-xs">config.json</code> and each env's{' '}
              <code className="font-mono text-xs">.env</code> (secrets included). No volume data — fast to take, easy to roll back.
            </p>
          </div>

          <div className="bg-surface border border-border rounded-xl p-4 space-y-3">
            <div>
              <label className="block text-xs font-medium text-content-muted mb-1.5">
                Snapshot name <span className="text-content-faint font-normal">(optional)</span>
              </label>
              <input
                value={snapName}
                onChange={e => setSnapName(e.target.value)}
                placeholder={selectedWs ? `${currentWs}_${selectedWs}_<timestamp>.rws` : 'auto: <workspace>_<project>_<timestamp>.rws'}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-faint focus:outline-none focus:border-brand-500"
              />
            </div>
            <button onClick={takeSnapshot} disabled={!selectedWs || snapBusy} className={createBtnClass(!!selectedWs && !snapBusy)}>
              {snapBusy ? 'Saving…' : 'Take configuration snapshot'}
            </button>
            {snapMsg && <p className={`text-xs px-1 ${snapMsg.ok ? 'text-success-fg' : 'text-danger-fg'}`}>{snapMsg.text}</p>}
          </div>

          <SavedList label="Saved snapshots" count={snapshots.length}>
            {snapshots.length === 0
              ? <p className="text-xs text-content-faint py-4 text-center">No snapshots yet.</p>
              : (
                <div className="space-y-2">
                  {snapshots.map(s => (
                    <FileRow key={s.filename} title={s.filename}
                      meta={<>{s.workspace ? <span className="text-content-subtle">{s.workspace}{s.project ? ` / ${s.project}` : ''}</span> : 'unknown project'} · {fmtDate(s.created_at)} · {fmtBytes(s.size_bytes)}</>}>
                      <button onClick={() => rollbackSnapshot(s)} disabled={rollingBack[s.filename]} className={ROW_BTN_PRIMARY}>
                        {rollingBack[s.filename] ? '…' : 'Roll back'}
                      </button>
                      <button onClick={() => downloadSnapshot(s.filename)} className={ROW_BTN_NEUTRAL}>⬇ Download</button>
                      <button onClick={() => removeSnapshot(s.filename)} disabled={snapDeleting[s.filename]} className={ROW_BTN_DELETE}>
                        {snapDeleting[s.filename] ? '…' : 'Delete'}
                      </button>
                    </FileRow>
                  ))}
                </div>
              )}
          </SavedList>

          {/* Bring a snapshot in from elsewhere */}
          <DropZone onFile={uploadSnapshot} accept=".rws" busy={snapUploading}
            busyLabel="Uploading snapshot…"
            hint="↑ Drop a .rws snapshot here, or click to browse" />

          {/* Snapshot-specific guidance */}
          <div className="bg-surface-raised/30 border border-border-strong/40 rounded-xl p-4 space-y-2">
            <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider">Rolling back configuration</p>
            <ol className="text-xs text-content-subtle space-y-1 list-decimal list-inside">
              <li>Click <strong className="text-content-muted">Roll back</strong> on a snapshot to overwrite the project's <code className="font-mono text-content-muted">config.json</code> and every <code className="font-mono text-content-muted">.env</code></li>
              <li>To use a snapshot from elsewhere, drop the <code className="font-mono text-content-muted">.rws</code> above — it joins the list, then roll back to it</li>
              <li>If a secret (DB password, API key…) changed since the snapshot, update it afterward and redeploy</li>
              <li>Volume data is never touched — use a full backup for that</li>
            </ol>
          </div>
        </section>

        {/* ── Full backup (.rwb) ── */}
        <section className="space-y-3">
          <div>
            <h3 className="text-base font-semibold text-content-strong">Full backup <span className="text-xs font-normal text-content-faint">.rwb</span></h3>
            <p className="text-sm text-content-subtle mt-1">
              Config + env files + the <strong className="text-content-muted">most recent backup snapshot per env</strong> (older snapshots excluded).
              Larger than a config snapshot; restoring re-creates the whole workspace.
            </p>
          </div>

          <div className="bg-surface border border-border rounded-xl p-4 space-y-3">
            <div>
              <label className="block text-xs font-medium text-content-muted mb-1.5">
                Backup name <span className="text-content-faint font-normal">(optional)</span>
              </label>
              <input
                value={bkpName}
                onChange={e => setBkpName(e.target.value)}
                placeholder={selectedWs ? `${currentWs}_${selectedWs}-<timestamp>.rwb` : 'auto: <workspace>_<project>-<timestamp>.rwb'}
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-faint focus:outline-none focus:border-brand-500"
              />
            </div>
            <button onClick={startBackup} disabled={!selectedWs || isRunning} className={createBtnClass(!!selectedWs && !isRunning)}>
              {isRunning ? '⏳ Backing up…' : 'Start full backup'}
            </button>
            {backupErr && <p className="text-xs text-danger-fg px-1">{backupErr}</p>}

            {/* Job status card */}
            {activeJob && (
              <div className={`px-4 py-3 rounded-xl border text-sm ${
                activeJob.status === 'running'   ? 'bg-brand-950/40 border-brand-700/40 text-brand-300' :
                activeJob.status === 'completed' ? 'bg-success-subtle/40 border-success-border/40 text-success-fg' :
                'bg-danger-subtle/40 border-danger-border/40 text-danger-fg'
              }`}>
                <div className="flex items-center gap-2">
                  <span className={activeJob.status === 'running' ? 'animate-spin inline-block' : ''}>
                    {activeJob.status === 'running' ? '⟳' : activeJob.status === 'completed' ? '✓' : '✗'}
                  </span>
                  <span className="font-medium capitalize">{activeJob.status}</span>
                </div>
                {activeJob.status === 'running' && (
                  <p className="text-xs mt-1 opacity-70">
                    Archiving <strong>{activeJob.workspace}</strong> — may take a while for large volumes…
                  </p>
                )}
                {activeJob.status === 'completed' && (
                  <p className="text-xs mt-1">
                    <code className="font-mono">{activeJob.archive}</code>
                    {' '}({fmtBytes(activeJob.size_bytes)}) — available below
                  </p>
                )}
                {activeJob.status === 'failed' && (
                  <p className="text-xs mt-1">{activeJob.error}</p>
                )}
              </div>
            )}
            {!activeJob && !backupErr && (
              <p className="text-xs text-content-faint px-1">Stored on the server; download to keep a copy off-box.</p>
            )}
          </div>

          <SavedList label="Backups on server" count={archives.length}>
            {bkpMsg && <p className={`text-xs px-1 mb-2 ${bkpMsg.ok ? 'text-success-fg' : 'text-danger-fg'}`}>{bkpMsg.text}</p>}
            {archives.length === 0
              ? <p className="text-xs text-content-faint py-4 text-center">No backups yet.</p>
              : (
                <div className="space-y-2">
                  {archives.map(a => (
                    <FileRow key={a.filename} title={a.filename}
                      meta={<>
                        {a.workspace ? <span className="text-content-subtle">{a.workspace}{a.project ? ` / ${a.project}` : ''}</span> : 'unknown project'} · {fmtDate(a.created_at)} · {fmtBytes(a.size_bytes)}
                        {a.sync?.status === 'ok' && <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded bg-success-subtle text-success-fg border border-success-border/60" title={`Synced to ${a.sync.target}`}>↑ {a.sync.target}</span>}
                        {a.sync?.status === 'fail' && <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded bg-danger-subtle text-danger-fg border border-danger-border/60" title="Last sync failed">↑!</span>}
                      </>}>
                      <button onClick={() => restoreArchive(a)} disabled={restoringArchive[a.filename]} className={ROW_BTN_PRIMARY}>
                        {restoringArchive[a.filename] ? '…' : 'Restore'}
                      </button>
                      <button onClick={() => syncArchive(a)} disabled={!!syncingArchive[a.filename]} className={ROW_BTN_NEUTRAL} title="Push to remote backup target">
                        {syncingArchive[a.filename] ? '…' : '↑ Sync'}
                      </button>
                      <button onClick={() => downloadArchive(a.filename)} disabled={!!downloading[a.filename]} className={ROW_BTN_NEUTRAL}>
                        {downloading[a.filename] ? '…' : '⬇ Download'}
                      </button>
                      <button onClick={() => deleteArchive(a.filename)} disabled={!!deleting[a.filename]} className={ROW_BTN_DELETE}>
                        {deleting[a.filename] ? '…' : 'Delete'}
                      </button>
                    </FileRow>
                  ))}
                </div>
              )}
          </SavedList>

          {/* Bring a backup in from elsewhere */}
          <DropZone onFile={uploadArchive} accept=".rwb,.tar.gz,.gz" busy={archiveUploading}
            busyLabel="Uploading backup…"
            hint="↑ Drop a .rwb backup here, or click to browse" />

          {/* Backup-specific guidance */}
          <div className="bg-surface-raised/30 border border-border-strong/40 rounded-xl p-4 space-y-2">
            <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider">Restoring a full backup</p>
            <ol className="text-xs text-content-subtle space-y-1 list-decimal list-inside">
              <li>Stop the project's containers if it already exists</li>
              <li>To use a backup from elsewhere, drop the <code className="font-mono text-content-muted">.rwb</code> above — it joins the list</li>
              <li>Click <strong className="text-content-muted">Restore</strong> on a backup row (an existing project is replaced)</li>
              <li>The project appears in the sidebar immediately</li>
              <li>Run <code className="font-mono text-content-muted">./run.sh refresh &lt;env&gt;</code> to regenerate compose files, then redeploy</li>
            </ol>
          </div>
        </section>
      </div>
    </div>
  )
}

// ── Migrate data between environments ───────────────────────────────────────────
// Thin wrapper over the backup→restore engine: copy one env's DATA into another
// (e.g. refresh staging from prod). Source is never modified; the target is
// overwritten (a default safety backup + typed confirm guard it).
function MigrateData() {
  const currentWs = useWorkspaceStore(s => s.current)
  const [project, setProject] = useState('')
  const [sourceEnv, setSourceEnv] = useState('')
  const [targetEnv, setTargetEnv] = useState('')
  const [skipBackup, setSkipBackup] = useState(false)
  const [confirm, setConfirm] = useState('')
  const [jobId, setJobId] = useState(null)
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)

  const { data: projects = [] } = useQuery({
    queryKey: ['projects', currentWs], queryFn: () => fetchProjects(currentWs),
    enabled: !!currentWs, staleTime: 30_000,
  })
  // Env list for the selected project (from its config.json).
  const { data: cfg } = useQuery({
    queryKey: ['config', currentWs, project],
    queryFn: () => fetchConfig(currentWs, project),
    enabled: !!currentWs && !!project,
  })
  const envs = cfg?.environments ? Object.keys(cfg.environments) : []

  // Poll the migration job until it finishes.
  const { data: job } = useQuery({
    queryKey: ['backup-job', jobId],
    queryFn: () => getBackupJob(jobId),
    enabled: !!jobId,
    refetchInterval: (q) => (q.state.data && q.state.data.status !== 'running' ? false : 2000),
  })
  const running = !!jobId && (!job || job.status === 'running')

  const valid = project && sourceEnv && targetEnv && sourceEnv !== targetEnv && confirm.trim() === targetEnv

  async function go() {
    if (!valid) return
    setBusy(true); setErr(''); setJobId(null)
    try {
      const res = await migrateEnvData(currentWs, project, {
        source_env: sourceEnv, target_env: targetEnv, confirm: confirm.trim(),
        skip_target_backup: skipBackup,
      })
      setJobId(res.id)
    } catch (e) {
      setErr(e?.response?.data?.error || e.message)
    } finally {
      setBusy(false)
    }
  }

  const selectCls = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 disabled:opacity-50'

  return (
    <div className="space-y-5">
      <div className="bg-surface-raised/50 border border-border-strong/60 rounded-xl p-4 text-sm text-content-muted leading-relaxed">
        Copy an environment’s <strong className="text-content">data</strong> (database + volumes) into another environment of the
        same project — e.g. refresh <em>staging</em> from <em>prod</em>. The source is read-only; the target’s data is
        <strong className="text-content"> overwritten</strong>. A safety backup of the target is taken first (so it’s reversible),
        and the target should already be deployed at least once. Config/secrets are <strong className="text-content">not</strong> touched —
        only data. Use <strong className="text-content">Copy environment</strong> (Edit Project) to clone configuration.
      </div>

      <div className="bg-surface border border-border rounded-xl p-4 space-y-4">
        <div>
          <label className="block text-xs font-medium text-content-muted mb-1.5">Project</label>
          <select value={project} className={selectCls}
            onChange={e => { setProject(e.target.value); setSourceEnv(''); setTargetEnv(''); setConfirm('') }}>
            <option value="">— select a project —</option>
            {projects.map(p => <option key={p.name} value={p.name}>{p.config?.project?.name || p.name} ({p.name})</option>)}
          </select>
        </div>

        <div className="grid grid-cols-[1fr_auto_1fr] gap-3 items-end">
          <div>
            <label className="block text-xs font-medium text-content-muted mb-1.5">Source (data copied FROM)</label>
            <select value={sourceEnv} disabled={!project} className={selectCls}
              onChange={e => setSourceEnv(e.target.value)}>
              <option value="">— source env —</option>
              {envs.map(en => <option key={en} value={en}>{en}</option>)}
            </select>
          </div>
          <div className="pb-2 text-content-subtle text-lg">→</div>
          <div>
            <label className="block text-xs font-medium text-content-muted mb-1.5">Target (overwritten)</label>
            <select value={targetEnv} disabled={!project} className={selectCls}
              onChange={e => { setTargetEnv(e.target.value); setConfirm('') }}>
              <option value="">— target env —</option>
              {envs.filter(en => en !== sourceEnv).map(en => <option key={en} value={en}>{en}</option>)}
            </select>
          </div>
        </div>

        <label className="flex items-center gap-2 cursor-pointer select-none">
          <input type="checkbox" checked={skipBackup} onChange={e => setSkipBackup(e.target.checked)}
            className="rounded border-border-strong bg-surface-overlay text-brand-500 focus:ring-brand-500" />
          <span className="text-sm text-content">Skip the target safety backup <span className="text-danger-fg">(not reversible)</span></span>
        </label>

        {targetEnv && (
          <div className="rounded-lg border border-warning-border/50 bg-warning-subtle/30 p-3 space-y-2">
            <p className="text-xs text-warning-fg">
              ⚠ This will <strong>overwrite all data</strong> in <code className="font-mono">{targetEnv}</code> with a copy of <code className="font-mono">{sourceEnv || '…'}</code>’s data. Type <code className="font-mono">{targetEnv}</code> to confirm:
            </p>
            <input type="text" value={confirm} onChange={e => setConfirm(e.target.value)}
              placeholder={targetEnv}
              className="w-full px-3 py-2 bg-surface border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-brand-500" />
          </div>
        )}

        <button onClick={go} disabled={!valid || busy || running}
          className={`px-4 py-2 rounded-lg text-sm font-semibold transition-colors ${
            !valid || busy || running ? 'bg-surface-overlay text-content-faint cursor-not-allowed' : 'bg-brand-600 hover:bg-brand-700 text-white'
          }`}>
          {running ? 'Migrating…' : busy ? 'Starting…' : 'Migrate data'}
        </button>
        {err && <p className="text-xs text-danger-fg">{err}</p>}
      </div>

      {/* Job status */}
      {jobId && job && (
        <div className={`rounded-xl border p-4 ${
          job.status === 'completed' ? 'border-success-border/50 bg-success-subtle/20' :
          job.status === 'failed' ? 'border-danger-border/50 bg-danger-subtle/20' :
          'border-border-strong bg-surface-raised/30'
        }`}>
          <p className="text-sm font-semibold text-content-strong mb-1">
            {job.status === 'completed' ? '✓ Migration complete' : job.status === 'failed' ? '✗ Migration failed' : '⏳ Migrating…'}
          </p>
          {job.error && <p className="text-xs text-danger-fg mb-2 whitespace-pre-wrap">{job.error}</p>}
          {job.log && (
            <pre className="text-[11px] text-content-subtle bg-canvas/60 border border-border-strong/40 rounded-lg p-3 max-h-72 overflow-auto whitespace-pre-wrap font-mono">{job.log}</pre>
          )}
          {job.status === 'completed' && (
            <p className="text-xs text-content-subtle mt-2">Deploy / refresh <code className="font-mono">{targetEnv}</code> to bring services up on the migrated data.</p>
          )}
        </div>
      )}
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

const TOOLS = [
  {
    id: 'workspace-backup',
    label: 'Project Tools',
    icon: '🧰',
    description: 'Snapshot or roll back project configuration, and create/restore full project backups (config + data).',
    component: WorkspaceBackup,
  },
  {
    id: 'migrate-data',
    label: 'Migrate Data',
    icon: '🔀',
    description: 'Copy one environment’s data (database + volumes) into another — e.g. refresh staging from prod. Source is read-only; the target is overwritten (with a safety backup first).',
    component: MigrateData,
  },
  {
    id: 'compose-to-template',
    label: 'Template Manager',
    icon: '📝',
    description: 'Convert a docker-compose.yml, open an existing template to revise it, or upload one — then edit, validate and save it as a reusable Rigger prebuilt template.',
    component: ComposeToTemplate,
  },
]

export default function ToolsPage() {
  const [activeTool, setActiveTool] = useState(TOOLS[0].id)
  const ActiveComponent = TOOLS.find(t => t.id === activeTool)?.component

  return (
    <Layout>
      <div className="p-6 max-w-[1400px] mx-auto">
        <div className="mb-6">
          <h1 className="text-xl font-bold text-content-strong">Tools</h1>
          <p className="text-sm text-content-subtle mt-0.5">Utilities for working with Rigger projects and templates.</p>
        </div>

        <VerticalTabs tabs={TOOLS} active={activeTool} onChange={setActiveTool}>
          <div className="bg-surface border border-border rounded-2xl p-6">
            {ActiveComponent && <ActiveComponent />}
          </div>
        </VerticalTabs>
      </div>
    </Layout>
  )
}
