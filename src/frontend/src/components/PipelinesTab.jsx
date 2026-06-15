import { useState, useRef, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  fetchPipelines, createPipeline, updatePipeline, deletePipeline,
  fetchPipelineRuns, fetchPipelineRun, startPipelineRun,
  approvePipelineRun, rejectPipelineRun, cancelPipelineRun,
  fetchPipelineWebhooks, createPipelineWebhook, deletePipelineWebhook,
  suggestPipeline,
} from '../lib/api'

// Phase 9 — Deployment Pipelines tab (inside Edit Project). A pipeline is an
// ordered list of stages; each stage maps to a deploy/build/restart/backup action
// or a sandboxed `test` (command run inside a service container). Runs stream live.

const STAGE_TYPES = [
  { value: 'deploy',  label: 'Deploy — up (current/built images)' },
  { value: 'refresh', label: 'Refresh — regenerate compose + redeploy (apply config changes)' },
  { value: 'update',  label: 'Update — pull latest + recreate' },
  { value: 'build',   label: 'Build images (custom apps)' },
  { value: 'restart', label: 'Restart' },
  { value: 'backup',  label: 'Backup' },
  { value: 'test',    label: 'Test — exec in container' },
  { value: 'script',  label: 'Script — run a tool container' },
  { value: 'version', label: 'Version — bump semver' },
  { value: 'push',    label: 'Promote — copy env → env (registry)' },
  { value: 'gate',    label: 'Gate — manual approval' },
]
export const STAGE_ICON = { deploy: '🚀', refresh: '♻️', update: '⬆️', build: '🧱', restart: '🔄', backup: '💾', test: '🧪', script: '🛠️', version: '🔖', push: '📤', gate: '⏸️' }

// fmtClock formats an epoch-ms instant as a short local wall-clock time.
const fmtClock = (ms) => (ms ? new Date(ms).toLocaleTimeString() : '')
// fmtDur turns a millisecond duration into a compact human string.
const fmtDur = (ms) => {
  if (!ms || ms < 0) return ''
  if (ms < 1000) return `${ms} ms`
  const s = ms / 1000
  if (s < 60) return `${s.toFixed(s < 10 ? 1 : 0)}s`
  const m = Math.floor(s / 60)
  return `${m}m ${Math.round(s % 60)}s`
}

// Script-stage presets prefill the tool image + command (env context is injected
// as RIGGER_* vars; secrets like a Sonar token are inlined by the user).
const SCRIPT_PRESETS = [
  { id: 'custom',    label: 'Custom', image: '', command: '', network: false },
  { id: 'trivy',     label: 'Vuln scan (Trivy)',   image: 'aquasec/trivy:latest',                 command: 'trivy image $RIGGER_IMAGES', network: false },
  { id: 'cypress',   label: 'E2E (Cypress)',        image: 'cypress/included:latest',              command: 'cypress run --config baseUrl=$RIGGER_APP_URL', network: true },
  { id: 'playwright',label: 'E2E (Playwright)',     image: 'mcr.microsoft.com/playwright:latest',  command: 'npx playwright test', network: true },
  { id: 'sonar',     label: 'Code analysis (SonarQube)', image: 'sonarsource/sonar-scanner-cli:latest', command: 'sonar-scanner -Dsonar.host.url=$SONAR_HOST_URL -Dsonar.login=$SONAR_TOKEN', network: false },
]

const blankStage = (env) => ({ type: 'deploy', env: env || '', service: '', command: '', on_failure: 'stop' })

const inputCls = 'px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm text-content-strong focus:outline-none focus:border-brand-500'

export default function PipelinesTab({ workspace, name, envNames = [], serviceNames = [] }) {
  const qc = useQueryClient()
  const { data: pipelines = [], isLoading } = useQuery({
    queryKey: ['pipelines', workspace, name],
    queryFn: () => fetchPipelines(workspace, name),
  })
  const [editing, setEditing] = useState(null) // draft pipeline (with optional id) or null
  const [gen, setGen] = useState(null) // null = closed; { target: null } = new; { target: pipeline } = regenerate

  const saveMut = useMutation({
    mutationFn: (p) => p.id ? updatePipeline(workspace, name, p.id, p) : createPipeline(workspace, name, p),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['pipelines', workspace, name] }); setEditing(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deletePipeline(workspace, name, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['pipelines', workspace, name] }),
  })

  if (editing) {
    return (
      <PipelineEditor
        draft={editing} envNames={envNames} serviceNames={serviceNames}
        onChange={setEditing}
        onCancel={() => setEditing(null)}
        onSave={() => saveMut.mutate(editing)}
        saving={saveMut.isPending}
        error={saveMut.error?.response?.data?.error}
      />
    )
  }

  return (
    <section className="mb-6">
      <div className="flex items-center justify-between mb-3">
        <div>
          <h2 className="text-sm font-semibold text-content">Pipelines</h2>
          <p className="text-xs text-content-subtle">Chain deploy, build, test and backup steps into a one-click run.</p>
        </div>
        <div className="flex items-center gap-2">
          <button
            onClick={() => setGen({ target: null })}
            className="text-xs font-semibold px-3 py-1.5 rounded-lg border border-border-strong text-content hover:bg-surface-raised transition-colors"
            title="Generate a release pipeline from this project's environments"
          >
            ✨ Generate from environments
          </button>
          <button
            onClick={() => setEditing({ name: '', enabled: true, stages: [blankStage(envNames[0])] })}
            className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 text-white transition-colors"
          >
            + Add pipeline
          </button>
        </div>
      </div>

      {gen && (
        <GeneratePipelineDialog
          workspace={workspace} name={name} envNames={envNames} target={gen.target}
          onClose={() => setGen(null)}
          onGenerated={(draft) => {
            const t = gen.target
            setGen(null)
            // Regenerate updates the existing pipeline in place (keep id + name);
            // a fresh draft opens as a new pipeline.
            setEditing(t ? { ...draft, id: t.id, name: t.name } : draft)
          }}
        />
      )}

      {isLoading ? (
        <p className="text-sm text-content-subtle">Loading…</p>
      ) : pipelines.length === 0 ? (
        <div className="border border-dashed border-border-strong rounded-xl p-8 text-center text-sm text-content-subtle">
          No pipelines yet. Create one to automate a deploy → test → backup sequence.
        </div>
      ) : (
        <div className="space-y-3">
          {pipelines.map(p => (
            <PipelineCard
              key={p.id} workspace={workspace} name={name} pipeline={p} envNames={envNames}
              onEdit={() => setEditing({ ...p })}
              onRegenerate={() => setGen({ target: p })}
              onDelete={() => { if (confirm(`Delete pipeline "${p.name}"?`)) delMut.mutate(p.id) }}
            />
          ))}
        </div>
      )}
    </section>
  )
}

