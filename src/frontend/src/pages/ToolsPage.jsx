import { useState, useRef } from 'react'
import Layout from '../components/Layout'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  saveToolTemplate, fetchTemplates, fetchTemplateDraft,
  startWorkspaceBackup, getBackupJob,
  listWorkspaceArchives, deleteWorkspaceArchive,
  restoreWorkspaceFromArchive, uploadWorkspaceArchive, fetchWorkspaces,
  createWorkspaceSnapshot, fetchWorkspaceSnapshots, deleteWorkspaceSnapshot, rollbackWorkspaceSnapshot, uploadWorkspaceSnapshot,
} from '../lib/api'
import { useAuthStore } from '../store/auth'
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
      out += `<span class="text-sky-300">${escapeHtml(m[1])}</span><span class="text-gray-500">${escapeHtml(m[2])}</span>`
    } else if (m[1]) {                           // "string value"
      out += `<span class="text-emerald-300">${escapeHtml(m[1])}</span>`
    } else if (m[3]) {                           // number
      out += `<span class="text-amber-300">${escapeHtml(m[3])}</span>`
    } else if (m[4]) {                           // true | false | null
      out += `<span class="text-purple-300">${escapeHtml(m[4])}</span>`
    } else if (m[5]) {                           // punctuation
      out += `<span class="text-gray-500">${escapeHtml(m[5])}</span>`
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
      className={`relative w-full flex rounded-xl bg-gray-950 border overflow-hidden ${valid ? 'border-gray-700 focus-within:border-brand-500' : 'border-red-700/60 focus-within:border-red-500'}`}
      style={{ height }}
    >
      {/* Line-number gutter (scrolls in sync, no scrollbar of its own) */}
      <pre ref={gutterRef} aria-hidden="true"
        className="m-0 py-3 pl-3 pr-2 text-xs font-mono leading-relaxed text-right text-gray-600 select-none overflow-hidden whitespace-pre border-r border-gray-800/80 bg-gray-950"
        style={{ minWidth: '2.75rem' }}
      >{gutter}</pre>

      {/* Code area: highlighted <pre> behind, transparent <textarea> on top */}
      <div className="relative flex-1 overflow-hidden">
        <pre ref={preRef} aria-hidden="true"
          className={`${shared} absolute inset-0 overflow-hidden text-gray-200 pointer-events-none`}
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
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-md mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-white">Select an image workspace</h3>
          <button onClick={onClose} className="text-gray-500 hover:text-white text-xl">×</button>
        </div>
        <p className="text-sm text-gray-500">
          Pulls the stack's images and environment-variable defaults (secrets masked) into the editor as a draft template.
        </p>

        {workspaces.length === 0 ? (
          <p className="text-sm text-gray-400 bg-gray-800/50 border border-gray-700/60 rounded-lg px-3 py-3">
            No image workspaces found. Only image stacks can become templates.
          </p>
        ) : (
          <>
            <div>
              <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Workspace</label>
              <select value={ws} onChange={e => { setWs(e.target.value); setEnv('') }}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500">
                {workspaces.map(w => <option key={w.name} value={w.name}>{w.name}</option>)}
              </select>
            </div>
            <div>
              <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">
                Environment <span className="normal-case font-normal text-gray-500">(for env-var defaults)</span>
              </label>
              <select value={chosenEnv} onChange={e => setEnv(e.target.value)}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500">
                {envs.map(en => <option key={en} value={en}>{en}</option>)}
              </select>
            </div>
            {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}
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
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-3xl p-6 space-y-3" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between">
          <h3 className="font-semibold text-white">Convert Docker Compose</h3>
          <button onClick={onClose} className="text-gray-500 hover:text-white text-xl">×</button>
        </div>
        <p className="text-sm text-gray-500">
          Paste or import a <code className="font-mono text-xs">docker-compose.yml</code>; converting turns its services
          into a Rigger template and loads it into the editor for review.
        </p>

        <div className="flex items-center justify-between">
          <label className="text-sm font-semibold text-gray-300">docker-compose.yml</label>
          <div className="flex items-center gap-2">
            <button onClick={paste} className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500`}>⎘ Paste</button>
            <button onClick={() => fileRef.current?.click()} className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500`}>↑ Import file</button>
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
          className="w-full px-3 py-3 bg-gray-950 border border-gray-700 rounded-xl text-gray-200 text-xs font-mono placeholder-gray-700 focus:outline-none focus:border-brand-500 resize-y leading-relaxed"
          style={{ minHeight: '24rem' }}
        />

        {error && (
          <div className="rounded-lg bg-red-950/40 border border-red-700/40 px-3 py-2">
            <p className="text-red-400 text-xs font-medium">Conversion failed</p>
            <p className="text-red-300/70 text-xs mt-0.5">{error}</p>
          </div>
        )}

        <button
          onClick={convert}
          disabled={!input.trim()}
          className={`w-full py-2 text-sm font-semibold rounded-lg transition-colors ${
            input.trim() ? 'bg-brand-600 hover:bg-brand-700 text-white' : 'bg-gray-800 text-gray-600 cursor-not-allowed'
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

  // "From a workspace" source — pick an image stack (in a modal) and pull it in.
  const [wsModalOpen, setWsModalOpen] = useState(false)
  const [wsBusy, setWsBusy]   = useState(false)
  const [wsError, setWsError] = useState('')
  const { data: allWorkspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces, staleTime: 30_000 })
  // Only image stacks can become templates (project.type lives in the nested config).
  const imageWorkspaces = allWorkspaces.filter(w => w.config?.project?.type === 'image')

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
  function loadTemplate(tpl) {
    editJson(JSON.stringify(tpl, null, 2))
    setTagsText(Array.isArray(tpl?.tags) ? tpl.tags.join(', ') : '')
  }

  // Pull an image workspace's stack (images + masked env-var defaults) into the
  // editor as a draft. Name/label are seeded from the workspace as an editable
  // starting point; the user reviews, names and validates before saving.
  async function loadFromWorkspace(wsName, env) {
    if (!wsName) return
    setWsBusy(true); setWsError('')
    try {
      const draft = await fetchTemplateDraft(wsName, env)
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
    // Name uniqueness — only checked once the name itself is well-formed.
    if (nm && /^[a-z0-9-]+$/.test(nm)) {
      try {
        const existing = await fetchTemplates()
        if ((existing || []).some(t => t.name === nm)) {
          errors.push(`A template named "${nm}" already exists — choose a different "name".`)
        }
      } catch {
        errors.push('Could not verify name uniqueness (failed to load existing templates).')
      }
    }
    setValidation(errors.length ? { ok: false, errors } : { ok: true, name: nm, services: tpl.images.length })
  }

  async function saveAsTemplate() {
    if (!validation?.ok || !parsed) return
    setSaveState('saving')
    try {
      await saveToolTemplate(parsed.name, parsed, false)
      setSaveState('saved')
    } catch (err) {
      setSaveState({ error: err?.response?.data?.error || err.message })
    }
  }

  const btnBase = 'text-xs px-2.5 py-1 rounded border transition-colors'

  return (
    <div className="space-y-6">
      {/* Description */}
      <div className="bg-gray-800/50 border border-gray-700/60 rounded-xl p-4 text-sm text-gray-400 leading-relaxed">
        Create a reusable prebuilt template from one of three sources —{' '}
        <strong className="text-gray-300">Convert Docker Compose</strong>, <strong className="text-gray-300">Upload
        template</strong>, or <strong className="text-gray-300">Select image workspace</strong> — then edit the JSON,
        fill in name / label / description / tags, and <strong className="text-gray-300">Validate</strong> (which also
        checks the name is unique) to unlock <strong className="text-gray-300">Save as template</strong>.
      </div>

      {/* ── Template editor (full width) ── */}
      <div className="space-y-2">
        {/* Editor toolbar */}
        <div className="flex items-center justify-between gap-2 flex-wrap">
          <label className="text-sm font-semibold text-gray-300">Rigger template JSON</label>
          <div className="flex items-center gap-2 flex-wrap">
            <button onClick={() => setComposeModalOpen(true)}
              className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500`}>
              ⇄ Convert Docker Compose
            </button>
            <button onClick={() => { setWsError(''); setWsModalOpen(true) }}
              className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500`}>
              ⊞ Select image workspace
            </button>
            <button onClick={() => tplFileRef.current?.click()}
              className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500`}>
              ↑ Upload template
            </button>
            <input ref={tplFileRef} type="file" accept=".json,application/json"
              onChange={uploadTemplateFile} className="hidden" />
            <button onClick={copyResult} disabled={!hasContent}
              className={`${btnBase} disabled:opacity-40 disabled:cursor-not-allowed ${copied ? 'border-green-600 bg-green-950 text-green-400' : 'border-gray-700 text-gray-400 hover:text-gray-200'}`}>
              {copied ? '✓ Copied' : '⎘ Copy'}
            </button>
            <button onClick={downloadResult} disabled={!parsed}
              className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 disabled:opacity-40 disabled:cursor-not-allowed`}>
              ⬇ Download
            </button>
          </div>
        </div>

        {/* Summary chips — only when the JSON parses */}
              {parsed && Array.isArray(parsed.images) && (
                <div className="flex items-center gap-2 flex-wrap">
                  <span className="text-xs px-2 py-0.5 rounded-full bg-green-950/40 text-green-400 border border-green-700/40">
                    ✓ {parsed.images.length} service{parsed.images.length !== 1 ? 's' : ''}
                  </span>
                  {parsed.default_env_vars && (
                    <span className="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-gray-500 border border-gray-700">
                      {Object.keys(parsed.default_env_vars).length} env vars
                    </span>
                  )}
                  {parsed.images.map((img, i) => (
                    <span key={img.name || i} className="text-xs px-2 py-0.5 rounded-full bg-gray-800 text-gray-400 border border-gray-700 font-mono">
                      {img.name}: {img.image}{img.tag ? ':' + img.tag : ''}
                    </span>
                  ))}
                </div>
              )}

              {/* Quick metadata editors — patch the JSON below. Shown in the New
                  Workspace picker (card title, blurb, tag chips and search). */}
              <div className="space-y-2 rounded-xl border border-gray-800 bg-gray-900/40 p-3">
                <p className="text-xs font-semibold text-gray-400">
                  Template details <span className="font-normal text-gray-600">— shown in the New Workspace picker</span>
                </p>
                <input
                  type="text"
                  value={parsed?.name ?? ''}
                  disabled={!parsed}
                  onChange={e => patchField('name', e.target.value)}
                  placeholder="Name (id / filename — lowercase, digits, hyphens)"
                  className="w-full px-2.5 py-1.5 bg-gray-950 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-600 focus:outline-none focus:border-brand-500 disabled:opacity-50 font-mono"
                />
                <input
                  type="text"
                  value={parsed?.label ?? ''}
                  disabled={!parsed}
                  onChange={e => patchField('label', e.target.value)}
                  placeholder="Label (e.g. Ghost CMS)"
                  className="w-full px-2.5 py-1.5 bg-gray-950 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-600 focus:outline-none focus:border-brand-500 disabled:opacity-50"
                />
                <textarea
                  value={parsed?.description ?? ''}
                  disabled={!parsed}
                  onChange={e => patchField('description', e.target.value)}
                  rows={2}
                  placeholder="Description — a short blurb about what this stack is for"
                  className="w-full px-2.5 py-1.5 bg-gray-950 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-600 focus:outline-none focus:border-brand-500 resize-y disabled:opacity-50"
                />
                <input
                  type="text"
                  value={tagsText}
                  disabled={!parsed}
                  onChange={e => { setTagsText(e.target.value); patchField('tags', e.target.value.split(',').map(t => t.trim()).filter(Boolean)) }}
                  placeholder="Tags (comma-separated, e.g. cms, blog, mysql)"
                  className="w-full px-2.5 py-1.5 bg-gray-950 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-600 focus:outline-none focus:border-brand-500 disabled:opacity-50"
                />
              </div>

              {/* Editable template JSON — color-coded, line-numbered, scrolls internally */}
              <JsonEditor value={tplJson} onChange={editJson} valid={!hasContent || !!parsed} height="min(20rem, 40vh)" />
              {hasContent && !parsed && (
                <p className="text-xs text-red-400/80">⚠ The JSON isn't valid yet — fix it to validate and save.</p>
              )}

              {/* Validate → Save (Save unlocks only after a successful validation) */}
              <div className="flex items-center gap-2 flex-wrap pt-1">
                <button
                  onClick={runValidate}
                  disabled={!parsed || validation?.checking}
                  className={`${btnBase} disabled:opacity-40 disabled:cursor-not-allowed ${
                    validation?.ok ? 'border-green-600 bg-green-950 text-green-400'
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
                    saveState === 'saved'   ? 'border-green-600 bg-green-950 text-green-400' :
                    saveState === 'saving'  ? 'border-gray-700 text-gray-500 cursor-wait' :
                    !validation?.ok         ? 'border-gray-800 text-gray-600 cursor-not-allowed' :
                    'border-brand-600 bg-brand-950 text-brand-300 hover:bg-brand-900'
                  }`}
                >
                  {saveState === 'saved' ? '✓ Saved' : saveState === 'saving' ? 'Saving…' : '💾 Save as template'}
                </button>
                {validation && !validation.checking && (
                  <span className="text-xs text-gray-600">
                    {validation.ok ? '' : `${validation.errors.length} issue${validation.errors.length !== 1 ? 's' : ''} to fix`}
                  </span>
                )}
              </div>

              {/* Validation results */}
              {validation && !validation.checking && !validation.ok && (
                <div className="rounded-lg bg-red-950/30 border border-red-700/30 p-3">
                  <p className="text-red-400 text-xs font-semibold mb-1">Validation failed</p>
                  <ul className="text-red-300/80 text-xs list-disc list-inside space-y-0.5">
                    {validation.errors.map((er, i) => <li key={i}>{er}</li>)}
                  </ul>
                </div>
              )}
              {validation?.ok && saveState !== 'saved' && (
                <p className="text-xs text-green-400/80">
                  ✓ Valid — name <code className="font-mono">{validation.name}</code> is available ({validation.services} service{validation.services !== 1 ? 's' : ''}). Ready to save.
                </p>
              )}

              {/* Save error / success */}
              {saveState?.error && (
                <div className="px-3 py-2 rounded-lg bg-red-950/30 border border-red-700/30">
                  <p className="text-red-400 text-xs">{saveState.error}</p>
                </div>
              )}
              {saveState === 'saved' && parsed && (
                <p className="text-xs text-green-400/70">
                  Saved to <code className="font-mono">{parsed.name}.json</code> — available immediately in the New Workspace wizard (no rebuild needed).
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
const ROW_BTN_PRIMARY = `${ROW_BTN} border-amber-700/60 text-amber-300 hover:bg-amber-900/30`            // Roll back / Restore
const ROW_BTN_NEUTRAL = `${ROW_BTN} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500` // Download
const ROW_BTN_DELETE  = `${ROW_BTN} border-gray-700 text-gray-500 hover:text-red-400 hover:border-red-700/60` // Delete
const createBtnClass  = (enabled) =>
  `w-full py-2 text-sm font-semibold rounded-lg transition-colors ${
    enabled ? 'bg-brand-600 hover:bg-brand-700 text-white' : 'bg-gray-800 text-gray-600 cursor-not-allowed'
  }`

// A single saved-file row (snapshot or backup) — identical layout for both.
function FileRow({ title, meta, children }) {
  return (
    <div className="flex items-center gap-3 px-3 py-2.5 bg-gray-800/50 border border-gray-700/60 rounded-lg">
      <div className="flex-1 min-w-0">
        <p className="text-xs font-mono text-gray-300 truncate" title={title}>{title}</p>
        <p className="text-xs text-gray-600 mt-0.5">{meta}</p>
      </div>
      {children}
    </div>
  )
}

// A saved-files list header with a count.
function SavedList({ label, count, children }) {
  return (
    <div>
      <div className="border-b border-gray-800 pb-2 mb-3">
        <h4 className="text-sm font-semibold text-gray-300">
          {label} <span className="ml-1 text-xs font-normal text-gray-600">({count})</span>
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
        busy ? 'opacity-60 cursor-wait border-gray-700'
        : over ? 'border-brand-500 bg-brand-950/20 cursor-pointer'
        : 'border-gray-700 hover:border-brand-600 cursor-pointer'
      }`}
    >
      <p className="text-xs text-gray-400">{busy ? busyLabel : hint}</p>
      <input ref={ref} type="file" accept={accept} className="hidden"
        onChange={e => { const f = e.target.files?.[0]; e.target.value = ''; if (f) onFile(f) }} />
    </div>
  )
}

