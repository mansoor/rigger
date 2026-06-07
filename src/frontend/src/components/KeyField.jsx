import { useEffect, useRef, useState } from 'react'
import { suggestKey, checkKey } from '../lib/api'

// KeyField renders the short identifier ("key") input for a workspace or project.
// It auto-suggests a derived, collision-free key from `name` until the user edits
// it, then live-validates the override. It reports (key, isValid) up via onChange.
//
// Props: type ('workspace'|'project'), name (display name), workspace (parent key,
// for project scope), onChange(key, valid), label (optional).
export default function KeyField({ type, name, workspace = '', onChange, label = 'Key' }) {
  const [key, setKey]       = useState('')
  const [touched, setTouched] = useState(false) // user overrode the suggestion
  const [status, setStatus] = useState({ state: 'idle' }) // idle|checking|ok|error
  const [bounds, setBounds] = useState({ min: 3, max: 4 })
  const debRef = useRef(null)

  // Keep parent informed.
  function report(k, valid) { onChange?.(k, valid) }

  // Auto-suggest from the name while the user hasn't overridden the key.
  useEffect(() => {
    if (touched) return
    const n = (name || '').trim()
    if (!n) { setKey(''); setStatus({ state: 'idle' }); report('', false); return }
    clearTimeout(debRef.current)
    debRef.current = setTimeout(async () => {
      try {
        const r = await suggestKey(type, n, workspace)
        setBounds({ min: r.min, max: r.max })
        setKey(r.key)
        setStatus({ state: 'ok', available: true, suggested: true })
        report(r.key, !!r.key)
      } catch {
        setStatus({ state: 'error', error: 'Could not suggest a key' })
        report('', false)
      }
    }, 250)
    return () => clearTimeout(debRef.current)
  }, [name, touched, type, workspace]) // eslint-disable-line react-hooks/exhaustive-deps

  // Validate a user override (debounced).
  useEffect(() => {
    if (!touched) return
    const k = key.trim()
    if (!k) { setStatus({ state: 'error', error: 'Key is required' }); report('', false); return }
    clearTimeout(debRef.current)
    setStatus({ state: 'checking' })
    debRef.current = setTimeout(async () => {
      try {
        const r = await checkKey(type, k, workspace)
        setBounds({ min: r.min, max: r.max })
        if (r.valid && r.available) {
          setStatus({ state: 'ok', available: true })
          report(r.key, true)
        } else {
          setStatus({ state: 'error', error: r.error || 'Invalid key' })
          report(r.key, false)
        }
      } catch {
        setStatus({ state: 'error', error: 'Validation failed' })
        report('', false)
      }
    }, 300)
    return () => clearTimeout(debRef.current)
  }, [key, touched, type, workspace]) // eslint-disable-line react-hooks/exhaustive-deps

  function onInput(e) {
    // Sanitize to the key charset as the user types.
    const v = e.target.value.toLowerCase().replace(/[^a-z0-9]/g, '')
    setTouched(true)
    setKey(v)
  }

  const ok = status.state === 'ok'
  const err = status.state === 'error'

  return (
    <div>
      <div className="flex items-center justify-between mb-1">
        <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider">{label}</label>
        {!touched && key && <span className="text-[10px] text-content-faint">auto · {bounds.min}–{bounds.max} chars</span>}
        {touched && <button type="button" onClick={() => setTouched(false)} className="text-[10px] text-brand-400 hover:text-brand-300">reset to auto</button>}
      </div>
      <div className="relative">
        <input
          value={key} onChange={onInput}
          maxLength={bounds.max}
          placeholder="auto"
          className={`w-full px-3 py-2 bg-surface-raised border rounded-lg text-content-strong text-sm font-mono focus:outline-none transition-colors ${
            err ? 'border-danger-border focus:border-danger' : ok ? 'border-success-border/70 focus:border-success' : 'border-border-strong focus:border-brand-500'
          }`}
        />
        <span className="absolute right-3 top-1/2 -translate-y-1/2 text-xs">
          {status.state === 'checking' ? <span className="text-content-faint">…</span>
            : ok ? <span className="text-success-fg">✓</span>
            : err ? <span className="text-danger-fg">✗</span> : null}
        </span>
      </div>
      <p className={`text-xs mt-1 ${err ? 'text-danger-fg' : 'text-content-subtle'}`}>
        {err ? status.error
          : 'Short identifier for folders, URLs and Docker names. Lowercase letters/digits; fixed after creation.'}
      </p>
    </div>
  )
}
