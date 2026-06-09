import { useState, useRef, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  fetchPipelines, createPipeline, updatePipeline, deletePipeline,
  fetchPipelineRuns, openPipelineSocket,
  fetchPipelineWebhooks, createPipelineWebhook, deletePipelineWebhook,
} from '../lib/api'

// Phase 9 — Deployment Pipelines tab (inside Edit Project). A pipeline is an
// ordered list of stages; each stage maps to a deploy/build/restart/backup action
// or a sandboxed `test` (command run inside a service container). Runs stream live.

const STAGE_TYPES = [
  { value: 'deploy',  label: 'Deploy — up (current/built images)' },
  { value: 'update',  label: 'Update — pull latest + recreate' },
  { value: 'build',   label: 'Build images (custom apps)' },
  { value: 'restart', label: 'Restart' },
  { value: 'backup',  label: 'Backup' },
  { value: 'test',    label: 'Test — exec in container' },
]
const STAGE_ICON = { deploy: '🚀', update: '⬆️', build: '🧱', restart: '🔄', backup: '💾', test: '🧪' }

const blankStage = (env) => ({ type: 'deploy', env: env || '', service: '', command: '', on_failure: 'stop' })

const inputCls = 'px-2 py-1 bg-surface-raised border border-border-strong rounded text-sm text-content-strong focus:outline-none focus:border-brand-500'

export default function PipelinesTab({ workspace, name, envNames = [] }) {
  const qc = useQueryClient()
  const { data: pipelines = [], isLoading } = useQuery({
    queryKey: ['pipelines', workspace, name],
    queryFn: () => fetchPipelines(workspace, name),
  })
  const [editing, setEditing] = useState(null) // draft pipeline (with optional id) or null
  const [running, setRunning] = useState(null) // pipeline being run (modal)

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
        draft={editing} envNames={envNames}
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
        <button
          onClick={() => setEditing({ name: '', enabled: true, stages: [blankStage(envNames[0])] })}
          className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 text-white transition-colors"
        >
          + Add pipeline
        </button>
      </div>

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
              key={p.id} workspace={workspace} name={name} pipeline={p}
              onRun={() => setRunning(p)}
              onEdit={() => setEditing({ ...p })}
              onDelete={() => { if (confirm(`Delete pipeline "${p.name}"?`)) delMut.mutate(p.id) }}
            />
          ))}
        </div>
      )}

      {running && (
        <RunConsole
          workspace={workspace} name={name} pipeline={running}
          onClose={() => { setRunning(null); qc.invalidateQueries({ queryKey: ['pipeline-runs', workspace, name, running.id] }) }}
        />
      )}
    </section>
  )
}

function stageSummary(s) {
  if (s.type === 'test') return `test ${s.service}`
  return `${s.type} ${s.env}`
}

function PipelineCard({ workspace, name, pipeline, onRun, onEdit, onDelete }) {
  const [showHistory, setShowHistory] = useState(false)
  const [showHooks, setShowHooks] = useState(false)
  return (
    <div className="bg-surface border border-border rounded-xl p-4">
      <div className="flex items-start gap-3">
        <div className="min-w-0 flex-1">
          <div className="flex items-center gap-2">
            <span className="font-semibold text-content-strong truncate">{pipeline.name}</span>
            {!pipeline.enabled && <span className="text-[10px] uppercase tracking-wide px-1.5 py-0.5 rounded bg-surface-raised text-content-faint">disabled</span>}
          </div>
          <div className="flex flex-wrap items-center gap-1.5 mt-2">
            {pipeline.stages.map((s, i) => (
              <span key={i} className="inline-flex items-center gap-1 text-[11px] font-mono px-1.5 py-0.5 rounded bg-surface-raised text-content-muted border border-border-strong">
                {STAGE_ICON[s.type]} {stageSummary(s)}
              </span>
            ))}
          </div>
        </div>
        <div className="flex items-center gap-2 shrink-0">
          <button onClick={onRun} className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 text-white transition-colors">▶ Run</button>
          <button onClick={onEdit} className="text-xs px-2.5 py-1.5 rounded-lg bg-surface-raised hover:bg-surface-overlay text-content transition-colors">Edit</button>
          <button onClick={onDelete} className="text-xs px-2.5 py-1.5 rounded-lg text-danger-fg hover:bg-danger-subtle/40 transition-colors">Delete</button>
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
      {showHistory && <RunHistory workspace={workspace} name={name} pipelineId={pipeline.id} />}
      {showHooks && <Webhooks workspace={workspace} name={name} pipelineId={pipeline.id} />}
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
          <code className="block font-mono break-all text-content-strong select-all">{newToken}</code>
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

function statusChipCls(status) {
  if (status === 'ok') return 'bg-success-subtle text-success-fg border-success-border/60'
  if (status === 'fail') return 'bg-danger-subtle text-danger-fg border-danger-border/60'
  if (status === 'skipped') return 'bg-surface-raised text-content-faint border-border-strong'
  return 'bg-warning-subtle text-warning-fg border-warning-border/60' // running
}

function RunHistory({ workspace, name, pipelineId }) {
  const { data: runs = [], isLoading } = useQuery({
    queryKey: ['pipeline-runs', workspace, name, pipelineId],
    queryFn: () => fetchPipelineRuns(workspace, name, pipelineId, 20),
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
          <div className="flex flex-wrap gap-1 ml-auto">
            {r.stages.map((s, i) => (
              <span key={i} title={`${s.label} — ${s.status}`} className={`px-1 rounded border text-[10px] ${statusChipCls(s.status)}`}>
                {STAGE_ICON[s.type] || '•'}
              </span>
            ))}
          </div>
        </div>
      ))}
    </div>
  )
}

