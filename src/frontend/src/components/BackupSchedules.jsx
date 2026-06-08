import { useState } from 'react'

// Reusable per-environment backup schedule editor (Phase 11 per-env redesign).
// Used in the New Workspace wizard and Edit Workspace. Schedules use the same
// shape the backend stores in config.environments[env].backup_schedules:
//   { id, name, services:[], interval_hours, target_id, retention, enabled }
// services empty = all data-bearing services.

export const FREQ_OPTIONS = [
  { h: 2,   label: 'Every 2 hours' },
  { h: 4,   label: 'Every 4 hours' },
  { h: 6,   label: 'Every 6 hours' },
  { h: 12,  label: 'Every 12 hours' },
  { h: 24,  label: 'Daily' },
  { h: 168, label: 'Weekly' },
]
const RETENTION_OPTIONS = [3, 7, 14, 30]

export function freqLabel(h) {
  const f = FREQ_OPTIONS.find(o => o.h === h)
  return f ? f.label : `every ${h}h`
}

const genId = () => 'bk_' + Math.random().toString(36).slice(2, 8)
const emptySchedule = (defaultTargetId = null) => ({ id: genId(), name: '', services: [], interval_hours: 24, target_id: defaultTargetId, retention: 7, enabled: true })

const inputCls = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'
const labelCls = 'block text-xs font-semibold text-content-subtle uppercase tracking-wider mb-1'

function ScheduleForm({ initial, services, targets, onSave, onCancel }) {
  const [s, setS] = useState(initial)
  const upd = (k, v) => setS(p => ({ ...p, [k]: v }))
  const allData = s.services.length === 0
  const toggleService = (id) =>
    upd('services', s.services.includes(id) ? s.services.filter(x => x !== id) : [...s.services, id])

  return (
    <div className="bg-surface-raised/40 border border-border-strong/60 rounded-lg p-4 space-y-3">
      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className={labelCls}>Name</label>
          <input className={inputCls} value={s.name} onChange={e => upd('name', e.target.value)} placeholder="e.g. DB hourly" />
        </div>
        <div>
          <label className={labelCls}>Frequency</label>
          <select className={inputCls} value={s.interval_hours} onChange={e => upd('interval_hours', parseInt(e.target.value))}>
            {FREQ_OPTIONS.map(o => <option key={o.h} value={o.h}>{o.label}</option>)}
          </select>
        </div>
      </div>

      <div>
        <label className={labelCls}>Services to back up</label>
        <label className="flex items-center gap-2 text-sm text-content mb-2 cursor-pointer select-none">
          <input type="checkbox" checked={allData} onChange={() => upd('services', [])} className="accent-brand-500" />
          All data services
        </label>
        <div className="flex flex-wrap gap-1.5">
          {services.map(sv => {
            const on = s.services.includes(sv.id)
            return (
              <button
                key={sv.id} type="button" onClick={() => toggleService(sv.id)} title={sv.hint}
                className={`px-2 py-1 rounded-lg text-xs border transition-colors ${on
                  ? 'bg-brand-600/20 border-brand-500 text-content-strong'
                  : 'bg-surface border-border-strong text-content-muted hover:text-content'}`}
              >
                {sv.label}{sv.kind === 'database' ? ' 🗄' : ''}
              </button>
            )
          })}
          {services.length === 0 && <span className="text-xs text-content-subtle">No services detected for this environment yet.</span>}
        </div>
        <p className="text-[11px] text-content-faint mt-1.5">
          {allData
            ? 'Every data-bearing service is backed up.'
            : `${s.services.length} selected. Databases are SQL-dumped; other services have their volumes archived.`}
        </p>
      </div>

      <div className="grid grid-cols-2 gap-3">
        <div>
          <label className={labelCls}>Destination</label>
          <select className={inputCls} value={s.target_id == null ? 'local' : String(s.target_id)}
            onChange={e => upd('target_id', e.target.value === 'local' ? null : parseInt(e.target.value))}>
            <option value="local">Local filesystem</option>
            {targets.map(t => <option key={t.id} value={String(t.id)}>{t.name} ({String(t.type).toUpperCase()})</option>)}
          </select>
        </div>
        <div>
          <label className={labelCls}>Keep</label>
          <select className={inputCls} value={s.retention} onChange={e => upd('retention', parseInt(e.target.value))}>
            {RETENTION_OPTIONS.map(n => <option key={n} value={n}>{n} snapshots</option>)}
          </select>
        </div>
      </div>

      <div className="flex items-center justify-between pt-1">
        <label className="flex items-center gap-2 text-sm text-content cursor-pointer select-none">
          <input type="checkbox" checked={s.enabled} onChange={e => upd('enabled', e.target.checked)} className="accent-brand-500" />
          Enabled
        </label>
        <div className="flex gap-2">
          <button type="button" onClick={onCancel} className="px-3 py-1.5 text-xs rounded-lg border border-border-strong text-content-muted hover:text-content">Cancel</button>
          <button type="button" onClick={() => onSave(s)} className="px-3 py-1.5 text-xs rounded-lg bg-brand-600 hover:bg-brand-700 text-white font-medium">Save schedule</button>
        </div>
      </div>
    </div>
  )
}

