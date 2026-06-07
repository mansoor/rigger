import { createContext, useContext, useEffect, useMemo, useRef, useState } from 'react'
import { useAuthStore } from '../store/auth'
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

  // Whether the user already had local prefs at boot — decides if we adopt the
  // server copy on first authenticated load (don't clobber fresh local edits).
  const hadLocalAtBoot = useRef(
    typeof localStorage !== 'undefined' && localStorage.getItem(STORAGE_KEY) != null
  )

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

  // Pull server prefs once, after auth — only adopt them if the user had no
  // local prefs at boot (server is the cross-device default; local edits win).
  const token = useAuthStore((s) => s.token)
  const pulledFromServer = useRef(false)
  useEffect(() => {
    if (!token || pulledFromServer.current) return
    pulledFromServer.current = true
    let cancelled = false
    fetchAppearancePrefs()
      .then((server) => {
        if (cancelled || !server || hadLocalAtBoot.current) return
        setPrefsState(normalizePrefs(server))
      })
      .catch(() => { /* offline / no server prefs — keep local */ })
    return () => { cancelled = true }
  }, [token])

  const value = useMemo(() => {
    function setPrefs(patch) {
      setPrefsState((prev) => {
        const next = normalizePrefs({ ...prev, ...patch })
        // Best-effort push so the choice syncs across devices.
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