export function stageSummary(s) {
  if (s.type === 'gate') return 'gate'
  if (s.type === 'test') return `test ${s.service}`
  if (s.type === 'push') return `promote ${s.env}→${s.to_env}`
  if (s.type === 'version') return `version ${s.part || ''}`
  if (s.type === 'script') return `script ${s.image || ''}`.trim()
  if (s.type === 'build') {
    let t = `build ${s.env}`
    if (s.service) t += `/${s.service}`
    if (s.part) t += ` ↑${s.part}`
    if (s.push) t += ' +push'
    return t
  }
  let t = `${s.type} ${s.env}`
  if (s.service && ['deploy', 'update', 'restart', 'refresh'].includes(s.type)) t += `/${s.service}`
  return t
}

function PipelineCard({ workspace, name, pipeline, envNames = [], onEdit, onDelete, onRegenerate }) {
  const qc = useQueryClient()
  const [showHistory, setShowHistory] = useState(false)
  const [showHooks, setShowHooks] = useState(false)
  const [openRunId, setOpenRunId] = useState(null) // run whose log/status window is open

  // Poll the latest run so the Run button disables and a "View log" link appears
  // while a run is in flight — regardless of who/what started it (UI or webhook).
  const { data: latest = [] } = useQuery({
    queryKey: ['pipeline-runs', workspace, name, pipeline.id],
    queryFn: () => fetchPipelineRuns(workspace, name, pipeline.id, 1),
    // Fast while active; slow baseline when idle so a webhook-started run is picked
    // up without a manual interaction (a bare `false` froze this until reload).
    refetchInterval: (q) => (q.state.data || []).some(r => r.status === 'running' || r.status === 'awaiting') ? 2500 : 15_000,
  })
  const latestRun = latest[0]
  const active = latestRun && (latestRun.status === 'running' || latestRun.status === 'awaiting')

  const runMut = useMutation({
    mutationFn: () => startPipelineRun(workspace, name, pipeline.id),
    onSuccess: ({ run_id }) => {
      setOpenRunId(run_id)
      qc.invalidateQueries({ queryKey: ['pipeline-runs', workspace, name, pipeline.id] })
    },
  })

  // Stale = a stage targets an environment that no longer exists (e.g. it was
  // deleted after the pipeline was generated). The run would fail on that stage.
  const known = new Set(envNames)
  const refEnvs = new Set()
  pipeline.stages.forEach(s => { if (s.env) refEnvs.add(s.env); if (s.to_env) refEnvs.add(s.to_env) })
  const staleEnvs = [...refEnvs].filter(e => !known.has(e))
  return (
    <div className="bg-surface border border-border rounded-xl p-4">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="font-semibold text-content-strong truncate">{pipeline.name}</span>
            {!pipeline.enabled && <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-surface-raised text-content-faint">disabled</span>}
            {staleEnvs.length > 0 && (
              <span title={`References removed environment(s): ${staleEnvs.join(', ')}. Regenerate or edit to fix.`}
                className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-warning-subtle text-warning-fg border border-warning-border/60">⚠ stale</span>
            )}
          </div>
          <div className="flex flex-wrap items-center gap-1.5 mt-2">
            {pipeline.stages.map((s, i) => (
              <span key={i} className="inline-flex items-center gap-1 text-[11px] font-mono px-1.5 py-0.5 rounded bg-surface-raised text-content-muted border border-border-strong">
                {STAGE_ICON[s.type]} {stageSummary(s)}
              </span>
            ))}
          </div>
        </div>
        <div className="flex flex-col items-end gap-1.5 shrink-0">
          <div className="flex items-center gap-2">
            <button onClick={() => runMut.mutate()} disabled={active || runMut.isPending}
              className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 text-white transition-colors disabled:opacity-50 disabled:cursor-not-allowed">
              {active ? '▶ Running…' : '▶ Run'}
            </button>
            {onRegenerate && (
              <button onClick={onRegenerate} title="Regenerate this pipeline's stages from the current environments"
                className="text-xs px-2.5 py-1.5 rounded-lg bg-surface-raised hover:bg-surface-overlay text-content transition-colors">🔄</button>
            )}
            <button onClick={onEdit} className="text-xs px-2.5 py-1.5 rounded-lg bg-surface-raised hover:bg-surface-overlay text-content transition-colors">Edit</button>
            <button onClick={onDelete} className="text-xs px-2.5 py-1.5 rounded-lg text-danger-fg hover:bg-danger-subtle/40 transition-colors">Delete</button>
          </div>
          {latestRun && (
            <button onClick={() => setOpenRunId(latestRun.id)}
              className="text-[11px] text-brand-400 hover:text-brand-300 transition-colors flex items-center gap-1">
              {active && <span className="inline-block w-1.5 h-1.5 rounded-full bg-amber-400 animate-pulse" />}
              View log
            </button>
          )}
        </div>
      </div>
      <div className="mt-3 flex items-center gap-4">
        <button onClick={() => setShowHistory(v => !v)} className="text-xs text-content-subtle hover:text-content transition-colors">
          {showHistory ? '▾' : '▸'} Run history
        </button>
        <button onClick={() => setShowHooks(v => !v)} className="text-xs text-content-subtle hover:text-content transition-colors">
          {showHooks ? '▾' : '▸'} Webhooks
        </button>
      </div>
      {showHistory && <RunHistory workspace={workspace} name={name} pipeline={pipeline} />}
      {showHooks && <Webhooks workspace={workspace} name={name} pipelineId={pipeline.id} />}

      {openRunId != null && (
        <RunModal workspace={workspace} name={name} pipeline={pipeline} runId={openRunId} onClose={() => setOpenRunId(null)} />
      )}
    </div>
  )
}

function Webhooks({ workspace, name, pipelineId }) {
  const qc = useQueryClient()
  const { data: hooks = [], isLoading } = useQuery({
    queryKey: ['pipeline-webhooks', workspace, name, pipelineId],
    queryFn: () => fetchPipelineWebhooks(workspace, name, pipelineId),
  })
  const [newToken, setNewToken] = useState(null) // raw token shown once after create
  const [copied, setCopied] = useState(false)
  async function copyUrl() {
    try { await navigator.clipboard.writeText(newToken); setCopied(true); setTimeout(() => setCopied(false), 1500) } catch { /* clipboard unavailable */ }
  }

  const addMut = useMutation({
    mutationFn: () => createPipelineWebhook(workspace, name, pipelineId, {}),
    onSuccess: (wh) => {
      setNewToken(`${window.location.origin}/api/pipelines/hooks/${wh.token}`)
      qc.invalidateQueries({ queryKey: ['pipeline-webhooks', workspace, name, pipelineId] })
    },
  })
  const delMut = useMutation({
    mutationFn: (id) => deletePipelineWebhook(workspace, name, pipelineId, id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['pipeline-webhooks', workspace, name, pipelineId] }),
  })

  return (
    <div className="mt-2 space-y-2 border-t border-border pt-2">
      <div className="flex items-center justify-between">
        <p className="text-xs text-content-subtle">POST to a webhook URL to trigger this pipeline (e.g. from GitHub/Gitea on push).</p>
        <button onClick={() => addMut.mutate()} disabled={addMut.isPending} className="text-xs font-semibold text-brand-400 hover:text-brand-300 disabled:opacity-40">+ Add webhook</button>
      </div>

      {newToken && (
        <div className="px-3 py-2 rounded-lg bg-warning-subtle/40 border border-warning-border/60 text-xs">
          <p className="text-warning-fg font-semibold mb-1">Copy this URL now — it won't be shown again:</p>
          <div className="flex items-start gap-2">
            <code className="flex-1 font-mono break-all text-content-strong select-all">{newToken}</code>
            <button onClick={copyUrl} title="Copy URL"
              className="shrink-0 px-2 py-1 rounded bg-surface-raised hover:bg-surface-overlay text-content transition-colors">
              {copied ? '✓ Copied' : '⧉ Copy'}
            </button>
          </div>
        </div>
      )}

      {isLoading ? (
        <p className="text-xs text-content-subtle">Loading…</p>
      ) : hooks.length === 0 ? (
        <p className="text-xs text-content-faint">No webhooks yet.</p>
      ) : (
        hooks.map(h => (
          <div key={h.id} className="flex items-center gap-2 text-xs">
            <span className="font-mono text-content-muted">hook #{h.id}</span>
            <span className="text-content-faint">created {new Date(h.created_at).toLocaleDateString()}</span>
            {h.last_triggered_at && <span className="text-content-faint">· last fired {new Date(h.last_triggered_at).toLocaleString()}</span>}
            <button onClick={() => delMut.mutate(h.id)} className="ml-auto text-danger-fg hover:bg-danger-subtle/40 px-1.5 py-0.5 rounded">Delete</button>
          </div>
        ))
      )}
    </div>
  )
}

