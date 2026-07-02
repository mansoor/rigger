import { useState, useEffect, useRef } from 'react'
import { useNavigate, useParams } from 'react-router-dom'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'
import { fetchTemplates, fetchTemplate, recordTemplateUse, openCreateSocket, fetchWorkspaceBackupTargets, fetchWorkspaceHosts, fetchWorkspaceSettings, fetchGeneralSettings, scanRepo, parseCompose, uploadSource, fetchBlueprints, fetchWorkspaces, createPipeline } from '../lib/api'
import TemplateBrowserModal, { TemplateCard } from '../components/TemplateBrowserModal'
import { resolveEnvRoute } from '../lib/envRoute'
import RegistryPicker from '../components/RegistryPicker'
import GitProviderPicker from '../components/GitProviderPicker'
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
import { Hint } from '../components/ui'

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
        {hint && <Hint>{hint}</Hint>}
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
          <Hint>A descriptive label — 1–32 chars, letters/digits/space/dash/underscore.</Hint>
        )}
      </div>

      <div>
        <KeyField
          type="project" name={data.name} workspace={workspace} label="Project key"
          onChange={(k, valid) => { onChange('key', k); onConflict(valid ? null : 'key') }}
        />
        {resourcePrefix && (
          <Hint>
            Docker resource prefix: <code className="font-mono text-content-muted">{resourcePrefix}</code> (immutable)
          </Hint>
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
          {hosts.filter(h => !h.build_only).map(h => <option key={h.id} value={String(h.id)}>{h.name} — {h.address}</option>)}
        </select>
        {defaultHostId > 0 && data.default_host_id === defaultHostId && (
          <Hint tone="faint">Inherited from this workspace's default.</Hint>
        )}
        <Hint>
          Where environments run by default — override per environment on the next steps. Files are pushed
          and the stack starts on the host the first time you deploy that environment.
        </Hint>
      </div>
    </div>
  )
}

// ── Step 2: Stack ─────────────────────────────────────────────────────────────

