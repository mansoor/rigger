import { useRef, useState } from 'react'

// DropZone is a click-or-drag file picker. onFile(file) fires with the chosen File;
// busy shows busyLabel and blocks interaction; accept narrows the file dialog.
export default function DropZone({ onFile, accept, hint, busy, busyLabel }) {
  const ref = useRef(null)
  const [over, setOver] = useState(false)
  return (
    <div
      onClick={() => !busy && ref.current?.click()}
      onDragOver={e => { e.preventDefault(); if (!busy) setOver(true) }}
      onDragLeave={() => setOver(false)}
      onDrop={e => { e.preventDefault(); setOver(false); if (busy) return; const f = e.dataTransfer.files?.[0]; if (f) onFile(f) }}
      className={`border-2 border-dashed rounded-xl px-4 py-3 text-center transition-colors ${
        busy ? 'opacity-60 cursor-wait border-border-strong'
        : over ? 'border-brand-500 bg-brand-950/20 cursor-pointer'
        : 'border-border-strong hover:border-brand-600 cursor-pointer'
      }`}
    >
      <p className="text-xs text-content-muted">{busy ? busyLabel : hint}</p>
      <input ref={ref} type="file" accept={accept} className="hidden"
        onChange={e => { const f = e.target.files?.[0]; e.target.value = ''; if (f) onFile(f) }} />
    </div>
  )
}
