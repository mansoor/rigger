import { useState } from 'react'
import { Hint } from './ui'

// Shared Docker Registry add/edit form, used by both the admin Settings page
// (global registries, with a workspace allowlist) and Manage Workspace
// (workspace-owned registries). Mirrors HostForm.
//
// Props: initial, onSave(body)→Promise, onCancel, saving, showGrants, workspaces

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
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle text-sm focus:outline-none focus:border-brand-500 transition-colors"
    />
  )
}

export default function RegistryForm({ initial, onSave, onCancel, saving, showGrants = false, workspaces = [] }) {
  const isEdit = !!initial?.id
  const [name, setName]         = useState(initial?.name || '')
  const [url, setUrl]           = useState(initial?.url || '')
  const [username, setUsername] = useState(initial?.username || '')
  const [password, setPassword] = useState('')
  const [error, setError]       = useState('')

  const initialGrants = initial?.grants
  const startAll = !initialGrants || initialGrants.includes('*')
  const [grantMode, setGrantMode] = useState(startAll ? 'all' : 'selected')
  const [grantKeys, setGrantKeys] = useState(startAll ? [] : initialGrants.filter(g => g !== '*'))

  function toggleGrant(k) {
    setGrantKeys(cur => cur.includes(k) ? cur.filter(x => x !== k) : [...cur, k])
  }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim() || !url.trim() || !username.trim()) { setError('Name, URL, and username are required'); return }
    if (!isEdit && !password) { setError('Password is required'); return }
    const body = { name: name.trim(), url: url.trim(), username: username.trim(), password }
    if (showGrants) body.grants = grantMode === 'all' ? ['*'] : grantKeys
    try {
      await onSave(body)
    } catch (err) {
      setError(err.response?.data?.error || 'Failed to save')
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="grid grid-cols-2 gap-4">
        <div>
          <Label required>Display name</Label>
          <Input value={name} onChange={setName} placeholder="My Registry" />
        </div>
        <div>
          <Label required>Registry URL</Label>
          <Input value={url} onChange={setUrl} placeholder="registry.example.com" />
          <Hint tone="faint">e.g. docker.io, ghcr.io, registry.example.com</Hint>
        </div>
        <div>
          <Label required>Username</Label>
          <Input value={username} onChange={setUsername} placeholder="myuser" />
        </div>
        <div>
          <Label required={!isEdit}>Password / Token</Label>
          <Input value={password} onChange={setPassword} type="password" placeholder={isEdit ? '(unchanged)' : '••••••••'} />
          {isEdit && <Hint tone="faint">Leave blank to keep existing password</Hint>}
        </div>
      </div>

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
            Controls which workspaces can pick this registry for their projects. Workspace-owned registries are private and aren't listed here.
          </Hint>
        </div>
      )}

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <button type="button" onClick={onCancel}
          className="font-semibold rounded-lg transition-colors px-4 py-2 text-sm bg-surface-overlay hover:bg-surface-overlay text-content disabled:opacity-50">Cancel</button>
        <button type="submit" disabled={saving}
          className="font-semibold rounded-lg transition-colors px-4 py-2 text-sm bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50">
          {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add registry'}
        </button>
      </div>
    </form>
  )
}
