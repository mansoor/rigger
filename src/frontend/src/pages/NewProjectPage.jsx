import { useState, useEffect, useRef } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { fetchTemplates, fetchTemplate, recordTemplateUse, openCreateSocket, fetchWorkspaceBackupTargets, fetchWorkspaceHosts, fetchWorkspaceSettings, scanRepo, uploadSource, fetchBlueprints, fetchWorkspaces } from '../lib/api'
import RegistryPicker from '../components/RegistryPicker'
import DatabaseSelect from '../components/DatabaseSelect'
import ManagedServices from '../components/ManagedServices'
import { useWorkspaceStore } from '../store/workspace'
import KeyField from '../components/KeyField'
import TrashIcon from '../components/TrashIcon'
import PortWarnings from '../components/PortWarnings'
import { BackupScheduleEditor } from '../components/BackupSchedules'
import DropZone from '../components/DropZone'
import { portConflicts, hostPortsFromMappings } from '../lib/ports'
import { usePortConflicts } from '../hooks/usePortConflicts'

// ── Shared UI primitives ──────────────────────────────────────────────────────

function Label({ children, required }) {
  return (
    <label className="block text-sm font-medium text-content mb-1">
      {children}{required && <span className="text-danger-fg ml-0.5">*</span>}
    </label>
  )
}

function Input({ value, onChange, placeholder, type = 'text', error, ...rest }) {
  return (
    <>
      <input
        type={type} value={value} onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        className={`w-full px-3 py-2 bg-surface-raised border rounded-lg text-content-strong placeholder-content-subtle text-sm focus:outline-none transition-colors ${
          error ? 'border-danger focus:border-danger' : 'border-border-strong focus:border-brand-500'
        }`}
        {...rest}
      />
      {error && <p className="text-danger-fg text-xs mt-1">{error}</p>}
    </>
  )
}

function Select({ value, onChange, options }) {
  return (
    <select
      value={value} onChange={e => onChange(e.target.value)}
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500"
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
        <p className="text-sm text-content">{label}</p>
        {hint && <p className="text-xs text-content-subtle">{hint}</p>}
      </div>
      <button
        type="button" onClick={() => onChange(!checked)}
        className={`relative w-10 h-5 rounded-full transition-colors ${checked ? 'bg-brand-600' : 'bg-surface-overlay'}`}
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
      <h2 className="text-xl font-semibold text-content-strong">{title}</h2>
      {subtitle && <p className="text-sm text-content-muted mt-0.5">{subtitle}</p>}
    </div>
  )
}

// ── Step 1: Project ───────────────────────────────────────────────────────────

function Step1({ data, onChange, errors, onConflict, workspace, defaultHostId }) {
  // Remote hosts available to this workspace (Phase 3) — for the default-host selector.
  const { data: hosts = [] } = useQuery({ queryKey: ['ws-hosts', workspace], queryFn: () => fetchWorkspaceHosts(workspace), enabled: !!workspace })

  // The key (set by KeyField) is the identity; display names may repeat within a
  // workspace. onConflict carries key-validity up so validate() can block Continue.
  const resourcePrefix = workspace && data.key ? `${workspace}_${data.key}` : ''

  return (
    <div className="space-y-5">
      <StepHeader step={1} title="Project" subtitle={`Name your project in workspace "${workspace}" and pick a default host.`} />

      <div>
        <Label required>Project name</Label>
        <Input
          value={data.name} onChange={v => onChange('name', v)}
          placeholder="My App" error={errors.name}
        />
        {!errors.name && (
          <p className="text-xs text-content-subtle mt-1">A descriptive label — 1–32 chars, letters/digits/space/dash/underscore.</p>
        )}
      </div>

      <div>
        <KeyField
          type="project" name={data.name} workspace={workspace} label="Project key"
          onChange={(k, valid) => { onChange('key', k); onConflict(valid ? null : 'key') }}
        />
        {resourcePrefix && (
          <p className="text-xs text-content-subtle mt-1">
            Docker resource prefix: <code className="font-mono text-content-muted">{resourcePrefix}</code> (immutable)
          </p>
        )}
      </div>

      {/* Default host (Phase 7) — pre-fills each environment's host; overridable per env */}
      <div>
        <Label>Default host</Label>
        <select
          value={String(data.default_host_id || 0)}
          onChange={e => onChange('default_host_id', Number(e.target.value))}
          className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 transition-colors"
        >
          <option value="0">Local control plane</option>
          {hosts.map(h => <option key={h.id} value={String(h.id)}>{h.name} — {h.address}</option>)}
        </select>
        {defaultHostId > 0 && data.default_host_id === defaultHostId && (
          <p className="text-xs text-content-faint mt-1">Inherited from this workspace's default.</p>
        )}
        <p className="text-xs text-content-subtle mt-1">
          Where environments run by default — override per environment on the next steps. Files are pushed
          and the stack starts on the host the first time you deploy that environment.
        </p>
      </div>
    </div>
  )
}

// ── Step 2: Stack ─────────────────────────────────────────────────────────────

// Display order (Phase 0 of the database-hosting roadmap): curated → image → custom →
// repo → blueprint. "Database Hosting" is appended later (roadmap Phase 4).
const STACK_TYPES = [
  { id: 'prebuilt',  label: 'Pre-built template',  desc: 'Pick from curated stacks — NPM, WordPress, Vaultwarden, Uptime Kuma…' },
  { id: 'image',     label: 'Image stack',          desc: 'Deploy any Docker images — specify your own image names, tags, and ports.' },
  { id: 'database',  label: 'Database hosting', desc: 'Run a managed database (PostgreSQL, MySQL, MariaDB) on its own — no app code.' },
  { id: 'scan',      label: 'From a Git repository',  desc: 'Point Rigger at your app repo — it detects the stack and drafts the services.' },
  { id: 'blueprint', label: 'Start from a stack template', desc: 'No repo yet — pick a stack (Laravel, Spring, Django, Go, .NET…); Rigger scaffolds a starter Dockerfile + services.' },
  { id: 'custom',    label: 'Custom application',  desc: 'Your own code — Laravel, Node.js, Next.js, React with a database.' },
]

// BlueprintStack: pick a stack template (no repo). The selected blueprint's
// seeded service graph (from GET /api/blueprints) becomes the project's
// services[]; the user fine-tunes each in Edit Project after creation.
function BlueprintStack({ data, onChange }) {
  const { data: blueprints = [], isLoading, error } = useQuery({
    queryKey: ['blueprints'], queryFn: fetchBlueprints, staleTime: 5 * 60_000,
  })
  const pick = (bp) => {
    onChange('blueprintId', bp.id)
    onChange('blueprintServices', bp.services || [])
  }
  // The seeds note counts the app service(s) plus a managed database when chosen,
  // so "creates 2 services from the start" reads true (e.g. Laravel + PostgreSQL).
  const appCount = (data.blueprintServices || []).length
  const dbCount = (data.database && data.database !== 'none') ? 1 : 0
  return (
    <div className="space-y-4">
      <div>
        <p className="text-xs font-semibold uppercase tracking-wider text-content-muted mb-2">Programming language</p>
        {isLoading && <p className="text-sm text-content-subtle">Loading stacks…</p>}
        {error && <p className="text-sm text-danger-fg">Couldn’t load stack templates.</p>}
        <div className="grid grid-cols-2 sm:grid-cols-3 gap-2">
          {blueprints.map(bp => (
            <button key={bp.id} type="button" onClick={() => pick(bp)}
              className={`text-left px-3 py-2.5 rounded-lg border transition-colors ${
                data.blueprintId === bp.id
                  ? 'border-brand-600 bg-brand-600/10'
                  : 'border-border hover:border-border-strong bg-surface'}`}>
              <div className="text-sm font-semibold text-content-strong">{bp.label}</div>
              <div className="text-[11px] text-content-faint mt-0.5">
                {bp.language}{bp.port ? ` · :${bp.port}` : ''}{bp.needs_nginx ? ' · +nginx' : ''}
              </div>
            </button>
          ))}
        </div>
      </div>

      {/* Database (+ Redis/Garage) is chosen on the next step (Dependencies) — it's
          project-level, consistent across environments. */}

      {(data.blueprintId || dbCount > 0) && (
        <div className="bg-surface border border-border rounded-xl p-3 text-xs text-content-subtle">
          Seeds {appCount + dbCount} service{appCount + dbCount !== 1 ? 's' : ''}:{' '}
          <span className="font-mono text-content">
            {[...(data.blueprintServices || []).map(s => s.name), dbCount ? data.database : null].filter(Boolean).join(', ')}
          </span>.
          {data.blueprintId && ' Rigger scaffolds a starter Dockerfile you replace with your code — fine-tune everything in '}
          {data.blueprintId && <strong>Edit Project → Services</strong>}{data.blueprintId && '.'}
        </div>
      )}
    </div>
  )
}

