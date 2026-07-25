/** @type {import('tailwindcss').Config} */

// Semantic color tokens. Each maps to a CSS variable defined per-theme in
// src/styles/themes.css. Variables hold space-separated RGB channels so the
// `rgb(var(--x) / <alpha-value>)` form keeps Tailwind opacity modifiers
// (e.g. `bg-surface/60`) working. Components reference *meaning* (bg-surface,
// text-content) — never raw grays — so adding a new color scheme is one CSS
// block in themes.css with no component changes.
const token = (name) => `rgb(var(--${name}) / <alpha-value>)`

export default {
  content: ['./index.html', './src/**/*.{js,jsx}'],
  // Most styling uses semantic tokens (auto-flip per theme). `dark:` is an
  // escape hatch for the decorative long-tail (category chips etc.) that has no
  // semantic token: write `bg-x-100 dark:bg-x-950`. Gated on the data-theme attr.
  darkMode: ['selector', '[data-theme="dark"]'],
  theme: {
    extend: {
      colors: {
        // Brand palette (used directly on both themes — accents/buttons).
        brand: {
          50:  '#ecfeff',
          100: '#cffafe',
          300: '#67e8f9',
          400: '#22d3ee',
          500: '#06b6d4',
          600: '#0891b2',
          700: '#0e7490',
          800: '#155e75',
          900: '#164e63',
          950: '#083344',
        },

        // ── Semantic theme tokens ──────────────────────────────────────────
        // Surfaces (elevation scale). In dark, higher = lighter; in light,
        // canvas is grey and surfaces are white — the token names hide this.
        canvas:           token('canvas'),
        surface: {
          DEFAULT:        token('surface'),
          raised:         token('surface-raised'),
          overlay:        token('surface-overlay'),
        },
        // Text hierarchy.
        content: {
          DEFAULT:        token('content'),
          strong:         token('content-strong'),
          muted:          token('content-muted'),
          subtle:         token('content-subtle'),
          faint:          token('content-faint'),
        },
        // Borders / dividers. `border-border` / `border-border-strong`.
        border: {
          DEFAULT:        token('border'),
          strong:         token('border-strong'),
        },
        // Accent (brand) as semantic tokens for tinted/text contexts.
        accent: {
          DEFAULT:        token('accent'),
          hover:          token('accent-hover'),
          fg:             token('accent-fg'),
          subtle:         token('accent-subtle'),
          text:           token('accent-text'),
          // Hover pair for accent-colored TEXT. Distinct from `hover` above,
          // which is the hover for an accent *fill*: text needs to go brighter
          // on dark and darker on light, a fill goes darker on both.
          'text-hover':   token('accent-text-hover'),
        },
        // Status families — each: solid, subtle bg, border, fg (text on subtle).
        success: { DEFAULT: token('success'), subtle: token('success-subtle'), border: token('success-border'), fg: token('success-fg') },
        warning: { DEFAULT: token('warning'), subtle: token('warning-subtle'), border: token('warning-border'), fg: token('warning-fg') },
        danger:  { DEFAULT: token('danger'),  subtle: token('danger-subtle'),  border: token('danger-border'),  fg: token('danger-fg')  },
        info:    { DEFAULT: token('info'),    subtle: token('info-subtle'),    border: token('info-border'),    fg: token('info-fg')    },
      },
      fontFamily: {
        // Configurable per-user via CSS vars (set by ThemeProvider). The var
        // falls back to the static stack so SSR / no-JS still renders sanely.
        sans: ['var(--font-sans)', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['var(--font-mono)', 'JetBrains Mono', 'Fira Code', 'ui-monospace', 'monospace'],
      },
      boxShadow: {
        // Elevation token — `none` in dark (surfaces separate via borders),
        // a soft shadow in light (surfaces lift off the grey canvas).
        card: 'var(--shadow-card)',
      },
    },
  },
  plugins: [],
}
