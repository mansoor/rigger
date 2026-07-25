import { useState } from 'react'
import { Hint, Btn, CONTROL } from './ui'

// Shared Notification Channel add/edit form (Apprise / Email), used by both the
// admin Settings page (global channels, with a workspace allowlist) and Manage
// Workspace (workspace-owned channels). Mirrors HostForm / RegistryForm / BackupTargetForm.
//
// Props: initial, onSave(body)→Promise, onCancel, saving, showGrants, workspaces

const EMAIL_DEFAULT   = { host: '', port: 587, username: '', password: '', from: '', to: '', use_tls: true }
const APPRISE_DEFAULT = { urls: '' }
const APPRISE_EXAMPLES = `slack://TokenA/TokenB/TokenC/#channel
discord://webhook_id/webhook_token
tgram://bot_token/chat_id
json://hooks.example.com/webhook`

function Label({ children, required }) {
  return (
    <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
      {children}{required && <span className="text-danger-fg ml-0.5">*</span>}
    </label>
  )
}

function Input({ value, onChange, placeholder, type = 'text' }) {
  return (
    <input
      type={type} value={value ?? ''} onChange={e => onChange(e.target.value)} placeholder={placeholder}
      className={`${CONTROL} w-full`}
    />
  )
}

function Select({ value, onChange, options, disabled }) {
  return (
    <select value={value} onChange={e => onChange(e.target.value)} disabled={disabled}
      className={`${CONTROL} w-full disabled:opacity-50`}>
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

function Toggle({ checked, onChange, label }) {
  return (
    <label className="flex items-center gap-2 cursor-pointer select-none">
      <button type="button" onClick={() => onChange(!checked)}
        className={`relative w-9 h-5 rounded-full transition-colors ${checked ? 'bg-brand-600' : 'bg-surface-overlay'}`}>
        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-4' : ''}`} />
      </button>
      {label && <span className="text-sm text-content">{label}</span>}
    </label>
  )
}

export default function ChannelForm({ initial, onSave, onCancel, saving, showGrants = false, workspaces = [] }) {
  const isEdit = !!initial?.id
  const [name, setName]       = useState(initial?.name || '')
  const [type, setType]       = useState(initial?.type || 'apprise')
  const [enabled, setEnabled] = useState(initial?.enabled ?? true)
  const [cfg, setCfg]         = useState(() => {
    if (initial?.config) {
      try { return typeof initial.config === 'string' ? JSON.parse(initial.config) : initial.config }
      catch { /* fallthrough */ }
    }
    return (initial?.type || 'apprise') === 'email' ? { ...EMAIL_DEFAULT } : { ...APPRISE_DEFAULT }
  })
  const [error, setError] = useState('')

  const initialGrants = initial?.grants
  const startAll = !initialGrants || initialGrants.includes('*')
  const [grantMode, setGrantMode] = useState(startAll ? 'all' : 'selected')
  const [grantKeys, setGrantKeys] = useState(startAll ? [] : initialGrants.filter(g => g !== '*'))

  function setField(k, v) { setCfg(c => ({ ...c, [k]: v })) }
  function changeType(t)  { setType(t); setCfg(t === 'email' ? { ...EMAIL_DEFAULT } : { ...APPRISE_DEFAULT }) }
  function toggleGrant(k) { setGrantKeys(cur => cur.includes(k) ? cur.filter(x => x !== k) : [...cur, k]) }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim()) { setError('Name is required'); return }
    const body = { name: name.trim(), type, config: cfg, enabled }
    if (showGrants) body.grants = grantMode === 'all' ? ['*'] : grantKeys
    try { await onSave(body) }
    catch (err) { setError(err.response?.data?.error || 'Failed to save') }
  }

  return (
    <form onSubmit={submit} className="space-y-5">
      <div className="grid grid-cols-2 gap-4">
        <div>
          <Label required>Name</Label>
          <Input value={name} onChange={setName} placeholder="Ops Slack" />
        </div>
        <div>
          <Label required>Type</Label>
          <Select value={type} onChange={changeType} disabled={isEdit}
            options={[
              { value: 'apprise', label: 'Apprise (Slack, Discord, Telegram, webhook…)' },
              { value: 'email',   label: 'Email (SMTP)' },
            ]} />
        </div>
      </div>

      {type === 'apprise' && (
        <div className="space-y-2 border border-border-strong/60 rounded-lg p-4">
          <Label required>Apprise URL(s)</Label>
          <textarea
            value={cfg.urls} onChange={e => setField('urls', e.target.value)}
            placeholder={APPRISE_EXAMPLES} rows={4}
            className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle text-xs font-mono focus:outline-none focus:border-brand-500 resize-y"
          />
          <Hint>
            One Apprise URL per line. Delivered via the Apprise sidecar — see the{' '}
            <a href="https://github.com/caronc/apprise/wiki" target="_blank" rel="noreferrer" className="text-accent-text hover:underline">Apprise wiki</a>{' '}
            for the URL format of each service.
          </Hint>
        </div>
      )}

      {type === 'email' && (
        <div className="space-y-4 border border-border-strong/60 rounded-lg p-4">
          <p className="text-xs font-semibold text-content-muted uppercase tracking-wider">SMTP (sent directly by Rigger)</p>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label required>SMTP host</Label>
              <Input value={cfg.host} onChange={v => setField('host', v)} placeholder="smtp.gmail.com" />
            </div>
            <div>
              <Label required>Port</Label>
              <Input value={cfg.port} onChange={v => setField('port', parseInt(v) || 0)} type="number" placeholder="587" />
              <Hint tone="faint">465 = implicit TLS; 587/25 = STARTTLS</Hint>
            </div>
            <div>
              <Label>Username</Label>
              <Input value={cfg.username} onChange={v => setField('username', v)} placeholder="alerts@example.com" />
            </div>
            <div>
              <Label>Password</Label>
              <Input value={cfg.password} onChange={v => setField('password', v)} type="password" placeholder="••••••••" />
            </div>
            <div>
              <Label required>From</Label>
              <Input value={cfg.from} onChange={v => setField('from', v)} placeholder="Rigger <alerts@example.com>" />
            </div>
            <div>
              <Label required>To</Label>
              <Input value={cfg.to} onChange={v => setField('to', v)} placeholder="you@example.com, oncall@example.com" />
            </div>
          </div>
          <Toggle checked={cfg.use_tls} onChange={v => setField('use_tls', v)} label="Use STARTTLS (recommended)" />
        </div>
      )}

      <Toggle checked={enabled} onChange={setEnabled} label={enabled ? 'Enabled' : 'Disabled'} />

      {showGrants && (
        <div className="rounded-lg border border-border-strong bg-canvas/60 p-3 space-y-2">
          <Label>Offered to workspaces</Label>
          <div className="flex gap-4 text-sm text-content">
            <label className="flex items-center gap-2 cursor-pointer">
              <input type="radio" checked={grantMode === 'all'} onChange={() => setGrantMode('all')} className="accent-brand-500" />
              All workspaces
            </label>
            <label className="flex items-center gap-2 cursor-pointer">
              <input type="radio" checked={grantMode === 'selected'} onChange={() => setGrantMode('selected')} className="accent-brand-500" />
              Selected workspaces
            </label>
          </div>
          {grantMode === 'selected' && (
            <div className="max-h-40 overflow-y-auto space-y-1 border border-border rounded-lg p-2">
              {workspaces.length === 0 && <p className="text-xs text-content-subtle">No workspaces.</p>}
              {workspaces.map(w => (
                <label key={w.key} className="flex items-center gap-2 text-sm text-content cursor-pointer">
                  <input type="checkbox" checked={grantKeys.includes(w.key)} onChange={() => toggleGrant(w.key)} className="accent-brand-500" />
                  <span>{w.name || w.key}</span>
                  <span className="text-[10px] font-mono text-content-faint ml-auto">{w.key}</span>
                </label>
              ))}
            </div>
          )}
          <Hint>
            Controls which workspaces can pick this channel for their alert rules. Workspace-owned channels are private and aren't listed here.
          </Hint>
        </div>
      )}

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" size="md" onClick={onCancel} >Cancel</Btn>
        <Btn variant="primary" size="md" type="submit" disabled={saving} >
          {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add channel'}
        </Btn>
      </div>
    </form>
  )
}
