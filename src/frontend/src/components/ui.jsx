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

// ── Primitives ─────────────────────────────────────────────────────────────────
export function Label({ children, required, htmlFor }) {
  return (
    <label htmlFor={htmlFor} className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
      {children}{required && <span className="text-danger-fg ml-0.5">*</span>}
    </label>
  )
}

export function Input({ value, onChange, placeholder, type = 'text', className = '', ...rest }) {
  return (
    <input
      type={type} value={value ?? ''} onChange={e => onChange(e.target.value)}
      placeholder={placeholder}
      className={`w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors ${className}`}
      {...rest}
    />
  )
}

export function Textarea({ value, onChange, placeholder, rows = 4, className = '', ...rest }) {
  return (
    <textarea
      value={value ?? ''} onChange={e => onChange(e.target.value)}
      placeholder={placeholder} rows={rows}
      className={`w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-subtle focus:outline-none focus:border-brand-500 transition-colors ${className}`}
      {...rest}
    />
  )
}

export function Select({ value, onChange, options, className = '', ...rest }) {
  return (
    <select value={value ?? ''} onChange={e => onChange(e.target.value)}
      className={`w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 ${className}`}
      {...rest}>
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

// Toggle: label + switch. Default spreads them (justify-between) for full-width setting
// rows; `inline` packs the switch right after the label (compact, for grids). The `hint`
// honors the help-text preference.
export function Toggle({ label, hint, checked, onChange, disabled = false, inline = false }) {
  return (
    <div className={`flex items-center ${inline ? 'gap-2.5' : 'justify-between'} ${disabled ? 'opacity-50' : ''}`}>
      <div>
        <p className="text-sm text-content">{label}</p>
        {hint && <Hint className="mt-0.5">{hint}</Hint>}
      </div>
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

// Field: Label + control + Hint in one block, the common vertical form row.
export function Field({ label, required, hint, htmlFor, children, className = '' }) {
  return (
    <div className={className}>
      {label && <Label required={required} htmlFor={htmlFor}>{label}</Label>}
      {children}
      {hint && <Hint>{hint}</Hint>}
    </div>
  )
}
