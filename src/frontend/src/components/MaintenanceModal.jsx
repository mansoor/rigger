import { useState } from 'react'
import { useQuery, useMutation } from '@tanstack/react-query'
import { fetchMaintenance, setMaintenance } from '../lib/api'
import { Hint, Btn, CloseBtn } from './ui'

// unix seconds → value for <input type="datetime-local"> (local time), and back.
function toLocalInput(unix) {
  if (!unix) return ''
  const d = new Date(unix * 1000)
  const pad = n => String(n).padStart(2, '0')
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}
function fromLocalInput(s) {
  if (!s) return 0
  const t = new Date(s).getTime()
  return Number.isNaN(t) ? 0 : Math.floor(t / 1000)
}

// MaintenanceModal — per-env maintenance mode. Toggle "now", optionally schedule a
// one-off window, customise the page title/message. Served by Rigger's proxy, so it
// works even while the environment is stopped.
export default function MaintenanceModal({ workspace, name, envName, canOp, onClose, onSaved }) {
  const { data, isLoading } = useQuery({
    queryKey: ['maintenance', workspace, name, envName],
    queryFn: () => fetchMaintenance(workspace, name, envName),
  })

  const [enabled, setEnabled] = useState(false)
  const [scheduleOn, setScheduleOn] = useState(false)
  const [start, setStart] = useState('')
  const [end, setEnd] = useState('')
  const [title, setTitle] = useState('')
  const [message, setMessage] = useState('')
  const [hydrated, setHydrated] = useState(false)
  const [err, setErr] = useState('')

  // Seed the form from the server state once.
  if (data && !hydrated) {
    setEnabled(!!data.enabled)
    setScheduleOn(!!(data.window_start && data.window_end))
    setStart(toLocalInput(data.window_start))
    setEnd(toLocalInput(data.window_end))
    setTitle(data.title || '')
    setMessage(data.message || '')
    setHydrated(true)
  }

  const save = useMutation({
    mutationFn: (body) => setMaintenance(workspace, name, envName, body),
    onSuccess: () => { onSaved?.(); onClose() },
    onError: (e) => setErr(e?.response?.data?.error || 'Save failed'),
  })

  function onSave() {
    setErr('')
    const ws = scheduleOn ? fromLocalInput(start) : 0
    const we = scheduleOn ? fromLocalInput(end) : 0
    if (scheduleOn && (!ws || !we)) { setErr('Set both a start and end time, or turn off the schedule.'); return }
    if (scheduleOn && we <= ws) { setErr('Window end must be after the start.'); return }
    save.mutate({ enabled, window_start: ws, window_end: we, title: title.trim(), message: message.trim() })
  }
  function onTurnOff() {
    setErr('')
    save.mutate({ enabled: false, window_start: 0, window_end: 0, title: title.trim(), message: message.trim() })
  }

  const active = !!data?.active
  const fieldCls = 'w-full px-2.5 py-1.5 rounded-lg bg-surface border border-border-strong text-sm text-content'

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/70 backdrop-blur-sm p-4" onClick={onClose}>
      <div className="bg-surface border border-border-strong rounded-xl w-full max-w-lg max-h-[85vh] flex flex-col" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between px-5 py-3 border-b border-border">
          <h3 className="font-semibold text-content-strong">🛠️ Maintenance — {envName}</h3>
          <CloseBtn onClick={onClose} />
        </div>

        {isLoading ? (
          <div className="p-5 text-sm text-content-muted">Loading…</div>
        ) : (
          <div className="flex-1 overflow-y-auto p-5 space-y-4">
            <div className={`text-xs rounded-lg px-3 py-2 ${active ? 'bg-amber-500/15 text-amber-300 border border-amber-500/40' : 'bg-surface-raised text-content-muted'}`}>
              {active
                ? 'Maintenance is ACTIVE — visitors see the maintenance page now.'
                : 'Maintenance is off — traffic reaches the app normally.'}
            </div>

            <label className="flex items-center gap-2 text-sm text-content">
              <input type="checkbox" className="w-4 h-4 accent-brand-500" checked={enabled}
                disabled={!canOp} onChange={e => setEnabled(e.target.checked)} />
              Maintenance now (route all traffic to the maintenance page)
            </label>

            <div className="space-y-2">
              <label className="flex items-center gap-2 text-sm text-content">
                <input type="checkbox" className="w-4 h-4 accent-brand-500" checked={scheduleOn}
                  disabled={!canOp} onChange={e => setScheduleOn(e.target.checked)} />
                Schedule a one-off window
              </label>
              {scheduleOn && (
                <div className="grid grid-cols-2 gap-3 pl-6">
                  <div>
                    <label className="block text-xs text-content-subtle mb-1">Start</label>
                    <input type="datetime-local" className={fieldCls} value={start} disabled={!canOp} onChange={e => setStart(e.target.value)} />
                  </div>
                  <div>
                    <label className="block text-xs text-content-subtle mb-1">End</label>
                    <input type="datetime-local" className={fieldCls} value={end} disabled={!canOp} onChange={e => setEnd(e.target.value)} />
                  </div>
                </div>
              )}
            </div>

            <div>
              <label className="block text-xs text-content-subtle mb-1">Page title</label>
              <input className={fieldCls} value={title} disabled={!canOp} placeholder="We'll be right back" onChange={e => setTitle(e.target.value)} />
            </div>
            <div>
              <label className="block text-xs text-content-subtle mb-1">Message</label>
              <textarea className={fieldCls + ' h-20 resize-none'} value={message} disabled={!canOp}
                placeholder="This site is undergoing scheduled maintenance. Please check back shortly."
                onChange={e => setMessage(e.target.value)} />
            </div>

            <Hint>
              Served by Rigger's proxy and returns HTTP 503 — it works even while this environment is
              stopped. Disabling maintenance also clears any scheduled window.
            </Hint>
            {err && <div className="text-xs text-danger-fg">{err}</div>}
          </div>
        )}

        <div className="flex items-center justify-between gap-2 px-5 py-3 border-t border-border">
          <Btn variant="secondary" size="sm" onClick={onTurnOff} disabled={!canOp || save.isPending} >
            Turn off
          </Btn>
          <div className="flex gap-2">
            <Btn variant="secondary" size="sm" onClick={onClose} >Cancel</Btn>
            <Btn variant="primary" size="sm" onClick={onSave} disabled={!canOp || save.isPending} >
              {save.isPending ? 'Saving…' : 'Save'}
            </Btn>
          </div>
        </div>
      </div>
    </div>
  )
}
