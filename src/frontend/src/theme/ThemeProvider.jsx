import { createContext, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { useAuthStore } from '../store/auth'
import { useWorkspaceStore } from '../store/workspace'
import { fetchAppearancePrefs, saveAppearancePrefs } from '../lib/api'
import { applyPrefs, normalizePrefs, resolveTheme, DEFAULT_PREFS } from './themes'

const STORAGE_KEY = 'rigger.prefs'

const ThemeContext = createContext(null)

// useTheme() → { prefs, resolvedTheme, setPrefs, resetPrefs }
export function useTheme() {
  const ctx = useContext(ThemeContext)
  if (!ctx) throw new Error('useTheme must be used within ThemeProvider')
  return ctx
}

function readLocal() {
  try { return JSON.parse(localStorage.getItem(STORAGE_KEY) || '{}') }
  catch { return {} }
}

export function ThemeProvider({ children }) {
  const [prefs, setPrefsState] = useState(() => normalizePrefs(readLocal()))
  const [resolvedTheme, setResolvedTheme] = useState(() => resolveTheme(prefs.theme))

  // Timestamp of the last local edit — guards against a server re-fetch clobbering
  // a just-made change before its PUT lands.
  const lastEdit = useRef(0)

  // Apply to <html> + persist locally whenever prefs change.
  useEffect(() => {
    applyPrefs(prefs)
    setResolvedTheme(resolveTheme(prefs.theme))
    try { localStorage.setItem(STORAGE_KEY, JSON.stringify(prefs)) } catch { /* ignore */ }
  }, [prefs])

  // Follow OS appearance live while on 'system'.
  useEffect(() => {
    if (prefs.theme !== 'system') return
    const mq = window.matchMedia('(prefers-color-scheme: light)')
    const handler = () => { applyPrefs(prefs); setResolvedTheme(resolveTheme('system')) }
    mq.addEventListener('change', handler)
    return () => mq.removeEventListener('change', handler)
  }, [prefs])

  // Resolve the EFFECTIVE prefs (user override ?? workspace default ?? global)
  // after auth and whenever the selected workspace changes — so an inheriting
  // user picks up the workspace's default, while a user with their own override
  // always gets their own back. Skipped briefly after a local edit to avoid a
  // race with its save.
  const token = useAuthStore((s) => s.token)
  const currentWs = useWorkspaceStore((s) => s.current)
  useEffect(() => {
    if (!token) return
    let cancelled = false
    fetchAppearancePrefs(currentWs)
      .then((server) => {
        if (cancelled || !server) return
        if (Date.now() - lastEdit.current < 3000) return // don't clobber a fresh edit
        setPrefsState(normalizePrefs(server))
      })
      .catch(() => { /* offline / none set — keep local */ })
    return () => { cancelled = true }
  }, [token, currentWs])

  const value = useMemo(() => {
    function setPrefs(patch) {
      setPrefsState((prev) => {
        const next = normalizePrefs({ ...prev, ...patch })
        // Per-user save (cross-device). Mark the edit so the resolver effect
        // doesn't immediately overwrite it with a stale server read.
        lastEdit.current = Date.now()
        if (useAuthStore.getState().token) saveAppearancePrefs(next).catch(() => {})
        return next
      })
    }
    return {
      prefs,
      resolvedTheme,
      setPrefs,
      resetPrefs: () => setPrefs(DEFAULT_PREFS),
    }
  }, [prefs, resolvedTheme])

  return <ThemeContext.Provider value={value}>{children}</ThemeContext.Provider>
}
