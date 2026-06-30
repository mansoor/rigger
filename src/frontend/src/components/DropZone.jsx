import { useRef, useState } from 'react'

// DropZone is a click-or-drag file picker. onFile(file) fires with the chosen File;
// busy shows busyLabel and blocks interaction; accept narrows the file dialog. When
// `multiple` is set, several files can be dropped/picked at once — onFile fires per file
// (default false keeps every existing caller single-file).
export default function DropZone({ onFile, accept, hint, busy, busyLabel, multiple = false }) {
  const ref = useRef(null)
  const [over, setOver] = useState(false)
  const emit = list => {
    const files = list ? Array.from(list) : []
    for (const f of (multiple ? files : files.slice(0, 1))) if (f) onFile(f)
  }
  return (
    <div
      onClick={() => !busy && ref.current?.click()}
      onDragOver={e => { e.preventDefault(); if (!busy) setOver(true) }}
      onDragLeave={() => setOver(false)}
      onDrop={e => { e.preventDefault(); setOver(false); if (busy) return; emit(e.dataTransfer.files) }}
      className={`border-2 border-dashed rounded-xl px-4 py-3 text-center transition-colors ${
        busy ? 'opacity-60 cursor-wait border-border-strong'
        : over ? 'border-brand-500 bg-brand-950/20 cursor-pointer'
        : 'border-border-strong hover:border-brand-600 cursor-pointer'
      }`}
    >
      <p className="text-xs text-content-muted">{busy ? busyLabel : hint}</p>
      <input ref={ref} type="file" accept={accept} multiple={multiple} className="hidden"
        onChange={e => { const fs = e.target.files; e.target.value = ''; emit(fs) }} />
    </div>
  )
}
