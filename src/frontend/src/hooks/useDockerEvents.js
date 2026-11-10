import { useEffect } from 'react'
import { useQueryClient } from '@tanstack/react-query'
import { useAuthStore } from '../store/auth'
import { fetchEventsTicket } from '../lib/api'

// Reconnect delay after the stream drops: doubles from 2s up to 30s, and starts
// over only after a connection that stayed up HEALTHY_FOR — so a stream that
// opens and dies at once (Docker unreachable, say) still backs off.
const RETRY_MIN   = 2000
const RETRY_MAX   = 30000
const HEALTHY_FOR = 30000

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
 * EventSource can't send an Authorization header, so each connection opens with
 * a single-use ticket fetched over the normal API; the access token never goes
 * in the URL, where proxies log it. Because a ticket works once, the browser's
 * own reconnect (which reuses the URL) can't succeed: on any error we close the
 * stream and open a new one with a fresh ticket.
 *
 * Should be mounted once at the app root level (e.g. inside Layout).
 */
export function useDockerEvents() {
  const qc       = useQueryClient()
  const signedIn = useAuthStore(s => !!s.token)

  useEffect(() => {
    if (!signedIn) return

    let es      = null
    let timer   = null
    let stopped = false
    let delay   = RETRY_MIN

    const retry = () => {
      if (stopped) return
      timer = setTimeout(connect, delay)
      delay = Math.min(delay * 2, RETRY_MAX)
    }

    async function connect() {
      let ticket
      try {
        ticket = await fetchEventsTicket()
      } catch {
        // A 401 signs the user out (lib/api.js), which unmounts this; anything
        // else — the server restarting, a network blip — is worth a retry.
        retry()
        return
      }
      if (stopped) return

      const source = new EventSource(`/api/events?ticket=${encodeURIComponent(ticket)}`)
      es = source
      let openedAt = 0
      source.onopen = () => { openedAt = Date.now() }

      source.addEventListener('container', () => {
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
      source.addEventListener('alert', (e) => {
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

      source.onerror = () => {
        source.close()
        if (es === source) es = null
        if (openedAt && Date.now() - openedAt >= HEALTHY_FOR) delay = RETRY_MIN
        retry()
      }
    }

    connect()
    return () => {
      stopped = true
      clearTimeout(timer)
      if (es) es.close()
    }
  }, [signedIn, qc])
}
