import { useState, useRef } from 'react'
import { putEnvOrder } from '../lib/api'
import { Hint, Btn } from './ui'

// EnvReorderModal lets an operator set the explicit deploy-tier order of a
// project's environments (low → high), which drives the release pipeline and the
// project-page env strip. Reordering is by drag-and-drop (native HTML5) or the
// ↑/↓ buttons; "Reset to auto" clears the explicit order so it falls back to the
// name-based guess. Saves the whole order as one list (no per-env numbering).
export default function EnvReorderModal({ workspace, name, envNames = [], onClose, onSaved }) {
  const [order, setOrder] = useState(() => [...envNames])
  const [saving, setSaving] = useState(false)
  const [err, setErr] = useState('')
  const dragFrom = useRef(null)

  function move(from, to) {
    if (to < 0 || to >= order.length || from === to) return
    setOrder(o => {
      const next = [...o]
      const [item] = next.splice(from, 1)
      next.splice(to, 0, item)
      return next
    })
  }

  async function save(list) {
    setSaving(true); setErr('')
    try {
      const res = await putEnvOrder(workspace, name, list)
      onSaved?.(res?.effective || list)
      onClose?.()
    } catch (e) {
      setErr(e?.response?.data?.error || 'Failed to save order')
      setSaving(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4" onClick={onClose}>
      <div className="w-full max-w-md bg-surface border border-border rounded-xl shadow-xl" onClick={e => e.stopPropagation()}>
        <div className="px-5 py-4 border-b border-border">
          <h2 className="text-sm font-semibold text-content-strong">Reorder environments</h2>
          <Hint>Lowest tier at the top → highest at the bottom. Drives the release pipeline's promote order.</Hint>
        </div>

        <ul className="px-5 py-4 space-y-1.5 max-h-80 overflow-y-auto">
          {order.map((env, i) => (
            <li key={env} draggable
              onDragStart={() => { dragFrom.current = i }}
              onDragOver={e => e.preventDefault()}
              onDrop={() => { if (dragFrom.current != null) move(dragFrom.current, i); dragFrom.current = null }}
              className="flex items-center gap-3 px-3 py-2 bg-surface-raised border border-border-strong rounded-lg cursor-grab active:cursor-grabbing">
              <span className="text-content-faint select-none">⠿</span>
              <span className="text-xs text-content-faint w-5 text-right tabular-nums">{i + 1}</span>
              <span className="text-sm font-medium text-content-strong flex-1 truncate">{env}</span>
              <div className="flex items-center gap-1 shrink-0">
                <button type="button" disabled={i === 0} onClick={() => move(i, i - 1)}
                  className="px-1.5 text-content-subtle hover:text-content-strong disabled:opacity-30 disabled:cursor-not-allowed">↑</button>
                <button type="button" disabled={i === order.length - 1} onClick={() => move(i, i + 1)}
                  className="px-1.5 text-content-subtle hover:text-content-strong disabled:opacity-30 disabled:cursor-not-allowed">↓</button>
              </div>
            </li>
          ))}
        </ul>

        {err && <p className="px-5 text-xs text-danger-fg">{err}</p>}

        <div className="px-5 py-4 border-t border-border flex items-center justify-between gap-3">
          <button type="button" onClick={() => save([])} disabled={saving}
            className="text-xs text-content-subtle hover:text-content-strong transition-colors disabled:opacity-50">
            Reset to auto
          </button>
          <div className="flex items-center gap-2">
            <Btn variant="secondary" size="sm" onClick={onClose} disabled={saving} >
              Cancel
            </Btn>
            <Btn variant="primary" size="sm" onClick={() => save(order)} disabled={saving}
              >
              {saving ? 'Saving…' : 'Save order'}
            </Btn>
          </div>
        </div>
      </div>
    </div>
  )
}