// ── Editor ────────────────────────────────────────────────────────────────────

function PipelineEditor({ draft, envNames, onChange, onCancel, onSave, saving, error }) {
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
              <StageRow key={i} idx={i} count={draft.stages.length} stage={s} envNames={envNames}
                onChange={patch => setStage(i, patch)} onRemove={() => removeStage(i)} onMove={dir => move(i, dir)} />
            ))}
          </div>
        </div>
      </div>
    </section>
  )
}

function StageRow({ idx, count, stage, envNames, onChange, onRemove, onMove }) {
  return (
    <div className="border border-border-strong rounded-lg p-3 bg-surface-raised/40">
      <div className="flex items-center gap-2 flex-wrap">
        <span className="text-content-faint text-xs font-mono w-5 text-center">{idx + 1}</span>
        <select value={stage.type} onChange={e => onChange({ type: e.target.value })} className={inputCls}>
          {STAGE_TYPES.map(t => <option key={t.value} value={t.value}>{t.label}</option>)}
        </select>
        <select value={stage.env} onChange={e => onChange({ env: e.target.value })} className={inputCls}>
          <option value="">— env —</option>
          {envNames.map(e => <option key={e} value={e}>{e}</option>)}
        </select>
        {stage.type === 'backup' && (
          <input value={stage.service} onChange={e => onChange({ service: e.target.value })} placeholder="service (optional)" className={`${inputCls} w-36`} />
        )}
        <select value={stage.on_failure} onChange={e => onChange({ on_failure: e.target.value })} className={inputCls} title="On failure">
          <option value="stop">on fail: stop</option>
          <option value="continue">on fail: continue</option>
        </select>
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

function RunConsole({ workspace, name, pipeline, onClose }) {
  const [text, setText] = useState('')
  const [status, setStatus] = useState('running')
  const boxRef = useRef(null)

  useEffect(() => {
    const ws = openPipelineSocket(workspace, name, pipeline.id)
    ws.addEventListener('message', e => setText(prev => prev + (e.data || '')))
    ws.addEventListener('close', () => setStatus(s => (s === 'running' ? 'done' : s)))
    ws.addEventListener('error', () => setStatus('error'))
    return () => ws.close()
  }, [workspace, name, pipeline.id])

  useEffect(() => { if (boxRef.current) boxRef.current.scrollTop = boxRef.current.scrollHeight }, [text])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border-strong rounded-xl w-full max-w-3xl flex flex-col max-h-[80vh]" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-3 border-b border-border">
          <div className="flex items-center gap-2">
            <span className="font-semibold text-content-strong">▶ {pipeline.name}</span>
            <span className={`text-[11px] px-1.5 py-0.5 rounded border ${status === 'error' ? statusChipCls('fail') : status === 'done' ? 'bg-surface-raised text-content-muted border-border-strong' : statusChipCls('running')}`}>
              {status === 'running' ? 'running…' : status}
            </span>
          </div>
          <button onClick={onClose} className="text-content-faint hover:text-content text-lg leading-none">✕</button>
        </div>
        <div ref={boxRef} className="flex-1 overflow-y-auto px-5 py-4 font-mono text-xs leading-relaxed whitespace-pre-wrap bg-[#0c1322] text-content rounded-b-xl">
          {text ? renderAnsi(text) : <span className="text-content-subtle">Connecting…</span>}
        </div>
      </div>
    </div>
  )
}
