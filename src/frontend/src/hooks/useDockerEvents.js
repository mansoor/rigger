import { useEffect } from 'react'
import { useQueryClient, useQuery } from '@tanstack/react-query'
import { useAuthStore } from '../store/auth'
import { fetchWorkspaces } from '../lib/api'

/**
 * Opens a single SSE connection to /api/events and invalidates React Query
 * status caches whenever a Docker container event arrives.
 *
 * Matching logic: Docker Compose sets com.docker.compose.project to the
 * compose project name, which compose-gen.sh sets to "{workspace}_{env}".
 * We look that up in the known workspace list to target the right query key.
 *
 * Should be mounted once at the app root level (e.g. inside Layout).
 */
export function useDockerEvents() {
  const qc     = useQueryClient()
  const token  = useAuthStore(s => s.token)

  // We need the workspace list to map project names → (workspace, env) pairs
  const { data: workspaces } = useQuery({
    queryKey: ['workspaces'],
    queryFn: fetchWorkspaces,
    staleTime: 60_000,
  })

  useEffect(() => {
    if (!token) return

    // Build a lookup map for both project name formats:
    //   "{workspace}_{env}" — explicit -p flag (deploy.sh after fix)
    //   "{env}"             — directory-derived name (older deployments)
    // We store all known (workspace, env) combos and match on either format.
    const projectMap = {}
    for (const ws of (workspaces || [])) {
      for (const env of (ws.envs || [])) {
        projectMap[`${ws.name}_${env}`] = { name: ws.name, env }  // new explicit name
        // For env-only keys, prefer the first workspace match to avoid collisions
        if (!projectMap[env]) {
          projectMap[env] = { name: ws.name, env }
        }
      }
    }

    const url = `/api/events?token=${encodeURIComponent(token)}`
    const es = new EventSource(url)

    es.addEventListener('container', (e) => {
      try {
        const { project } = JSON.parse(e.data)

        const match = projectMap[project]
        if (match) {
          // Invalidate the specific env's status AND container list.
          // Both are polled on a slow fallback interval — SSE gives us the
          // real-time invalidation so the UI reacts within seconds.
          qc.invalidateQueries({ queryKey: ['envstatus',  match.name, match.env] })
          qc.invalidateQueries({ queryKey: ['containers', match.name, match.env] })
        }
        // Dashboard live-stats table reacts to any container lifecycle change.
        qc.invalidateQueries({ queryKey: ['liveStats'] })
        // Container not from a known workspace (e.g. rigger itself) — ignore
      } catch {
        // Malformed event — ignore
      }
    })

    // Phase 6: alert events pushed by the rule evaluator (fired/resolved/dismissed).
    // The payload carries the authoritative unread_count so we update the bell
    // badge instantly, and invalidate the inbox list so an open panel refreshes.
    es.addEventListener('alert', (e) => {
      try {
        const { unread_count } = JSON.parse(e.data)
        if (typeof unread_count === 'number') {
          qc.setQueryData(['alertUnread'], { unread_count })
        }
        qc.invalidateQueries({ queryKey: ['alertEvents'] })
        qc.invalidateQueries({ queryKey: ['alertSummary'] }) // dashboard 6e
      } catch {
        // Malformed event — fall back to a refetch
        qc.invalidateQueries({ queryKey: ['alertUnread'] })
        qc.invalidateQueries({ queryKey: ['alertEvents'] })
        qc.invalidateQueries({ queryKey: ['alertSummary'] })
      }
    })

    es.onerror = () => {
      // EventSource auto-reconnects — no manual handling needed
    }

    return () => es.close()
  }, [token, workspaces, qc])
}
