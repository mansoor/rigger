import { useEffect, useRef, useState } from 'react'
import { THEMES, FONT_SANS_OPTIONS, FONT_MONO_OPTIONS, DENSITY_OPTIONS } from '../theme/themes'
import { Btn } from './ui'

const FIELD = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
const LBL = 'block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1'

// AppearanceDefaultEditor edits a tier's DEFAULT appearance blob (theme + typography)
// — used for the global default (Admin → Preferences) and the workspace default
// (Manage Workspace → Preferences). Each field has an "inherit" option; the saved
// blob carries only the fields explicitly set, and clears entirely when all are left
// to inherit. Precedence is whole-blob (user → workspace → global); normalizePrefs
// backfills any field a blob omits, so partial defaults are fine.
//
// Props: value = parsed saved blob (object) | null (none) | undefined (loading);
// onSave(blobOrNull); saving; savedOk; inheritLabel (theme's "no default" wording).
export default function AppearanceDefaultEditor({ value, onSave, saving, savedOk, inheritLabel }) {
  const [form, setForm] = useState(null) // null until seeded from the loaded value
  const original = useRef(null)

  // Seed once the saved value has loaded (undefined while the query is in flight).
  useEffect(() => {
    if (form !== null || value === undefined) return
    const v = value || {}
    const seeded = { theme: v.theme || '', fontSans: v.fontSans || '', fontMono: v.fontMono || '', density: v.density || '' }
    original.current = seeded
    setForm(seeded)
  }, [value, form])

  if (!form) return null

  const set = (k, val) => setForm(f => ({ ...f, [k]: val }))
  const dirty = original.current && JSON.stringify(form) !== JSON.stringify(original.current)

  function blobFrom(f) {
    const b = {}
    if (f.theme) b.theme = f.theme
    if (f.fontSans) b.fontSans = f.fontSans
    if (f.fontMono) b.fontMono = f.fontMono
    if (f.density) b.density = f.density
    return Object.keys(b).length ? b : null
  }

  function save() {
    original.current = { ...form }
    onSave(blobFrom(form))
  }
  function clearAll() {
    const empty = { theme: '', fontSans: '', fontMono: '', density: '' }
    original.current = empty
    setForm(empty)
    onSave(null)
  }

  const sel = (label, key, options, inherit) => (
    <div>
      <label className={LBL}>{label}</label>
      <select value={form[key]} onChange={e => set(key, e.target.value)} className={FIELD}>
        <option value="">{inherit}</option>
        {options.map(o => <option key={o.id} value={o.id}>{o.label}</option>)}
      </select>
    </div>
  )

  return (
    <div className="space-y-4">
      <div className="grid sm:grid-cols-2 gap-4">
        {sel('Default theme', 'theme', THEMES, inheritLabel || 'No default')}
        {sel('Interface font', 'fontSans', FONT_SANS_OPTIONS, 'Inherit')}
        {sel('Monospace font', 'fontMono', FONT_MONO_OPTIONS, 'Inherit')}
        {sel('Density', 'density', DENSITY_OPTIONS, 'Inherit')}
      </div>
      <div className="flex items-center gap-3">
        <Btn variant="primary" size="md" onClick={save} disabled={!dirty || saving} >
          {saving ? 'Saving…' : 'Save'}
        </Btn>
        <Btn variant="outline" size="md" onClick={clearAll} disabled={saving} >
          Clear (inherit)
        </Btn>
        {savedOk && !dirty && <span className="text-xs text-success-fg">✓ Saved</span>}
      </div>
    </div>
  )
}