function WorkspaceBackup() {
  const qc    = useQueryClient()
  const token = useAuthStore(s => s.token)
  const confirm = useConfirm()

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
  const [restoringArchive, setRestoringArchive] = useState({})
  const [archiveUploading, setArchiveUploading] = useState(false)

  // Workspace list
  const { data: workspaces = [] } = useQuery({
    queryKey: ['workspaces'],
    queryFn: fetchWorkspaces,
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

  // Refresh archives list when job completes
  const { data: archives = [], refetch: refetchArchives } = useQuery({
    queryKey: ['workspace-archives'],
    queryFn: listWorkspaceArchives,
  })

  // Config snapshots
  const { data: snapshots = [], refetch: refetchSnapshots } = useQuery({
    queryKey: ['workspace-snapshots'],
    queryFn: fetchWorkspaceSnapshots,
  })

  // When job completes/fails, refresh archives
  if (activeJob?.status === 'completed' || activeJob?.status === 'failed') {
    if (activeJob.status === 'completed') refetchArchives()
  }

  async function startBackup() {
    if (!selectedWs) return
    setBackupErr(null)
    setActiveJobId(null)
    try {
      const job = await startWorkspaceBackup(selectedWs, bkpName.trim())
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
      title: `Restore ${a.workspace || 'workspace'}?`,
      message: `This restores "${a.workspace || 'the workspace'}" from "${a.filename}", including all volume data. If a workspace named "${a.workspace}" already exists it will be REPLACED and its current data lost. Stop its containers first to avoid conflicts.`,
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
    if (!selectedWs) return
    setSnapBusy(true); setSnapMsg(null)
    try {
      const snap = await createWorkspaceSnapshot(selectedWs, snapName.trim())
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
      title: `Roll back ${snap.workspace || 'workspace'} configuration?`,
      message: `This OVERWRITES the current config.json and every .env file for "${snap.workspace}" with the snapshot "${snap.filename}". Any configuration changed since then is lost. If a DB password, API token or other key has changed since this snapshot was taken, you must update it to the new value afterwards and redeploy. Data volumes are NOT touched.`,
      confirmLabel: 'Roll back',
    })
    if (!ok) return
    setRollingBack(s => ({ ...s, [snap.filename]: true }))
    try {
      const res = await rollbackWorkspaceSnapshot(snap.filename)
      setSnapMsg({ ok: true, text: `Rolled "${res.workspace}" back to ${snap.filename}. Review secrets in Edit Workspace, then redeploy.` })
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
      <div className="bg-gray-800/50 border border-gray-700/60 rounded-xl p-4 text-sm text-gray-400 leading-relaxed">
        Pick a workspace, then take a lightweight <strong className="text-gray-300">configuration snapshot</strong>{' '}
        (<code className="font-mono text-xs">.rws</code> — <code className="font-mono text-xs">config.json</code> + each{' '}
        <code className="font-mono text-xs">.env</code>, no data) or a <strong className="text-gray-300">full backup</strong>{' '}
        (<code className="font-mono text-xs">.rwb</code> — config plus all volume data). Both can be downloaded, uploaded
        and restored on the server.
      </div>

      {/* Shared workspace selector */}
      <div className="bg-gray-900 border border-gray-800 rounded-xl p-4">
        <label className="block text-xs font-medium text-gray-400 mb-1.5">Workspace</label>
        <select
          value={selectedWs}
          onChange={e => { setSelectedWs(e.target.value); setSnapMsg(null); setBkpMsg(null); setBackupErr(null); setActiveJobId(null) }}
          className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500"
        >
          <option value="">— select workspace —</option>
          {workspaces.map(ws => <option key={ws.name} value={ws.name}>{ws.name}</option>)}
        </select>
        <p className="text-xs text-gray-600 mt-2">Applies to both <strong className="text-gray-500">Take snapshot</strong> and <strong className="text-gray-500">Start backup</strong> below.</p>
      </div>

      {/* Two consistent panels */}
      <div className="grid grid-cols-2 gap-6 items-start">
        {/* ── Configuration snapshot (.rws) ── */}
        <section className="space-y-3">
          <div>
            <h3 className="text-base font-semibold text-white">Configuration snapshot <span className="text-xs font-normal text-gray-600">.rws</span></h3>
            <p className="text-sm text-gray-500 mt-1">
              Just <code className="font-mono text-xs">config.json</code> and each env's{' '}
              <code className="font-mono text-xs">.env</code> (secrets included). No volume data — fast to take, easy to roll back.
            </p>
          </div>

          <div className="bg-gray-900 border border-gray-800 rounded-xl p-4 space-y-3">
            <div>
              <label className="block text-xs font-medium text-gray-400 mb-1.5">
                Snapshot name <span className="text-gray-600 font-normal">(optional)</span>
              </label>
              <input
                value={snapName}
                onChange={e => setSnapName(e.target.value)}
                placeholder={selectedWs ? `${selectedWs}_<timestamp>.rws` : 'auto: <workspace>_<timestamp>.rws'}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-600 focus:outline-none focus:border-brand-500"
              />
            </div>
            <button onClick={takeSnapshot} disabled={!selectedWs || snapBusy} className={createBtnClass(!!selectedWs && !snapBusy)}>
              {snapBusy ? 'Saving…' : 'Take configuration snapshot'}
            </button>
            {snapMsg && <p className={`text-xs px-1 ${snapMsg.ok ? 'text-green-400' : 'text-red-400'}`}>{snapMsg.text}</p>}
          </div>

          <SavedList label="Saved snapshots" count={snapshots.length}>
            {snapshots.length === 0
              ? <p className="text-xs text-gray-600 py-4 text-center">No snapshots yet.</p>
              : (
                <div className="space-y-2">
                  {snapshots.map(s => (
                    <FileRow key={s.filename} title={s.filename}
                      meta={<>{s.workspace ? <span className="text-gray-500">{s.workspace}</span> : 'unknown workspace'} · {fmtDate(s.created_at)} · {fmtBytes(s.size_bytes)}</>}>
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
          <div className="bg-gray-800/30 border border-gray-700/40 rounded-xl p-4 space-y-2">
            <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider">Rolling back configuration</p>
            <ol className="text-xs text-gray-500 space-y-1 list-decimal list-inside">
              <li>Click <strong className="text-gray-400">Roll back</strong> on a snapshot to overwrite the workspace's <code className="font-mono text-gray-400">config.json</code> and every <code className="font-mono text-gray-400">.env</code></li>
              <li>To use a snapshot from elsewhere, drop the <code className="font-mono text-gray-400">.rws</code> above — it joins the list, then roll back to it</li>
              <li>If a secret (DB password, API key…) changed since the snapshot, update it afterward and redeploy</li>
              <li>Volume data is never touched — use a full backup for that</li>
            </ol>
          </div>
        </section>

        {/* ── Full backup (.rwb) ── */}
        <section className="space-y-3">
          <div>
            <h3 className="text-base font-semibold text-white">Full backup <span className="text-xs font-normal text-gray-600">.rwb</span></h3>
            <p className="text-sm text-gray-500 mt-1">
              Config plus <strong className="text-gray-400">all volume data</strong> (per-env backup folders excluded).
              Larger and slower; restoring re-creates the whole workspace.
            </p>
          </div>

          <div className="bg-gray-900 border border-gray-800 rounded-xl p-4 space-y-3">
            <div>
              <label className="block text-xs font-medium text-gray-400 mb-1.5">
                Backup name <span className="text-gray-600 font-normal">(optional)</span>
              </label>
              <input
                value={bkpName}
                onChange={e => setBkpName(e.target.value)}
                placeholder={selectedWs ? `${selectedWs}-<timestamp>.rwb` : 'auto: <workspace>-<timestamp>.rwb'}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-600 focus:outline-none focus:border-brand-500"
              />
            </div>
            <button onClick={startBackup} disabled={!selectedWs || isRunning} className={createBtnClass(!!selectedWs && !isRunning)}>
              {isRunning ? '⏳ Backing up…' : 'Start full backup'}
            </button>
            {backupErr && <p className="text-xs text-red-400 px-1">{backupErr}</p>}

            {/* Job status card */}
            {activeJob && (
              <div className={`px-4 py-3 rounded-xl border text-sm ${
                activeJob.status === 'running'   ? 'bg-brand-950/40 border-brand-700/40 text-brand-300' :
                activeJob.status === 'completed' ? 'bg-green-950/40 border-green-700/40 text-green-300' :
                'bg-red-950/40 border-red-700/40 text-red-300'
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
              <p className="text-xs text-gray-600 px-1">Stored on the server; download to keep a copy off-box.</p>
            )}
          </div>

          <SavedList label="Backups on server" count={archives.length}>
            {bkpMsg && <p className={`text-xs px-1 mb-2 ${bkpMsg.ok ? 'text-green-400' : 'text-red-400'}`}>{bkpMsg.text}</p>}
            {archives.length === 0
              ? <p className="text-xs text-gray-600 py-4 text-center">No backups yet.</p>
              : (
                <div className="space-y-2">
                  {archives.map(a => (
                    <FileRow key={a.filename} title={a.filename}
                      meta={<>{a.workspace ? <span className="text-gray-500">{a.workspace}</span> : 'unknown workspace'} · {fmtDate(a.created_at)} · {fmtBytes(a.size_bytes)}</>}>
                      <button onClick={() => restoreArchive(a)} disabled={restoringArchive[a.filename]} className={ROW_BTN_PRIMARY}>
                        {restoringArchive[a.filename] ? '…' : 'Restore'}
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
          <div className="bg-gray-800/30 border border-gray-700/40 rounded-xl p-4 space-y-2">
            <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider">Restoring a full backup</p>
            <ol className="text-xs text-gray-500 space-y-1 list-decimal list-inside">
              <li>Stop the workspace's containers if it already exists</li>
              <li>To use a backup from elsewhere, drop the <code className="font-mono text-gray-400">.rwb</code> above — it joins the list</li>
              <li>Click <strong className="text-gray-400">Restore</strong> on a backup row (an existing workspace is replaced)</li>
              <li>The workspace appears in the sidebar immediately</li>
              <li>Run <code className="font-mono text-gray-400">./run.sh refresh &lt;env&gt;</code> to regenerate compose files, then redeploy</li>
            </ol>
          </div>
        </section>
      </div>
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

const TOOLS = [
  {
    id: 'workspace-backup',
    label: 'Workspace Manager',
    description: 'Snapshot or roll back workspace configuration, and create/restore full workspace backups (config + data).',
    component: WorkspaceBackup,
  },
  {
    id: 'compose-to-template',
    label: 'Template Manager',
    description: 'Convert a docker-compose.yml, or upload an existing template, then edit, validate and save it as a reusable Rigger prebuilt template.',
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
          <h1 className="text-xl font-bold text-white">Tools</h1>
          <p className="text-sm text-gray-500 mt-0.5">Utilities for working with Rigger workspaces and templates.</p>
        </div>

        {/* Tool tabs */}
        <div className="flex items-center gap-1 mb-6 border-b border-gray-800 pb-0">
          {TOOLS.map(tool => (
            <button
              key={tool.id}
              onClick={() => setActiveTool(tool.id)}
              className={`px-4 py-2 text-sm font-medium rounded-t-lg transition-colors border-b-2 -mb-px ${
                activeTool === tool.id
                  ? 'border-brand-500 text-white bg-gray-900'
                  : 'border-transparent text-gray-500 hover:text-gray-300 hover:bg-gray-800/50'
              }`}
            >{tool.label}</button>
          ))}
        </div>

        {/* Active tool */}
        <div className="bg-gray-900 border border-gray-800 rounded-2xl p-6">
          {ActiveComponent && <ActiveComponent />}
        </div>
      </div>
    </Layout>
  )
}
