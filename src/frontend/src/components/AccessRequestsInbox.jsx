import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchPendingAccessRequests, approveAccessRequest, rejectAccessRequest } from '../lib/api'
import { Btn } from './ui'

// AccessRequestsInbox lists pending access requests the caller can act on. The
// backend already scopes the list (super-admin: all; workspace admin: their
// workspaces). Pass wsKey to narrow the view to a single workspace.
export default function AccessRequestsInbox({ wsKey }) {
  const qc = useQueryClient()
  const { data: all = [], isLoading } = useQuery({
    queryKey: ['pending-access-requests'],
    queryFn: fetchPendingAccessRequests,
    refetchInterval: 30_000,
  })
  const reqs = wsKey ? all.filter(r => r.ws_key === wsKey) : all

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['pending-access-requests'] })
    qc.invalidateQueries({ queryKey: ['ws-members'] })
  }
  const approveMut = useMutation({ mutationFn: approveAccessRequest, onSuccess: invalidate })
  const rejectMut  = useMutation({ mutationFn: rejectAccessRequest,  onSuccess: invalidate })
  const busy = (id) => (approveMut.isPending && approveMut.variables === id) || (rejectMut.isPending && rejectMut.variables === id)

  if (isLoading) return <div className="py-8 text-center text-content-subtle text-sm">Loading…</div>
  if (reqs.length === 0) return <p className="text-sm text-content-subtle py-6 text-center">No pending access requests.</p>

  return (
    <div className="space-y-2">
      {reqs.map(r => (
        <div key={r.id} className="flex items-center gap-4 p-4 bg-surface border border-border rounded-xl">
          <div className="flex-1 min-w-0">
            <p className="text-sm font-semibold text-content-strong truncate">{r.email || r.username}</p>
            <p className="text-xs text-content-subtle mt-0.5">
              wants <span className="text-content font-medium">{r.role}</span> on{' '}
              <span className="font-mono text-content">{r.ws_key}{r.proj_key ? ` / ${r.proj_key}` : ''}</span>
              {r.proj_key ? ' (project)' : ' (whole workspace)'}
            </p>
            {r.message && <p className="text-xs text-content-muted mt-1 italic">“{r.message}”</p>}
          </div>
          <div className="flex items-center gap-2 shrink-0">
            <Btn variant="ghost" size="xs" onClick={() => approveMut.mutate(r.id)} disabled={busy(r.id)}
              >Approve</Btn>
            <Btn variant="secondary" size="xs" onClick={() => rejectMut.mutate(r.id)} disabled={busy(r.id)}
              >Reject</Btn>
          </div>
        </div>
      ))}
    </div>
  )
}
