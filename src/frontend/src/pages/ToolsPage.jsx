import { useState, useRef } from 'react'
import Layout from '../components/Layout'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import {
  saveToolTemplate, fetchTemplates,
  startWorkspaceBackup, getBackupJob,
  listWorkspaceArchives, deleteWorkspaceArchive,
  restoreWorkspace, fetchWorkspaces,
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

function ComposeToTemplate() {
  const [input, setInput]           = useState('')      // compose YAML (left panel)
  const [templateName, setName]     = useState('')      // name used by the compose converter
  const [convertError, setConvertError] = useState('')  // compose → JSON parse error
  const [tplJson, setTplJson]       = useState('')      // editable template JSON — the source of truth
  const [tagsText, setTagsText]     = useState('')      // comma-separated tags helper (synced on load)
  const [validation, setValidation] = useState(null)    // null | {checking} | {ok:true,...} | {ok:false,errors:[]}
  const [copied, setCopied]         = useState(false)
  const [saveState, setSaveState]   = useState(null)    // null | 'saving' | 'saved' | { error }
  const fileInputRef                = useRef(null)       // compose import
  const tplFileRef                  = useRef(null)       // template upload

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

  // Load a template object into the editor (from Convert or Upload).
  function loadTemplate(tpl) {
    editJson(JSON.stringify(tpl, null, 2))
    setTagsText(Array.isArray(tpl?.tags) ? tpl.tags.join(', ') : '')
  }

  // Patch one top-level key on the parsed template and write it back to the editor.
  function patchField(key, value) {
    if (!parsed) return
    editJson(JSON.stringify({ ...parsed, [key]: value }, null, 2))
  }

  function convert() {
    if (!input.trim()) return
    const res = convertCompose(input, templateName || 'my-stack')
    if (res.error) { setConvertError(res.error); return }
    setConvertError('')
    loadTemplate(res.result)
  }

  // Paste compose from clipboard
  async function pasteFromClipboard() {
    try {
      const text = await navigator.clipboard.readText()
      if (text) setInput(text)
    } catch {
      // Browser denied clipboard access — focus the textarea so Ctrl+V works
      document.getElementById('compose-input')?.focus()
    }
  }

  // Import a docker-compose.yml into the left textarea
  function importFile(e) {
    const file = e.target.files?.[0]
    if (!file) return
    const reader = new FileReader()
    reader.onload = ev => {
      setInput(ev.target.result || '')
      // Auto-derive template name from filename
      if (!templateName) {
        const base = file.name.replace(/\.(ya?ml|txt)$/i, '').replace(/[^a-z0-9]/gi, '-').toLowerCase()
        setName(base || '')
      }
    }
    reader.readAsText(file)
    e.target.value = ''  // reset so same file can be re-imported
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
        Paste or import a <code className="font-mono text-gray-300 text-xs">docker-compose.yml</code> on the left and
        convert it — or <strong className="text-gray-300">Upload template</strong> on the right to load an existing
        one. Edit the template JSON, fill in label / description / tags, then <strong className="text-gray-300">Validate</strong>{' '}
        (which also checks the name is unique) to unlock <strong className="text-gray-300">Save as template</strong>.
      </div>

      <div className="grid grid-cols-2 gap-6 items-start">
        {/* ── Left: Input ── */}
        <div className="space-y-2">
          {/* Input toolbar */}
          <div className="flex items-center justify-between">
            <label className="text-sm font-semibold text-gray-300">docker-compose.yml</label>
            <div className="flex items-center gap-2">
              <button onClick={pasteFromClipboard}
                className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500`}>
                ⎘ Paste
              </button>
              <button onClick={() => fileInputRef.current?.click()}
                className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500`}>
                ↑ Import file
              </button>
              <button onClick={() => setInput(PLACEHOLDER)}
                className="text-xs text-brand-400 hover:text-brand-300 transition-colors">
                Example
              </button>
              <input ref={fileInputRef} type="file" accept=".yml,.yaml,.txt"
                onChange={importFile} className="hidden" />
            </div>
          </div>

          {/* Compose textarea — Ctrl+V and right-click paste work natively */}
          <textarea
            id="compose-input"
            value={input}
            onChange={e => setInput(e.target.value)}
            placeholder={PLACEHOLDER}
            spellCheck={false}
            className="w-full px-3 py-3 bg-gray-950 border border-gray-700 rounded-xl text-gray-200 text-xs font-mono placeholder-gray-700 focus:outline-none focus:border-brand-500 resize-y leading-relaxed"
            style={{ minHeight: '28rem' }}
          />

          {/* Convert bar */}
          <div className="flex items-center gap-3 pt-1">
            <input
              type="text"
              value={templateName}
              onChange={e => setName(e.target.value)}
              onKeyDown={e => e.key === 'Enter' && convert()}
              placeholder="Template name (e.g. Ghost CMS)"
              className="flex-1 px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm placeholder-gray-500 focus:outline-none focus:border-brand-500"
            />
            <button
              onClick={convert}
              disabled={!input.trim()}
              className={`px-5 py-2 text-sm font-semibold rounded-lg transition-colors shrink-0 ${
                input.trim() ? 'bg-brand-600 hover:bg-brand-700 text-white' : 'bg-gray-800 text-gray-600 cursor-not-allowed'
              }`}
            >Convert →</button>
          </div>
        </div>

        {/* ── Right: Template editor ── */}
        <div className="space-y-2">
          {/* Editor toolbar */}
          <div className="flex items-center justify-between gap-2 flex-wrap">
            <label className="text-sm font-semibold text-gray-300">Rigger template JSON</label>
            <div className="flex items-center gap-2">
              <button onClick={() => tplFileRef.current?.click()}
                className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500`}>
                ↑ Upload template
              </button>
              <input ref={tplFileRef} type="file" accept=".json,application/json"
                onChange={uploadTemplateFile} className="hidden" />
              {hasContent && (
                <>
                  <button onClick={copyResult}
                    className={`${btnBase} ${copied ? 'border-green-600 bg-green-950 text-green-400' : 'border-gray-700 text-gray-400 hover:text-gray-200'}`}>
                    {copied ? '✓ Copied' : '⎘ Copy'}
                  </button>
                  <button onClick={downloadResult} disabled={!parsed}
                    className={`${btnBase} border-gray-700 text-gray-400 hover:text-gray-200 disabled:opacity-40 disabled:cursor-not-allowed`}>
                    ⬇ Download
                  </button>
                </>
              )}
            </div>
          </div>

          {/* Conversion error (compose → JSON) */}
          {convertError && (
            <div className="rounded-xl bg-red-950/40 border border-red-700/40 p-4">
              <p className="text-red-400 text-sm font-medium">Conversion failed</p>
              <p className="text-red-300/70 text-xs mt-1">{convertError}</p>
            </div>
          )}

          {/* Empty state */}
          {!hasContent && !convertError && (
            <div className="rounded-xl bg-gray-950 border border-gray-800 flex items-center justify-center"
              style={{ minHeight: '28rem' }}>
              <p className="text-gray-700 text-sm text-center px-6">
                Convert a compose file, or <span className="text-gray-500">Upload template</span> to load an existing one for editing.
              </p>
            </div>
          )}

          {hasContent && (
            <>
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

              {/* Editable template JSON — the source of truth */}
              <textarea
                value={tplJson}
                onChange={e => editJson(e.target.value)}
                spellCheck={false}
                className={`w-full px-4 py-3 bg-gray-950 border rounded-xl text-gray-200 text-xs font-mono leading-relaxed focus:outline-none resize-y ${parsed ? 'border-gray-700 focus:border-brand-500' : 'border-red-700/60 focus:border-red-500'}`}
                style={{ minHeight: '22rem' }}
              />
              {!parsed && (
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
            </>
          )}
        </div>
      </div>
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

function WorkspaceBackup() {
  const qc    = useQueryClient()
  const token = useAuthStore(s => s.token)
  const confirm = useConfirm()

  const [selectedWs, setSelectedWs]   = useState('')
  const [activeJobId, setActiveJobId] = useState(null)  // job ID string while running
  const [backupErr, setBackupErr]     = useState(null)
  const [restoreFile, setRestoreFile] = useState(null)
  const [restoreForce, setForce]      = useState(false)
  const [restoreMsg, setRestoreMsg]   = useState(null)
  const [restoring, setRestoring]     = useState(false)
  const [deleting, setDeleting]       = useState({})
  const [downloading, setDownloading] = useState({})
  const restoreRef = useRef(null)
  // Config snapshot (.rws) state
  const [snapName, setSnapName]         = useState('')
  const [snapBusy, setSnapBusy]         = useState(false)
  const [snapMsg, setSnapMsg]           = useState(null) // { ok, text }
  const [snapDeleting, setSnapDeleting] = useState({})
  const [rollingBack, setRollingBack]   = useState({})
  const [snapUploading, setSnapUploading] = useState(false)
  const snapUploadRef = useRef(null)

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
      const job = await startWorkspaceBackup(selectedWs)
      setActiveJobId(job.id)
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
      alert(`Download failed: ${e.message}`)
    } finally {
      setDownloading(d => ({ ...d, [filename]: false }))
    }
  }

  async function deleteArchive(filename) {
    if (!(await confirm({
      title: 'Delete archive?',
      message: `Permanently delete the backup archive "${filename}"? This can't be undone.`,
      confirmLabel: 'Delete',
    }))) return
    setDeleting(d => ({ ...d, [filename]: true }))
    try {
      await deleteWorkspaceArchive(filename)
      refetchArchives()
      qc.removeQueries({ queryKey: ['workspace-archives'] })
    } catch (e) {
      alert(e?.response?.data?.error || e.message)
    } finally {
      setDeleting(d => ({ ...d, [filename]: false }))
    }
  }

  async function doRestore() {
    if (!restoreFile) return
    setRestoring(true)
    setRestoreMsg(null)
    const fd = new FormData()
    fd.append('archive', restoreFile)
    if (restoreForce) fd.append('force', 'true')
    try {
      const res = await restoreWorkspace(fd)
      setRestoreMsg({ ok: true, text: `Workspace "${res.workspace}" restored successfully.` })
      qc.invalidateQueries({ queryKey: ['workspaces'] })
      setRestoreFile(null)
      setForce(false)
    } catch (e) {
      const msg = e?.response?.data?.error || e.message
      setRestoreMsg({ ok: false, text: msg, conflict: e?.response?.status === 409 })
    } finally {
      setRestoring(false)
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

  async function uploadSnapshot(e) {
    const f = e.target.files?.[0]
    e.target.value = '' // allow re-uploading the same file
    if (!f) return
    setSnapUploading(true); setSnapMsg(null)
    try {
      const fd = new FormData()
      fd.append('snapshot', f)
      const snap = await uploadWorkspaceSnapshot(fd)
      setSnapMsg({ ok: true, text: `Uploaded ${snap.filename}${snap.workspace ? ` (workspace: ${snap.workspace})` : ''}.` })
      refetchSnapshots()
    } catch (err) {
      setSnapMsg({ ok: false, text: err?.response?.data?.error || err.message })
    } finally {
      setSnapUploading(false)
    }
  }

  const isRunning = activeJob?.status === 'running' || (activeJobId && !activeJob)

  return (
    <div className="space-y-8">
      {/* ── Configuration snapshots (.rws) ── */}
      <div className="space-y-4">
        <div>
          <h3 className="text-base font-semibold text-white">Configuration snapshots</h3>
          <p className="text-sm text-gray-500 mt-1">
            Save a lightweight snapshot of just the workspace <strong className="text-gray-400">configuration</strong> —{' '}
            <code className="font-mono text-xs">config.json</code> and each environment's{' '}
            <code className="font-mono text-xs">.env</code> (secrets included) — and roll back to it later.
            No volume data is captured (use a full backup below for that).
          </p>
        </div>

        <div className="bg-gray-900 border border-gray-800 rounded-xl p-4 space-y-3">
          <div className="grid grid-cols-2 gap-3">
            <div>
              <label className="block text-xs font-medium text-gray-400 mb-1.5">Workspace</label>
              <select
                value={selectedWs}
                onChange={e => { setSelectedWs(e.target.value); setSnapMsg(null); setBackupErr(null); setActiveJobId(null) }}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500"
              >
                <option value="">— select workspace —</option>
                {workspaces.map(ws => <option key={ws.name} value={ws.name}>{ws.name}</option>)}
              </select>
            </div>
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
          </div>
          <button
            onClick={takeSnapshot}
            disabled={!selectedWs || snapBusy}
            className={`w-full py-2 text-sm font-semibold rounded-lg transition-colors ${
              !selectedWs || snapBusy ? 'bg-gray-800 text-gray-600 cursor-not-allowed' : 'bg-brand-600 hover:bg-brand-700 text-white'
            }`}
          >{snapBusy ? 'Saving…' : 'Take configuration snapshot'}</button>
          {snapMsg && <p className={`text-xs px-1 ${snapMsg.ok ? 'text-green-400' : 'text-red-400'}`}>{snapMsg.text}</p>}
        </div>

        {/* Saved snapshots */}
        <div>
          <div className="flex items-center justify-between border-b border-gray-800 pb-2 mb-3">
            <h4 className="text-sm font-semibold text-gray-300">
              Saved snapshots <span className="ml-2 text-xs font-normal text-gray-600">({snapshots.length})</span>
            </h4>
            <button
              onClick={() => snapUploadRef.current?.click()}
              disabled={snapUploading}
              className="text-xs px-2.5 py-1 rounded border border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500 transition-colors disabled:opacity-50"
            >{snapUploading ? 'Uploading…' : '↑ Upload .rws'}</button>
            <input ref={snapUploadRef} type="file" accept=".rws" onChange={uploadSnapshot} className="hidden" />
          </div>
          {snapshots.length === 0
            ? <p className="text-xs text-gray-600 py-4 text-center">No snapshots yet.</p>
            : (
              <div className="space-y-2">
                {snapshots.map(s => (
                  <div key={s.filename} className="flex items-center gap-3 px-3 py-2.5 bg-gray-800/50 border border-gray-700/60 rounded-lg">
                    <div className="flex-1 min-w-0">
                      <p className="text-xs font-mono text-gray-300 truncate" title={s.filename}>{s.filename}</p>
                      <p className="text-xs text-gray-600 mt-0.5">
                        {s.workspace ? <span className="text-gray-500">{s.workspace}</span> : 'unknown workspace'} · {fmtDate(s.created_at)} · {fmtBytes(s.size_bytes)}
                      </p>
                    </div>
                    <button onClick={() => rollbackSnapshot(s)} disabled={rollingBack[s.filename]}
                      className="text-xs px-2.5 py-1 rounded border border-amber-700/60 text-amber-300 hover:bg-amber-900/30 transition-colors disabled:opacity-50">
                      {rollingBack[s.filename] ? '…' : 'Roll back'}
                    </button>
                    <button onClick={() => downloadSnapshot(s.filename)}
                      className="text-xs px-2.5 py-1 rounded border border-gray-700 text-gray-400 hover:text-gray-200 transition-colors">Download</button>
                    <button onClick={() => removeSnapshot(s.filename)} disabled={snapDeleting[s.filename]}
                      className="text-xs px-2.5 py-1 rounded border border-gray-700 text-gray-500 hover:text-red-400 transition-colors disabled:opacity-50">
                      {snapDeleting[s.filename] ? '…' : 'Delete'}
                    </button>
                  </div>
                ))}
              </div>
            )}
        </div>
      </div>

      {/* ── Full backup & restore (config + all volume data) ── */}
      <div className="border-t border-gray-800 pt-6">
        <h3 className="text-base font-semibold text-white mb-1">Full backup &amp; restore</h3>
        <div className="bg-gray-800/50 border border-gray-700/60 rounded-xl p-4 text-sm text-gray-400 leading-relaxed">
          Create a full backup of any workspace — config, environment files, and all volume data
          (per-env backup snapshots excluded). Archives are stored on the server; download to keep locally.
          To restore, upload a previously downloaded archive.
          <span className="block mt-1 text-gray-600">
            Tip: stop the workspace's containers before restoring to avoid data conflicts.
          </span>
        </div>
      </div>

      <div className="grid grid-cols-2 gap-8 items-start">
        {/* ── Left: Create Backup + Archives ── */}
        <div className="space-y-4">
          <h3 className="text-sm font-semibold text-gray-300 border-b border-gray-800 pb-2">Create backup</h3>

          <div className="space-y-3">
            <div>
              <label className="block text-xs font-medium text-gray-400 mb-1.5">Workspace</label>
              <select
                value={selectedWs}
                onChange={e => { setSelectedWs(e.target.value); setBackupErr(null); setActiveJobId(null) }}
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500"
              >
                <option value="">— select workspace —</option>
                {workspaces.map(ws => <option key={ws.name} value={ws.name}>{ws.name}</option>)}
              </select>
            </div>

            <button
              onClick={startBackup}
              disabled={!selectedWs || isRunning}
              className={`w-full py-2 text-sm font-semibold rounded-lg transition-colors ${
                !selectedWs || isRunning
                  ? 'bg-gray-800 text-gray-600 cursor-not-allowed'
                  : 'bg-brand-600 hover:bg-brand-700 text-white'
              }`}
            >{isRunning ? '⏳ Backing up…' : 'Start backup'}</button>

            {backupErr && (
              <p className="text-xs text-red-400 px-1">{backupErr}</p>
            )}

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
          </div>

          {/* Archives list */}
          <div className="pt-2">
            <h3 className="text-sm font-semibold text-gray-300 border-b border-gray-800 pb-2 mb-3">
              Archives on server
              <span className="ml-2 text-xs font-normal text-gray-600">({archives.length})</span>
            </h3>
            {archives.length === 0
              ? <p className="text-xs text-gray-600 py-4 text-center">No archives yet.</p>
              : (
                <div className="space-y-2">
                  {archives.map(a => (
                    <div key={a.filename} className="flex items-center gap-3 px-3 py-2.5 bg-gray-800/50 border border-gray-700/60 rounded-lg">
                      <div className="flex-1 min-w-0">
                        <p className="text-xs font-mono text-gray-300 truncate" title={a.filename}>{a.filename}</p>
                        <p className="text-xs text-gray-600 mt-0.5">{fmtDate(a.created_at)} · {fmtBytes(a.size_bytes)}</p>
                      </div>
                      <button
                        onClick={() => downloadArchive(a.filename)}
                        disabled={!!downloading[a.filename]}
                        className="text-xs px-2.5 py-1 rounded border border-gray-700 text-gray-400 hover:text-gray-200 hover:border-gray-500 transition-colors shrink-0"
                      >{downloading[a.filename] ? '…' : '⬇ Download'}</button>
                      <button
                        onClick={() => deleteArchive(a.filename)}
                        disabled={!!deleting[a.filename]}
                        className="text-xs px-2.5 py-1 rounded border border-gray-700 text-red-500 hover:text-red-300 hover:border-red-700 transition-colors shrink-0"
                      >{deleting[a.filename] ? '…' : 'Delete'}</button>
                    </div>
                  ))}
                </div>
              )}
          </div>
        </div>

        {/* ── Right: Restore ── */}
        <div className="space-y-4">
          <h3 className="text-sm font-semibold text-gray-300 border-b border-gray-800 pb-2">Restore workspace</h3>

          <div className="space-y-3">
            <div
              onClick={() => restoreRef.current?.click()}
              onDragOver={e => e.preventDefault()}
              onDrop={e => { e.preventDefault(); const f = e.dataTransfer.files?.[0]; if (f) { setRestoreFile(f); setRestoreMsg(null); setForce(false) } }}
              className="border-2 border-dashed border-gray-700 hover:border-brand-600 rounded-xl p-8 text-center cursor-pointer transition-colors"
            >
              {restoreFile ? (
                <div>
                  <p className="text-sm text-white font-medium">{restoreFile.name}</p>
                  <p className="text-xs text-gray-500 mt-1">{fmtBytes(restoreFile.size)}</p>
                  <button type="button" onClick={e => { e.stopPropagation(); setRestoreFile(null); setRestoreMsg(null); setForce(false) }}
                    className="text-xs text-red-400 hover:text-red-300 mt-2">Remove</button>
                </div>
              ) : (
                <div>
                  <p className="text-sm text-gray-400">Drop archive here or click to browse</p>
                  <p className="text-xs text-gray-600 mt-1">.tar.gz workspace archive</p>
                </div>
              )}
              <input ref={restoreRef} type="file" accept=".tar.gz,.gz"
                onChange={e => { const f = e.target.files?.[0]; if (f) { setRestoreFile(f); setRestoreMsg(null); setForce(false) }; e.target.value = '' }}
                className="hidden" />
            </div>

            <label className="flex items-center gap-2 cursor-pointer select-none">
              <input type="checkbox" checked={restoreForce} onChange={e => setForce(e.target.checked)}
                className="accent-brand-500 w-3.5 h-3.5" />
              <span className="text-xs text-gray-400">Overwrite if workspace already exists</span>
            </label>

            <button
              onClick={doRestore}
              disabled={!restoreFile || restoring}
              className={`w-full py-2 text-sm font-semibold rounded-lg transition-colors ${
                !restoreFile || restoring
                  ? 'bg-gray-800 text-gray-600 cursor-not-allowed'
                  : 'bg-green-700 hover:bg-green-600 text-white'
              }`}
            >{restoring ? '⏳ Restoring…' : 'Restore workspace'}</button>

            {restoreMsg && (
              <div className={`px-4 py-3 rounded-xl border text-sm ${
                restoreMsg.ok
                  ? 'bg-green-950/40 border-green-700/40 text-green-300'
                  : 'bg-red-950/40 border-red-700/40 text-red-300'
              }`}>
                <p>{restoreMsg.text}</p>
                {restoreMsg.conflict && (
                  <p className="text-xs mt-1.5 opacity-70">Enable "Overwrite if workspace already exists" and try again.</p>
                )}
              </div>
            )}

            <div className="bg-gray-800/30 border border-gray-700/40 rounded-xl p-4 space-y-2">
              <p className="text-xs font-semibold text-gray-500 uppercase tracking-wider">Steps to restore</p>
              <ol className="text-xs text-gray-500 space-y-1 list-decimal list-inside">
                <li>Stop the workspace's containers if running</li>
                <li>Download the archive from the list on the left</li>
                <li>Drop or select the archive above</li>
                <li>Click Restore — workspace appears in the sidebar immediately</li>
                <li>Run <code className="font-mono text-gray-400">./run.sh refresh &lt;env&gt;</code> to regenerate compose files</li>
              </ol>
            </div>
          </div>
        </div>
      </div>
    </div>
  )
}

// ── Page ──────────────────────────────────────────────────────────────────────

const TOOLS = [
  {
    id: 'compose-to-template',
    label: 'Template Manager',
    description: 'Convert a docker-compose.yml, or upload an existing template, then edit, validate and save it as a reusable Rigger prebuilt template.',
    component: ComposeToTemplate,
  },
  {
    id: 'workspace-backup',
    label: 'Workspace Manager',
    description: 'Snapshot or roll back workspace configuration, and create/restore full workspace backups (config + data).',
    component: WorkspaceBackup,
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
