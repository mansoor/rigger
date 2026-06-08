import { useState } from 'react'
import { useParams, useNavigate } from 'react-router-dom'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import { fetchWorkspaces, fetchProjects, renameWorkspaceTier, deleteWorkspaceTier, transferWorkspace } from '../lib/api'
import { useWorkspaceStore } from '../store/workspace'

const NAME_RE = /^[A-Za-z0-9][A-Za-z0-9 _-]{0,31}$/

export default function ManageWorkspacePage() {
  const { workspace } = useParams()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const setCurrent = useWorkspaceStore(s => s.setCurrent)

  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const { data: projects = [] } = useQuery({
    queryKey: ['projects', workspace], queryFn: () => fetchProjects(workspace), enabled: !!workspace,
  })
  const ws = workspaces.find(w => w.key === workspace)
  const others = workspaces.filter(w => w.key !== workspace)

  return (
    <Layout>
      <div className="p-6 max-w-3xl space-y-8">
        <div>
          <p className="text-xs font-medium uppercase tracking-wider text-content-subtle">Manage workspace</p>
          <div className="flex items-center gap-2.5 mt-0.5">
            <h1 className="text-2xl font-bold text-content-strong">{ws?.name || workspace}</h1>
            <span className="text-xs font-mono text-content-faint px-1.5 py-0.5 rounded bg-surface-raised border border-border-strong" title="Workspace key (folder / URL / Docker prefix)">{workspace}</span>
          </div>
          <p className="text-sm text-content-muted mt-1">{projects.length} project{projects.length !== 1 ? 's' : ''}</p>
        </div>

        <GeneralSection workspace={workspace} ws={ws} qc={qc} setCurrent={setCurrent} />
        <DangerZone workspace={workspace} ws={ws} projects={projects} others={others} qc={qc} setCurrent={setCurrent} navigate={navigate} />
      </div>
    </Layout>
  )
}

function GeneralSection({ workspace, ws, qc, setCurrent }) {
  const [name, setName] = useState('')
  const [err, setErr] = useState('')
  // Seed once the workspace meta loads.
  const [seeded, setSeeded] = useState(false)
  if (!seeded && ws) { setName(ws.name || workspace); setSeeded(true) }

  const mut = useMutation({
    mutationFn: () => renameWorkspaceTier(workspace, name.trim()),
    onSuccess: () => { setErr(''); qc.invalidateQueries({ queryKey: ['workspaces'] }) },
    onError: (e) => setErr(e.response?.data?.error || 'Rename failed'),
  })
  const ok = NAME_RE.test(name.trim())
  const dirty = ws && name.trim() !== (ws.name || workspace)

  return (
    <section>
      <h2 className="text-sm font-semibold text-content mb-3">General</h2>
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Display name</label>
          <input
            value={name} onChange={e => setName(e.target.value)} maxLength={32}
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500"
          />
          <p className="text-xs text-content-subtle mt-1">1–32 chars, letters/digits/space/dash/underscore. The key <code className="font-mono">{workspace}</code> is fixed.</p>
        </div>
        {err && <p className="text-sm text-danger-fg">{err}</p>}
        <div className="flex items-center gap-3">
          <button
            onClick={() => mut.mutate()} disabled={!ok || !dirty || mut.isPending}
            className="bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white text-sm font-semibold px-4 py-2 rounded-lg transition-colors"
          >
            {mut.isPending ? 'Saving…' : 'Save'}
          </button>
          {mut.isSuccess && !dirty && <span className="text-xs text-success-fg">✓ Saved</span>}
        </div>
      </div>
    </section>
  )
}

function DangerZone({ workspace, ws, projects, others, qc, setCurrent, navigate }) {
  const [mode, setMode] = useState(null) // null | 'transfer' | 'delete'
  return (
    <section>
      <h2 className="text-sm font-semibold text-danger-fg mb-3">Danger zone</h2>
      <div className="bg-surface border border-danger-border/50 rounded-xl divide-y divide-border">
        <div className="p-5 flex items-center justify-between gap-4">
          <div>
            <p className="text-sm font-medium text-content-strong">Transfer projects to another workspace</p>
            <p className="text-xs text-content-subtle mt-0.5">Move all (or selected) projects to another workspace, then remove this one. Containers keep running (their resource prefix is unchanged).</p>
          </div>
          <button onClick={() => setMode('transfer')} disabled={projects.length === 0 || others.length === 0}
            className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg border border-border-strong text-content hover:bg-surface-raised disabled:opacity-40 transition-colors">
            Transfer…
          </button>
        </div>
        <div className="p-5 flex items-center justify-between gap-4">
          <div>
            <p className="text-sm font-medium text-content-strong">Delete this workspace</p>
            <p className="text-xs text-content-subtle mt-0.5">Stops and removes every project, environment, container, network and volume in this workspace, then deletes it. Cannot be undone.</p>
          </div>
          <button onClick={() => setMode('delete')}
            className="shrink-0 px-3 py-2 text-sm font-medium rounded-lg bg-red-800 hover:bg-red-700 text-white transition-colors">
            Delete…
          </button>
        </div>
      </div>

      {mode === 'transfer' && (
        <TransferModal workspace={workspace} projects={projects} others={others}
          onClose={() => setMode(null)}
          onDone={(target) => { qc.invalidateQueries({ queryKey: ['workspaces'] }); setCurrent(target); navigate('/') }} />
      )}
      {mode === 'delete' && (
        <DeleteModal workspace={workspace} ws={ws} projects={projects}
          onClose={() => setMode(null)}
          onDone={() => { qc.invalidateQueries({ queryKey: ['workspaces'] }); setCurrent(''); navigate('/') }} />
      )}
    </section>
  )
}

