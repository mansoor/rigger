import { useTheme } from '../theme/ThemeProvider'

// Quick "show/hide help text" switch in the top nav. Flips the per-user prefs.helpText,
// which gates every <Hint> (field hints + section descriptions) across the app. Persisted
// local + server like the theme. Finer appearance controls live in Settings → Appearance.
export default function HelpToggle() {
  const { prefs, setPrefs } = useTheme()
  const on = prefs.helpText !== false

  return (
    <button
      onClick={() => setPrefs({ helpText: !on })}
      title={on ? 'Hide help text' : 'Show help text'}
      aria-label={on ? 'Hide help text' : 'Show help text'}
      aria-pressed={on}
      className={`flex items-center justify-center w-9 h-8 rounded-lg transition-colors ${
        on ? 'text-brand-400 hover:bg-surface-raised' : 'text-content-muted hover:text-content-strong hover:bg-surface-raised'
      }`}
    >
      {/* Help / question-mark in a circle. A slash overlays it when help is hidden. */}
      <svg xmlns="http://www.w3.org/2000/svg" className="w-5 h-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={1.8}>
        <circle cx="12" cy="12" r="9" />
        <path strokeLinecap="round" strokeLinejoin="round" d="M9.5 9.5a2.5 2.5 0 1 1 3.5 2.3c-.7.3-1 .8-1 1.7" />
        <circle cx="12" cy="16.5" r="0.6" fill="currentColor" stroke="none" />
        {!on && <path strokeLinecap="round" d="M4 20 20 4" />}
      </svg>
    </button>
  )
}
