import { useState, useRef, useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchCompose } from '../lib/api'

// ── Clipboard helper — works on HTTP (no secure context required) ─────────────

function writeClipboard(text) {
  // Prefer the modern API (requires HTTPS / localhost)
  if (navigator.clipboard && window.isSecureContext) {
    return navigator.clipboard.writeText(text)
  }
  // Fallback: off-screen textarea + execCommand (works on plain HTTP)
  return new Promise((resolve, reject) => {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.cssText = 'position:fixed;top:0;left:0;opacity:0;pointer-events:none'
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    try {
      document.execCommand('copy') ? resolve() : reject(new Error('execCommand failed'))
    } catch (e) {
      reject(e)
    } finally {
      document.body.removeChild(ta)
    }
  })
}

// ── Compose Viewer (read-only) ────────────────────────────────────────────────
// docker-compose.yml is a generated artefact derived from config.json.
// Editing it directly is not supported — use Edit Workspace to modify
// config.json, then Refresh (Deploy ▾ → Refresh) to regenerate.

export default function ComposeEditor({ name, env, onClose, onRefresh }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['compose', name, env],
    queryFn:  () => fetchCompose(name, env),
  })

  const content      = data?.content ?? ''
  const containerRef = useRef(null)   // ref to the table container
  const [copyState, setCopyState] = useState('idle') // 'idle' | 'copied' | 'error'

  const handleCopy = useCallback(() => {
    // Determine what to copy: selection within our container, or full content
    let text = content
    const sel = window.getSelection()
    if (sel && sel.rangeCount > 0 && sel.toString().trim()) {
      const range = sel.getRangeAt(0)
      // Only use selection if it's actually inside our compose table
      if (containerRef.current?.contains(range.commonAncestorContainer)) {
        text = sel.toString()
      }
    }

    writeClipboard(text)
      .then(() => {
        setCopyState('copied')
        setTimeout(() => setCopyState('idle'), 2000)
      })
      .catch(() => {
        setCopyState('error')
        setTimeout(() => setCopyState('idle'), 2000)
      })
  }, [content])

  const copyLabel = copyState === 'copied' ? '✓ Copied'
                  : copyState === 'error'  ? '✗ Failed'
                  : 'Copy'

  const copyClass = copyState === 'copied'
    ? 'border-green-600 bg-green-950 text-green-400'
    : copyState === 'error'
    ? 'border-red-600 bg-red-950 text-red-400'
    : 'border-gray-700 bg-gray-800 hover:bg-gray-700 text-gray-300'

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-black/70 backdrop-blur-sm" onClick={onClose}>
      <div
        className="mt-16 mx-auto w-full max-w-4xl flex flex-col bg-gray-900 border border-gray-700 rounded-2xl overflow-hidden shadow-2xl"
        style={{ maxHeight: 'calc(100vh - 80px)' }}
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-3 border-b border-gray-800 shrink-0">
          <div>
            <div className="flex items-center gap-2">
              <h3 className="font-semibold text-white">docker-compose.yml</h3>
              <span className="text-xs px-2 py-0.5 rounded-full bg-gray-700 text-gray-400">read-only</span>
            </div>
            <p className="text-xs text-gray-500 mt-0.5">{name} / {env}</p>
          </div>
          <div className="flex items-center gap-2">
            {onRefresh && (
              <button
                onClick={() => { onRefresh(); onClose() }}
                className="text-sm font-medium px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 text-white transition-colors"
                title="Regenerate docker-compose.yml from config.json and deploy"
              >
                Refresh
              </button>
            )}
            <button
              onClick={handleCopy}
              disabled={!content}
              title={content ? 'Copy file (or selected text)' : 'Loading…'}
              className={`text-sm font-medium px-3 py-1.5 rounded-lg border transition-all duration-150 ${copyClass} ${!content ? 'opacity-40 cursor-not-allowed' : ''}`}
            >
              {copyLabel}
            </button>
            <button onClick={onClose} className="text-gray-400 hover:text-white text-xl leading-none ml-1">×</button>
          </div>
        </div>

        {/* Read-only content with line numbers */}
        <div ref={containerRef} className="flex-1 overflow-auto min-h-0 bg-gray-950">
          {isLoading && <p className="p-6 text-gray-400 text-sm">Loading…</p>}
          {error     && <p className="p-6 text-red-400 text-sm">{error.message}</p>}
          {!isLoading && !error && (
            <table className="w-full font-mono text-sm leading-relaxed border-collapse min-h-full">
              <tbody>
                {content.split('\n').map((line, i) => (
                  <tr key={i} className="hover:bg-gray-800/40">
                    <td className="select-none text-right text-gray-600 px-4 py-0 w-12 shrink-0 border-r border-gray-800 align-top">
                      {i + 1}
                    </td>
                    <td className="text-gray-200 px-4 py-0 whitespace-pre align-top">{line || ' '}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        {/* Footer */}
        <div className="px-5 py-2.5 border-t border-gray-800 bg-gray-900/80 shrink-0">
          <p className="text-xs text-gray-500">
            Generated from <code className="font-mono text-gray-400">config.json</code>.
            Use <strong className="text-gray-400">Edit Workspace</strong> to change configuration,
            then <strong className="text-gray-400">Deploy ▾ → Refresh</strong> to regenerate.
            Select text in the viewer then click Copy to copy only the selection.
          </p>
        </div>
      </div>
    </div>
  )
}