function TransferModal({ workspace, projects, others, onClose, onDone }) {
  const [target, setTarget] = useState(others[0]?.key || '')
  const [sel, setSel] = useState(null) // null = all
  const [err, setErr] = useState('')
  const chosen = sel ?? projects.map(p => p.name)
  const mut = useMutation({
    mutationFn: () => transferWorkspace(workspace, target, chosen.length === projects.length ? [] : chosen),
    onSuccess: () => onDone(target),
    onError: (e) => setErr(e.response?.data?.error || 'Transfer failed'),
  })
  function toggle(k) {
    const cur = sel ?? projects.map(p => p.name)
    setSel(cur.includes(k) ? cur.filter(x => x !== k) : [...cur, k])
  }
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-md p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <h3 className="font-semibold text-content-strong">Transfer projects</h3>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Target workspace</label>
          <select value={target} onChange={e => setTarget(e.target.value)}
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500">
            {others.map(o => <option key={o.key} value={o.key}>{o.name} ({o.key})</option>)}
          </select>
        </div>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Projects</label>
          <div className="max-h-48 overflow-y-auto space-y-1 border border-border-strong rounded-lg p-2">
            {projects.map(p => (
              <label key={p.name} className="flex items-center gap-2 text-sm text-content cursor-pointer">
                <input type="checkbox" checked={chosen.includes(p.name)} onChange={() => toggle(p.name)} className="accent-brand-500" />
                <span>{p.config?.project?.name || p.name}</span>
                <span className="text-[10px] font-mono text-content-faint ml-auto">{p.config?.project?.resource_prefix || p.name}</span>
              </label>
            ))}
          </div>
          <p className="text-xs text-content-subtle mt-1">Keys are kept; on a name clash in the target the moved project's key is suffixed. Resource prefixes (and running containers) are unchanged.</p>
        </div>
        {err && <p className="text-sm text-danger-fg">{err}</p>}
        <div className="flex gap-2 justify-end">
          <button onClick={onClose} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
          <button onClick={() => mut.mutate()} disabled={!target || chosen.length === 0 || mut.isPending}
            className="px-4 py-2 text-sm font-semibold rounded-lg bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white">
            {mut.isPending ? 'Transferring…' : `Transfer ${chosen.length}`}
          </button>
        </div>
      </div>
    </div>
  )
}

function DeleteModal({ workspace, ws, projects, onClose, onDone }) {
  const [confirm, setConfirm] = useState('')
  const [err, setErr] = useState('')
  const mut = useMutation({
    mutationFn: () => deleteWorkspaceTier(workspace),
    onSuccess: onDone,
    onError: (e) => setErr(e.response?.data?.error || 'Delete failed'),
  })
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-danger-border/60 rounded-2xl w-full max-w-md p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <h3 className="font-semibold text-content-strong">Delete workspace “{ws?.name || workspace}”?</h3>
        <p className="text-sm text-content-muted">
          This stops and removes <span className="text-content-strong font-medium">{projects.length} project{projects.length !== 1 ? 's' : ''}</span> and all their
          environments, containers, networks and volumes — then deletes the workspace. <span className="text-danger-fg font-medium">This cannot be undone.</span>
        </p>
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Type <code className="font-mono text-content">{workspace}</code> to confirm</label>
          <input value={confirm} onChange={e => setConfirm(e.target.value)}
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm font-mono focus:outline-none focus:border-danger" />
        </div>
        {err && <p className="text-sm text-danger-fg">{err}</p>}
        <div className="flex gap-2 justify-end">
          <button onClick={onClose} className="px-4 py-2 text-sm rounded-lg border border-border-strong text-content hover:bg-surface-raised">Cancel</button>
          <button onClick={() => mut.mutate()} disabled={confirm !== workspace || mut.isPending}
            className="px-4 py-2 text-sm font-semibold rounded-lg bg-red-800 hover:bg-red-700 disabled:opacity-40 text-white">
            {mut.isPending ? 'Deleting…' : 'Delete workspace'}
          </button>
        </div>
      </div>
    </div>
  )
}