// ScanStack: enter a repo, scan it, review the detected service graph. The user
// fine-tunes each service in Edit Project after creation.
function ScanStack({ data, onChange }) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  async function scan() {
    setErr(''); setBusy(true)
    try {
      const d = await scanRepo((data.source_repo || '').trim(), (data.source_branch || '').trim())
      applyDraft(onChange, d)
    } catch (e) {
      onChange('scanDraft', null)
      setErr(e?.response?.data?.error || 'Scan failed')
    } finally { setBusy(false) }
  }
  return (
    <div className="space-y-4">
      <div className="grid grid-cols-[1fr_8rem_auto] gap-2 items-end">
        <div>
          <Label>Source repository</Label>
          <Input value={data.source_repo || ''} onChange={v => onChange('source_repo', v)} placeholder="https://github.com/org/app.git" />
        </div>
        <div>
          <Label>Branch</Label>
          <Input value={data.source_branch || ''} onChange={v => onChange('source_branch', v)} placeholder="main" />
        </div>
        <button type="button" onClick={scan} disabled={busy || !(data.source_repo || '').trim()}
          className="px-4 py-2 rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white text-sm font-semibold">
          {busy ? 'Scanning…' : 'Scan'}
        </button>
      </div>
      <p className="text-xs text-content-subtle">Public HTTPS or token URL — Rigger clones it read-only and detects the stack. SSH keys aren't supported yet.</p>
      {err && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{err}</p>}
      <ScanReview data={data} onChange={onChange} />
    </div>
  )
}

// applyDraft stores a detector draft (from git scan or upload) into wizard state and
// mirrors its managed-dependency flags, so the shared review + create payload pick
// them up. The default draft has managed deps chosen; the review can flip them.
function applyDraft(onChange, d) {
  onChange('scanDraft', d)
  onChange('database', d.database || 'none')
  onChange('dbVersion', d.db_version || '')
  onChange('redis', !!d.redis)
  onChange('storageMinio', d.object_storage === 'minio')
  onChange('storageLocal', d.object_storage === 'local')
}

// ScanReview renders the detected-services review for data.scanDraft — the web-entry
// picker, the managed-dependency offer (managed vs keep-own), and the profile-gated
// opt-in. Shared by the Git-scan path (ScanStack) and the upload-source path
// (CustomStack). Renders nothing until a draft exists.
function ScanReview({ data, onChange }) {
  const draft = data.scanDraft
  const svcs = draft?.services || []
  const candidates = draft?.managed_candidates || []
  const omitted = draft?.profile_omitted || []
  // Set the web entry: web_routed=true on the chosen service, false on the rest.
  function pickWeb(idx) {
    if (!draft) return
    onChange('scanDraft', {
      ...draft,
      services: svcs.map((s, i) => ({ ...s, web_routed: i === idx })),
    })
  }
  const envCount = s => Object.keys(s.env_vars || {}).length

  // ── Managed-dependency offer ──
  // The default draft already chose "managed" (the container is dropped, host refs
  // rebased, flag set). "Keep own" is derived from whether the raw container is back
  // in services[]. Flipping re-adds/removes it and reverses the recorded host
  // rewrites using the {original,managed} pairs the detector captured.
  const isKeepOwn = c => svcs.some(s => s.name === c.detected_name)
  function restoreEnv(s, c, toManaged) {
    const rws = (c.rewrites || []).filter(rw => rw.service === s.name)
    if (!rws.length) return s
    const env_vars = { ...(s.env_vars || {}) }
    for (const rw of rws) env_vars[rw.key] = toManaged ? rw.managed : rw.original
    return { ...s, env_vars }
  }
  function setChoice(c, keepOwn) {
    if (!draft || keepOwn === isKeepOwn(c)) return
    let services
    if (keepOwn) {
      services = [...svcs, c.raw_service].map(s => restoreEnv(s, c, false))
    } else {
      services = svcs.filter(s => s.name !== c.detected_name).map(s => restoreEnv(s, c, true))
    }
    // Env-level (.env) rewrites carry service === "".
    const env_vars = { ...(draft.env_vars || {}) }
    for (const rw of (c.rewrites || [])) {
      if (rw.service === '') env_vars[rw.key] = keepOwn ? rw.original : rw.managed
    }
    onChange('scanDraft', { ...draft, services, env_vars })
    // Clear/restore the flag attributable to THIS candidate only.
    if (c.role === 'redis') {
      onChange('redis', !keepOwn)
    } else {
      onChange('database', keepOwn ? 'none' : (data.database && data.database !== 'none' ? data.database : c.role))
      onChange('dbVersion', keepOwn ? '' : (c.db_version || ''))
    }
  }

  // ── Profile-gated opt-in ──
  const isIncluded = o => svcs.some(s => s.name === o.name)
  function toggleInclude(o, on) {
    if (!draft) return
    onChange('scanDraft', {
      ...draft,
      services: on ? [...svcs, o.service] : svcs.filter(s => s.name !== o.name),
    })
  }

  // ── DB-seed offer (v3) ──
  // Bundled SQL dumps the detector found. Only relevant with a managed DB. Default to
  // the largest dump + auto-import on; the user can switch to "Don't import" (manual
  // later) or toggle auto off.
  const seedCands = draft?.seed_candidates || []
  const hasManagedDB = data.database && data.database !== 'none'
  const showSeed = hasManagedDB && seedCands.length > 0
  const seedSel = data.dbSeedFile ?? (showSeed ? seedCands[0].path : '')
  const seedAuto = data.dbSeedAuto ?? true
  useEffect(() => {
    // Seed a sensible default once candidates appear with a managed DB.
    if (showSeed && data.dbSeedFile === undefined) {
      onChange('dbSeedFile', seedCands[0].path)
      if (data.dbSeedAuto === undefined) onChange('dbSeedAuto', true)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showSeed, seedCands.length])
  const fmtBytes = n => n >= 1048576 ? `${(n / 1048576).toFixed(1)} MB` : `${Math.max(1, Math.round(n / 1024))} KB`

  if (!draft) return null
  return (
        <div className="bg-surface border border-border rounded-xl p-4 space-y-3">
          <div className="flex items-center gap-2">
            <span className="text-sm font-semibold text-content-strong">Detected: {draft.detected || 'services'}</span>
            <span className="text-xs text-content-subtle">{svcs.length} service{svcs.length !== 1 ? 's' : ''}</span>
          </div>
          <div className="space-y-1.5">
            {svcs.length > 0 && (
              <p className="text-[11px] text-content-faint uppercase tracking-wide">Web entry — which service receives external traffic:</p>
            )}
            {svcs.map((s, i) => {
              const ports = [s.port, ...(s.extra_ports || []).map(p => String(p).split(':').pop())].filter(Boolean)
              const meta = [
                envCount(s) > 0 && `${envCount(s)} env`,
                (s.volumes || []).length > 0 && `${s.volumes.length} vol`,
                s.healthcheck && 'healthcheck',
              ].filter(Boolean)
              return (
                <label key={i} className="flex items-center gap-2 text-xs cursor-pointer">
                  <input type="radio" name="webentry" checked={!!s.web_routed} onChange={() => pickWeb(i)}
                    className="w-3.5 h-3.5 accent-brand-500 shrink-0" title="Set as web entry" />
                  <span className="font-mono text-content">{s.name}</span>
                  <span className="text-content-faint">
                    {s.build ? `build${s.build.context && s.build.context !== '.' ? ` (${s.build.context})` : ''}`
                      : s.image_from ? `worker → ${s.image_from}`
                      : `image ${s.image}${s.tag ? `:${s.tag}` : ''}`}
                  </span>
                  {ports.length > 0 && <span className="text-content-subtle">:{ports.join(',')}</span>}
                  {meta.length > 0 && <span className="text-content-faint">· {meta.join(' · ')}</span>}
                  {s.web_routed && <span className="text-success-fg">web</span>}
                </label>
              )
            })}
            {svcs.length === 0 && <p className="text-xs text-content-subtle">No services detected — you can add them in Edit Project after creating.</p>}
          </div>
          {(data.database !== 'none' || data.redis || (draft.object_storage && draft.object_storage !== 'none')) && (
            <p className="text-xs text-content-muted">Managed dependencies: {[data.database !== 'none' && data.database, data.redis && 'redis', draft.object_storage && draft.object_storage !== 'none' && draft.object_storage].filter(Boolean).join(', ') || 'none'}</p>
          )}
          {candidates.length > 0 && (
            <div className="space-y-2 border-t border-border pt-3">
              <p className="text-[11px] text-content-faint uppercase tracking-wide">Detected dependencies — use a Rigger-managed service, or keep your own container:</p>
              {candidates.map((c, i) => {
                const keepOwn = isKeepOwn(c)
                return (
                  <div key={i} className="text-xs space-y-1">
                    <div className="font-mono text-content">{c.detected_name}<span className="text-content-faint"> ({c.image}{c.tag ? `:${c.tag}` : ''})</span></div>
                    <div className="flex flex-col gap-1 pl-2">
                      <label className="flex items-center gap-2 cursor-pointer">
                        <input type="radio" name={`mc-${i}`} checked={!keepOwn} onChange={() => setChoice(c, false)} className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
                        <span>Use Rigger-managed <span className="font-mono">{c.managed_name}</span> <span className="text-content-faint">(recommended — backups, credentials, versioning)</span></span>
                      </label>
                      <label className="flex items-center gap-2 cursor-pointer">
                        <input type="radio" name={`mc-${i}`} checked={keepOwn} onChange={() => setChoice(c, true)} className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
                        <span>Keep my own <span className="font-mono">{c.detected_name}</span> container from the compose file</span>
                      </label>
                    </div>
                  </div>
                )
              })}
            </div>
          )}
          {omitted.length > 0 && (
            <div className="space-y-1.5 border-t border-border pt-3">
              <p className="text-[11px] text-content-faint uppercase tracking-wide">Profile-gated services (not started by default) — include any you need:</p>
              {omitted.map((o, i) => (
                <label key={i} className="flex items-center gap-2 text-xs cursor-pointer">
                  <input type="checkbox" checked={isIncluded(o)} onChange={e => toggleInclude(o, e.target.checked)} className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
                  <span className="font-mono text-content">{o.name}</span>
                  <span className="text-content-faint">{o.service?.image ? `image ${o.service.image}` : 'build'} · profile: {(o.profiles || []).join(', ')}</span>
                </label>
              ))}
            </div>
          )}
          {showSeed && (
            <div className="space-y-2 border-t border-border pt-3">
              <p className="text-[11px] text-content-faint uppercase tracking-wide">Database seed — found a bundled SQL dump; import it into the managed {data.database}:</p>
              <div className="flex flex-col gap-1 pl-2 text-xs">
                {seedCands.map((c, i) => (
                  <label key={i} className="flex items-center gap-2 cursor-pointer">
                    <input type="radio" name="dbseed" checked={seedSel === c.path}
                      onChange={() => onChange('dbSeedFile', c.path)} className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
                    <span className="font-mono text-content">{c.path}</span>
                    <span className="text-content-faint">({fmtBytes(c.bytes)})</span>
                  </label>
                ))}
                <label className="flex items-center gap-2 cursor-pointer">
                  <input type="radio" name="dbseed" checked={!seedSel}
                    onChange={() => onChange('dbSeedFile', '')} className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
                  <span>Don't import <span className="text-content-faint">(seed manually later from Edit Project)</span></span>
                </label>
              </div>
              {seedSel && (
                <label className="flex items-center gap-2 text-xs cursor-pointer pl-2">
                  <input type="checkbox" checked={seedAuto} onChange={e => onChange('dbSeedAuto', e.target.checked)}
                    className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
                  <span>Auto-import on first deploy <span className="text-content-faint">(only when the database is empty; imported as-is)</span></span>
                </label>
              )}
            </div>
          )}
          {(draft.notes || []).length > 0 && (
            <ul className="text-xs text-content-subtle list-disc pl-4 space-y-0.5">
              {draft.notes.map((n, i) => <li key={i}>{n}</li>)}
            </ul>
          )}
          <p className="text-xs text-content-faint">Review here, then fine-tune every service in <strong>Edit Project → Services</strong> after creation.</p>
        </div>
  )
}

// CustomStack: upload application source as an archive; Rigger extracts + detects the
// stack (the same engine the Git-scan path uses), then the shared review applies. The
// uploaded archive is adopted into the project on create (via the returned token).
function CustomStack({ data, onChange, error }) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  async function upload(file) {
    if (!file) return
    setErr(''); setBusy(true)
    try {
      const fd = new FormData()
      fd.append('archive', file)
      const { draft, upload_token } = await uploadSource(fd)
      applyDraft(onChange, draft)
      onChange('sourceUploadToken', upload_token)
      onChange('sourceFileName', file.name)
    } catch (e) {
      onChange('scanDraft', null)
      onChange('sourceUploadToken', '')
      setErr(e?.response?.data?.error || 'Upload failed')
    } finally { setBusy(false) }
  }
  return (
    <div className="space-y-4">
      <DropZone onFile={upload} accept=".zip,.tar,.tar.gz,.tgz,.gz" busy={busy}
        busyLabel="Extracting & detecting…"
        hint={data.sourceFileName
          ? `↻ ${data.sourceFileName} uploaded — drop another to replace`
          : '↑ Drop your app source (.zip / .tar.gz) here, or click to browse'} />
      <p className="text-xs text-content-subtle">
        Upload your application source — Rigger extracts it, detects the stack (framework, ports, a Dockerfile if present),
        and seeds env from its <code className="font-mono text-xs">.env.example</code>. No git repo or Dockerfile required;
        Rigger scaffolds one for the detected framework when missing.
      </p>
      {(err || error) && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{err || error}</p>}
      <ScanReview data={data} onChange={onChange} />
    </div>
  )
}

