import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '../store/auth'

/**
 * Opens a single SSE connection to /api/events and invalidates React Query
 * status caches whenever a Docker container event arrives.
 *
 * With the Workspace→Project tier the compose project name is
 * "{workspace}_{project}_{env}", which is ambiguous to parse back (names may
 * contain underscores). Since SSE is only a real-time nudge over the slow
 * polling fallback, we invalidate the status/container caches by key prefix —
 * React Query only refetches the queries that are currently mounted.
 *
 * Should be mounted once at the app root level (e.g. inside Layout).
 */
export function useDockerEvents() {
  const qc     = useQueryClient()
  const token  = useAuthStore(s => s.token)

  useEffect(() => {
    if (!token) return

    const url = `/api/events?token=${encodeURIComponent(token)}`
    const es = new EventSource(url)

    es.addEventListener('container', () => {
      // Invalidate every env-status and container-list query (any
      // workspace/project/env). Only mounted queries actually refetch, so the
      // visible UI reacts within seconds; everything else stays lazy.
      qc.invalidateQueries({ queryKey: ['envstatus'] })
      qc.invalidateQueries({ queryKey: ['containers'] })
      // Dashboard live-stats table reacts to any container lifecycle change.
      qc.invalidateQueries({ queryKey: ['liveStats'] })
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
  }, [token, qc])
}