export function statusChipCls(status) {
  if (status === 'ok') return 'bg-success-subtle text-success-fg border-success-border/60'
  if (status === 'fail' || status === 'rejected') return 'bg-danger-subtle text-danger-fg border-danger-border/60'
  if (status === 'cancelled' || status === 'skipped') return 'bg-surface-raised text-content-faint border-border-strong'
  return 'bg-warning-subtle text-warning-fg border-warning-border/60' // running | awaiting
}

function RunHistory({ workspace, name, pipeline }) {
  const pipelineId = pipeline.id
  const qc = useQueryClient()
  const [logRun, setLogRun] = useState(null) // run id whose saved logs are open
  const { data: runs = [], isLoading } = useQuery({
    queryKey: ['pipeline-runs', workspace, name, pipelineId],
    queryFn: () => fetchPipelineRuns(workspace, name, pipelineId, 20),
    // Slow baseline when idle so a webhook-started run appears in history without a
    // manual interaction; fast while one is in flight.
    refetchInterval: (q) => (q.state.data || []).some(r => r.status === 'running' || r.status === 'awaiting') ? 3000 : 15_000,
  })
  const approveMut = useMutation({
    mutationFn: (runId) => approvePipelineRun(workspace, name, pipelineId, runId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['pipeline-runs', workspace, name, pipelineId] }),
  })
  const rejectMut = useMutation({
    mutationFn: (runId) => rejectPipelineRun(workspace, name, pipelineId, runId),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['pipeline-runs', workspace, name, pipelineId] }),
  })
  if (isLoading) return <p className="mt-2 text-xs text-content-subtle">Loading…</p>
  if (runs.length === 0) return <p className="mt-2 text-xs text-content-subtle">No runs yet.</p>
  return (
    <div className="mt-2 space-y-2">
      {runs.map(r => (
        <div key={r.id} className="flex items-center gap-2 text-xs border-t border-border pt-2">
          <span className={`px-1.5 py-0.5 rounded border ${statusChipCls(r.status)}`}>{r.status}</span>
          <span className="text-content-subtle">{new Date(r.started_at).toLocaleString()}</span>
          <span className="text-content-faint">· {r.username || 'system'}</span>
          {r.status === 'awaiting' && (
            <span className="flex items-center gap-1.5 ml-2">
              <button onClick={() => approveMut.mutate(r.id)} disabled={approveMut.isPending}
                className="px-2 py-0.5 rounded bg-brand-600 hover:bg-brand-700 text-white font-semibold disabled:opacity-40">Approve</button>
              <button onClick={() => rejectMut.mutate(r.id)} disabled={rejectMut.isPending}
                className="px-2 py-0.5 rounded text-danger-fg hover:bg-danger-subtle/40">Reject</button>
            </span>
          )}
          <div className="flex flex-wrap gap-1 ml-auto items-center">
            {r.stages.map((s, i) => (
              <span key={i} title={`${s.label} — ${s.status}`} className={`px-1 rounded border text-[10px] ${statusChipCls(s.status)}`}>
                {STAGE_ICON[s.type] || '•'}
              </span>
            ))}
            <button onClick={() => setLogRun(r.id)} title="View saved run logs"
              className="ml-1 px-1.5 py-0.5 rounded bg-surface-raised hover:bg-surface-overlay text-content-subtle hover:text-content text-[10px]">
              Logs
            </button>
          </div>
        </div>
      ))}
      {logRun != null && (
        <RunModal workspace={workspace} name={name} pipeline={pipeline} runId={logRun} onClose={() => setLogRun(null)} />
      )}
    </div>
  )
}

