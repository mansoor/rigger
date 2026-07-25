import { useState } from 'react'
import { Hint, Btn, CONTROL, Label, Input, Select, Toggle } from './ui'

// Shared Backup Target add/edit form (S3 / SFTP), used by both the admin Settings
// page (global targets, with a workspace allowlist) and Manage Workspace
// (workspace-owned targets). Mirrors HostForm / RegistryForm.
//
// Props: initial, onSave(body)→Promise, onCancel, saving, showGrants, workspaces

const S3_DEFAULT = { endpoint: '', bucket: '', region: 'us-east-1', access_key: '', secret_key: '', path_prefix: 'backups/', use_ssl: true }
const SFTP_DEFAULT = { host: '', port: 22, username: '', auth_type: 'password', password: '', private_key: '', remote_path: '/backups' }





export default function BackupTargetForm({ initial, onSave, onCancel, saving, showGrants = false, workspaces = [] }) {
  const isEdit = !!initial?.id
  const [name, setName] = useState(initial?.name || '')
  const [type, setType] = useState(initial?.type || 's3')
  const [cfg, setCfg]   = useState(() => {
    if (initial?.config) {
      try { return JSON.parse(typeof initial.config === 'string' ? initial.config : JSON.stringify(initial.config)) }
      catch { /* fallthrough */ }
    }
    return (initial?.type || 's3') === 's3' ? { ...S3_DEFAULT } : { ...SFTP_DEFAULT }
  })
  const [error, setError] = useState('')

  const initialGrants = initial?.grants
  const startAll = !initialGrants || initialGrants.includes('*')
  const [grantMode, setGrantMode] = useState(startAll ? 'all' : 'selected')
  const [grantKeys, setGrantKeys] = useState(startAll ? [] : initialGrants.filter(g => g !== '*'))

  function setField(key, val) { setCfg(c => ({ ...c, [key]: val })) }
  function handleTypeChange(t) { setType(t); setCfg(t === 's3' ? { ...S3_DEFAULT } : { ...SFTP_DEFAULT }) }
  function toggleGrant(k) { setGrantKeys(cur => cur.includes(k) ? cur.filter(x => x !== k) : [...cur, k]) }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim()) { setError('Name is required'); return }
    const body = { name: name.trim(), type, config: cfg }
    if (showGrants) body.grants = grantMode === 'all' ? ['*'] : grantKeys
    try {
      await onSave(body)
    } catch (err) {
      setError(err.response?.data?.error || 'Failed to save')
    }
  }

  return (
    <form onSubmit={submit} className="space-y-5">
      <div className="grid grid-cols-2 gap-4">
        <div>
          <Label required>Name</Label>
          <Input value={name} onChange={setName} placeholder="my-s3-backup" />
        </div>
        <div>
          <Label required>Type</Label>
          <Select value={type} onChange={handleTypeChange} disabled={isEdit}
            options={[{ value: 's3', label: 'S3 / Object Storage' }, { value: 'sftp', label: 'SFTP' }]} />
        </div>
      </div>

      {type === 's3' && (
        <div className="space-y-4 border border-border-strong/60 rounded-lg p-4">
          <p className="text-xs font-semibold text-content-muted uppercase tracking-wider">S3 Configuration</p>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label required>Endpoint</Label>
              <Input value={cfg.endpoint} onChange={v => setField('endpoint', v)} placeholder="s3.amazonaws.com" />
              <Hint tone="faint">Use custom endpoint for MinIO / Wasabi / R2</Hint>
            </div>
            <div>
              <Label required>Bucket</Label>
              <Input value={cfg.bucket} onChange={v => setField('bucket', v)} placeholder="my-backups" />
            </div>
            <div>
              <Label>Region</Label>
              <Input value={cfg.region} onChange={v => setField('region', v)} placeholder="us-east-1" />
            </div>
            <div>
              <Label>Path Prefix</Label>
              <Input value={cfg.path_prefix} onChange={v => setField('path_prefix', v)} placeholder="backups/" />
            </div>
            <div>
              <Label required>Access Key</Label>
              <Input value={cfg.access_key} onChange={v => setField('access_key', v)} placeholder="AKIAIOSFODNN7EXAMPLE" />
            </div>
            <div>
              <Label required>Secret Key</Label>
              <Input value={cfg.secret_key} onChange={v => setField('secret_key', v)} type="password" placeholder="••••••••" />
            </div>
          </div>
          <Toggle switchFirst checked={cfg.use_ssl} onChange={v => setField('use_ssl', v)} label="Use SSL/TLS" />
        </div>
      )}

      {type === 'sftp' && (
        <div className="space-y-4 border border-border-strong/60 rounded-lg p-4">
          <p className="text-xs font-semibold text-content-muted uppercase tracking-wider">SFTP Configuration</p>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label required>Host</Label>
              <Input value={cfg.host} onChange={v => setField('host', v)} placeholder="backup.example.com" />
            </div>
            <div>
              <Label required>Port</Label>
              <Input value={cfg.port} onChange={v => setField('port', parseInt(v) || 22)} type="number" placeholder="22" />
            </div>
            <div>
              <Label required>Username</Label>
              <Input value={cfg.username} onChange={v => setField('username', v)} placeholder="backup" />
            </div>
            <div>
              <Label required>Remote Path</Label>
              <Input value={cfg.remote_path} onChange={v => setField('remote_path', v)} placeholder="/backups" />
            </div>
          </div>
          <div>
            <Label required>Authentication</Label>
            <Select value={cfg.auth_type} onChange={v => setField('auth_type', v)}
              options={[{ value: 'password', label: 'Password' }, { value: 'key', label: 'SSH Private Key' }]} />
          </div>
          {cfg.auth_type === 'password' && (
            <div>
              <Label required>Password</Label>
              <Input value={cfg.password} onChange={v => setField('password', v)} type="password" placeholder="••••••••" />
            </div>
          )}
          {cfg.auth_type === 'key' && (
            <div>
              <Label required>Private Key</Label>
              <textarea
                value={cfg.private_key} onChange={e => setField('private_key', e.target.value)}
                placeholder="-----BEGIN OPENSSH PRIVATE KEY-----&#10;..."
                rows={6}
                className={`${CONTROL} w-full font-mono resize-none`}
              />
            </div>
          )}
        </div>
      )}

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
            Controls which workspaces can pick this target for their backups. Workspace-owned targets are private and aren't listed here.
          </Hint>
        </div>
      )}

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" size="md" onClick={onCancel} >Cancel</Btn>
        <Btn variant="primary" size="md" type="submit" disabled={saving} >
          {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add target'}
        </Btn>
      </div>
    </form>
  )
}
