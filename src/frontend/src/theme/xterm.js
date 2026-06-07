// Bridges the theme/typography CSS variables into xterm.js terminals, which
// can't use Tailwind classes. Read at terminal creation AND re-applied live
// when the user switches theme or changes log font prefs (see useXtermTheme).

function rgb(varName, fallback) {
  if (typeof window === 'undefined') return fallback
  const v = getComputedStyle(document.documentElement).getPropertyValue(varName).trim()
  if (!v) return fallback
  // Stored as space-separated channels, e.g. "3 7 18".
  return `rgb(${v.split(/\s+/).join(', ')})`
}

// xterm theme object derived from the active color scheme.
export function xtermTheme() {
  return {
    background: rgb('--canvas', '#030712'),
    foreground: rgb('--content', '#f3f4f6'),
    cursor: rgb('--accent-text', '#22d3ee'),
    selectionBackground: rgb('--surface-overlay', '#374151'),
  }
}

// Font config derived from the user's typography prefs.
export function xtermFont(prefs) {
  const mono = prefs?.fontMono && prefs.fontMono !== 'system'
    ? `'${prefs.fontMono}', 'JetBrains Mono', monospace`
    : "ui-monospace, SFMono-Regular, Menlo, monospace"
  return {
    fontFamily: mono,
    fontSize: prefs?.logFontSize || 13,
    lineHeight: prefs?.logLineHeight || 1.5,
  }
}

// Initial xterm options block (spread into `new Terminal({...})`).
export function xtermOptions(prefs) {
  return { theme: xtermTheme(), ...xtermFont(prefs) }
}

// Apply current theme + font prefs to a live terminal, then refit.
export function applyXterm(term, fit, prefs) {
  if (!term) return
  term.options.theme = xtermTheme()
  const f = xtermFont(prefs)
  term.options.fontFamily = f.fontFamily
  term.options.fontSize = f.fontSize
  term.options.lineHeight = f.lineHeight
  try { fit?.fit() } catch { /* not yet mounted */ }
}
