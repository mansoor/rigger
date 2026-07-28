import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import {
  fetchHostComponents, fetchWorkspaceHostComponents,
  installNixpacks, installWorkspaceHostNixpacks,
  installHostEdge, installWorkspaceHostEdge,
  createHostWorkspacesDir, createWorkspaceHostWorkspacesDir,
} from '../lib/api'
import { Btn } from './ui'

// SwarmNodes lists the cluster a manager host fronts. Registering a manager tells
// you a Swarm exists; this tells you how big it is and which nodes can actually
// take work — a node that is Down or set to Drain won't be scheduled onto, which
// is the usual reason a service "won't move" or a replica never starts.
function SwarmNodes({ nodes }) {
  const schedulable = nodes.filter(n => n.status === 'Ready' && n.availability === 'Active').length
  return (
    <div className="mt-3">
      <p className="text-xs font-semibold text-content-muted uppercase tracking-wider mb-2">
        Swarm cluster
        <span className="ml-2 font-normal normal-case text-content-faint">
          {nodes.length} node{nodes.length === 1 ? '' : 's'} · {schedulable} schedulable
        </span>
      </p>
      <div className="bg-canvas/50 border border-border rounded-lg divide-y divide-border/40">
        {nodes.map(n => {
          const ready = n.status === 'Ready'
          const active = n.availability === 'Active'
          return (
            <div key={n.hostname + n.version} className="flex items-center justify-between gap-3 px-3 py-2">
              <div className="min-w-0 flex items-center gap-2">
                <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${ready && active ? 'bg-success' : ready ? 'bg-warning' : 'bg-danger'}`} />
                <span className="text-sm text-content truncate">{n.hostname}</span>
                {n.self && <span className="text-[10px] px-1.5 py-0.5 rounded bg-surface-raised text-content-subtle shrink-0">this host</span>}
                {n.leader && <span className="text-[10px] px-1.5 py-0.5 rounded bg-accent-subtle text-accent-text shrink-0">leader</span>}
              </div>
              <div className="flex items-center gap-2 shrink-0 text-xs">
                <span className="text-content-subtle">{n.role}</span>
                <span className={ready ? 'text-content-subtle' : 'text-danger-fg'}>{n.status}</span>
                {!active && <span className="text-warning-fg">{n.availability}</span>}
                {n.version && <span className="text-content-faint">v{n.version}</span>}
              </div>
            </div>
          )
        })}
      </div>
      <p className="mt-1.5 text-[11px] text-content-faint">
        Swarm schedules across every Active node. Services holding data need a placement
        constraint pinning them to the node their volume lives on.
      </p>
    </div>
  )
}

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
  // Only a manager can list the cluster, so this is empty for workers and for a
  // host that isn't in a Swarm at all.
  const nodes = c.swarm_nodes || []

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
          value={c.docker_reachable
            ? `v${c.docker_version}${c.swarm_manager ? ` · swarm manager${nodes.length ? ` of ${nodes.length} node${nodes.length === 1 ? '' : 's'}` : ''}` : ''}`
            : 'not reachable'} />
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
      {nodes.length > 0 && <SwarmNodes nodes={nodes} />}
      <p className="mt-1.5 text-[11px] text-content-faint">Provision here; version <em>updates</em> live on the Updates tab.</p>
    </div>
  )
}
