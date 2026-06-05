import { useEffect, useRef } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import '@xterm/xterm/css/xterm.css'

export default function LogDrawer({ ws, title, onClose }) {
  const containerRef = useRef(null)
  const termRef      = useRef(null)
  const fitRef       = useRef(null)

  useEffect(() => {
    const term = new Terminal({
      theme: {
        background: '#030712',   // gray-950
        foreground: '#f3f4f6',   // gray-100
        cursor:     '#6366f1',   // brand-500
        selectionBackground: '#374151',
      },
      fontFamily: "'JetBrains Mono', 'Fira Code', monospace",
      fontSize: 13,
      lineHeight: 1.5,
      convertEol: true,
      scrollback: 5000,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(containerRef.current)
    fit.fit()
    termRef.current = term
    fitRef.current  = fit

    const ro = new ResizeObserver(() => fit.fit())
    ro.observe(containerRef.current)

    // Stream WebSocket messages into the terminal
    ws.addEventListener('message', (e) => term.write(e.data))
    ws.addEventListener('close',   () => term.write('\r\n\x1b[2m[connection closed]\x1b[0m\r\n'))
    ws.addEventListener('error',   () => term.write('\r\n\x1b[31m[connection error]\x1b[0m\r\n'))

    return () => {
      ro.disconnect()
      term.dispose()
    }
  }, [ws])

  return (
    <div className="fixed inset-0 z-50 flex flex-col bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div
        className="mt-auto h-2/3 bg-gray-950 border-t border-gray-800 flex flex-col"
        onClick={e => e.stopPropagation()}
      >
        {/* Drawer header */}
        <div className="flex items-center justify-between px-4 py-2.5 border-b border-gray-800 shrink-0">
          <div className="flex items-center gap-2">
            <span className="w-2 h-2 rounded-full bg-green-500 animate-pulse" />
            <span className="font-mono text-sm text-gray-300">{title}</span>
          </div>
          <button
            onClick={onClose}
            className="text-gray-500 hover:text-white text-xl leading-none transition-colors"
          >
            ×
          </button>
        </div>

        {/* Terminal */}
        <div ref={containerRef} className="flex-1 p-2 overflow-hidden" />
      </div>
    </div>
  )
}
