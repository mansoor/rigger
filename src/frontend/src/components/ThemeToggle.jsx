import { useTheme } from '../theme/ThemeProvider'

// Quick light/dark switch in the top nav. Sets an explicit theme (leaving
// "System"); finer control (System, fonts, log size) lives in Settings →
// Appearance.
export default function ThemeToggle() {
  const { resolvedTheme, setPrefs } = useTheme()
  const isDark = resolvedTheme === 'dark'
  const next = isDark ? 'light' : 'dark'

  return (
    <button
      onClick={() => setPrefs({ theme: next })}
      title={`Switch to ${next} theme`}
      aria-label={`Switch to ${next} theme`}
      className="flex items-center justify-center w-9 h-8 rounded-lg text-content-muted hover:text-content-strong hover:bg-surface-raised transition-colors"
    >
      {isDark ? (
        // Sun — click to go light
        <svg xmlns="http://www.w3.org/2000/svg" className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
          <circle cx="12" cy="12" r="4" />
          <path strokeLinecap="round" d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41" />
        </svg>
      ) : (
        // Moon — click to go dark
        <svg xmlns="http://www.w3.org/2000/svg" className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
          <path strokeLinecap="round" strokeLinejoin="round" d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z" />
        </svg>
      )}
    </button>
  )
}