// stepCls / stepIcon map a run-stage status to the pipeline-graph node style.
// Shared with the project-page release graph (imported there).
export function stepCls(status) {
  if (status === 'ok') return 'bg-green-500 border-success text-green-900'
  if (status === 'running') return 'bg-amber-400 border-warning text-amber-900 animate-pulse'
  if (status === 'awaiting') return 'bg-amber-400 border-warning text-amber-900'
  if (status === 'fail' || status === 'rejected') return 'bg-danger-subtle border-danger-border text-danger-fg'
  if (status === 'cancelled') return 'bg-surface-raised border-danger-border/60 text-danger-fg'
  return 'bg-surface-raised border-border-strong text-content-subtle' // skipped / pending
}
export function stepIcon(status) {
  if (status === 'ok') return '✓'
  if (status === 'fail' || status === 'rejected') return '✕'
  if (status === 'cancelled') return '■'
  if (status === 'running') return '◌'
  if (status === 'awaiting') return '⏸'
  return '○'
}

// RunModal is the single live + historical run viewer. It POLLS the run record
// (no socket), so it can be closed and reopened freely while the run keeps going
// in the background — behaving identically whether the run was launched from the
// project page, the Edit-Project pipelines tab, or a webhook. Each stage shows a
// live indicator (pending ○ / running ◌ pulsing / ok ✓ / fail ✕) plus its captured
// output (the in-flight stage streams in, persisted ~1×/sec by the executor).
export function RunModal({ workspace, name, pipeline, runId, onClose }) {
  const logRef = useRef(null)
  const qc = useQueryClient()
  const { data: run } = useQuery({
    queryKey: ['pipeline-run', workspace, name, pipeline.id, runId],
    queryFn: () => fetchPipelineRun(workspace, name, pipeline.id, runId),
    refetchInterval: (q) => {
      const s = q.state.data?.status
      return (s === 'running' || s === 'awaiting') ? 1500 : false
    },
  })

  // Force-stop a still-active run: signals the backend to kill the in-flight stage.
  // The poll then picks up the cancelled status; also refresh the history list.
  const cancelMut = useMutation({
    mutationFn: () => cancelPipelineRun(workspace, name, pipeline.id, runId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['pipeline-run', workspace, name, pipeline.id, runId] })
      qc.invalidateQueries({ queryKey: ['pipeline-runs', workspace, name, pipeline.id] })
    },
  })

  // Overlay the recorded results onto the pipeline definition so future (not-yet-run)
  // stages render as pending. For a finished/edited pipeline, recorded results are
  // authoritative, so iterate the longer of the two.
  const defs = pipeline.stages || []
  const res = run?.stages || []
  const count = Math.max(defs.length, res.length)
  const typeAt = (i) => res[i]?.type || defs[i]?.type
  const statusAt = (i) => res[i]?.status || 'pending'
  const labelAt = (i) => res[i]?.label || (defs[i] ? stageSummary(defs[i]) : `stage ${i + 1}`)
  const overall = run?.status || 'running'

  useEffect(() => { if (logRef.current) logRef.current.scrollTop = logRef.current.scrollHeight }, [run])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border-strong rounded-xl w-full max-w-3xl flex flex-col max-h-[85vh]" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-3 border-b border-border gap-3">
          <div className="flex items-center gap-2 min-w-0">
            <span className="font-semibold text-content-strong truncate">▶ {pipeline.name}</span>
            <span className={`text-[11px] px-1.5 py-0.5 rounded border ${statusChipCls(overall)}`}>
              {overall === 'running' ? 'running…' : overall}
            </span>
            <span className="text-[11px] text-content-faint shrink-0">run #{runId}{run?.trigger === 'webhook' ? ' · webhook' : ''}</span>
          </div>
          <div className="flex items-center gap-2 shrink-0">
            {(overall === 'running' || overall === 'awaiting') && (
              <button
                onClick={() => { if (window.confirm('Force-stop this run? The in-flight step is killed; later steps are skipped.')) cancelMut.mutate() }}
                disabled={cancelMut.isPending}
                className="text-[11px] px-2 py-1 rounded border border-danger-border text-danger-fg hover:bg-danger-subtle disabled:opacity-50"
                title="Force-stop this running pipeline">
                {cancelMut.isPending ? 'Cancelling…' : '■ Cancel'}
              </button>
            )}
            <button onClick={onClose} className="text-content-faint hover:text-content text-lg leading-none">✕</button>
          </div>
        </div>

        {/* Stage graph — current step pulses, completed steps go green. */}
        <div className="flex items-center gap-0 px-5 py-4 border-b border-border overflow-x-auto">
          {Array.from({ length: count }, (_, i) => {
            const st = statusAt(i)
            return (
              <div key={i} className="flex items-center">
                <div className="flex flex-col items-center gap-1.5 min-w-[86px]">
                  <div className={`w-8 h-8 rounded-full border-2 flex items-center justify-center text-xs font-bold ${stepCls(st)}`}>{stepIcon(st)}</div>
                  <span className="text-[10px] text-center leading-tight text-content">{STAGE_ICON[typeAt(i)]} {labelAt(i)}</span>
                </div>
                {i < count - 1 && <div className="h-0.5 w-6 shrink-0 mx-1 bg-surface-overlay" />}
              </div>
            )
          })}
        </div>

        {/* Per-stage logs — pending stages are hidden until they start. */}
        <div ref={logRef} className="flex-1 overflow-y-auto px-5 py-4 space-y-3">
          {!run ? (
            <p className="text-xs text-content-subtle">Loading…</p>
          ) : count === 0 ? (
            <p className="text-xs text-content-subtle">No stages.</p>
          ) : Array.from({ length: count }, (_, i) => {
            const st = statusAt(i)
            if (st === 'pending') return null
            const r = res[i]
            return (
              <div key={i}>
                <div className="flex items-center gap-2 mb-1">
                  <span className="text-xs">{STAGE_ICON[typeAt(i)] || '•'}</span>
                  <span className="text-xs font-medium text-content-strong">{labelAt(i)}</span>
                  <span className={`px-1.5 py-0.5 rounded border text-[10px] ${statusChipCls(st)}`}>{st === 'running' ? 'running…' : st}</span>
                  {r?.started_at ? (
                    <span className="text-[10px] text-content-faint">
                      {fmtClock(r.started_at)}{r.finished_at ? ` → ${fmtClock(r.finished_at)}` : ''}{r.ms ? ` · ${fmtDur(r.ms)}` : ''}
                    </span>
                  ) : r?.ms ? <span className="text-[10px] text-content-faint">{fmtDur(r.ms)}</span> : null}
                </div>
                {/* Terminal box: the background is always dark, so the text must be a
                    fixed light colour — `text-content` is dark in light theme and would
                    vanish on this bg. */}
                {(r?.output || st === 'running') && (
                  <pre className="text-[11px] font-mono bg-[#0c1322] border border-border-strong rounded-lg p-3 overflow-x-auto whitespace-pre-wrap break-words text-gray-100">
                    {r?.output ? renderAnsi(r.output) : <span className="text-gray-400">running…</span>}
                  </pre>
                )}
              </div>
            )
          })}
        </div>
      </div>
    </div>
  )
}

