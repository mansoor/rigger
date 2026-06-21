import { useEffect, useRef, useState } from 'react'

const FIELD = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
const LBL = 'block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1'

// ConfirmDefaultEditor sets a tier's DEFAULT for destructive-action confirmations
// plus, when ON, whether lower tiers (members) may override it. Used for the global
// default (Admin → Preferences, allowInherit=false) and the workspace default
// (Manage Workspace → Preferences, allowInherit=true). Turning it OFF never locks —
// safety can only be tightened downward, so the "can users override?" radio shows
// only when ON.
//
// value: { confirm: ''|'true'|'false', allow: ''|'true'|'false' } | undefined (loading).
// onSave({ confirm, allow }) with allow cleared unless confirm==='true'.
export default function ConfirmDefaultEditor({ value, onSave, saving, savedOk, allowInherit }) {
  const [form, setForm] = useState(null)
  const original = useRef(null)

  useEffect(() => {
    if (form !== null || value === undefined) return
    const v = value || {}
    const seeded = { confirm: v.confirm || (allowInherit ? '' : 'true'), allow: v.allow || '' }
    original.current = seeded
    setForm(seeded)
  }, [value, form, allowInherit])

  if (!form) return null

  const set = (patch) => setForm(f => ({ ...f, ...patch }))
  const dirty = original.current && JSON.stringify(form) !== JSON.stringify(original.current)

  function save() {
    const out = { confirm: form.confirm, allow: form.confirm === 'true' ? (form.allow || 'true') : '' }
    original.current = out
    setForm(out)
    onSave(out)
  }

  const confirmOptions = [
    ...(allowInherit ? [{ value: '', label: 'Inherit global default' }] : []),
    { value: 'true', label: 'On — confirm destructive actions' },
    { value: 'false', label: 'Off — no confirmation prompt' },
  ]

  return (
    <div className="space-y-4">
      <div>
        <label className={LBL}>Confirm destructive actions</label>
        <select value={form.confirm} onChange={e => set({ confirm: e.target.value })} className={FIELD}>
          {confirmOptions.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
        </select>
      </div>
      {form.confirm === 'true' && (
        <div>
          <label className={LBL}>Can users override this?</label>
          <div className="flex flex-col gap-1.5 mt-1">
            <label className="flex items-center gap-2 text-sm text-content cursor-pointer">
              <input type="radio" checked={(form.allow || 'true') !== 'false'} onChange={() => set({ allow: 'true' })} />
              Yes — members may turn confirmations on/off for themselves
            </label>
            <label className="flex items-center gap-2 text-sm text-content cursor-pointer">
              <input type="radio" checked={form.allow === 'false'} onChange={() => set({ allow: 'false' })} />
              No — enforce confirmations for everyone (members can't disable)
            </label>
          </div>
        </div>
      )}
      <div className="flex items-center gap-3">
        <button onClick={save} disabled={!dirty || saving}
          className="bg-brand-600 hover:bg-brand-700 disabled:opacity-40 text-white text-sm font-semibold px-4 py-2 rounded-lg transition-colors">
          {saving ? 'Saving…' : 'Save'}
        </button>
        {savedOk && !dirty && <span className="text-xs text-success-fg">✓ Saved</span>}
      </div>
    </div>
  )
}