function TemplateCard({ tmpl, selected, onClick }) {
  const tagColors = ['bg-info-subtle text-info-fg', 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300', 'bg-success-subtle text-success-fg']
  return (
    <button
      type="button" onClick={onClick}
      className={`text-left w-full p-4 rounded-xl border transition-all ${
        selected
          ? 'border-brand-500 bg-brand-950/30'
          : 'border-border-strong bg-surface-raised/40 hover:border-border-strong'
      }`}
    >
      <div className="flex items-start justify-between gap-2 mb-1">
        <p className="font-medium text-content-strong text-sm">{tmpl.label}</p>
        <span className="text-xs text-content-subtle shrink-0">{tmpl.image_count} container{tmpl.image_count !== 1 ? 's' : ''}</span>
      </div>
      <p className="text-xs text-content-muted mb-2">{tmpl.description}</p>
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
        <div key={i} className="bg-surface-raised/50 border border-border-strong rounded-xl p-4 space-y-3">
          <div className="flex items-center justify-between">
            <p className="text-xs font-semibold text-content-muted uppercase tracking-wider">Service {i + 1}</p>
            {images.length > 1 && (
              <button type="button" onClick={() => remove(i)} className="text-xs text-danger-fg hover:text-danger-fg">Remove</button>
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
          <p className="text-xs text-content-faint">Port mappings, volumes and healthcheck configured in the Services step.</p>
        </div>
      ))}
      <button
        type="button" onClick={add}
        className="w-full py-2 border border-dashed border-border-strong text-content-muted hover:text-content hover:border-border-strong rounded-xl text-sm transition-colors"
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
        : <p className="text-xs text-warning-fg/70">⚠ Compose keeps values plaintext in .env — flag secrets and deploy with Swarm for encryption at rest.</p>}
      {entries.map(([k, v]) => {
        const secret = secretSet.has(k)
        return (
          <div key={k} className={`flex items-center gap-2 pl-1.5 border-l-2 ${secret ? 'border-warning/70' : 'border-transparent'}`}>
            <button type="button" onClick={() => toggleSecret(k)} title={secret ? 'Secret — click to unflag' : 'Flag as secret'}
              className={`shrink-0 w-6 h-6 flex items-center justify-center rounded text-xs ${secret ? 'text-warning-fg' : 'text-content-faint hover:text-content'}`}>
              {secret ? '🔒' : '🔓'}
            </button>
            <span className="font-mono text-xs text-content w-40 shrink-0 truncate">{k}</span>
            <input
              type={secret ? 'password' : 'text'} value={v} onChange={e => update(k, e.target.value)}
              className="flex-1 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500"
            />
            <button type="button" onClick={() => remove(k)} className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
          </div>
        )
      })}
      <div className="flex gap-2 pt-1">
        <button type="button" onClick={() => setNewSecret(s => !s)} title={newSecret ? 'New var is a secret' : 'Flag new var as secret'}
          className={`shrink-0 w-7 h-7 flex items-center justify-center rounded text-xs ${newSecret ? 'text-warning-fg bg-surface-raised' : 'text-content-faint hover:text-content'}`}>
          {newSecret ? '🔒' : '🔓'}
        </button>
        <input
          type="text" placeholder="KEY" value={newKey} onChange={e => setNewKey(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && add()}
          className="w-40 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500"
        />
        <input
          type={newSecret ? 'password' : 'text'} placeholder="value" value={newVal} onChange={e => setNewVal(e.target.value)}
          onKeyDown={e => e.key === 'Enter' && add()}
          className="flex-1 px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500"
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
        <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider mb-2">Popular</p>
        <div className="grid grid-cols-2 gap-3">
          {popular.map(tmpl => (
            <TemplateCard key={tmpl.name} tmpl={tmpl} selected={selected === tmpl.name} onClick={() => handleSelect(tmpl)} />
          ))}
        </div>
      </div>

      {/* Recently used — only shown when there's history */}
      {recentlyUsed.length > 0 && (
        <div>
          <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider mb-2">Recently used</p>
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
        className="w-full py-2.5 border border-dashed border-border-strong text-content-muted hover:text-content hover:border-border-strong rounded-xl text-sm transition-colors"
      >
        Browse all templates ({templates.length}) →
      </button>

      {/* Selected template confirmation */}
      {selectedTmpl && (
        <p className="text-xs text-content-subtle flex items-center gap-1.5">
          <span className="text-brand-400">✓</span>
          <strong className="text-content">{selectedTmpl.label}</strong> selected —
          env vars and volumes pre-filled in the Services step. Review secrets before creating.
        </p>
      )}

      {/* Browse all modal */}
      {modalOpen && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/70">
          <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-2xl max-h-[80vh] flex flex-col shadow-2xl">
            {/* Modal header */}
            <div className="flex items-center justify-between px-5 py-4 border-b border-border">
              <h3 className="text-base font-semibold text-content-strong">All templates</h3>
              <button
                type="button"
                onClick={() => setModalOpen(false)}
                className="text-content-subtle hover:text-content-strong transition-colors text-xl leading-none"
              >×</button>
            </div>
            {/* Search */}
            <div className="px-5 py-3 border-b border-border">
              <input
                type="text"
                value={search}
                onChange={e => setSearch(e.target.value)}
                placeholder="Search templates…"
                autoFocus
                className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-subtle focus:outline-none focus:border-brand-500"
              />
            </div>
            {/* Template grid */}
            <div className="overflow-y-auto p-5">
              {filtered.length === 0 ? (
                <p className="text-sm text-content-subtle text-center py-8">No templates match "{search}"</p>
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

// RegistryField — container registry selector for custom (build-type) stacks:
// those tag & push built images to the registry so remote hosts can pull them
// without rebuilding. Image/prebuilt stacks pull their images directly (registry
// embedded in each image ref), so it's hidden. The picker lets the user choose a
// saved workspace registry or add a new one inline (with credentials), so the
// build can authenticate to push.
function RegistryField({ data, onChange, errors, workspace, defaultRegistryId }) {
  return (
    <div>
      <Label>Container registry</Label>
      <p className="text-xs text-content-subtle mb-2">Optional — only needed so remote hosts can pull built images. Leave as "Local" to build &amp; run images on the deploy host.</p>
      <RegistryPicker
        workspace={workspace}
        value={data.registry}
        onChange={v => onChange('registry', v)}
        defaultRegistryId={defaultRegistryId}
        error={errors.registry}
      />
    </div>
  )
}

function Step2({ data, onChange, errors, workspace, defaultRegistryId }) {
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
                : 'border-border-strong bg-surface-raised/40 hover:border-border-strong'
            }`}
          >
            <p className="font-medium text-content-strong text-sm mb-1">{t.label}</p>
            <p className="text-xs text-content-muted">{t.desc}</p>
          </button>
        ))}
      </div>

      {/* Container registry — build stacks (custom + scan) need it to tag/push images */}
      {(data.stackType === 'custom' || data.stackType === 'scan' || data.stackType === 'blueprint') && (
        <RegistryField data={data} onChange={onChange} errors={errors} workspace={workspace} defaultRegistryId={defaultRegistryId} />
      )}

      {/* Repo scan */}
      {data.stackType === 'scan' && <ScanStack data={data} onChange={onChange} />}

      {/* No-repo stack template picker */}
      {data.stackType === 'blueprint' && <BlueprintStack data={data} onChange={onChange} />}

      {/* Database hosting: a managed database on its own (no app code). The engine
          + CloudBeaver are chosen on the next step (Dependencies). */}
      {data.stackType === 'database' && (
        <p className="text-xs text-content-subtle">
          A standalone managed database with auto-generated credentials. Pick the engine on the next
          step (Dependencies). Connect from other projects (shared network), from outside (enable
          external access per-environment in Edit Project), or browse it via CloudBeaver.
        </p>
      )}

      {/* Image stack: custom image list + env vars */}
      {data.stackType === 'image' && (
        <div className="space-y-5">
          <div>
            <Label>Services</Label>
            <p className="text-xs text-content-subtle mb-2">Add each Docker image you want to deploy.</p>
            <ImageEditor images={data.images} onChange={v => onChange('images', v)} />
          </div>
          <div>
            <Label>Environment variables</Label>
            <p className="text-xs text-content-subtle mb-2">These will be written to <code className="font-mono text-xs">.env</code>. Secrets can be set now or edited after creation.</p>
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

      {/* Custom stack options. Database/Redis/Garage are chosen on the next step
          (Dependencies) — project-level, consistent across environments. */}
      {data.stackType === 'custom' && (
        <CustomStack data={data} onChange={onChange} error={errors.source_upload} />
      )}
    </div>
  )
}

// Managed services (database / Redis / Garage) are chosen on the Services step via
// the shared <ManagedServices> editor — applicable to custom / blueprint / database /
// scan stacks. See the Step4 (Services) component below.

// ── Environments (wizard step 4 — Step3 component) ────────────────────────────

const DEFAULT_ENV = { name: '', domain: '', http_port: 8080, traefik: false, traefik_network: 'traefik_net', ssl_enabled: false, deployment: 'compose', backend_replicas: 1, frontend_replicas: 1, git_enabled: false, git_repo: '', git_branch: '', vars: {}, secret_keys: [], backup_schedules: [] }
const DEPLOYMENT_OPTIONS = [{ value: 'compose', label: 'Docker Compose' }, { value: 'swarm', label: 'Docker Swarm' }]

// looksLocalOrIP reports whether a domain can't get a public Let's Encrypt cert:
// localhost / *.localhost, a bare IPv4, or an IPv6 literal. (Magic-DNS hosts like
// sslip.io / nip.io are real public names and ARE eligible, so they're allowed.)
export function looksLocalOrIP(domain) {
  const h = (domain || '').trim().toLowerCase().replace(/:\d+$/, '')
  if (!h) return false
  if (h === 'localhost' || h.endsWith('.localhost')) return true
  if (/^\d{1,3}(\.\d{1,3}){3}$/.test(h)) return true // IPv4
  if (h.includes(':')) return true                   // IPv6 literal
  return false
}

function EnvForm({ env, idx, onChange, onRemove, canRemove, stackType, hosts = [], defaultHostId = 0 }) {
  const upd = (k, v) => onChange(idx, { ...env, [k]: v })
  const [tosAccepted, setTosAccepted] = useState(!!env.ssl_enabled) // LE ToS ack gates SSL
  const sslBlocked = looksLocalOrIP(env.domain)
  const canSSL = !!env.domain && !sslBlocked
  const hostOptions = [{ value: '0', label: 'Local control plane' },
    ...hosts.map(h => ({ value: String(h.id), label: h.name }))]
  return (
    <div className="bg-surface-raised/50 border border-border-strong rounded-xl p-4 space-y-4">
      <div className="flex items-center justify-between">
        <p className="text-sm font-semibold text-content-strong">Environment {idx + 1}</p>
        {canRemove && (
          <button type="button" onClick={() => onRemove(idx)} className="text-xs text-danger-fg hover:text-danger-fg">Remove</button>
        )}
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <Label required>Name</Label>
          <Input value={env.name} onChange={v => upd('name', v)} placeholder="dev" />
        </div>
        {/* HTTP port — only relevant for custom stacks without Traefik (direct Nginx binding) */}
        {!env.traefik && stackType !== 'image' && (
          <div>
            <Label>HTTP port</Label>
            <Input type="number" value={env.http_port} onChange={v => upd('http_port', parseInt(v) || 8080)} placeholder="8080" />
            <p className="text-xs text-content-subtle mt-1">Host port Nginx binds to — access your app at <code className="font-mono text-xs">host:{env.http_port || 8080}</code></p>
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
          <p className="text-xs text-content-subtle mt-1">Where this environment runs.</p>
        </div>
      </div>

      <div className="space-y-3 pt-2 border-t border-border-strong/60">
        <Toggle
          label="Traefik reverse proxy"
          hint="Route traffic via Traefik instead of direct port binding"
          checked={env.traefik}
          onChange={v => {
            // Single update — two sequential upd() calls would each spread the
            // stale `env`, so the second clobbers the first (turning Traefik off
            // would silently revert). Clear SSL in the same change when disabling.
            onChange(idx, { ...env, traefik: v, ...(v ? {} : { ssl_enabled: false }) })
          }}
        />

        {/* Domain — only relevant under Traefik; blank yields an automatic URL. */}
        {env.traefik && (
          <div>
            <Label>Domain <span className="font-normal normal-case text-content-faint">(optional)</span></Label>
            <Input value={env.domain} onChange={v => upd('domain', v)} placeholder="leave blank for an automatic URL" />
            {!env.domain && (
              <p className="text-xs text-content-subtle mt-1">
                Blank → an automatic URL (base domain if the admin set one, else the sslip/nip auto-URL, else <code className="font-mono text-xs">*.localhost</code>). Set a value only for your own custom domain.
              </p>
            )}
          </div>
        )}

        {/* SSL — only under Traefik. Disabled for localhost/IP domains (LE can't issue
            for those) and gated on accepting the Let's Encrypt Terms of Service. */}
        {env.traefik && (
          <div className={`pl-4 border-l-2 ${env.ssl_enabled && canSSL ? 'border-success-border' : 'border-border-strong'}`}>
            <div className="flex items-start justify-between">
              <div>
                <p className="text-sm text-content">Request SSL certificate</p>
                <p className="text-xs text-content-subtle mt-0.5">
                  {!env.domain
                    ? 'Enter a domain above to enable SSL'
                    : sslBlocked
                      ? 'Not available for localhost or IP addresses — use a public domain'
                      : 'Traefik will issue a Let\'s Encrypt cert for this domain'}
                </p>
              </div>
              <button
                type="button"
                disabled={!canSSL || !tosAccepted}
                onClick={() => upd('ssl_enabled', !env.ssl_enabled)}
                className={`relative w-10 h-5 rounded-full transition-colors shrink-0 ml-4 ${
                  env.ssl_enabled && canSSL ? 'bg-green-600' : 'bg-surface-overlay'
                } disabled:opacity-40`}
              >
                <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${
                  env.ssl_enabled && canSSL ? 'translate-x-5' : ''
                }`} />
              </button>
            </div>

            {/* Let's Encrypt ToS acknowledgment — required before SSL can be enabled. */}
            {canSSL && (
              <label className="mt-2 flex items-start gap-2 text-xs text-content-muted cursor-pointer">
                <input type="checkbox" checked={tosAccepted}
                  onChange={e => { setTosAccepted(e.target.checked); if (!e.target.checked) upd('ssl_enabled', false) }}
                  className="w-3.5 h-3.5 mt-0.5 accent-brand-500" />
                <span>I agree to the Let's Encrypt <a href="https://letsencrypt.org/repository/" target="_blank" rel="noreferrer" className="text-brand-400 hover:underline">Terms of Service</a>.</span>
              </label>
            )}

            {env.ssl_enabled && canSSL && (
              <div className="mt-2 flex items-start gap-2 px-3 py-2 bg-success-subtle/40 border border-success-border/50 rounded-lg">
                <span className="text-success-fg shrink-0 mt-0.5">🔒</span>
                <div className="text-xs text-success-fg space-y-0.5">
                  <p>SSL will be active for <strong>{env.domain}</strong></p>
                  <p className="text-success-fg/70">
                    Port 80 must be publicly reachable for the Let's Encrypt HTTP-01 challenge.
                    The cert is registered to the instance's configured ACME email.
                  </p>
                </div>
              </div>
            )}
          </div>
        )}
        {/* Git sync only applies to source-code projects (built from a repo) — not
            image/prebuilt (no source) or database stacks. Matches Edit Project,
            which exposes the git repo only for source-code projects. */}
        {['custom', 'scan', 'blueprint'].includes(stackType) && (
          <>
            <Toggle
              label="Git sync"
              hint="Pull this environment's source from a git repo on deploy"
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
          </>
        )}
      </div>

      {/* Per-environment variables */}
      <div className="pt-3 border-t border-border-strong/60">
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
        className="flex items-center gap-2 text-xs font-semibold text-content-muted uppercase tracking-wider hover:text-content transition-colors w-full">
        <span className={`transition-transform ${open ? 'rotate-90' : ''}`}>▶</span>
        Environment Variables
        {count > 0 && <span className="ml-1 text-brand-400 normal-case font-normal">{count} set</span>}
        {secretCount > 0 && <span className="ml-1 text-warning-fg normal-case font-normal">· {secretCount} 🔒</span>}
        <span className="ml-auto text-content-faint normal-case font-normal">per-environment .env</span>
      </button>
      {open && <div className="mt-3"><EnvVarEditor envVars={vars} secretKeys={secretKeys} onChange={onChange} onSecretKeysChange={onSecretKeysChange} deployment={deployment} /></div>}
    </div>
  )
}

function Step3({ data, onChange, workspace }) {
  const { data: hosts = [] } = useQuery({ queryKey: ['ws-hosts', workspace], queryFn: () => fetchWorkspaceHosts(workspace), enabled: !!workspace })
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
        className="w-full py-2 border border-dashed border-border-strong text-content-muted hover:text-content hover:border-border-strong rounded-xl text-sm transition-colors"
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
      t === 'bind' ? 'bg-info-subtle text-info-fg' : 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300'
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
            className="flex-1 px-2 py-1.5 bg-surface-raised border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500"
          />
          <span className="text-content-faint text-xs shrink-0">→</span>
          <input
            type="text" value={vol.mountPath} onChange={e => update(i, 'mountPath', e.target.value)}
            placeholder="/var/lib/mysql"
            className="flex-1 px-2 py-1.5 bg-surface-raised border border-border-strong rounded text-sm text-content-strong font-mono focus:outline-none focus:border-brand-500"
          />
          <button type="button" onClick={() => remove(i)} className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
        </div>
      ))}
      <p className="text-xs text-content-faint">
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

