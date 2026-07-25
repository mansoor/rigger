import { useState, useEffect, useRef, useCallback } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { fetchContainers } from '../lib/api'
import { useAuthStore } from '../store/auth'
import { useTheme } from '../theme/ThemeProvider'
import { xtermOptions, applyXterm } from '../theme/xterm'
import '@xterm/xterm/css/xterm.css'
import { Btn, CloseBtn, CONTROL } from './ui'

export default function TerminalModal({ workspace, wsName, envName, initialService, onClose }) {
  const token      = useAuthStore(s => s.token)
  const { prefs, resolvedTheme } = useTheme()
  const termRef       = useRef(null)   // xterm instance
  const fitRef        = useRef(null)   // FitAddon instance
  const mountRef      = useRef(null)   // DOM element for xterm
  const wsRef         = useRef(null)   // WebSocket
  const resizeRef     = useRef(null)   // ResizeObserver
  const firstMsgRef   = useRef(false)  // tracks first WS message (avoids stale closure)
  const autoConnRef   = useRef(false)  // ensures we auto-connect at most once

  const [service, setService]     = useState('')
  const [connected, setConnected] = useState(false)
  const [connecting, setConnecting] = useState(false)
  const [error, setError]         = useState('')

  const { data: containers, isLoading } = useQuery({
    queryKey: ['containers', workspace, wsName, envName],
    queryFn:  () => fetchContainers(workspace, wsName, envName),
    retry: false,
  })

  const runningContainers = (containers || []).filter(c =>
    c.State === 'running' || c.State === 'Up' || String(c.State).toLowerCase().startsWith('up')
  )

  // Pre-select the container: the one the Terminal button was clicked on
  // (initialService), else the first running container.
  useEffect(() => {
    if (service) return
    if (initialService) { setService(initialService); return }
    if (runningContainers.length > 0) setService(runningContainers[0].Service)
  }, [runningContainers, initialService])

  // Initialise xterm once on mount
  useEffect(() => {
    const term = new Terminal({
      ...xtermOptions(prefs),
      cursorBlink: true,
      convertEol: false,
      scrollback: 5000,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    term.open(mountRef.current)
    fit.fit()
    termRef.current = term
    fitRef.current  = fit

    term.writeln('\x1b[2mSelect a container and click Connect.\x1b[0m')

    // ResizeObserver keeps the terminal fitted to its container
    const ro = new ResizeObserver(() => {
      try { fit.fit() } catch {}
      if (wsRef.current?.readyState === WebSocket.OPEN && termRef.current) {
        const { cols, rows } = termRef.current
        wsRef.current.send(JSON.stringify({ type: 'resize', cols, rows }))
      }
    })
    ro.observe(mountRef.current)
    resizeRef.current = ro

    return () => {
      ro.disconnect()
      term.dispose()
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  // Live re-theme / re-size when the user switches theme or log-font prefs.
  useEffect(() => {
    applyXterm(termRef.current, fitRef.current, prefs)
  }, [resolvedTheme, prefs.fontMono, prefs.logFontSize, prefs.logLineHeight])

  const disconnect = useCallback(() => {
    if (wsRef.current) {
      wsRef.current.close()
      wsRef.current = null
    }
    firstMsgRef.current = false
    setConnected(false)
    setConnecting(false)
  }, [])

  const connect = useCallback(() => {
    if (!service) { setError('Select a container first'); return }
    disconnect()
    setError('')
    setConnecting(true)

    const term = termRef.current
    const fit  = fitRef.current
    term.reset()
    fit.fit()
    const { cols, rows } = term

    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws'
    const ws = new WebSocket(
      `${proto}://${window.location.host}/api/workspaces/${workspace}/projects/${wsName}/envs/${envName}/terminal`
    )
    wsRef.current = ws

    ws.addEventListener('open', () => {
      ws.send(JSON.stringify({ token, service, cols, rows }))
    })

    // Hook xterm input → WebSocket
    const inputDisposer = term.onData(data => {
      if (ws.readyState === WebSocket.OPEN) ws.send(data)
    })

    ws.addEventListener('message', e => {
      const write = (data) => term.write(data)
      if (typeof e.data === 'string') {
        write(e.data)
      } else {
        e.data.arrayBuffer().then(buf => write(new Uint8Array(buf)))
      }
      // Use a ref (not state) to detect first message — avoids stale closure
      if (!firstMsgRef.current) {
        firstMsgRef.current = true
        setConnecting(false)
        setConnected(true)
      }
    })

    ws.addEventListener('close', () => {
      inputDisposer.dispose()
    })

    ws.addEventListener('close', () => {
      term.writeln('\r\n\x1b[33m--- disconnected ---\x1b[0m')
      firstMsgRef.current = false
      setConnected(false)
      setConnecting(false)
      wsRef.current = null
    })

    ws.addEventListener('error', () => {
      term.writeln('\r\n\x1b[31m--- connection error ---\x1b[0m')
      firstMsgRef.current = false
      setConnected(false)
      setConnecting(false)
    })
  }, [service, token, workspace, wsName, envName, disconnect])

  // When opened from a per-container Terminal button, connect straight away
  // (once) instead of waiting for the user to click Connect.
  useEffect(() => {
    if (initialService && service === initialService && termRef.current && !autoConnRef.current) {
      autoConnRef.current = true
      connect()
    }
  }, [service, connect, initialService])

  // Clean up on unmount
  useEffect(() => () => disconnect(), [disconnect])

  const canConnect = !!service && !connecting

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4">
      <div
        className="bg-surface border border-border-strong rounded-xl flex flex-col shadow-2xl"
        style={{ width: '860px', maxWidth: '100%', height: '560px' }}
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-border shrink-0">
          <span className="font-mono text-xs text-success-fg bg-surface-raised px-2 py-0.5 rounded shrink-0">
            &gt; bash
          </span>
          <span className="text-xs text-content-subtle shrink-0">{wsName} / {envName}</span>

          <div className="flex items-center gap-2 ml-auto">
            {/* Container selector — running only */}
            <select
              value={service}
              onChange={e => { setService(e.target.value); disconnect() }}
              disabled={isLoading || connected || connecting}
              className={`${CONTROL} w-full disabled:opacity-50`}
            >
              {isLoading
                ? <option value="">Loading…</option>
                : runningContainers.length === 0
                  ? <option value="">No running containers</option>
                  : <>
                      <option value="">— select container —</option>
                      {runningContainers.map(c => (
                        <option key={c.Name} value={c.Service}>{c.Service}</option>
                      ))}
                    </>
              }
            </select>

            {/* Connect / Disconnect */}
            {!connected ? (
              <Btn variant="ghost" size="xs" onClick={connect}
                disabled={!canConnect}
                
              >
                {connecting ? 'Connecting…' : 'Connect'}
              </Btn>
            ) : (
              <Btn variant="dangerSubtle" size="xs" onClick={disconnect}
                
              >
                Disconnect
              </Btn>
            )}

            {/* Connected indicator */}
            {connected && (
              <span className="flex items-center gap-1.5 text-xs text-success-fg">
                <span className="w-1.5 h-1.5 rounded-full bg-green-400 animate-pulse" />
                connected
              </span>
            )}

            {/* Close */}
            <CloseBtn onClick={() => { disconnect(); onClose() }} title="Close terminal" />
          </div>
        </div>

        {/* Error banner */}
        {error && (
          <div className="px-4 py-2 bg-danger-subtle/50 border-b border-danger-border/50 text-xs text-danger-fg shrink-0">
            {error}
          </div>
        )}

        {/* Terminal */}
        <div
          ref={mountRef}
          className="flex-1 p-2 min-h-0"
          style={{ background: 'rgb(var(--canvas))' }}
        />
      </div>
    </div>
  )
}
