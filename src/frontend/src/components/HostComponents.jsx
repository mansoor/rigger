import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  fetchHostComponents, fetchWorkspaceHostComponents,
  installNixpacks, installWorkspaceHostNixpacks,
  installHostEdge, installWorkspaceHostEdge,
  createHostWorkspacesDir, createWorkspaceHostWorkspacesDir,
} from '../lib/api'
import { Btn } from './ui'

// HostComponents is the single place to see and provision what a host needs to run
// workloads: Docker (info), Nixpacks (build backend), the Traefik edge (routing), and
// the remote workspaces directory. Scope-aware — pass `workspace` for a workspace-pool
// host (uses the pooled endpoints) or omit it for the admin (global) view. Install
// actions appear only for components that still need provisioning.
export default function HostComponents({ hostId, workspace = null }) {
  const ws = workspace
  const { data, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: ['host-components', ws || 'admin', hostId],
    queryFn: () => ws ? fetchWorkspaceHostComponents(ws, hostId) : fetchHostComponents(hostId),
    refetchOnWindowFocus: false,
  })
  const [busy, setBusy] = useState('')
  const [err, setErr] = useState('')

  async function run(key, fn) {
    setBusy(key); setErr('')
    try {
      const res = await fn()
      if (res?.status === 'error') setErr(res.error || 'Action failed')
      await refetch()
    } catch (e) {
      setErr(e?.response?.data?.error || 'Action failed')
    } finally { setBusy('') }
  }

  if (isLoading) return <p className="text-sm text-content-subtle">Probing components…</p>
  const c = data || {}
  const unreachable = !!c.connect_error || (!!error && !data)

  const Row = ({ label, ok, warn, value, onAct, actLabel, actKey }) => (
    <div className="flex items-center justify-between gap-3 py-2 border-b border-border/40 last:border-0">
      <div className="min-w-0">
        <span className="text-sm text-content">{label}</span>
        <span className={`ml-2 text-xs ${ok ? 'text-success-fg' : warn ? 'text-warning-fg' : 'text-content-subtle'}`}>{value}</span>
      </div>
      {onAct && !unreachable && (
        <Btn variant="primary" size="xs" onClick={onAct} disabled={!!busy} className="shrink-0">
          {busy === actKey ? 'Working…' : actLabel}
        </Btn>
      )}
    </div>
  )

  return (
    <div>
      <div className="flex items-center justify-between mb-2">
        <p className="text-xs font-semibold text-content-muted uppercase tracking-wider">Components</p>
        <button onClick={() => refetch()} disabled={isFetching} className="text-xs text-content-subtle hover:text-content-strong disabled:opacity-50">↻ Refresh</button>
      </div>
      {unreachable && <div className="mb-2 text-xs text-danger-fg">{c.connect_error || 'Host unreachable over SSH.'}</div>}
      {err && <div className="mb-2 text-xs text-danger-fg">{err}</div>}
      <div className="bg-canvas/50 border border-border rounded-lg px-3">
        <Row label="Docker" ok={c.docker_reachable} warn={!c.docker_reachable}
          value={c.docker_reachable ? `v${c.docker_version}${c.swarm_manager ? ' · swarm manager' : ''}` : 'not reachable'} />
        <Row label="Nixpacks" ok={!!c.nixpacks_version} warn={!c.nixpacks_version}
          value={c.nixpacks_version ? `v${c.nixpacks_version}` : 'not installed'}
          onAct={!c.nixpacks_version ? () => run('nixpacks', () => ws ? installWorkspaceHostNixpacks(ws, hostId) : installNixpacks(hostId)) : null}
          actLabel="Install" actKey="nixpacks" />
        <Row label="Traefik edge" ok={c.edge_running} warn={!c.edge_running}
          value={c.edge_running ? 'running' : 'not installed'}
          onAct={!c.edge_running ? () => run('edge', () => ws ? installWorkspaceHostEdge(ws, hostId) : installHostEdge(hostId)) : null}
          actLabel="Install" actKey="edge" />
        <Row label="Workspaces dir" ok={c.workspaces_dir_exists} warn={!c.workspaces_dir_exists}
          value={c.workspaces_dir_exists ? (c.workspaces_dir || 'ready') : (c.workspaces_dir ? `missing (${c.workspaces_dir})` : 'missing')}
          onAct={!c.workspaces_dir_exists ? () => run('dir', () => ws ? createWorkspaceHostWorkspacesDir(ws, hostId) : createHostWorkspacesDir(hostId)) : null}
          actLabel="Create" actKey="dir" />
      </div>
      <p className="mt-1.5 text-[11px] text-content-faint">Provision here; version <em>updates</em> live on the Updates tab.</p>
    </div>
  )
}
