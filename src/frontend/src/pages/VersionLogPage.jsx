import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { fetchWorkspaces, fetchActivity } from '../lib/api'
import Layout from '../components/Layout'

function VersionBadge({ version }) {
  if (!version) return <span className="text-content-faint text-xs">—</span>
  const v = version
  return (
    <span className="font-mono text-xs bg-surface-raised text-content px-2 py-0.5 rounded">
      v{v.major}.{v.minor}.{v.patch}-build.{v.build}
    </span>
  )
}

function WorkspaceVersionCard({ ws }) {
  const cfg = ws.config
  const version = cfg?.project?.version
  const type = cfg?.project?.type || 'custom'

  const { data: activity } = useQuery({
    queryKey: ['activity', ws.name],
    queryFn: () => fetchActivity(ws.name),
  })

  const versionEvents = (activity || []).filter(a =>
    a.command === 'version' || a.command === 'build' || a.command === 'promote'
  )

  return (
    <div className="bg-surface border border-border rounded-xl overflow-hidden">
      {/* Header */}
      <div className="px-5 py-4 border-b border-border flex items-center justify-between">
        <div className="flex items-center gap-3">
          <span className="w-2 h-2 rounded-full bg-brand-500 shrink-0" />
          <Link to={`/workspaces/${ws.name}`} className="text-sm font-semibold text-content-strong hover:text-accent-text transition-colors">
            {ws.name}
          </Link>
          <span className={`text-xs px-2 py-0.5 rounded-full ${type === 'image' ? 'bg-info-subtle text-info-fg' : 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300'}`}>
            {type}
          </span>
        </div>
        <VersionBadge version={version} />
      </div>

      {/* Version events */}
      {type === 'image'
        ? <p className="px-5 py-4 text-xs text-content-subtle">Image stacks use upstream image tags — version tracking is not applicable.</p>
        : versionEvents.length === 0
          ? <p className="px-5 py-4 text-xs text-content-subtle">No version activity recorded yet.</p>
          : (
            <div className="divide-y divide-border">
              {versionEvents.map((e, i) => (
                <div key={i} className="px-5 py-3 flex items-center gap-3">
                  <span className={`w-1.5 h-1.5 rounded-full shrink-0 ${
                    e.command === 'build'   ? 'bg-brand-500' :
                    e.command === 'promote' ? 'bg-purple-500' : 'bg-surface-overlay'
                  }`} />
                  <span className="text-sm text-content capitalize">{e.command}</span>
                  {e.env && <span className="text-xs text-content-subtle">→ {e.env}</span>}
                  <span className="ml-auto text-xs text-content-faint">{e.created_at?.slice(0, 16) || ''}</span>
                </div>
              ))}
            </div>
          )
      }
    </div>
  )
}

export default function VersionLogPage() {
  const { data: workspaces, isLoading } = useQuery({
    queryKey: ['workspaces'],
    queryFn: fetchWorkspaces,
  })

  const customWorkspaces = (workspaces || []).filter(ws => ws.config?.project?.type !== 'image')
  const imageWorkspaces  = (workspaces || []).filter(ws => ws.config?.project?.type === 'image')

  return (
    <Layout>
      <div className="p-6 max-w-3xl">
        <div className="mb-6">
          <h1 className="text-xl font-bold text-content-strong">Version log</h1>
          <p className="text-sm text-content-muted mt-0.5">Current versions and build history per workspace</p>
        </div>

        {isLoading && <p className="text-content-subtle text-sm">Loading…</p>}

        {!isLoading && workspaces?.length === 0 && (
          <div className="bg-surface border border-border rounded-xl p-8 text-center">
            <p className="text-content-subtle text-sm">No workspaces yet.</p>
          </div>
        )}

        {customWorkspaces.length > 0 && (
          <section className="mb-6">
            <h2 className="text-xs font-semibold text-content-subtle uppercase tracking-wider mb-3">Custom application stacks</h2>
            <div className="space-y-4">
              {customWorkspaces.map(ws => <WorkspaceVersionCard key={ws.name} ws={ws} />)}
            </div>
          </section>
        )}

        {imageWorkspaces.length > 0 && (
          <section>
            <h2 className="text-xs font-semibold text-content-subtle uppercase tracking-wider mb-3">Image stacks</h2>
            <div className="space-y-4">
              {imageWorkspaces.map(ws => <WorkspaceVersionCard key={ws.name} ws={ws} />)}
            </div>
          </section>
        )}
      </div>
    </Layout>
  )
}
