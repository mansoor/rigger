// Theme + appearance registry.
//
// Adding a new color scheme = add an entry to THEMES here AND a matching
// [data-theme="<id>"] block in src/styles/themes.css. Nothing else changes.

export const THEMES = [
  { id: 'system', label: 'System', hint: 'Match your OS appearance' },
  { id: 'dark',   label: 'Dark',   hint: 'The original Rigger look' },
  { id: 'light',  label: 'Light',  hint: 'Bright, high-contrast' },
]

// Selectable color schemes (excludes the meta "system" option).
export const COLOR_SCHEMES = THEMES.filter((t) => t.id !== 'system')

export const FONT_SANS_OPTIONS = [
  { id: 'system', label: 'System default' },
  { id: 'Inter',  label: 'Inter' },
]

export const FONT_MONO_OPTIONS = [
  { id: 'JetBrains Mono', label: 'JetBrains Mono' },
  { id: 'Fira Code',      label: 'Fira Code' },
  { id: 'system',         label: 'System mono' },
]

export const DENSITY_OPTIONS = [
  { id: 'comfortable', label: 'Comfortable' },
  { id: 'compact',     label: 'Compact' },
]

export const LOG_FONT_SIZE_MIN = 10
export const LOG_FONT_SIZE_MAX = 20

export const LOG_LINE_HEIGHT_OPTIONS = [
  { id: 1.4, label: 'Normal' },
  { id: 1.7, label: 'Relaxed' },
]

export const DEFAULT_PREFS = {
  theme: 'system',           // 'system' | 'dark' | 'light' | <future scheme>
  fontSans: 'Inter',         // id from FONT_SANS_OPTIONS or 'system'
  fontMono: 'JetBrains Mono', // id from FONT_MONO_OPTIONS or 'system'
  density: 'comfortable',    // 'comfortable' | 'compact'
  logFontSize: 13,           // px, clamped to [MIN, MAX]
  logLineHeight: 1.5,        // unitless
  logWrap: false,            // log viewer: wrap long lines (per-user)
  logRowNumbers: false,      // log viewer: show row numbers (per-user)
}

const SYS_SANS = 'ui-sans-serif, system-ui, sans-serif'
const SYS_MONO = 'ui-monospace, SFMono-Regular, Menlo, monospace'

// Resolve the meta 'system' theme to a concrete scheme using the OS setting.
export function resolveTheme(theme) {
  if (theme === 'system') {
    const light = typeof window !== 'undefined' &&
      window.matchMedia('(prefers-color-scheme: light)').matches
    return light ? 'light' : 'dark'
  }
  return theme || 'dark'
}

function clamp(n, lo, hi) { return Math.min(hi, Math.max(lo, n)) }

// Normalize a (possibly partial / legacy) prefs object to a full valid one.
export function normalizePrefs(raw) {
  const p = { ...DEFAULT_PREFS, ...(raw || {}) }
  p.logFontSize = clamp(Number(p.logFontSize) || DEFAULT_PREFS.logFontSize,
    LOG_FONT_SIZE_MIN, LOG_FONT_SIZE_MAX)
  p.logLineHeight = Number(p.logLineHeight) || DEFAULT_PREFS.logLineHeight
  p.logWrap = !!p.logWrap
  p.logRowNumbers = !!p.logRowNumbers
  return p
}

// Apply prefs to <html>: data-theme + typography CSS vars. Mirrors the no-FOUC
// script in index.html (keep the two in sync).
export function applyPrefs(prefs) {
  const p = normalizePrefs(prefs)
  const root = document.documentElement
  root.setAttribute('data-theme', resolveTheme(p.theme))
  root.style.setProperty('--font-sans', p.fontSans === 'system' ? SYS_SANS : `'${p.fontSans}'`)
  root.style.setProperty('--font-mono', p.fontMono === 'system' ? SYS_MONO : `'${p.fontMono}'`)
  root.style.setProperty('--log-font-size', `${p.logFontSize}px`)
  root.style.setProperty('--log-line-height', String(p.logLineHeight))
  root.style.fontSize = p.density === 'compact' ? '14px' : '16px'
}