export function BackupScheduleEditor({ schedules = [], onChange, services = [], targets = [], defaultTargetId = null }) {
  const [editIdx, setEditIdx] = useState(null) // index | 'new' | null

  function save(sched) {
    if (editIdx === 'new') onChange([...(schedules || []), sched])
    else onChange((schedules || []).map((x, i) => (i === editIdx ? sched : x)))
    setEditIdx(null)
  }
  const remove = (i) => onChange((schedules || []).filter((_, idx) => idx !== i))

  const targetName = (id) => (id == null ? 'Local' : (targets.find(t => t.id === id)?.name || `target ${id}`))
  const svcLabel = (sched) => (sched.services.length === 0 ? 'all data' : sched.services.join(', '))

  return (
    <div className="space-y-2">
      {(schedules || []).map((sched, i) => (
        editIdx === i ? (
          <ScheduleForm key={sched.id || i} initial={sched} services={services} targets={targets} onSave={save} onCancel={() => setEditIdx(null)} />
        ) : (
          <div key={sched.id || i} className="flex items-center gap-3 p-3 bg-surface border border-border rounded-lg">
            <span className={`w-2 h-2 rounded-full shrink-0 ${sched.enabled ? 'bg-success' : 'bg-content-faint'}`} />
            <div className="flex-1 min-w-0">
              <p className="text-sm font-medium text-content-strong truncate">{sched.name || 'Unnamed schedule'}</p>
              <p className="text-xs text-content-subtle truncate">{svcLabel(sched)} · {freqLabel(sched.interval_hours)} · {targetName(sched.target_id)} · keep {sched.retention}</p>
            </div>
            <button type="button" onClick={() => setEditIdx(i)} className="text-xs px-2 py-1 rounded-lg border border-border-strong text-content-muted hover:text-content">Edit</button>
            <button type="button" onClick={() => remove(i)} className="text-xs px-2 py-1 rounded-lg border border-danger-border/60 text-danger-fg hover:bg-danger-subtle/40">✕</button>
          </div>
        )
      ))}

      {editIdx === 'new' ? (
        <ScheduleForm initial={emptySchedule(defaultTargetId)} services={services} targets={targets} onSave={save} onCancel={() => setEditIdx(null)} />
      ) : (
        <button type="button" onClick={() => setEditIdx('new')}
          className="w-full px-3 py-2 text-sm rounded-lg border border-dashed border-border-strong text-content-muted hover:text-content hover:border-brand-500 transition-colors">
          ＋ Add schedule
        </button>
      )}

      {(!schedules || schedules.length === 0) && editIdx !== 'new' && (
        <p className="text-xs text-content-faint">No automatic backups for this environment.</p>
      )}
    </div>
  )
}
