// Centralized form-control kit.
//
// Before this, every page/component defined its own Label/Input/Select/Toggle with
// slightly-drifting Tailwind classes. This is the single source of truth: import from
// here so a styling change (or a branded control) is a one-file edit. All controls are
// native elements skinned with semantic theme tokens (--content-*, --surface-*, etc.),
// so they track light/dark automatically.
//
// Help text: <Hint> and useHelpText() gate the small descriptive text under fields and
// section descriptions behind the per-user "Show help text" preference (theme prefs).
// Warnings/errors are NOT help — render those directly so they always show.
import { useTheme } from '../theme/ThemeProvider'

// ── Help-text visibility ──────────────────────────────────────────────────────
// useHelpText() → boolean. For inline/custom help that can't use <Hint>, gate manually:
//   const showHelp = useHelpText(); return showHelp && <span>…</span>
export function useHelpText() {
  const { prefs } = useTheme()
  return prefs.helpText !== false
}

// Hint: the standard field-hint / section-description paragraph. Hidden when the user
// turns help text off. Defaults to `text-xs text-content-subtle mt-1`. To vary it without
// Tailwind class-conflict surprises: pass tone="faint" for the dimmer tier, and put size
// (text-[11px]) / spacing (mt-0.5, mb-2) / layout in className — the base drops its own
// text-size or margin when className already supplies one.
export function Hint({ children, className = '', tone = 'subtle' }) {
  const show = useHelpText()
  if (!show || children == null || children === false) return null
  const color = tone === 'faint' ? 'text-content-faint' : 'text-content-subtle'
  const size = /\btext-(\[|xs|sm|base|lg)/.test(className) ? '' : 'text-xs'
  const margin = /\bm[tby]-/.test(className) ? '' : 'mt-1'
  return <p className={`${size} ${color} ${margin} ${className}`.replace(/\s+/g, ' ').trim()}>{children}</p>
}

// ── Control class strings ──────────────────────────────────────────────────────
// Input/Select/Textarea below are the preferred API. But plenty of controls in
// the app stay raw <input>/<select> for real reasons — uncontrolled fields, refs,
// inline editors with bespoke event wiring — and those had drifted into ~77
// different class strings: same control, different height, radius and text size
// depending on the screen. These constants let a raw element be pixel-identical
// to a kit one without restating the recipe.
//
// CONTROL is the standard form field. CONTROL_SM is the compact variant for
// controls that sit inline in a dense table or list row.
export const CONTROL =
  'px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm ' +
  'placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors ' +
  'disabled:opacity-50 disabled:cursor-not-allowed'

export const CONTROL_SM =
  'px-2 py-1 bg-surface-raised border border-border-strong rounded text-content-strong text-sm ' +
  'placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors ' +
  'disabled:opacity-50 disabled:cursor-not-allowed'

// ── Primitives ─────────────────────────────────────────────────────────────────
export function Label({ children, required, htmlFor }) {
  return (
    <label htmlFor={htmlFor} className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
      {children}{required && <span className="text-danger-fg ml-0.5">*</span>}
    </label>
  )
}

// The rendered height of a standard control (py-2 + text-sm line box + borders).
// Use it to centre a non-input control — a toggle, a badge — so it lines up with
// an <Input> beside it WITHOUT resorting to `items-end` on the row. items-end
// bottom-aligns against the tallest cell, which changes when a neighbouring
// <Hint> is hidden by the help-text preference, so the control visibly jumps.
export const CONTROL_H = 'min-h-[2.375rem]'

// LabelSpacer: occupies exactly a <Label>'s height (text-xs line box + mb-1),
// for a cell whose control has no label but must still line up with the
// labelled fields next to it.
export function LabelSpacer() {
  return <div aria-hidden className="h-4 mb-1" />
}

// `error` turns the field red and prints the message under it. The border is
// swapped inside the CONTROL string rather than appended after it: two competing
// border-color utilities have equal specificity, so which one won would depend on
// Tailwind's emit order rather than on this code.
export function Input({ value, onChange, placeholder, type = 'text', error, className = '', ...rest }) {
  const base = error
    ? CONTROL.replace('border-border-strong', 'border-danger').replace('focus:border-brand-500', 'focus:border-danger')
    : CONTROL
  return (
    <>
      <input
        type={type} value={value ?? ''} onChange={e => onChange(e.target.value)}
        placeholder={placeholder}
        className={`w-full ${base} ${className}`}
        {...rest}
      />
      {error && <p className="text-danger-fg text-xs mt-1">{error}</p>}
    </>
  )
}

export function Textarea({ value, onChange, placeholder, rows = 4, className = '', ...rest }) {
  return (
    <textarea
      value={value ?? ''} onChange={e => onChange(e.target.value)}
      placeholder={placeholder} rows={rows}
      className={`w-full ${CONTROL} ${className}`}
      {...rest}
    />
  )
}

export function Select({ value, onChange, options, className = '', ...rest }) {
  return (
    <select value={value ?? ''} onChange={e => onChange(e.target.value)}
      className={`w-full ${CONTROL} ${className}`}
      {...rest}>
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

// Toggle: label + switch. Default spreads them (justify-between) for full-width setting
// rows; `inline` packs the switch right after the label (compact, for grids);
// `switchFirst` puts the switch before the label, for the compact "[switch] Enabled"
// state readout. The `hint` honors the help-text preference.
export function Toggle({ label, hint, checked, onChange, disabled = false, inline = false, switchFirst = false }) {
  const sw = (
    <button
      type="button"
      onClick={() => !disabled && onChange(!checked)}
      disabled={disabled}
      className={`relative w-10 h-5 rounded-full transition-colors shrink-0 disabled:cursor-not-allowed ${
        checked && !disabled ? 'bg-brand-600' : checked ? 'bg-brand-800' : 'bg-surface-overlay'
      }`}
    >
      <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-5' : ''}`} />
    </button>
  )
  const text = (label || hint) ? (
    <div>
      {label && <p className="text-sm text-content">{label}</p>}
      {hint && <Hint className="mt-0.5">{hint}</Hint>}
    </div>
  ) : null
  const layout = switchFirst ? 'gap-2.5' : inline ? 'gap-2.5' : 'justify-between'
  return (
    <div className={`flex items-center ${layout} ${disabled ? 'opacity-50' : ''}`}>
      {switchFirst ? <>{sw}{text}</> : <>{text}{sw}</>}
    </div>
  )
}

// Checkbox: native input (accent-tinted) + optional label/hint. onChange gets the boolean.
// The box and label share one vertically-centered row (so they line up whether or not the
// hint shows); the gated hint drops below, indented (ml-6) to sit under the label text.
export function Checkbox({ checked, onChange, label, hint, disabled = false, className = '' }) {
  return (
    <div className={`${disabled ? 'opacity-50' : ''} ${className}`}>
      <label className={`flex items-center gap-2 ${disabled ? 'cursor-not-allowed' : 'cursor-pointer'}`}>
        <input type="checkbox" checked={!!checked} disabled={disabled}
          onChange={e => onChange(e.target.checked)}
          className="w-4 h-4 accent-brand-500 shrink-0" />
        {label && <span className="text-sm text-content select-none">{label}</span>}
      </label>
      {hint && <Hint className="mt-1 ml-6">{hint}</Hint>}
    </div>
  )
}

// RadioGroup: one choice from options [{value,label,hint}]. `row` lays them horizontally.
// Each option's control + label are centered on one row; a per-option hint sits below it.
export function RadioGroup({ name, value, onChange, options, row = false, className = '' }) {
  return (
    <div className={`${row ? 'flex flex-wrap items-center gap-x-5 gap-y-2' : 'space-y-2'} ${className}`}>
      {options.map(o => (
        <div key={o.value}>
          <label className="flex items-center gap-2 text-sm text-content-muted cursor-pointer">
            <input type="radio" name={name} value={o.value} checked={value === o.value}
              onChange={() => onChange(o.value)} className="w-3.5 h-3.5 accent-brand-500 shrink-0" />
            <span className="select-none">{o.label}</span>
          </label>
          {o.hint && <Hint className="mt-0.5 ml-6">{o.hint}</Hint>}
        </div>
      ))}
    </div>
  )
}

// CertBadge renders a TLS cert's expiry as a colored pill from a certInfo object
// ({ found, days_remaining, expired, not_after, issuer }): red if expired / ≤0d, amber
// within 14 days, else green. Renders a muted dash when there's no managed cert.
export function CertBadge({ cert, className = '' }) {
  if (!cert || !cert.found) return <span className={`text-content-faint ${className}`}>—</span>
  const d = cert.days_remaining
  const expired = cert.expired || d < 0
  const tone = expired ? 'text-danger-fg' : d <= 14 ? 'text-warning-fg' : 'text-success-fg'
  const label = expired ? 'expired' : d === 0 ? 'expires today' : `${d}d left`
  const title = cert.not_after
    ? `Valid until ${new Date(cert.not_after).toLocaleString()}${cert.issuer ? ` · ${cert.issuer}` : ''}`
    : undefined
  return (
    <span className={`inline-flex items-center gap-1 text-xs font-medium ${tone} ${className}`} title={title}>
      <span aria-hidden>{expired ? '⚠' : '🔒'}</span>{label}
    </span>
  )
}

// Field: Label + control + Hint in one block, the common vertical form row.
// `span` (1-4) places it on the FormRow track — see FormRow below. Outside a
// FormRow the span is inert, so plain <Field> keeps working unchanged.
export function Field({ label, required, hint, htmlFor, span, children, className = '' }) {
  return (
    <div className={`${span ? COL_SPAN[span] || '' : ''} ${className}`.trim()}>
      {label && <Label required={required} htmlFor={htmlFor}>{label}</Label>}
      {children}
      {hint && <Hint>{hint}</Hint>}
    </div>
  )
}

// ── Form layout ───────────────────────────────────────────────────────────────
// Every multi-field row in a form shares ONE 4-column track. Before this, each
// row picked its own split (grid-cols-5 with 4+1 here, [1fr_auto] with a w-40
// sidecar there), so a "name + key" row and a "repo + branch" row directly below
// it had visibly different column edges. Give each Field a span and consecutive
// rows line up exactly.
//
// Spans are written out (not interpolated) so Tailwind's scanner keeps them.
const COL_SPAN = {
  1: 'sm:col-span-1',
  2: 'sm:col-span-2',
  3: 'sm:col-span-3',
  4: 'sm:col-span-4',
}

export function FormRow({ children, className = '' }) {
  return <div className={`grid gap-4 sm:grid-cols-4 ${className}`.trim()}>{children}</div>
}

// ReadOnly: a fixed/derived value shown in a control-shaped box. Matches Input's
// box model exactly (px-3 py-2 text-sm + 1px border) so a read-only field and an
// editable one sitting in the same FormRow are the same height and baseline.
export function ReadOnly({ children, mono = true, title, className = '' }) {
  return (
    <div
      title={title}
      className={`w-full px-3 py-2 bg-surface-raised/60 border border-border-strong rounded-lg text-content-muted text-sm ${mono ? 'font-mono' : ''} cursor-not-allowed select-all truncate ${className}`}
    >
      {children}
    </div>
  )
}

// ── Buttons ───────────────────────────────────────────────────────────────────
// One <Btn> in place of the ~290 distinct hand-written button class strings this
// app had grown (434 buttons, 21 different sizes for "primary" alone). Pick a
// role + size; never restate padding or colors at the call site. Layout classes
// (w-full, flex-1, shrink-0, ml-auto…) still go in className — they compose.
//
// Solid roles use the brand/red palette directly, the same convention the token
// system already carves out for accents and buttons; the tinted/text roles use
// semantic tokens so they flip with the theme.
const BTN_BASE =
  'inline-flex items-center justify-center gap-1.5 rounded-lg border transition-colors ' +
  'focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/60 ' +
  'disabled:opacity-50 disabled:cursor-not-allowed'

const BTN_SIZE = {
  xs: 'px-2.5 py-1.5 text-xs',
  sm: 'px-3 py-1.5 text-sm',
  md: 'px-4 py-2 text-sm',
}

// Every variant carries a border (transparent when it has no visible one) so a
// solid and an outline button placed side by side are exactly the same height.
//
// Destructive actions are deliberately two-tier, matching how the app already
// used them: `danger` (solid red) for the final irreversible confirm in a modal,
// `dangerSubtle` for an inline destructive action sitting in a list or row.
const BTN_VARIANT = {
  primary:      'border-transparent bg-brand-600 hover:bg-brand-700 text-white font-semibold',
  danger:       'border-transparent bg-red-600 hover:bg-red-700 text-white font-semibold',
  dangerSubtle: 'border-danger-border/50 bg-danger-subtle/60 hover:bg-danger/20 text-danger-fg font-medium',
  secondary:    'border-transparent bg-surface-raised hover:bg-surface-overlay text-content font-medium',
  outline:      'border-border-strong bg-transparent hover:bg-surface-raised text-content font-medium',
  ghost:        'border-transparent bg-transparent text-content-muted hover:text-content-strong hover:bg-surface-raised font-medium',
  dangerGhost:  'border-transparent bg-transparent text-danger-fg hover:bg-danger-subtle font-medium',
}

export function Btn({ variant = 'secondary', size = 'md', type = 'button', className = '', children, ...rest }) {
  const v = BTN_VARIANT[variant] || BTN_VARIANT.secondary
  return (
    <button type={type} className={`${BTN_BASE} ${BTN_SIZE[size] || BTN_SIZE.md} ${v} ${className}`.replace(/\s+/g, ' ').trim()} {...rest}>
      {children}
    </button>
  )
}

// CloseBtn: the dismiss control on a modal/panel header. Renders its own glyph —
// call sites had drifted to two different characters (× and ✕) at two different
// sizes across 37 dialogs. Also supplies the aria-label none of them had.
export function CloseBtn({ label = 'Close', className = '', ...rest }) {
  return (
    <button
      type="button" aria-label={label} title={label}
      className={`inline-flex items-center justify-center shrink-0 w-8 h-8 rounded-md text-xl leading-none text-content-subtle hover:text-content-strong hover:bg-surface-raised transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/60 ${className}`.trim()}
      {...rest}
    >
      ×
    </button>
  )
}

// LinkBtn: an inline, box-less text action ("Add variable", "Show advanced").
// Uses the accent-TEXT token rather than raw brand-400 so it stays readable on
// the light theme, where bright cyan on white fails contrast badly.
export function LinkBtn({ size = 'xs', className = '', children, ...rest }) {
  return (
    <button
      type="button"
      className={`inline-flex items-center gap-1 ${size === 'sm' ? 'text-sm' : 'text-xs'} text-accent-text hover:text-accent-text-hover hover:underline transition-colors rounded focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/60 disabled:opacity-50 disabled:cursor-not-allowed ${className}`.trim()}
      {...rest}
    >
      {children}
    </button>
  )
}

// IconBtn: square icon-only button (✕, trash, reorder arrows). Fixed box so a
// column of them lines up regardless of glyph width.
const ICON_SIZE = { xs: 'w-6 h-6 text-xs', sm: 'w-7 h-7 text-sm', md: 'w-8 h-8 text-sm' }

export function IconBtn({ variant = 'ghost', size = 'sm', type = 'button', className = '', children, ...rest }) {
  const v = BTN_VARIANT[variant] || BTN_VARIANT.ghost
  return (
    <button
      type={type}
      className={`inline-flex items-center justify-center shrink-0 rounded-md border transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-brand-500/60 disabled:opacity-50 disabled:cursor-not-allowed ${ICON_SIZE[size] || ICON_SIZE.sm} ${v} ${className}`.replace(/\s+/g, ' ').trim()}
      {...rest}
    >
      {children}
    </button>
  )
}