// ── Editor ────────────────────────────────────────────────────────────────────

function PipelineEditor({ draft, envNames, serviceNames = [], onChange, onCancel, onSave, saving, error }) {
  const setStage = (i, patch) => onChange({ ...draft, stages: draft.stages.map((s, j) => j === i ? { ...s, ...patch } : s) })
  const addStage = () => onChange({ ...draft, stages: [...draft.stages, blankStage(envNames[0])] })
  const removeStage = (i) => onChange({ ...draft, stages: draft.stages.filter((_, j) => j !== i) })
  const move = (i, dir) => {
    const j = i + dir
    if (j < 0 || j >= draft.stages.length) return
    const next = [...draft.stages]
    ;[next[i], next[j]] = [next[j], next[i]]
    onChange({ ...draft, stages: next })
  }

  return (
    <section className="mb-6">
      <div className="flex items-center justify-between mb-4">
        <h2 className="text-sm font-semibold text-content">{draft.id ? 'Edit pipeline' : 'New pipeline'}</h2>
        <div className="flex gap-2">
          <button onClick={onCancel} className="text-xs px-3 py-1.5 rounded-lg bg-surface-raised hover:bg-surface-overlay text-content transition-colors">Cancel</button>
          <button onClick={onSave} disabled={saving} className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white transition-colors">
            {saving ? 'Saving…' : 'Save pipeline'}
          </button>
        </div>
      </div>

      {error && <div className="mb-4 px-3 py-2 bg-danger-subtle border border-danger-border text-danger-fg rounded-lg text-sm">{error}</div>}

      <div className="bg-surface border border-border rounded-xl p-5 space-y-5">
        <div className="flex items-end gap-4">
          <div className="flex-1">
            <label className="block text-[11px] font-semibold uppercase tracking-wide text-content-muted mb-1">Pipeline name</label>
            <input value={draft.name} onChange={e => onChange({ ...draft, name: e.target.value })} placeholder="e.g. Ship dev" className={`${inputCls} w-full`} />
          </div>
          <label className="flex items-center gap-2 pb-1.5 cursor-pointer">
            <input type="checkbox" checked={draft.enabled} onChange={e => onChange({ ...draft, enabled: e.target.checked })} className="w-4 h-4 accent-brand-500" />
            <span className="text-xs text-content-muted">Enabled</span>
          </label>
        </div>

        <div>
          <div className="flex items-center justify-between mb-2">
            <label className="block text-[11px] font-semibold uppercase tracking-wide text-content-muted">Stages</label>
            <button onClick={addStage} className="text-xs font-semibold text-brand-400 hover:text-brand-300">+ Add stage</button>
          </div>
          <div className="space-y-2">
            {draft.stages.map((s, i) => (
              <StageRow key={i} idx={i} count={draft.stages.length} stage={s} envNames={envNames} serviceNames={serviceNames}
                onChange={patch => setStage(i, patch)} onRemove={() => removeStage(i)} onMove={dir => move(i, dir)} />
            ))}
          </div>
        </div>
      </div>
    </section>
  )
}

