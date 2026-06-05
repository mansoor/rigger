import { useState, useEffect } from 'react'
import { checkPorts } from '../lib/api'

// usePortConflicts — debounced host-aware port check (tiers C+D). `checks` is a
// list of { host_id, port, service }; it asks the server whether each host port
// is already in use on its target host (by a running container or another
// workspace's config) and returns human-readable warning strings.
export function usePortConflicts(checks, excludeWorkspace = '') {
  const [warnings, setWarnings] = useState([])
  const key = JSON.stringify(checks) // stable dependency for the effect

  useEffect(() => {
    const list = (checks || []).filter(c => Number(c.port) > 0)
    if (list.length === 0) { setWarnings([]); return }
    let cancelled = false
    const t = setTimeout(() => {
      checkPorts(list, excludeWorkspace)
        .then(conflicts => {
          if (cancelled) return
          setWarnings((conflicts || []).map(c =>
            `Host port ${c.port}${c.service ? ` (${c.service})` : ''} is already in use on the target host — ${c.used_by}.`))
        })
        .catch(() => { if (!cancelled) setWarnings([]) })
    }, 600)
    return () => { cancelled = true; clearTimeout(t) }
  }, [key, excludeWorkspace]) // eslint-disable-line react-hooks/exhaustive-deps

  return warnings
}