// Display order (Phase 0 of the database-hosting roadmap): curated → image → custom →
// repo → blueprint. "Database Hosting" is appended later (roadmap Phase 4).
const STACK_TYPES = [
  { id: 'prebuilt',  label: 'Pre-built template',  desc: 'Pick from curated stacks — NPM, WordPress, Vaultwarden, Uptime Kuma…' },
  { id: 'image',     label: 'Image stack / Docker Compose', desc: 'Deploy any Docker images — specify your own image names, tags, and ports, or import a docker-compose file.' },
  { id: 'database',  label: 'Managed service hosting', desc: 'Run managed services on their own — databases (PostgreSQL, MySQL, MariaDB, MongoDB), Redis, object storage, search, metrics — no app code.' },
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
function ScanStack({ data, onChange, workspace }) {
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  async function scan() {
    setErr(''); setBusy(true)
    try {
      const d = await scanRepo((data.source_repo || '').trim(), (data.source_branch || '').trim(), data.git_provider_id || 0)
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
      <Hint>Public HTTPS URL, or pick a Git provider below for a private repo (token or SSH deploy key).</Hint>
      <div>
        <Label>Git provider <span className="font-normal normal-case text-content-faint">(private repos)</span></Label>
        <GitProviderPicker workspace={workspace}
          value={data.git_provider_id || 0}
          onChange={(id) => onChange('git_provider_id', id)} />
      </div>
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
  // Set a build service's pre-deploy (release/migrate) command, persisted on the draft
  // service so it round-trips verbatim into config.json (composegen synthesizes the gate).
  function setPreDeploy(idx, val) {
    if (!draft) return
    onChange('scanDraft', { ...draft, services: svcs.map((s, i) => i === idx ? { ...s, pre_deploy: val } : s) })
  }

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
              <Hint tone="faint" className="text-[11px] uppercase tracking-wide">Web entry — which service receives external traffic:</Hint>
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
          {/* Pre-deploy (release / migrate) command — build services only. Hidden when
              the imported compose already runs its own one-shot migrate gate. */}
          {svcs.some(s => s.build) && (
            draft.has_predeploy
              ? <div className="rounded-lg border border-border bg-surface-raised/40 p-3 text-xs text-content-subtle">
                  ℹ This compose already runs <span className="font-mono">{draft.predeploy_service}</span> as a one-shot migrate/release step before the app starts, so Rigger won&apos;t add its own pre-deploy command.
                </div>
              : <div className="space-y-2 border-t border-border pt-3">
                  <Hint tone="faint" className="text-[11px] uppercase tracking-wide">Pre-deploy command (optional) — runs once, in the service&apos;s image, before the app starts:</Hint>
                  {svcs.map((s, i) => s.build ? (
                    <div key={i}>
                      <label className="block text-xs text-content-subtle mb-1 font-mono">{s.name}</label>
                      <input type="text" value={s.pre_deploy || ''} placeholder="php artisan migrate --force"
                        onChange={e => setPreDeploy(i, e.target.value)} className={`w-full ${monoInput}`} />
                    </div>
                  ) : null)}
                  <Hint tone="faint">A non-zero exit aborts the deploy (the previous version keeps serving). Make it idempotent — it runs on every deploy. Compose only; Swarm environments skip it.</Hint>
                </div>
          )}
          {(data.database !== 'none' || data.redis || (draft.object_storage && draft.object_storage !== 'none')) && (
            <p className="text-xs text-content-muted">Managed dependencies: {[data.database !== 'none' && data.database, data.redis && 'redis', draft.object_storage && draft.object_storage !== 'none' && draft.object_storage].filter(Boolean).join(', ') || 'none'}</p>
          )}
          {candidates.length > 0 && (
            <div className="space-y-2 border-t border-border pt-3">
              <Hint tone="faint" className="text-[11px] uppercase tracking-wide">Detected dependencies — use a Rigger-managed service, or keep your own container:</Hint>
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
              <Hint tone="faint" className="text-[11px] uppercase tracking-wide">Profile-gated services (not started by default) — include any you need:</Hint>
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
              <Hint tone="faint" className="text-[11px] uppercase tracking-wide">Database seed — found a bundled SQL dump; import it into the managed {data.database}:</Hint>
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
          <Hint tone="faint">Review here, then fine-tune every service in <strong>Edit Project → Services</strong> after creation.</Hint>
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
      <Hint>
        Upload your application source — Rigger extracts it, detects the stack (framework, ports, a Dockerfile if present),
        and seeds env from its <code className="font-mono text-xs">.env.example</code>. No git repo or Dockerfile required;
        Rigger scaffolds one for the detected framework when missing.
      </Hint>
      {(err || error) && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{err || error}</p>}
      <ScanReview data={data} onChange={onChange} />
    </div>
  )
}

// TemplateCard is shared via components/TemplateBrowserModal.

const DEFAULT_IMAGE = {
  name: '', image: '', tag: 'latest', command: '', env_vars: {},
  portMappings: [{ host: '', container: '' }],
  volumes: [],
  healthcheck: '',
  healthcheck_config: { interval: '30', timeout: '10', retries: '3', start_period: '30' },
}

// splitColonOutsideBraces splits on ':' but ignores colons inside ${...} — compose
// volume/port specs use env-var defaults like ${RIGGER_BIND_ROOT:-.}/data:/x or
// ${APP_PORT:-80}:80 whose inner colon must NOT be treated as a host:container break.
// Mirrors the backend detector's splitter.
export function splitColonOutsideBraces(s) {
  const parts = []
  let depth = 0, start = 0
  for (let i = 0; i < s.length; i++) {
    const ch = s[i]
    if (ch === '{') depth++
    else if (ch === '}') { if (depth > 0) depth-- }
    else if (ch === ':' && depth === 0) { parts.push(s.slice(start, i)); start = i + 1 }
  }
  parts.push(s.slice(start))
  return parts
}

// parseVolumeSpec splits a compose volume string into {source, path, mode}, ignoring
// colons inside ${...}. Shared shape with EditProjectPage's parseVolumeString.
export function parseVolumeSpec(v) {
  const parts = splitColonOutsideBraces(v)
  const last = parts[parts.length - 1]
  if ((last === 'ro' || last === 'rw') && parts.length >= 3) {
    return { source: parts[0], path: parts.slice(1, -1).join(':'), mode: last }
  }
  if (parts.length === 1) return { source: parts[0], path: '', mode: 'rw' }
  return { source: parts[0], path: parts.slice(1).join(':'), mode: 'rw' }
}

// imagesToCompose renders the current image-stack entries as a docker-compose.yml
// string (the editor's starting point), so edits made in the UI are reflected when the
// user re-opens the compose editor. environment + ports use list form to dodge YAML
// quoting. Inverse of draftToImages (which parses compose back into entries).
function imagesToCompose(images) {
  const out = ['services:']
  const list = (images || []).filter(im => (im.name || '').trim() || im.image)
  if (list.length === 0) return 'services:\n  # add a service, or paste a compose file here\n'
  for (const im of list) {
    const name = (im.name || '').trim() || 'service'
    out.push(`  ${name}:`)
    if (im.image) out.push(`    image: ${im.tag && im.tag !== '' ? `${im.image}:${im.tag}` : im.image}`)
    if (im.command) out.push(`    command: ${im.command}`)
    const ports = (im.portMappings || []).filter(p => p.container)
    if (ports.length) {
      out.push('    ports:')
      ports.forEach(p => out.push(`      - "${p.host ? `${p.host}:` : ''}${p.container}"`))
    }
    const env = im.env_vars || {}
    const keys = Object.keys(env)
    if (keys.length) {
      out.push('    environment:')
      keys.forEach(k => out.push(`      - ${k}=${env[k]}`))
    }
    const vols = (im.volumes || []).filter(v => typeof v === 'string' && v.includes(':'))
    if (vols.length) {
      out.push('    volumes:')
      vols.forEach(v => out.push(`      - ${v}`))
    }
  }
  return out.join('\n') + '\n'
}

// draftToImages maps a parsed compose draft's services into image-stack entries. Build
// services can't be represented as a pre-built image, so they're skipped (reported).
function draftToImages(services) {
  const imgs = []
  const skipped = []
  for (const s of (services || [])) {
    if (s.build) { skipped.push(s.name); continue }
    const ports = []
    if (s.port) ports.push({ host: s.host_port || '', container: String(s.port) })
    for (const ep of (s.extra_ports || [])) {
      const parts = splitColonOutsideBraces(String(ep))
      ports.push(parts.length > 1 ? { host: parts[0], container: parts[parts.length - 1] } : { host: '', container: parts[0] })
    }
    imgs.push({
      ...DEFAULT_IMAGE,
      name: s.name || '', image: s.image || '', tag: s.tag || 'latest',
      command: s.command || '', web_routed: !!s.web_routed,
      portMappings: ports.length ? ports : [{ host: '', container: '' }],
      volumes: Array.isArray(s.volumes) ? s.volumes : [],
      env_vars: s.env_vars || {},
      healthcheck: s.healthcheck || '',
      healthcheck_config: (s.healthcheck_config && Object.keys(s.healthcheck_config).length) ? s.healthcheck_config : DEFAULT_IMAGE.healthcheck_config,
    })
  }
  return { imgs, skipped }
}

// ComposeImportModal — a two-way compose editor for the image stack. Opens pre-filled
// with YAML from the current entries; "Parse" sends it to the backend (same parser the
// repo scanner uses) and previews what will be imported; "Apply" replaces the entries.
function ComposeImportModal({ images, onApply, onClose }) {
  const [text, setText] = useState(() => imagesToCompose(images))
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')
  const [preview, setPreview] = useState(null) // { mapped:{imgs,skipped}, draft }

  async function parse() {
    setBusy(true); setErr(''); setPreview(null)
    try {
      const draft = await parseCompose(text)
      const mapped = draftToImages(draft.services || [])
      if (mapped.imgs.length === 0) {
        setErr('No pre-built image services found. The image stack runs ready-made images — build services aren\'t supported here (use "From a Git repository" instead).')
      } else {
        setPreview({ mapped, draft })
      }
    } catch (e) {
      setErr(e?.response?.data?.error || 'Could not parse the compose file.')
    } finally {
      setBusy(false)
    }
  }
  function apply() {
    if (!preview) return
    onApply(preview.mapped.imgs)
    onClose()
  }

  const draft = preview?.draft
  const mdeps = draft ? [draft.database !== 'none' && draft.database, draft.redis && 'redis', draft.object_storage && draft.object_storage !== 'none' && draft.object_storage].filter(Boolean) : []

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface-raised border border-border rounded-xl w-full max-w-2xl max-h-[85vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="px-5 py-4 border-b border-border">
          <h3 className="text-base font-semibold text-content-strong">Paste / edit docker-compose</h3>
          <Hint className="mt-0.5">Paste a compose file to fill the services, or edit the YAML below. Parsing replaces the service list.</Hint>
        </div>
        <div className="px-5 py-4 overflow-y-auto space-y-3">
          <textarea value={text} onChange={e => { setText(e.target.value); setPreview(null) }} spellCheck={false}
            className="w-full h-64 px-3 py-2 bg-surface border border-border-strong rounded-lg text-content-strong font-mono text-xs focus:outline-none focus:border-brand-500 resize-y" />
          {err && <p className="text-xs text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{err}</p>}
          {preview && (
            <div className="text-xs text-content-subtle space-y-1 bg-surface border border-border rounded-lg px-3 py-2">
              <p className="text-success-fg">Ready to import {preview.mapped.imgs.length} service{preview.mapped.imgs.length !== 1 ? 's' : ''}: <span className="font-mono text-content">{preview.mapped.imgs.map(i => i.name).join(', ')}</span></p>
              {preview.mapped.skipped.length > 0 && <p>Skipped build service{preview.mapped.skipped.length !== 1 ? 's' : ''} (not pre-built images): <span className="font-mono">{preview.mapped.skipped.join(', ')}</span></p>}
              {mdeps.length > 0 && <p>Detected managed deps: <span className="font-mono">{mdeps.join(', ')}</span> — set these in the Services step.</p>}
              {(draft.notes || []).map((n, i) => <p key={i}>• {n}</p>)}
            </div>
          )}
        </div>
        <div className="px-5 py-3 border-t border-border flex items-center justify-end gap-2">
          <button type="button" onClick={onClose} className="px-3 py-1.5 rounded-lg text-sm text-content-subtle hover:text-content">Cancel</button>
          {preview
            ? <button type="button" onClick={apply} className="px-4 py-1.5 rounded-lg text-sm font-semibold bg-brand-600 hover:bg-brand-700 text-white">Apply {preview.mapped.imgs.length} service{preview.mapped.imgs.length !== 1 ? 's' : ''}</button>
            : <button type="button" onClick={parse} disabled={busy || !text.trim()} className="px-4 py-1.5 rounded-lg text-sm font-semibold bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white">{busy ? 'Parsing…' : 'Parse'}</button>}
        </div>
      </div>
    </div>
  )
}

function ImageEditor({ images, onChange }) {
  const [composeOpen, setComposeOpen] = useState(false)
  function update(idx, field, val) {
    const next = images.map((img, i) => i === idx ? { ...img, [field]: val } : img)
    onChange(next)
  }
  function add() { onChange([...images, { ...DEFAULT_IMAGE }]) }
  function remove(idx) { onChange(images.filter((_, i) => i !== idx)) }

  return (
    <div className="space-y-3">
      <div className="flex justify-end">
        <button type="button" onClick={() => setComposeOpen(true)}
          className="text-xs px-2.5 py-1 rounded-lg border border-border-strong text-content-subtle hover:text-content hover:border-brand-500 transition-colors">
          ⇕ Paste / edit compose
        </button>
      </div>
      {composeOpen && <ComposeImportModal images={images} onApply={onChange} onClose={() => setComposeOpen(false)} />}
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
          <Hint tone="faint">Port mappings, volumes and healthcheck configured in the Services step.</Hint>
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
        onClick={() => setModalOpen(true)}
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

      {/* Browse all modal — shared with the Template Manager. */}
      {modalOpen && (
        <TemplateBrowserModal
          templates={templates}
          selected={selected}
          onSelect={handleSelect}
          onClose={() => setModalOpen(false)}
          footer={
            <Hint>
              Can't find what you're looking for? Build your own in{' '}
              <strong className="text-content">Tools → Template Manager</strong> — convert a
              <span className="font-mono text-content-muted"> docker-compose.yml</span> to a template, import a
              template file, or generate one from an existing image stack.
            </Hint>
          }
        />
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
      <Hint className="mb-2">Optional — only needed so remote hosts can pull built images. Leave as "Local" to build &amp; run images on the deploy host.</Hint>
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
      {data.stackType === 'scan' && <ScanStack data={data} onChange={onChange} workspace={workspace} />}

      {/* No-repo stack template picker */}
      {data.stackType === 'blueprint' && <BlueprintStack data={data} onChange={onChange} />}

      {/* Managed service hosting: managed services on their own (no app code). The
          engines + tooling are chosen on the next step (Managed services). */}
      {data.stackType === 'database' && (
        <Hint>
          Standalone managed services with auto-generated credentials — a database, Redis cache,
          object storage, search or metrics engine. Pick what to run on the next step (Managed
          services). Connect from other projects (shared network), from outside (enable external
          access per-environment in Edit Project), or browse a SQL database via Adminer.
        </Hint>
      )}

      {/* Image stack: custom image list + env vars */}
      {data.stackType === 'image' && (
        <div className="space-y-5">
          <div>
            <Label>Services</Label>
            <Hint className="mb-2">Add each Docker image you want to deploy.</Hint>
            <ImageEditor images={data.images} onChange={v => onChange('images', v)} />
          </div>
          <div>
            <Label>Environment variables</Label>
            <Hint className="mb-2">These will be written to <code className="font-mono text-xs">.env</code>. Secrets can be set now or edited after creation.</Hint>
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
                onChange('images', detail.images.map(img => {
                  // Faithful copy of the template service so wizard edits (e.g. removing
                  // a host port) are what gets created. Build port rows from the main
                  // port + extra_ports, flagging any host port the template links.
                  const linkSet = new Set((img.link_ports || []).map(String))
                  const rows = []
                  if (img.port) rows.push({ host: String(img.host_port || ''), container: String(img.port), link: linkSet.has(String(img.host_port || '')) })
                  for (const ep of (img.extra_ports || [])) {
                    const parts = splitColonOutsideBraces(String(ep))
                    const host = parts.length > 1 ? parts[0] : ''
                    rows.push({ host, container: parts[parts.length - 1], link: linkSet.has(host) })
                  }
                  return {
                    ...DEFAULT_IMAGE,
                    name:  img.name  || '',
                    image: img.image || '',
                    tag:   img.tag   || 'latest',
                    command: img.command || '',
                    web_routed: !!img.web_routed,
                    subdomain: img.subdomain || '',
                    restart: img.restart || '',
                    depends_on: img.depends_on || [],
                    portMappings: rows.length ? rows : DEFAULT_IMAGE.portMappings,
                    volumes: img.volumes || [],
                    env_vars: img.env_vars || {},
                    healthcheck: img.healthcheck || '',
                    healthcheck_config: img.healthcheck_config || DEFAULT_IMAGE.healthcheck_config,
                  }
                }))
                // Build templateVolumes display list for Step 4
                const seen = new Set()
                const templateVols = []
                for (const img of detail.images) {
                  for (const v of (img.volumes || [])) {
                    const { source, path: mountPath } = parseVolumeSpec(v)
                    if (!mountPath) continue
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

// leCapable reports whether a host can get a real Let's Encrypt cert (a public DNS
// name). localhost/IP can't; magic-DNS hosts (sslip/nip/traefik.me) are HTTP-only in
// Phase 1 → treated as not-LE so "local HTTPS" (self-signed) is offered instead.
export function leCapable(domain) {
  const h = (domain || '').trim().toLowerCase()
  if (!h || looksLocalOrIP(h)) return false
  if (/\.(sslip\.io|nip\.io|traefik\.me)$/.test(h)) return false
  return true
}

function EnvForm({ env, idx, onChange, onRemove, canRemove, stackType, hosts = [], defaultHostId = 0, acmeDefault = '', resourcePrefix = '', baseDomain = '', autoUrlMode = '', appHost = '' }) {
  const upd = (k, v) => onChange(idx, { ...env, [k]: v })
  const [tosAccepted, setTosAccepted] = useState(!!env.ssl_enabled) // LE ToS ack gates SSL
  const sslBlocked = looksLocalOrIP(env.domain)
  const canSSL = !!env.domain && !sslBlocked
  const hostOptions = [{ value: '0', label: 'Local control plane' },
    ...hosts.filter(h => !h.build_only).map(h => ({ value: String(h.id), label: h.name }))]
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
            <Hint>Host port Nginx binds to — access your app at <code className="font-mono text-xs">host:{env.http_port || 8080}</code></Hint>
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
          <Hint>Where this environment runs.</Hint>
        </div>
      </div>

      <div className="space-y-3 pt-2 border-t border-border-strong/60">
        <Toggle
          label="Expose via domain (Traefik)"
          hint="Route through the shared Traefik proxy by hostname instead of binding a host port (avoids port conflicts; gives the env a URL)."
          checked={env.traefik}
          onChange={v => {
            // Single update — two sequential upd() calls would each spread the
            // stale `env`, so the second clobbers the first (turning Traefik off
            // would silently revert). Clear SSL in the same change when disabling.
            onChange(idx, { ...env, traefik: v, ...(v ? {} : { ssl_enabled: false }) })
          }}
        />

        {/* Route preview — the URL this env will be reachable at (matches deploy). For a
            URL that can't get a Let's Encrypt cert (localhost / IP / magic-DNS), an
            inline "Enable local HTTPS" upgrades it to Traefik's self-signed cert. */}
        {env.traefik && env.name && (() => {
          const route = resolveEnvRoute({ ...env, traefik_enabled: true }, resourcePrefix, env.name, baseDomain, false, autoUrlMode, appHost)
          if (!route) return null
          const showLocalHTTPS = !leCapable(route.domain)
          return (
            <div className="flex items-center justify-between gap-3">
              <p className="text-xs text-content-subtle min-w-0 truncate">
                Reachable at <a href={route.url} target="_blank" rel="noreferrer" className="font-mono text-brand-600 hover:underline">{route.url}</a>
                {route.auto && <span className="text-content-faint"> (auto{route.ssl ? ' · TLS' : ''})</span>}
              </p>
              {showLocalHTTPS && (
                <label className="flex items-center gap-2 text-xs text-content-muted cursor-pointer shrink-0" title="Serve this URL over HTTPS with Traefik's self-signed cert (browsers warn; useful for apps that require HTTPS). No public cert is possible for localhost / IP / magic-DNS.">
                  <input type="checkbox" checked={!!env.ssl_self_signed}
                    onChange={e => onChange(idx, { ...env, ssl_self_signed: e.target.checked, ssl_enabled: e.target.checked })}
                    className="w-3.5 h-3.5 accent-brand-500" />
                  Enable local HTTPS
                </label>
              )}
            </div>
          )
        })()}

        {/* Domain + "Request SSL" on one row. SSL is enabled only once a real public
            domain is entered (localhost/IP can't get a Let's Encrypt cert). */}
        {env.traefik && (
          <div>
            <Label>Domain <span className="font-normal normal-case text-content-faint">(optional)</span></Label>
            <div className="flex items-center gap-3">
              <div className="flex-1"><Input value={env.domain} onChange={v => upd('domain', v)} placeholder="leave blank for an automatic URL" /></div>
              <div className="flex items-center gap-2 shrink-0">
                <span className={`text-xs ${canSSL ? 'text-content' : 'text-content-faint'}`}>Request SSL</span>
                <button
                  type="button"
                  disabled={!canSSL}
                  title={!env.domain ? 'Enter a domain to enable SSL' : sslBlocked ? 'Not available for localhost or IP addresses' : 'Request a Let\'s Encrypt certificate'}
                  onClick={() => upd('ssl_enabled', !env.ssl_enabled)}
                  className={`relative w-10 h-5 rounded-full transition-colors ${env.ssl_enabled && canSSL ? 'bg-green-600' : 'bg-surface-overlay'} disabled:opacity-40`}
                >
                  <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${env.ssl_enabled && canSSL ? 'translate-x-5' : ''}`} />
                </button>
              </div>
            </div>
            {!env.domain && (
              <Hint>
                Blank → an automatic URL (base domain if the admin set one, else the sslip/nip auto-URL, else <code className="font-mono text-xs">*.localhost</code>). Set a value only for your own custom domain.
              </Hint>
            )}
            {sslBlocked && (
              <Hint tone="faint">SSL isn&apos;t available for localhost or IP addresses — use a public domain.</Hint>
            )}
          </div>
        )}

        {/* When SSL is on: the LE email (inherited, overridable) + the ToS acknowledgment
            on one row. Unchecking the ToS turns SSL back off. */}
        {env.traefik && env.ssl_enabled && canSSL && (
          <div className="pl-4 border-l-2 border-success-border space-y-1.5">
            <div className="flex items-end gap-3">
              <div className="flex-1">
                <label className="block text-xs text-content-subtle mb-1">Let&apos;s Encrypt email</label>
                <Input type="email" value={env.acme_email || ''} onChange={v => upd('acme_email', v)}
                  placeholder={acmeDefault ? `inherits ${acmeDefault}` : 'inherit workspace / instance email'} />
              </div>
              <label className="flex items-center gap-2 text-xs text-content-muted cursor-pointer shrink-0 pb-2 max-w-[48%]">
                <input type="checkbox" checked={tosAccepted}
                  onChange={e => { setTosAccepted(e.target.checked); if (!e.target.checked) upd('ssl_enabled', false) }}
                  className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
                <span>I agree to the Let&apos;s Encrypt <a href="https://letsencrypt.org/repository/" target="_blank" rel="noreferrer" className="text-brand-400 hover:underline">Terms of Service</a></span>
              </label>
            </div>
            <Hint tone="faint" className="text-[11px]">Account/recovery contact for the cert — blank inherits the {acmeDefault ? 'workspace' : 'instance'} default. Port 80 must be reachable for the HTTP-01 challenge.</Hint>
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

      {/* Optional default CI for build-type stacks: one build→deploy pipeline per
          checked env, created right after the project (refine later in Pipelines). */}
      {['custom', 'scan', 'blueprint'].includes(stackType) && (
        <label className="pt-3 border-t border-border-strong/60 flex items-start gap-2 cursor-pointer select-none">
          <input type="checkbox" className="mt-0.5" checked={!!env.auto_pipeline}
            onChange={e => upd('auto_pipeline', e.target.checked)} />
          <span className="text-xs text-content">
            <span className="font-medium text-content-strong">Auto-create a build &amp; deploy pipeline</span>
            <span className="block text-content-subtle">
              Adds a “{env.name || 'env'} — build &amp; deploy” pipeline (version bump → build → deploy this env) you can run or refine later in the Pipelines tab.
            </span>
          </span>
        </label>
      )}
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
  // Workspace ACME email override — the default the per-env SSL email inherits (blank
  // ⇒ the instance-global ACME email, shown generically since the wizard doesn't fetch it).
  const { data: wsSettings } = useQuery({ queryKey: ['ws-settings', workspace], queryFn: () => fetchWorkspaceSettings(workspace), enabled: !!workspace })
  const acmeDefault = (wsSettings?.acme_email || '').trim()
  // Global routing context so the env's "Reachable at …" preview matches what deploy
  // emits (base domain → auto-URL/sslip/nip → *.localhost). Best-effort (admin-gated).
  const { data: gs = {} } = useQuery({ queryKey: ['general-settings'], queryFn: fetchGeneralSettings, retry: false })
  const baseDomain = (wsSettings?.domain || gs.apps_base_domain || '').trim()
  const autoUrlMode = gs.auto_url_mode || ''
  const appHost = (gs.app_host || gs.auto_url_host || '').trim()
  const resourcePrefix = workspace && data.key ? `${workspace}_${data.key}` : ''
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
          acmeDefault={acmeDefault}
          resourcePrefix={resourcePrefix}
          baseDomain={baseDomain}
          autoUrlMode={autoUrlMode}
          appHost={appHost}
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
      <Hint tone="faint">
        Paths starting with <code className="font-mono">./</code> or <code className="font-mono">/</code> = bind mount (scoped to workspace). Plain names = Docker named volume.
      </Hint>
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
      return parseVolumeSpec(v)
    })
    return rows.length ? rows : []
  })
  const [envRows, setEnvRows] = useState(() => Object.entries(img.env_vars || {}).map(([k, v]) => ({ key: k, val: String(v) })))

  function syncPorts(rows) {
    setPortRows(rows)
    onChange(idx, { ...img, portMappings: rows })
  }
  function syncEnv(rows) {
    setEnvRows(rows)
    const obj = {}
    for (const r of rows) { const k = r.key.trim(); if (k) obj[k] = r.val }
    onChange(idx, { ...img, env_vars: obj })
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
        <Hint className="mb-2">
          HOST : CONTAINER — leave host blank to expose internally only.
          <span className="ml-2 text-content-faint">🔗 = show as link on env card</span>
        </Hint>
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
        <Hint className="mb-2">SOURCE (named vol or path) : CONTAINER PATH</Hint>
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

      {/* Environment variables (per service) */}
      <div>
        <Label>Environment variables</Label>
        <Hint className="mb-2">KEY : VALUE — passed to this service. <code className="font-mono">${'{VAR}'}</code> values resolve from the env&apos;s .env at deploy.</Hint>
        <div className="space-y-1.5">
          {envRows.map((row, ri) => (
            <div key={ri} className="flex items-center gap-2">
              <input type="text" value={row.key} placeholder="KEY"
                onChange={e => syncEnv(envRows.map((x, j) => j === ri ? { ...x, key: e.target.value } : x))}
                className={`flex-1 ${monoInput}`} />
              <span className="text-content-subtle font-bold shrink-0">=</span>
              <input type="text" value={row.val} placeholder="value"
                onChange={e => syncEnv(envRows.map((x, j) => j === ri ? { ...x, val: e.target.value } : x))}
                className={`flex-1 ${monoInput}`} />
              <button type="button" onClick={() => syncEnv(envRows.filter((_, j) => j !== ri))}
                className="text-content-subtle hover:text-danger-fg transition-colors shrink-0 p-0.5 rounded hover:bg-danger-subtle/30"><TrashIcon /></button>
            </div>
          ))}
          <button type="button" onClick={() => setEnvRows(r => [...r, { key: '', val: '' }])}
            className="text-xs text-brand-400 hover:text-brand-300 transition-colors flex items-center gap-1 mt-1">
            <span className="text-base leading-none">＋</span> Add variable
          </button>
        </div>
      </div>

      {/* Healthcheck */}
      <div className="space-y-2">
        <div>
          <Label>Healthcheck command</Label>
          <Hint className="mb-1">Shell command to test container health. Leave blank to disable.</Hint>
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

// Images that back infrastructure (DB / cache / queue / search) — never the public web
// entry. Mirrors composegen.isDatastoreImage so the wizard's default web-entry pick
// matches what the backend fallback would choose for an app+db stack.
const DATASTORE_IMAGES = new Set([
  'postgres', 'postgresql', 'mysql', 'mariadb', 'percona', 'mongo', 'mongodb', 'redis',
  'valkey', 'keydb', 'memcached', 'clickhouse', 'rabbitmq', 'nats', 'kafka', 'zookeeper',
  'elasticsearch', 'opensearch', 'etcd', 'cassandra', 'influxdb', 'victoriametrics',
  'meilisearch', 'typesense', 'qdrant',
])
// Engine forks/variants whose image name embeds the engine (clickhouse/clickhouse-server,
// tensorchord/pgvecto-rs, postgis/postgis). High-confidence substrings — no common web/UI
// image embeds these. Mirrors composegen.datastoreSubstrings.
const DATASTORE_SUBSTRINGS = ['postgres', 'postgis', 'pgvecto', 'pgvector', 'timescale', 'clickhouse', 'mariadb', 'mysql']
function isDatastoreImage(image) {
  if (!image) return false
  const base = String(image).split('/').pop().split(':')[0].split('@')[0].toLowerCase()
  return DATASTORE_IMAGES.has(base) || DATASTORE_SUBSTRINGS.some(p => base.includes(p))
}
// Index of the implicit web entry among image services: the first non-datastore service
// (so app+db stacks route to the app, not the database). -1 when all look like datastores.
function defaultWebEntryIdx(images) {
  return images.findIndex(im => !isDatastoreImage(im.image))
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

  // Default web entry: when a multi-service image/prebuilt stack marks none (the bundled
  // app+db templates like Gitea/Ghost/WordPress don't), pre-select the first non-datastore
  // service so it routes by domain under Traefik instead of 404'ing. Mirrors the backend
  // fallback; the radio below lets the user override. Single-service stacks are handled at
  // payload build (a lone image is implicitly the entry).
  useEffect(() => {
    if (!showServices) return
    const entries = data.images.map((im, i) => ({ im, i })).filter(x => x.im.name && x.im.image)
    if (entries.length < 2 || entries.some(x => x.im.web_routed)) return
    const pick = defaultWebEntryIdx(entries.map(x => x.im))
    if (pick < 0) return
    const target = entries[pick].i
    onChange('images', data.images.map((im, i) => ({ ...im, web_routed: i === target })))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [showServices, data.images.length])

  // Set the web entry from the radio: web_routed=true on the chosen image, false on the rest.
  function pickWebEntry(idx) {
    onChange('images', data.images.map((im, i) => ({ ...im, web_routed: i === idx })))
  }

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
          {/* Web entry — which service Traefik routes the env's domain to. Only meaningful
              for multi-service stacks (a lone image is always the entry). */}
          {serviceImages.length >= 2 && (
            <div className="bg-surface border border-border rounded-xl p-4 space-y-2">
              <Hint tone="faint" className="text-[11px] uppercase tracking-wide">Web entry — which service receives external traffic (the env&apos;s domain / Traefik route):</Hint>
              {data.images.map((img, i) => (img.name && img.image) ? (
                <label key={i} className="flex items-center gap-2 text-xs cursor-pointer">
                  <input type="radio" name="img-webentry" checked={!!img.web_routed} onChange={() => pickWebEntry(i)}
                    className="w-3.5 h-3.5 accent-brand-500 shrink-0" title="Set as web entry" />
                  <span className="font-mono text-content">{img.name}</span>
                  <span className="text-content-faint">{img.image}{img.tag ? `:${img.tag}` : ''}</span>
                  {isDatastoreImage(img.image) && <span className="text-content-faint">· datastore</span>}
                  {img.web_routed && <span className="text-success-fg">web</span>}
                </label>
              ) : null)}
            </div>
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
          <Hint>
            App services are seeded from your backend/frontend choice. After creation, use
            <strong> Edit Project → Services</strong> to add workers and adjust each service.
          </Hint>
        </div>
      )}

      {/* Scan stacks: services were detected + reviewed in the Stack step */}
      {data.stackType === 'scan' && (
        <div className="px-4 py-3 bg-surface-raised/40 border border-border-strong/50 rounded-xl">
          <p className="text-sm text-content font-medium mb-1">{(data.scanDraft?.services || []).length} service{(data.scanDraft?.services || []).length !== 1 ? 's' : ''} detected from your repository</p>
          <Hint>
            Reviewed in the <strong>Stack</strong> step. After creation, fine-tune each service
            (ports, healthchecks, workers) in <strong>Edit Project → Services</strong>.
          </Hint>
        </div>
      )}

      {/* Blueprint stacks: services were seeded from the template in the Stack step */}
      {data.stackType === 'blueprint' && (
        <div className="px-4 py-3 bg-surface-raised/40 border border-border-strong/50 rounded-xl">
          <p className="text-sm text-content font-medium mb-1">{(data.blueprintServices || []).length} service{(data.blueprintServices || []).length !== 1 ? 's' : ''} seeded from the <span className="font-mono">{data.blueprintId}</span> template</p>
          <Hint>
            Rigger scaffolds a starter Dockerfile per build service — replace it with your code,
            then fine-tune each service in <strong>Edit Project → Services</strong>.
          </Hint>
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
          <Hint className="mb-3">
            Only for named Docker volumes that need to be shared between multiple services
            and aren't already declared in a service's volume list above.
            Bind mounts are defined per-service and don't need declaring here.
          </Hint>
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
        <Hint tone="faint">New schedules default to this workspace's backup target (<span className="text-content-muted">{defaultTargetName}</span>); change it per schedule.</Hint>
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

function Step7({ payload, onDone, onEditProject, onDeployProject, onBackToWorkspace, onResult, onGoBack }) {
  // Pull-only stacks (image / prebuilt / database) can deploy straight away;
  // build stacks (custom / from-repo / blueprint) need a build first, so they
  // don't get the "Deploy project" shortcut.
  const deployable = payload.type === 'image' || payload.type === 'database'
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
      if (result === 'success') seedAutoPipelines(term)
    }

    // For build-type projects, create one "build & deploy" pipeline per environment
    // the user opted into (Environments step checkbox). Best-effort — runs after the
    // project exists; a failure is logged to the terminal but never blocks the result.
    async function seedAutoPipelines(term) {
      if (payload.type !== 'custom') return // build stacks only (custom/scan/blueprint)
      const envs = (payload.environments || []).filter(e => e.auto_pipeline && e.name)
      if (!envs.length) return
      term.write('\r\n\x1b[36mCreating build & deploy pipelines…\x1b[0m\r\n')
      for (const e of envs) {
        try {
          await createPipeline(payload.workspace, payload.key, {
            name: `${e.name} — build & deploy`,
            enabled: true,
            stages: [
              { type: 'build', env: e.name, part: 'build', on_failure: 'stop' },
              { type: 'deploy', env: e.name, on_failure: 'stop' },
            ],
          })
          term.write(`  \x1b[32m✓\x1b[0m ${e.name} — build & deploy\r\n`)
        } catch (err) {
          term.write(`  \x1b[31m✗\x1b[0m ${e.name}: ${err?.response?.data?.error || err.message}\r\n`)
        }
      }
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

      {isFailure && (
        <button
          onClick={onGoBack}
          className="w-full py-2.5 border border-border-strong hover:border-border-strong text-content hover:text-content-strong font-medium rounded-lg transition-colors"
        >
          ← Go back &amp; fix
        </button>
      )}

      {isSuccess && (
        <div className="space-y-2.5">
          {/* Primary action — open the new project. */}
          <button
            onClick={onDone}
            className="w-full py-2.5 bg-brand-600 hover:bg-brand-700 text-white font-medium rounded-lg transition-colors"
          >
            Open project →
          </button>
          {/* Secondary actions. "Deploy project" only for pull-only stacks. */}
          <div className="flex flex-wrap gap-2.5">
            {deployable && (
              <button
                onClick={onDeployProject}
                className="flex-1 min-w-[8rem] py-2.5 border border-border-strong hover:bg-surface-overlay text-content hover:text-content-strong font-medium rounded-lg transition-colors"
              >
                Deploy project
              </button>
            )}
            <button
              onClick={onEditProject}
              className="flex-1 min-w-[8rem] py-2.5 border border-border-strong hover:bg-surface-overlay text-content hover:text-content-strong font-medium rounded-lg transition-colors"
            >
              Edit project
            </button>
            <button
              onClick={onBackToWorkspace}
              className="flex-1 min-w-[8rem] py-2.5 border border-border-strong hover:bg-surface-overlay text-content hover:text-content-strong font-medium rounded-lg transition-colors"
            >
              Back to workspace
            </button>
          </div>
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
  const setCurrentWs = useWorkspaceStore(s => s.setCurrent)
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
    // Registry is OPTIONAL for build stacks: an empty registry inherits the system
    // registry, or falls back to local-only (build + run on one daemon) when none is
    // configured — valid for single-host deploys. A registry is only required so a
    // Swarm/remote host can pull (enforced at deploy time), so don't force one here.
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
      git_provider_id: isScan ? (Number(data.git_provider_id) || 0) : 0,
      source_kind: isUpload ? 'upload' : '',
      source_token: isUpload ? (data.sourceUploadToken || '') : '',
      // Bundled SQL dump chosen in the scan review (only with a managed DB).
      db_seed_file: (isUpload && data.database && data.database !== 'none') ? (data.dbSeedFile || '') : '',
      db_seed_auto: data.dbSeedAuto ?? true,
      images: (() => {
        // Both custom image stacks AND prebuilt templates are authored/edited as
        // data.images here, so send them — otherwise the backend falls back to the
        // template file and silently discards wizard edits (e.g. a removed host port).
        if (!isImage) return []
        const isPrebuilt = data.stackType === 'prebuilt'
        const imgs = data.images.filter(img => img.name && img.image)
        return imgs.map(img => {
            const ports = (img.portMappings || []).filter(p => p.container)
            return {
              name: img.name, image: img.image, tag: img.tag || 'latest',
              command: img.command || '',
              // Prebuilt templates carry their own web entry verbatim. For a hand-built
              // single-image stack, default it to the web entry so enabling Traefik
              // routes to it (composegen defaults its port to 80 when unset).
              web_routed: isPrebuilt ? !!img.web_routed : (!!img.web_routed || imgs.length === 1),
              subdomain: img.subdomain || '',
              restart: img.restart || '',
              port: parseInt((ports[0] || {}).container) || 0,
              host_port: (ports[0] || {}).host || '',
              extra_ports: ports.slice(1).filter(p => p.host && p.container).map(p => `${p.host}:${p.container}`),
              link_ports: ports.filter(p => p.link && p.host).map(p => p.host),
              volumes: (img.volumes || []).filter(v => typeof v === 'string' ? v.includes(':') : false),
              depends_on: img.depends_on || [],
              env_vars: img.env_vars || {},
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
        // SSL: Let's Encrypt needs a real public domain; self-signed (local HTTPS) is
        // allowed on any Traefik env (localhost/IP/magic-DNS included).
        ssl_self_signed: e.traefik && !!e.ssl_self_signed,
        ssl_enabled: e.traefik && !!e.ssl_enabled && (!!e.ssl_self_signed || (!!e.domain && !looksLocalOrIP(e.domain))),
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

  // Edit the freshly-created project.
  function handleEditProject() {
    qc.invalidateQueries({ queryKey: ['projects', workspace] })
    navigate(`/workspaces/${workspace}/projects/${data.key}/edit`)
  }

  // Open the project page and immediately kick off a deploy of its first env.
  // Only offered for pull-only stacks (image/prebuilt/database) — build stacks
  // need a build first, so deploying right away wouldn't work.
  function handleDeployProject() {
    qc.invalidateQueries({ queryKey: ['projects', workspace] })
    navigate(`/workspaces/${workspace}/projects/${data.key}`, { state: { autoDeploy: true } })
  }

  // Back to the active workspace's dashboard. This page has its own nav (no Layout),
  // so sync the workspace store before navigating so the dashboard scopes correctly.
  function handleBackToWorkspace() {
    qc.invalidateQueries({ queryKey: ['projects', workspace] })
    if (workspace) setCurrentWs(workspace)
    navigate('/')
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
                onEditProject={handleEditProject}
                onDeployProject={handleDeployProject}
                onBackToWorkspace={handleBackToWorkspace}
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