// Stage types that act on a whole env but can be scoped to a single service.
const SERVICE_SCOPED = ['build', 'deploy', 'update', 'restart', 'refresh']

function StageRow({ idx, count, stage, envNames, serviceNames = [], onChange, onRemove, onMove }) {
  return (
    <div className="border border-border-strong rounded-lg p-3 bg-surface-raised/40">
      <div className="flex items-center gap-2 flex-wrap">
        <span className="text-content-faint text-xs font-mono w-5 text-center">{idx + 1}</span>
        <select value={stage.type} onChange={e => onChange({ type: e.target.value })} className={inputCls}>
          {STAGE_TYPES.map(t => <option key={t.value} value={t.value}>{t.label}</option>)}
        </select>
        {stage.type === 'gate' && (
          <input value={stage.command} onChange={e => onChange({ command: e.target.value })} placeholder="approval note (optional)" className={`${inputCls} flex-1`} />
        )}
        {stage.type === 'version' && (
          <>
            <select value={stage.part || ''} onChange={e => onChange({ part: e.target.value })} className={inputCls}>
              <option value="">— part —</option>
              <option value="major">major</option>
              <option value="minor">minor</option>
              <option value="patch">patch</option>
              <option value="build">build</option>
            </select>
            <span className="text-xs text-content-faint">bump semver (custom apps)</span>
          </>
        )}
        {stage.type !== 'gate' && stage.type !== 'version' && (
          <>
            <select value={stage.env} onChange={e => onChange({ env: e.target.value })} className={inputCls}>
              <option value="">{stage.type === 'push' ? '— from —' : '— env —'}</option>
              {envNames.map(e => <option key={e} value={e}>{e}</option>)}
            </select>
            {SERVICE_SCOPED.includes(stage.type) && (
              serviceNames.length > 0 ? (
                <select value={stage.service || ''} onChange={e => onChange({ service: e.target.value })} className={inputCls} title="Scope this stage to one service (microservices) — default is all services">
                  <option value="">all services</option>
                  {serviceNames.map(s => <option key={s} value={s}>{s}</option>)}
                </select>
              ) : (
                <input value={stage.service || ''} onChange={e => onChange({ service: e.target.value })} placeholder="service (all)" className={`${inputCls} w-32`} title="Scope to one service (blank = all)" />
              )
            )}
            {stage.type === 'push' && (
              <>
                <span className="text-content-faint">→</span>
                <select value={stage.to_env || ''} onChange={e => onChange({ to_env: e.target.value })} className={inputCls}>
                  <option value="">— to —</option>
                  {envNames.map(e => <option key={e} value={e}>{e}</option>)}
                </select>
              </>
            )}
            {stage.type === 'build' && (
              <>
                <select value={stage.part || ''} onChange={e => onChange({ part: e.target.value })} className={inputCls} title="Bump the version as part of this build (so the deploy rolls to the new version)">
                  <option value="">no bump</option>
                  <option value="build">bump build</option>
                  <option value="patch">bump patch</option>
                  <option value="minor">bump minor</option>
                  <option value="major">bump major</option>
                </select>
                <label className="flex items-center gap-1.5 text-xs text-content-muted cursor-pointer" title="Push images to the registry (required before a later promote)">
                  <input type="checkbox" checked={!!stage.push} onChange={e => onChange({ push: e.target.checked })} className="w-3.5 h-3.5 accent-brand-500" />
                  push to registry
                </label>
              </>
            )}
            {stage.type === 'backup' && (
              <input value={stage.service} onChange={e => onChange({ service: e.target.value })} placeholder="service (optional)" className={`${inputCls} w-36`} />
            )}
            {stage.type === 'script' && (
              <>
                <select defaultValue="" onChange={e => {
                  const p = SCRIPT_PRESETS.find(x => x.id === e.target.value)
                  if (p && p.id !== 'custom') onChange({ image: p.image, command: p.command, network: p.network })
                }} className={inputCls} title="Preset tool">
                  <option value="">preset…</option>
                  {SCRIPT_PRESETS.map(p => <option key={p.id} value={p.id}>{p.label}</option>)}
                </select>
                <label className="flex items-center gap-1.5 text-xs text-content-muted cursor-pointer">
                  <input type="checkbox" checked={!!stage.network} onChange={e => onChange({ network: e.target.checked })} className="w-3.5 h-3.5 accent-brand-500" />
                  app network
                </label>
              </>
            )}
            <select value={stage.on_failure} onChange={e => onChange({ on_failure: e.target.value })} className={inputCls} title="On failure">
              <option value="stop">on fail: stop</option>
              <option value="continue">on fail: continue</option>
            </select>
          </>
        )}
        <div className="ml-auto flex items-center gap-1">
          <button onClick={() => onMove(-1)} disabled={idx === 0} className="text-xs px-1.5 py-1 rounded text-content-faint hover:text-content disabled:opacity-30">↑</button>
          <button onClick={() => onMove(1)} disabled={idx === count - 1} className="text-xs px-1.5 py-1 rounded text-content-faint hover:text-content disabled:opacity-30">↓</button>
          <button onClick={onRemove} className="text-xs px-1.5 py-1 rounded text-danger-fg hover:bg-danger-subtle/40">✕</button>
        </div>
      </div>
      {stage.type === 'test' && (
        <div className="flex items-center gap-2 mt-2 pl-7">
          <input value={stage.service} onChange={e => onChange({ service: e.target.value })} placeholder="service (e.g. app)" className={`${inputCls} w-40`} />
          <input value={stage.command} onChange={e => onChange({ command: e.target.value })} placeholder='command, e.g. curl -f http://localhost/' className={`${inputCls} flex-1 font-mono`} />
        </div>
      )}
      {stage.type === 'script' && (
        <div className="mt-2 pl-7 space-y-2">
          <input value={stage.image || ''} onChange={e => onChange({ image: e.target.value })} placeholder="tool image, e.g. aquasec/trivy:latest" className={`${inputCls} w-full font-mono`} />
          <input value={stage.command || ''} onChange={e => onChange({ command: e.target.value })} placeholder='command (sh -c), e.g. trivy image $RIGGER_IMAGES' className={`${inputCls} w-full font-mono`} />
          <p className="text-[11px] text-content-faint">Injected: <code className="font-mono">$RIGGER_ENV $RIGGER_APP_URL $RIGGER_STACK $RIGGER_IMAGES $RIGGER_IMAGE_&lt;SVC&gt;</code>. Secrets (e.g. a Sonar token) are inlined here and stored in the pipeline — treat with care.</p>
        </div>
      )}
    </div>
  )
}