const monoInput = 'px-2 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-brand-500'

const RESTART_OPTIONS_WIZ = [
  { value: 'unless-stopped', label: 'Unless stopped (recommended)' },
  { value: 'always',         label: 'Always' },
  { value: 'on-failure',     label: 'On failure' },
  { value: 'no',             label: 'No (never restart)' },
]

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

function VolBadge({ src }) {
  const isBind = src.startsWith('./') || src.startsWith('/')
  return (
    <span className={`text-xs px-1.5 py-0.5 rounded font-medium shrink-0 ${isBind ? 'bg-info-subtle text-info-fg' : 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300'}`}>
      {isBind ? 'bind' : 'named'}
    </span>
  )
}

// Full service card matching Edit Workspace ServiceCard appearance
function ServiceConfigCard({ img, idx, allImages, onChange }) {
  const [cmdOverride, setCmdOverride] = useState(() => !!img.command)
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
    <div className="bg-surface-raised/50 border border-border-strong rounded-xl p-4 space-y-4">
      <p className="text-xs font-semibold text-content-muted uppercase tracking-wider">
        {img.name || `Service ${idx + 1}`}
        <span className="ml-2 text-content-faint font-mono font-normal normal-case">{img.image}:{img.tag || 'latest'}</span>
      </p>

      {/* Port mappings */}
      <div>
        <Label>Port mappings</Label>
        <p className="text-xs text-content-subtle mb-2">
          HOST : CONTAINER — leave host blank to expose internally only.
          <span className="ml-2 text-content-faint">🔗 = show as link on env card</span>
        </p>
        <div className="space-y-1.5">
          {portRows.map((row, ri) => (
            <div key={ri} className="flex items-center gap-2">
              <input type="text" value={row.host} placeholder="8080"
                onChange={e => syncPorts(portRows.map((x,j) => j===ri?{...x,host:e.target.value}:x))}
                className={`flex-1 ${monoInput}`} />
              <span className="text-content-subtle font-bold shrink-0">:</span>
              <input type="text" value={row.container} placeholder="80"
                onChange={e => syncPorts(portRows.map((x,j) => j===ri?{...x,container:e.target.value}:x))}
                className={`flex-1 ${monoInput}`} />
              <label title="Show as clickable link on env card" className={`flex items-center gap-1 shrink-0 cursor-pointer select-none ${row.host.trim() ? 'text-content-muted hover:text-brand-400' : 'text-content-faint cursor-not-allowed'}`}>
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
                  className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
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
        <p className="text-xs text-content-subtle mb-2">SOURCE (named vol or path) : CONTAINER PATH</p>
        <div className="space-y-1.5">
          {volRows.map((row, ri) => (
            <div key={ri} className="flex items-center gap-2">
              <VolBadge src={row.source || ''} />
              <input type="text" value={row.source} placeholder="./volumes/app_data"
                onChange={e => syncVols(volRows.map((x,j) => j===ri?{...x,source:e.target.value}:x))}
                className={`flex-1 ${monoInput}`} />
              <span className="text-content-subtle font-bold shrink-0">:</span>
              <input type="text" value={row.path} placeholder="/var/lib/data"
                onChange={e => syncVols(volRows.map((x,j) => j===ri?{...x,path:e.target.value}:x))}
                className={`flex-1 ${monoInput}`} />
              <VolModeToggle mode={row.mode || 'rw'} onChange={m => syncVols(volRows.map((x,j) => j===ri?{...x,mode:m}:x))} />
              <button type="button" onClick={() => syncVols(volRows.filter((_,j)=>j!==ri))}
                className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
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
          className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500">
          {RESTART_OPTIONS_WIZ.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
      </div>

      {/* Command override — explicit toggle; off uses the image's default CMD */}
      <div>
        <label className="flex items-center gap-2 cursor-pointer select-none">
          <input type="checkbox" checked={cmdOverride}
            onChange={e => { setCmdOverride(e.target.checked); if (!e.target.checked && img.command) upd('command', '') }}
            className="rounded border-border-strong bg-surface-overlay text-brand-500 focus:ring-brand-500" />
          <span className="text-sm text-content">Override default command</span>
        </label>
        {cmdOverride && (
          <input type="text" value={img.command || ''} placeholder="php artisan queue:work"
            onChange={e => upd('command', e.target.value)} className={`w-full mt-2 ${monoInput}`} />
        )}
      </div>

      {/* Healthcheck */}
      <div className="space-y-2">
        <div>
          <Label>Healthcheck command</Label>
          <p className="text-xs text-content-subtle mb-1">Shell command to test container health. Leave blank to disable.</p>
          <input type="text" value={img.healthcheck || ''} placeholder="curl -sf http://localhost/health || exit 1"
            onChange={e => upd('healthcheck', e.target.value)} className={`w-full ${monoInput}`} />
        </div>
        {img.healthcheck && (
          <div className="grid grid-cols-5 gap-2">
            {[['interval','30'],['timeout','10'],['retries','3',true],['start_period','30'],['start_interval','5']].map(([k,ph,noSuffix]) => (
              <div key={k}>
                <label className="block text-xs text-content-subtle mb-1">{k.replace(/_/g,' ')}{!noSuffix && ' (s)'}</label>
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
                  className="rounded border-border-strong bg-surface-overlay text-brand-500 focus:ring-brand-500" />
                <span className="text-sm text-content font-mono">{svcName}</span>
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
          <span className="px-1.5 py-0.5 rounded text-xs bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300 shrink-0">named</span>
          <span className="text-xs font-mono text-content flex-1">{v.name}</span>
          <button type="button" onClick={() => onChange(volumes.filter((_,j)=>j!==i))}
            className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
        </div>
      ))}
      <div className="flex gap-2">
        <input type="text" value={newName} onChange={e => setNewName(e.target.value.replace(/[./\\]/g,''))}
          onKeyDown={e => e.key === 'Enter' && add()}
          placeholder="volume_name (no paths)"
          className="flex-1 px-2 py-1.5 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-brand-500" />
        <button type="button" onClick={add}
          className="text-xs text-brand-400 hover:text-brand-300 shrink-0 px-3 transition-colors">Add</button>
      </div>
    </div>
  )
}

function Step4({ data, onChange, errors = {}, workspace = '' }) {
  // updateImage uses data.images indices (not filtered activeImages indices)
  function updateImage(idx, updated) {
    onChange('images', data.images.map((img, i) => i === idx ? updated : img))
  }
  // Managed services (DB / Redis / Garage) apply to app stacks, not image/prebuilt.
  const managedApplies = ['custom', 'blueprint', 'database', 'scan'].includes(data.stackType)
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
      <StepHeader step={3} title="Services" subtitle="Managed services + per-service ports, volumes, restart policy and healthchecks." />

      {/* Managed services (project-level) — applies to app stacks; image/prebuilt
          stacks bring their own data services as images so it's hidden for them. */}
      {managedApplies && (
        <ManagedServices
          value={{ database: data.database, dbVersion: data.dbVersion, redis: data.redis, storageLocal: data.storageLocal, storageMinio: data.storageMinio, storageBucket: data.storageBucket, storagePath: data.storagePath, storageUi: data.storageUi, webSql: data.webSql, mailpit: data.mailpit }}
          onChange={v => {
            onChange('database', v.database); onChange('dbVersion', v.dbVersion)
            onChange('redis', !!v.redis); onChange('webSql', !!v.webSql); onChange('mailpit', !!v.mailpit)
            onChange('storageLocal', !!v.storageLocal); onChange('storageMinio', !!v.storageMinio); onChange('storageBucket', v.storageBucket || ''); onChange('storagePath', v.storagePath || ''); onChange('storageUi', !!v.storageUi)
          }}
          showWebSql={data.stackType === 'database'}
          requireDatabase={data.stackType === 'database'}
          error={errors.database}
          resourcePrefix={data.key ? `${workspace}_${data.key}` : ''}
        />
      )}

      {/* Per-service config — image and prebuilt stacks */}
      {showServices && serviceImages.length > 0 && (
        <div className="space-y-4">
          {data.stackType === 'prebuilt' && (
            <p className="text-xs text-warning-fg/80 flex items-center gap-1.5">
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

      {/* Custom stacks: no per-service config here */}
      {data.stackType === 'custom' && (
        <div className="px-4 py-3 bg-surface-raised/40 border border-border-strong/50 rounded-xl">
          <p className="text-sm text-content font-medium mb-1">Custom application stack</p>
          <p className="text-xs text-content-subtle">
            App services are seeded from your backend/frontend choice. After creation, use
            <strong> Edit Project → Services</strong> to add workers and adjust each service.
          </p>
        </div>
      )}

      {/* Scan stacks: services were detected + reviewed in the Stack step */}
      {data.stackType === 'scan' && (
        <div className="px-4 py-3 bg-surface-raised/40 border border-border-strong/50 rounded-xl">
          <p className="text-sm text-content font-medium mb-1">{(data.scanDraft?.services || []).length} service{(data.scanDraft?.services || []).length !== 1 ? 's' : ''} detected from your repository</p>
          <p className="text-xs text-content-subtle">
            Reviewed in the <strong>Stack</strong> step. After creation, fine-tune each service
            (ports, healthchecks, workers) in <strong>Edit Project → Services</strong>.
          </p>
        </div>
      )}

      {/* Blueprint stacks: services were seeded from the template in the Stack step */}
      {data.stackType === 'blueprint' && (
        <div className="px-4 py-3 bg-surface-raised/40 border border-border-strong/50 rounded-xl">
          <p className="text-sm text-content font-medium mb-1">{(data.blueprintServices || []).length} service{(data.blueprintServices || []).length !== 1 ? 's' : ''} seeded from the <span className="font-mono">{data.blueprintId}</span> template</p>
          <p className="text-xs text-content-subtle">
            Rigger scaffolds a starter Dockerfile per build service — replace it with your code,
            then fine-tune each service in <strong>Edit Project → Services</strong>.
          </p>
        </div>
      )}

      {/* Extra named volumes — only named volumes, not bind mounts */}
      <details className="group">
        <summary className="text-xs text-content-subtle cursor-pointer hover:text-content transition-colors select-none list-none flex items-center gap-1">
          <span className="group-open:rotate-90 transition-transform inline-block">▶</span>
          Extra named volumes
          <span className="ml-2 text-content-faint font-normal">shared Docker volumes across services</span>
        </summary>
        <div className="mt-3">
          <p className="text-xs text-content-subtle mb-3">
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

// wizardServices derives the data-bearing services from the in-progress wizard
// state (the env isn't created yet, so we can't ask the backend).
function wizardServices(data) {
  if (data.type === 'image') {
    return (data.images || []).map(img => {
      const low = String(img.image || '').toLowerCase()
      let kind = 'service', hint = 'Container volumes (if any)'
      if (low.includes('postgres')) { kind = 'database'; hint = 'PostgreSQL — SQL dump' }
      else if (low.includes('mariadb')) { kind = 'database'; hint = 'MariaDB — SQL dump' }
      else if (low.includes('mysql')) { kind = 'database'; hint = 'MySQL — SQL dump' }
      return { id: img.name, label: img.name, kind, hint }
    })
  }
  const out = []
  if (data.database && data.database !== 'none') {
    out.push({ id: 'database', label: 'Database', kind: 'database', hint: data.database + ' — SQL dump' })
  }
  out.push({ id: 'uploads', label: 'App uploads', kind: 'volume', hint: 'Uploads volume' })
  if (data.storageMinio) out.push({ id: 'minio', label: 'MinIO S3 data', kind: 'volume', hint: 'MinIO object-store volume' })
  if (data.storageLocal) out.push({ id: 'storage', label: 'Local storage', kind: 'volume', hint: 'App storage volume' })
  return out
}

function Step5({ data, onChange, workspace, defaultTargetId }) {
  const { data: targets = [] } = useQuery({ queryKey: ['ws-backup-targets', workspace], queryFn: () => fetchWorkspaceBackupTargets(workspace), enabled: !!workspace })
  const services = wizardServices(data)
  const namedEnvs = data.environments.filter(e => e.name)
  const defaultTargetName = targets.find(t => t.id === defaultTargetId)?.name

  const setSchedules = (env, list) =>
    onChange('environments', data.environments.map(e => (e === env ? { ...e, backup_schedules: list } : e)))

  return (
    <div className="space-y-5">
      <StepHeader step={5} title="Backups (optional)" subtitle="Set automatic backups per environment — or skip and configure later." />

      <div className="px-4 py-3 bg-surface-raised/50 border border-border-strong/60 rounded-lg text-sm text-content-muted leading-relaxed">
        Add schedules to an environment to choose <strong className="text-content">which services' data</strong> to back up,
        <strong className="text-content"> how often</strong>, <strong className="text-content">where</strong> to store it, and
        <strong className="text-content"> how many copies</strong> to keep — e.g. back up prod's database hourly and everything daily,
        while stage runs once a day. Leave any environment empty to skip; you can always add schedules later from Edit Project.
      </div>

      {namedEnvs.length === 0 && (
        <p className="text-sm text-content-subtle">Name your environments first (Step 4) to configure their backups.</p>
      )}

      {defaultTargetName && (
        <p className="text-xs text-content-faint">New schedules default to this workspace's backup target (<span className="text-content-muted">{defaultTargetName}</span>); change it per schedule.</p>
      )}

      {namedEnvs.map(env => (
        <div key={env.name} className="bg-surface border border-border rounded-xl p-4">
          <h3 className="text-sm font-semibold text-content-strong mb-2">{env.name}</h3>
          <BackupScheduleEditor
            schedules={env.backup_schedules || []}
            onChange={list => setSchedules(env, list)}
            services={services}
            targets={targets}
            defaultTargetId={defaultTargetId}
          />
        </div>
      ))}
    </div>
  )
}


// ── Step 6: Review ────────────────────────────────────────────────────────────

function ReviewRow({ label, value }) {
  return (
    <div className="flex items-start justify-between py-2 border-b border-border last:border-0">
      <span className="text-sm text-content-muted">{label}</span>
      <span className="text-sm text-content-strong font-medium text-right max-w-[60%]">{value || '—'}</span>
    </div>
  )
}

function Step6({ data }) {
  const stackDesc = data.stackType === 'prebuilt'
    ? `Pre-built: ${data.template || '(none selected)'}`
    : data.stackType === 'image'
    ? `Image stack: ${data.images.filter(i => i.name).map(i => `${i.name} (${i.image}:${i.tag || 'latest'})`).join(', ') || '(no services)'}`
    : data.stackType === 'scan'
    ? `Scanned repo (${data.scanDraft?.detected || 'detected'}): ${(data.scanDraft?.services || []).map(s => s.name).join(', ') || '(no services)'}`
    : data.stackType === 'blueprint'
    ? `Template (${data.blueprintId || 'none'}): ${(data.blueprintServices || []).map(s => s.name).join(', ') || '(no services)'}`
    : `Uploaded source (${data.scanDraft?.detected || 'detected'}): ${(data.scanDraft?.services || []).map(s => s.name).join(', ') || '(no services)'}`

  const reviewImages = data.images.filter(i => i.name && i.image)
  const buildLike = data.stackType === 'custom' || data.stackType === 'scan' || data.stackType === 'blueprint'
  const dupWarnings = buildLike ? [] :
    portConflicts(reviewImages.map(img => ({ name: img.name, ports: hostPortsFromMappings(img) })))
  const hostChecks = []
  if (!buildLike) {
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

      <div className="bg-surface border border-border rounded-xl p-4 space-y-0">
        <ReviewRow label="Project name" value={data.name} />
        <ReviewRow label="Registry" value={data.registry} />
        <ReviewRow label="Stack" value={stackDesc} />
        <ReviewRow label="Environments" value={data.environments.map(e => e.name || '(unnamed)').join(', ')} />
        {data.environments.map((e, i) => e.domain && (
          <ReviewRow key={i} label={`  ${e.name} domain`} value={e.domain} />
        ))}
        {data.redis  && <ReviewRow label="Redis" value="Enabled" />}
        {data.mailpit && <ReviewRow label="Mailpit (test SMTP)" value="Enabled (per-env overridable)" />}
        {(data.storageMinio || data.storageLocal) && <ReviewRow label="Object storage" value={[data.storageLocal && 'Local volume', data.storageMinio && `MinIO (S3)${data.storageUi ? ' + console' : ''}`].filter(Boolean).join(' + ')} />}
        {data.environments.filter(e => Object.keys(e.vars || {}).length > 0).map((e, i) => (
          <ReviewRow key={i} label={`  ${e.name} vars`} value={`${Object.keys(e.vars).length} variable(s)`} />
        ))}
        {data.volumes.filter(v => v.name).length > 0 && (
          <ReviewRow label="Named volumes" value={data.volumes.filter(v => v.name).map(v => v.name).join(', ')} />
        )}
        {(() => {
          const total = data.environments.reduce((n, e) => n + (e.backup_schedules?.length || 0), 0)
          return <ReviewRow label="Backups" value={total === 0 ? 'No schedules (configure later)' : `${total} schedule${total !== 1 ? 's' : ''} across environments`} />
        })()}
      </div>

      <PortWarnings warnings={[...dupWarnings, ...hostWarnings]} />

      <div className="bg-warning-subtle/40 border border-warning-border/50 rounded-xl px-4 py-3">
        <p className="text-sm text-warning-fg">
          After creation, edit <code className="font-mono text-xs bg-warning-subtle/40 px-1 py-0.5 rounded">envs/&#123;env&#125;/.env</code> to fill in secrets before starting the stack.
        </p>
      </div>
    </div>
  )
}

// ── Step 7: Creating (live terminal) ─────────────────────────────────────────

function Step7({ payload, onDone, onResult, onGoBack }) {
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

    let sawError = false
    const ws = openCreateSocket(payload)
    ws.addEventListener('message', e => {
      term.write(e.data)
      const text = e.data
      // Failure markers first — every fatal path the server emits is prefixed with
      // ✗ / [ERROR] / "Error:". Check before success so a denial isn't misread.
      if (text.includes('[ERROR]') || text.includes('✗') || /error:/i.test(text) || text.includes('failed')) {
        sawError = true
        resolve('failure')
      } else if (text.includes('is ready!')) {
        resolve('success')
      }
    })
    ws.addEventListener('error', () => {
      term.write('\r\n\x1b[31m[connection error]\x1b[0m\r\n')
      sawError = true
      resolve('failure')
    })
    ws.addEventListener('close', () => {
      // Closed without an explicit marker: failure if we saw any error line,
      // otherwise treat a clean finish as success.
      if (!resolved) resolve(sawError ? 'failure' : 'success')
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
        isSuccess ? 'Project created successfully.' :
        'Project creation failed — review the output above.'
      } />

      <div ref={containerRef} className="rounded-xl overflow-hidden" style={{ height: 320 }} />

      {isDone && (
        <div className={`flex items-center gap-3 px-4 py-3 rounded-xl border ${
          isSuccess
            ? 'bg-success-subtle/40 border-success-border/40 text-success-fg'
            : 'bg-danger-subtle/40 border-danger-border/40 text-danger-fg'
        }`}>
          <span className="text-lg">{isSuccess ? '✓' : '✗'}</span>
          <span className="text-sm font-medium">
            {isSuccess ? 'Project created successfully.' : 'Creation failed. Review output above for details.'}
          </span>
        </div>
      )}

      {isDone && (
        <div className="flex gap-3">
          {isFailure && (
            <button
              onClick={onGoBack}
              className="flex-1 py-2.5 border border-border-strong hover:border-border-strong text-content hover:text-content-strong font-medium rounded-lg transition-colors"
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
                : 'bg-surface-raised text-content-faint cursor-not-allowed border border-border-strong'
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
        // The final Result step is never clickable — can't skip back to it
        const clickable = n <= maxVisited && n !== current && n < STEPS.length
        return (
          <div key={label} className="flex items-center flex-1 last:flex-none">
            <div className="flex flex-col items-center gap-1">
              <button
                type="button"
                onClick={() => clickable && onStepClick(n)}
                disabled={!clickable}
                className={`w-7 h-7 rounded-full flex items-center justify-center text-xs font-bold transition-colors ${
                  state === 'done'   ? 'bg-brand-600 text-white' :
                  state === 'active' ? 'bg-brand-600 text-white ring-2 ring-brand-400 ring-offset-2 ring-offset-canvas' :
                  'bg-surface-raised text-content-subtle border border-border-strong'
                } ${clickable ? 'cursor-pointer hover:ring-2 hover:ring-brand-400 hover:ring-offset-1 hover:ring-offset-canvas' : 'cursor-default'}`}
                title={clickable ? `Go to step ${n}: ${label}` : undefined}
              >
                {state === 'done' ? '✓' : n}
              </button>
              <span className={`text-xs ${state === 'active' ? 'text-content-strong' : clickable ? 'text-content-muted' : 'text-content-subtle'}`}>{label}</span>
            </div>
            {i < STEPS.length - 1 && (
              <div className={`flex-1 h-px mx-2 mb-4 ${n < current ? 'bg-brand-600' : 'bg-surface-overlay'}`} />
            )}
          </div>
        )
      })}
    </div>
  )
}

// ── Main wizard page ──────────────────────────────────────────────────────────

const DEFAULT_DATA = {
  name: '', key: '', registry: '',
  stackType: 'prebuilt', template: '', images: [{ ...DEFAULT_IMAGE }], customEnvVars: {},
  backend: 'laravel', frontend: 'none', database: 'none', dbVersion: '', webSql: false, mailpit: false, redis: false, storageLocal: false, storageMinio: false, storageBucket: '', storagePath: '', storageUi: false,
  default_host_id: 0, // Phase 7: default host for environments (0 = local)
  environments: [{ ...DEFAULT_ENV, name: 'dev' }],
  volumes: [],
  templateVolumes: [], // read-only display list populated from selected prebuilt template
}

export default function NewProjectPage() {
  const navigate = useNavigate()
  const params = useParams()
  const storeWs = useWorkspaceStore(s => s.current)
  // The parent-tier workspace: from the route (/workspaces/:workspace/projects/new),
  // falling back to the selected workspace for the bare /new shortcut.
  const workspace = params.workspace || storeWs
  const qc = useQueryClient()
  // Resolve the workspace's display name (the route/store carry the KEY); fall
  // back to the key until the list loads or if it's not found.
  const { data: wsList = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const workspaceName = wsList.find(w => w.key === workspace)?.name || workspace
  const [step, setStep]           = useState(1)
  const [data, setData]           = useState(DEFAULT_DATA)
  const [errors, setErrors]       = useState({})
  const [nameConflict, setNameConflict] = useState(null)
  const [maxVisited, setMaxVisited]     = useState(1) // highest step reached — enables stepper navigation
  const [createResult, setCreateResult] = useState(null) // null | 'success' | 'failure'

  // Phase 4: defaults this workspace set for new projects (registry/host/backup).
  const { data: wsDefaults } = useQuery({
    queryKey: ['ws-settings', workspace], queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace,
  })
  const [seededDefaults, setSeededDefaults] = useState(false)
  useEffect(() => {
    if (!wsDefaults || seededDefaults) return
    setSeededDefaults(true)
    const h = parseInt(wsDefaults.default_host_id, 10)
    if (wsDefaults.default_host_id && !Number.isNaN(h)) {
      setData(prev => ({ ...prev, default_host_id: h }))
    }
  }, [wsDefaults, seededDefaults])
  const defaultRegistryId = wsDefaults?.default_registry_id || ''
  const defaultTargetId = wsDefaults?.default_backup_target_id ? parseInt(wsDefaults.default_backup_target_id, 10) : null
  const defaultHostId = wsDefaults?.default_host_id ? (parseInt(wsDefaults.default_host_id, 10) || 0) : 0

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
    else if (!/^[A-Za-z0-9][A-Za-z0-9 _-]{0,31}$/.test(data.name.trim())) e.name = '1–32 chars: letters, digits, space, dash, underscore'
    if (step === 1 && nameConflict) e.key = 'Choose a valid, available key' // key validity from Step1
    // Registry is OPTIONAL for build stacks: "Local — no registry" builds images on
    // the deploy host and is valid for single-host deploys. A registry is only needed
    // so remote hosts can pull, so don't force one here.
    if (step === 2 && data.stackType === 'scan') {
      if (!(data.source_repo || '').trim()) e.source_repo = 'Enter a repository URL'
      else if (!data.scanDraft) e.source_repo = 'Click Scan to detect the stack first'
    }
    if (step === 2 && data.stackType === 'custom') {
      if (!data.sourceUploadToken || !data.scanDraft) e.source_upload = 'Upload your application source to continue'
    }
    if (step === 2 && data.stackType === 'blueprint') {
      if (!data.blueprintId) e.blueprint = 'Pick a stack template'
    }
    if (step === 2 && data.stackType === 'prebuilt' && !data.template) e.template = 'Select a template'
    if (step === 2 && data.stackType === 'image' && data.images.every(img => !img.name || !img.image)) e.images = 'Add at least one service with a name and image'
    // Step 3 = Services (incl. the Managed Services picker). The database-hosting
    // stack requires an engine; app volumes need mount paths.
    if (step === 3 && data.stackType === 'database' && (!data.database || data.database === 'none')) e.database = 'Select a database engine'
    if (step === 3) {
      const badVols = data.volumes.filter(v => v.name && !v.mountPath)
      if (badVols.length > 0) e.volumes = 'Each volume needs a mount path'
    }
    // Step 4 = Environments.
    if (step === 4 && data.environments.some(e => !e.name.trim())) e.envs = 'All environments need a name'
    setErrors(e)
    return Object.keys(e).length === 0
  }

  function next() {
    if (!validate()) return
    setStep(s => { const n = s + 1; setMaxVisited(m => Math.max(m, n)); return n })
  }

  function buildPayload() {
    const isScan = data.stackType === 'scan'
    const isUpload = data.stackType === 'custom' // Custom application = uploaded source (detect-driven)
    const isBlueprint = data.stackType === 'blueprint'
    const isDatabase = data.stackType === 'database'
    const isImage = !isScan && !isBlueprint && !isDatabase && (data.stackType === 'prebuilt' || data.stackType === 'image')
    const detected = isScan || isUpload // both pre-fill from a detector draft
    return {
      workspace: workspace, // parent-tier workspace KEY
      name: data.name.trim(), // free-form display name
      key: data.key,          // project key (resource_prefix = {workspace}_{key})
      // Registry tags/pushes built images (custom + scan + blueprint build stacks).
      // Image and prebuilt stacks pull directly, so send empty.
      registry: (data.stackType === 'custom' || isScan || isBlueprint) ? data.registry.trim() : '',
      type: isDatabase ? 'database' : isImage ? 'image' : 'custom',
      template: data.stackType === 'prebuilt' ? data.template : '',
      // Repo-scan sends the detected graph; blueprint sends the seeded graph.
      // Blueprint has no repo — Dockerfiles scaffold from the template on bootstrap.
      services: detected ? (data.scanDraft?.services || []) : isBlueprint ? (data.blueprintServices || []) : [],
      source_repo: isScan ? (data.source_repo || '').trim() : '',
      source_branch: isScan ? (data.source_branch || '').trim() : '',
      source_kind: isUpload ? 'upload' : '',
      source_token: isUpload ? (data.sourceUploadToken || '') : '',
      // Bundled SQL dump chosen in the scan review (only with a managed DB).
      db_seed_file: (isUpload && data.database && data.database !== 'none') ? (data.dbSeedFile || '') : '',
      db_seed_auto: data.dbSeedAuto ?? true,
      images: (() => {
        if (data.stackType !== 'image') return []
        const imgs = data.images.filter(img => img.name && img.image)
        return imgs.map(img => {
            const ports = (img.portMappings || []).filter(p => p.container)
            return {
              name: img.name, image: img.image, tag: img.tag || 'latest',
              command: img.command || '',
              // A single-image stack is the web entry by default, so enabling
              // Traefik routes to it (composegen defaults its port to 80 when
              // unset). Multi-image stacks set the web entry in Edit Project.
              web_routed: !!img.web_routed || imgs.length === 1,
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
      })(),
      custom_env_vars: data.stackType === 'image' ? data.customEnvVars : {},
      // Repo scan seeds the env's .env from the repo's .env.example so ${VAR} refs in
      // the imported compose `environment:` resolve (and secrets get generated).
      initial_env_vars: detected ? (data.scanDraft?.env_vars || {}) : {},
      named_volumes: data.volumes.filter(v => v.name && v.mountPath),
      backend: (isImage || isScan || isUpload || isDatabase) ? '' : data.backend,
      frontend: (isImage || isScan || isUpload || isDatabase) ? 'none' : data.frontend,
      database: isImage ? 'none' : data.database,
      db_version: (isImage || data.database === 'none') ? '' : data.dbVersion,
      web_sql: (!isImage && data.database && data.database !== 'none') ? !!data.webSql : false,
      mailpit: isImage ? false : !!data.mailpit,
      redis: (isImage || isDatabase) ? false : data.redis,
      storage_local: (isImage || isDatabase) ? false : !!data.storageLocal,
      storage_minio: (isImage || isDatabase) ? false : !!data.storageMinio,
      storage_bucket: (isImage || isDatabase) ? '' : (data.storageMinio ? (data.storageBucket || '') : ''),
      storage_path: (isImage || isDatabase) ? '' : (data.storageLocal ? (data.storagePath || '') : ''),
      storage_ui: (isImage || isDatabase) ? false : (!!data.storageMinio && !!data.storageUi),
      environments: data.environments.filter(e => e.name).map(e => ({
        ...e,
        ssl_enabled: e.traefik && !!e.domain && !looksLocalOrIP(e.domain) && !!e.ssl_enabled,
        vars: e.vars || {},
        host_id: e.host_id ?? (data.default_host_id || 0),
      })),
      versions: {},
    }
  }

  function handleDone() {
    qc.invalidateQueries({ queryKey: ['projects', workspace] })
    navigate(`/workspaces/${workspace}/projects/${data.key}`)
  }

  return (
    <div className="min-h-screen bg-canvas flex flex-col">
      {/* Nav bar (same style as Layout) */}
      <nav className="border-b border-border bg-surface shrink-0">
        <div className="px-6 h-12 flex items-center justify-between relative">
          <div className="flex items-center gap-2.5">
            <img src="/rigger-icon.png" alt="Rigger" className="w-8 h-8 rounded-lg" />
            {workspace && (
              <span
                className="text-sm text-content px-2.5 py-1 rounded-lg bg-surface-raised border border-border-strong"
                title={`Workspace "${workspaceName}" (${workspace}) — fixed for this wizard`}
              >
                {workspaceName}
              </span>
            )}
          </div>
          <span className="absolute left-1/2 -translate-x-1/2 text-sm font-semibold text-content-strong">New Project</span>
          {step < 7
            ? <button onClick={() => navigate(-1)} className="text-sm font-medium px-4 py-1.5 rounded-lg border border-warning-border/60 bg-warning-subtle/30 hover:bg-warning/20 text-warning-fg transition-colors">Cancel</button>
            : <button onClick={() => navigate(-1)} className="text-sm font-medium px-4 py-1.5 rounded-lg border border-border-strong bg-surface-raised hover:bg-surface-overlay text-content transition-colors">Close</button>
          }
        </div>
      </nav>

      {/* Wizard body — matches the Edit Project content column width (~max-w-7xl page
          minus the vertical-tab rail) so the two screens feel consistent. */}
      <div className="flex-1 flex items-start justify-center p-8">
        <div className="w-full max-w-5xl">
          <Stepper current={step} maxVisited={maxVisited} onStepClick={n => setStep(n)} />

          <div className="bg-surface border border-border rounded-2xl p-8">
            {step === 1 && <Step1 data={data} onChange={update} errors={errors} onConflict={setNameConflict} workspace={workspace} defaultHostId={defaultHostId} />}
            {step === 2 && <Step2 data={data} onChange={update} errors={errors} workspace={workspace} defaultRegistryId={defaultRegistryId} />}
            {/* Services (3, incl. Managed Services) then Environments (4) — define the
                stack shape before its environments. Step4=Services, Step3=Environments. */}
            {step === 3 && <Step4 data={data} onChange={update} errors={errors} workspace={workspace} />}
            {step === 4 && <Step3 data={data} onChange={update} workspace={workspace} />}
            {step === 5 && <Step5 data={data} onChange={update} workspace={workspace} defaultTargetId={defaultTargetId} />}
            {step === 6 && <Step6 data={data} />}
            {step === 7 && (
              <Step7
                payload={buildPayload()}
                onDone={handleDone}
                onResult={result => setCreateResult(result)}
                onGoBack={() => { setCreateResult(null); setStep(6) }}
              />
            )}

            {/* Navigation buttons (hidden on the final Result step) */}
            {step < 7 && (
              <div className="flex items-center justify-between mt-8 pt-6 border-t border-border">
                <button
                  type="button"
                  onClick={() => step > 1 ? setStep(s => s - 1) : navigate(-1)}
                  className={`text-sm font-medium px-4 py-2 rounded-lg border transition-colors ${
                    step === 1
                      ? 'border-warning-border/60 bg-warning-subtle/30 hover:bg-warning/20 text-warning-fg'
                      : 'border-border-strong bg-surface-raised hover:bg-surface-overlay text-content'
                  }`}
                >
                  {step === 1 ? 'Cancel' : '← Back'}
                </button>
                <button
                  type="button"
                  onClick={step === 6 ? () => { if (validate()) { setMaxVisited(7); setStep(7) } } : next}
                  disabled={step === 1 && !!nameConflict}
                  className={`text-content-strong text-sm font-semibold px-6 py-2 rounded-lg transition-colors ${
                    step === 1 && nameConflict
                      ? 'bg-brand-800 text-brand-400 cursor-not-allowed'
                      : 'bg-brand-600 hover:bg-brand-700'
                  }`}
                >
                  {step === 6 ? 'Create project' : 'Continue →'}
                </button>
              </div>
            )}
          </div>
        </div>
      </div>
    </div>
  )
}
