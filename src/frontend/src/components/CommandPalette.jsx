import { useState, useMemo, useEffect, useRef } from 'react'
import { useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import { useWorkspaceStore } from '../store/workspace'
import { fetchProjects } from '../lib/api'
import { buildEntries, rank } from '../lib/commandPalette'

// CommandPalette — a Ctrl/Cmd-K quick-jump over the current workspace's projects
// and Rigger's navigation surface. Frontend-only: it reads the already-cached
// projects list (RBAC-filtered server-side) and a static, role-gated registry, so
// there's no new endpoint and nothing the user can't see is ever indexed. Secrets
// and config values are intentionally NOT indexed — names and destinations only.
export default function CommandPalette({ open, onClose, isAdmin, canAdminWs }) {
  const navigate = useNavigate()
  const current = useWorkspaceStore(s => s.current)
  const inputRef = useRef(null)
  const listRef = useRef(null)
  const [query, setQuery] = useState('')
  const [active, setActive] = useState(0)

  // The palette rides the same cache key the sidebar uses, so opening it is free
  // when a workspace is already loaded (no refetch, no spinner).
  const { data: projects } = useQuery({
    queryKey: ['projects', current],
    queryFn: () => fetchProjects(current),
    enabled: !!current && open,
    staleTime: 30_000,
  })

  const entries = useMemo(
    () => buildEntries({ projects: projects || [], currentWs: current, isAdmin, canAdminWs }),
    [projects, current, isAdmin, canAdminWs],
  )
  const results = useMemo(() => rank(entries, query), [entries, query])

  // Reset each time it opens; keep the active row valid as results shrink.
  useEffect(() => { if (open) { setQuery(''); setActive(0) } }, [open])
  useEffect(() => { setActive(0) }, [query])
  useEffect(() => { if (open) inputRef.current?.focus() }, [open])

  // Keep the highlighted row scrolled into view as you arrow through.
  useEffect(() => {
    if (!open) return
    listRef.current?.querySelector('[data-active="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [active, open])

  if (!open) return null

  function choose(entry) {
    if (!entry) return
    onClose()
    navigate(entry.url)
  }

  function onKeyDown(e) {
    if (e.key === 'ArrowDown') { e.preventDefault(); setActive(a => Math.min(a + 1, results.length - 1)) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); setActive(a => Math.max(a - 1, 0)) }
    else if (e.key === 'Enter') { e.preventDefault(); choose(results[active]) }
    else if (e.key === 'Escape') { e.preventDefault(); onClose() }
  }

  let lastCat = null

  return (
    <div className="fixed inset-0 z-[80] flex items-start justify-center bg-black/50 backdrop-blur-sm pt-[12vh] px-4" onClick={onClose}>
      <div
        className="w-full max-w-xl bg-surface border border-border-strong rounded-xl shadow-2xl overflow-hidden"
        onClick={e => e.stopPropagation()}
      >
        <div className="flex items-center gap-2 px-4 border-b border-border">
          <svg className="w-4 h-4 text-content-subtle shrink-0" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
            <path strokeLinecap="round" strokeLinejoin="round" d="m21 21-4.3-4.3M11 19a8 8 0 1 0 0-16 8 8 0 0 0 0 16Z" />
          </svg>
          <input
            ref={inputRef}
            value={query}
            onChange={e => setQuery(e.target.value)}
            onKeyDown={onKeyDown}
            placeholder="Search projects, settings, pages…"
            className="flex-1 bg-transparent py-3 text-sm text-content-strong placeholder:text-content-faint focus:outline-none"
          />
          <kbd className="text-[10px] text-content-faint border border-border-strong rounded px-1.5 py-0.5 shrink-0">Esc</kbd>
        </div>

        <div ref={listRef} className="max-h-[52vh] overflow-y-auto py-1">
          {results.length === 0 ? (
            <p className="px-4 py-8 text-center text-sm text-content-subtle">No matches in this workspace.</p>
          ) : (
            results.map((r, i) => {
              const header = r.category !== lastCat ? r.category : null
              lastCat = r.category
              const isActive = i === active
              return (
                <div key={r.id}>
                  {header && (
                    <p className="px-4 pt-2 pb-1 text-[10px] font-semibold uppercase tracking-wider text-content-faint">{header}</p>
                  )}
                  <button
                    data-active={isActive}
                    onMouseMove={() => setActive(i)}
                    onClick={() => choose(r)}
                    className={`w-full flex items-center gap-3 px-4 py-2 text-left transition-colors ${
                      isActive ? 'bg-surface-raised' : 'hover:bg-surface-raised/60'
                    }`}
                  >
                    <span className="w-5 text-center text-content-subtle shrink-0">{r.icon}</span>
                    <span className="min-w-0 flex-1">
                      <span className="block text-sm text-content-strong truncate">{r.title}</span>
                      {r.subtitle && <span className="block text-xs text-content-subtle truncate">{r.subtitle}</span>}
                    </span>
                    {isActive && <span className="text-[10px] text-content-faint shrink-0">↵</span>}
                  </button>
                </div>
              )
            })
          )}
        </div>
      </div>
    </div>
  )
}