// ── Run console (streaming) ───────────────────────────────────────────────────

const ANSI_COLORS = { '31': '#f87171', '32': '#34d399', '33': '#fbbf24', '36': '#22d3ee', '37': '#e5e7eb' }

// renderAnsi converts the SGR escape codes the backend emits into styled spans.
function renderAnsi(text) {
  const out = []
  const re = /\[([0-9;]*)m/g
  let cur = { color: null, bold: false, dim: false }
  let last = 0, m, key = 0
  const push = (str) => {
    str = str.split(String.fromCharCode(27)).join('') // drop the leftover ESC byte
    if (!str) return
    out.push(<span key={key++} style={{ color: cur.color || undefined, fontWeight: cur.bold ? 700 : undefined, opacity: cur.dim ? 0.6 : undefined }}>{str}</span>)
  }
  while ((m = re.exec(text))) {
    push(text.slice(last, m.index))
    last = re.lastIndex
    const codes = m[1].split(';').filter(Boolean)
    if (codes.length === 0 || codes.includes('0')) cur = { color: null, bold: false, dim: false }
    for (const c of codes) {
      if (c === '1') cur.bold = true
      else if (c === '2') cur.dim = true
      else if (ANSI_COLORS[c]) cur.color = ANSI_COLORS[c]
    }
  }
  push(text.slice(last))
  return out
}

// GeneratePipelineDialog proposes a release/hotfix pipeline from the project's
// ordered environments. The server returns a draft (it does not persist); the
// caller opens it in the normal editor, so every stage stays editable.
function GeneratePipelineDialog({ workspace, name, envNames = [], target = null, onClose, onGenerated }) {
  const [template, setTemplate] = useState('release')
  const [bumpPart, setBumpPart] = useState('')
  const [gate, setGate] = useState(true)
  const [hotfixTo, setHotfixTo] = useState(envNames[envNames.length - 1] || '')
  const [busy, setBusy] = useState(false)
  const [err, setErr] = useState('')

  async function generate() {
    setBusy(true); setErr('')
    try {
      const draft = await suggestPipeline(workspace, name, {
        template, bump_part: bumpPart, gate,
        hotfix_to: template === 'hotfix' ? hotfixTo : '',
      })
      onGenerated?.(draft)
    } catch (e) {
      setErr(e?.response?.data?.error || 'Failed to generate')
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div className="w-full max-w-md bg-surface border border-border rounded-xl shadow-xl" onClick={e => e.stopPropagation()}>
        <div className="px-5 py-4 border-b border-border">
          <h2 className="text-sm font-semibold text-content-strong">{target ? 'Regenerate pipeline' : 'Generate pipeline'}</h2>
          <p className="text-xs text-content-subtle mt-1">
            {target
              ? <>Re-seeds <span className="font-medium text-content">{target.name}</span> from the current environments — replaces its stages. You can review before saving.</>
              : 'Seeded from your environments (in deploy-tier order). Review and edit before saving.'}
          </p>
        </div>
        <div className="px-5 py-4 space-y-4">
          <div>
            <label className="text-xs font-semibold uppercase tracking-wide text-content-muted">Template</label>
            <div className="flex gap-2 mt-1.5">
              {[['release', 'Release — full chain'], ['hotfix', 'Hotfix — bypass lower envs']].map(([v, label]) => (
                <button key={v} type="button" onClick={() => setTemplate(v)}
                  className={`flex-1 px-3 py-2 rounded-lg text-xs font-medium border transition-colors ${template === v ? 'border-brand-500 bg-brand-500/10 text-content-strong' : 'border-border-strong text-content-muted hover:bg-surface-raised'}`}>
                  {label}
                </button>
              ))}
            </div>
          </div>

          {template === 'hotfix' && (
            <div>
              <label className="text-xs font-semibold uppercase tracking-wide text-content-muted">Promote straight to</label>
              <select value={hotfixTo} onChange={e => setHotfixTo(e.target.value)} className={`${inputCls} w-full mt-1.5`}>
                {envNames.map(e => <option key={e} value={e}>{e}</option>)}
              </select>
            </div>
          )}

          <div className="grid grid-cols-2 gap-3 items-end">
            <div>
              <label className="text-xs font-semibold uppercase tracking-wide text-content-muted">Version bump</label>
              <select value={bumpPart} onChange={e => setBumpPart(e.target.value)} className={`${inputCls} w-full mt-1.5`}>
                <option value="">{template === 'hotfix' ? 'patch (default)' : 'none'}</option>
                <option value="build">build</option>
                <option value="patch">patch</option>
                <option value="minor">minor</option>
                <option value="major">major</option>
              </select>
            </div>
            <label className="flex items-center gap-2 text-xs text-content-muted cursor-pointer pb-2">
              <input type="checkbox" checked={gate} onChange={e => setGate(e.target.checked)} className="w-3.5 h-3.5 accent-brand-500" />
              Gate before final promote
            </label>
          </div>

          {err && <p className="text-xs text-danger-fg">{err}</p>}
        </div>
        <div className="px-5 py-4 border-t border-border flex items-center justify-end gap-2">
          <button type="button" onClick={onClose} disabled={busy}
            className="px-3 py-1.5 rounded-lg text-sm border border-border-strong text-content hover:bg-surface-raised transition-colors">Cancel</button>
          <button type="button" onClick={generate} disabled={busy}
            className="px-3 py-1.5 rounded-lg text-sm font-semibold bg-brand-600 hover:bg-brand-700 text-white transition-colors disabled:opacity-50">
            {busy ? (target ? 'Regenerating…' : 'Generating…') : (target ? 'Regenerate' : 'Generate')}
          </button>
        </div>
      </div>
    </div>
  )
}
