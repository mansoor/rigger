import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import Layout from '../components/Layout'
import {
  fetchBackupTargets, createBackupTarget, updateBackupTarget, deleteBackupTarget,
  fetchRegistries, createRegistry, updateRegistry, deleteRegistry, testRegistry,
  fetchHosts, createHost, updateHost, deleteHost, testHost, scanHost, importHost, fetchHostStats, fetchManagedHostKey,
  fetchGeneralSettings, updateGeneralSettings,
  fetchAlertRules, createAlertRule, updateAlertRule, deleteAlertRule, fetchAlertMeta,
  fetchWorkspaces,
  fetchNotificationChannels, createNotificationChannel, updateNotificationChannel,
  deleteNotificationChannel, testNotificationChannel,
} from '../lib/api'

// ── Shared primitives ─────────────────────────────────────────────────────────

function Label({ children, required }) {
  return (
    <label className="block text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">
      {children}{required && <span className="text-red-400 ml-0.5">*</span>}
    </label>
  )
}

function Input({ value, onChange, placeholder, type = 'text', disabled, ...rest }) {
  return (
    <input
      type={type} value={value ?? ''} onChange={e => onChange(e.target.value)}
      placeholder={placeholder} disabled={disabled}
      className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white placeholder-gray-500 text-sm focus:outline-none focus:border-brand-500 transition-colors disabled:opacity-50"
      {...rest}
    />
  )
}

function Select({ value, onChange, options, disabled }) {
  return (
    <select
      value={value} onChange={e => onChange(e.target.value)} disabled={disabled}
      className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white text-sm focus:outline-none focus:border-brand-500 disabled:opacity-50"
    >
      {options.map(o => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

function Toggle({ checked, onChange, label }) {
  return (
    <label className="flex items-center gap-2 cursor-pointer select-none">
      <button
        type="button" onClick={() => onChange(!checked)}
        className={`relative w-9 h-5 rounded-full transition-colors ${checked ? 'bg-brand-600' : 'bg-gray-700'}`}
      >
        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-4' : ''}`} />
      </button>
      <span className="text-sm text-gray-300">{label}</span>
    </label>
  )
}

function Btn({ onClick, disabled, variant = 'primary', children, type = 'button', size = 'md' }) {
  const base = 'font-semibold rounded-lg transition-colors focus:outline-none'
  const sizes = { sm: 'px-3 py-1.5 text-xs', md: 'px-4 py-2 text-sm' }
  const variants = {
    primary:   'bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50',
    secondary: 'bg-gray-700 hover:bg-gray-600 text-gray-200 disabled:opacity-50',
    danger:    'bg-red-900/60 hover:bg-red-800 text-red-300 disabled:opacity-50',
    ghost:     'text-gray-400 hover:text-white hover:bg-gray-800 disabled:opacity-50',
  }
  return (
    <button type={type} onClick={onClick} disabled={disabled}
      className={`${base} ${sizes[size]} ${variants[variant]}`}>
      {children}
    </button>
  )
}

function EmptyState({ icon, title, description, action }) {
  return (
    <div className="flex flex-col items-center justify-center py-16 text-center">
      <div className="text-4xl mb-3 opacity-40">{icon}</div>
      <p className="text-gray-300 font-medium mb-1">{title}</p>
      <p className="text-sm text-gray-500 mb-4">{description}</p>
      {action}
    </div>
  )
}

function ConfirmDeleteModal({ name, onConfirm, onClose, loading }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60 backdrop-blur-sm" onClick={onClose}>
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-sm mx-4 p-6 space-y-4" onClick={e => e.stopPropagation()}>
        <h3 className="font-semibold text-white">Delete {name}?</h3>
        <p className="text-sm text-gray-400">This cannot be undone.</p>
        <div className="flex gap-2 justify-end pt-2">
          <Btn variant="secondary" onClick={onClose}>Cancel</Btn>
          <Btn variant="danger" onClick={onConfirm} disabled={loading}>
            {loading ? 'Deleting…' : 'Delete'}
          </Btn>
        </div>
      </div>
    </div>
  )
}

// ── Backup Targets ─────────────────────────────────────────────────────────────

const S3_DEFAULT = { endpoint: '', bucket: '', region: 'us-east-1', access_key: '', secret_key: '', path_prefix: 'backups/', use_ssl: true }
const SFTP_DEFAULT = { host: '', port: 22, username: '', auth_type: 'password', password: '', private_key: '', remote_path: '/backups' }

function BackupTargetForm({ initial, onSave, onCancel, saving }) {
  const isEdit = !!initial?.id
  const [name, setName]   = useState(initial?.name || '')
  const [type, setType]   = useState(initial?.type || 's3')
  const [cfg, setCfg]     = useState(() => {
    if (initial?.config) {
      try { return JSON.parse(typeof initial.config === 'string' ? initial.config : JSON.stringify(initial.config)) }
      catch { /* fallthrough */ }
    }
    return type === 's3' ? { ...S3_DEFAULT } : { ...SFTP_DEFAULT }
  })
  const [error, setError] = useState('')

  function setField(key, val) { setCfg(c => ({ ...c, [key]: val })) }

  function handleTypeChange(t) {
    setType(t)
    setCfg(t === 's3' ? { ...S3_DEFAULT } : { ...SFTP_DEFAULT })
  }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim()) { setError('Name is required'); return }
    try {
      await onSave({ name: name.trim(), type, config: cfg })
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
        <div className="space-y-4 border border-gray-700/60 rounded-lg p-4">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">S3 Configuration</p>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label required>Endpoint</Label>
              <Input value={cfg.endpoint} onChange={v => setField('endpoint', v)} placeholder="s3.amazonaws.com" />
              <p className="text-xs text-gray-600 mt-1">Use custom endpoint for MinIO / Wasabi / R2</p>
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
          <Toggle checked={cfg.use_ssl} onChange={v => setField('use_ssl', v)} label="Use SSL/TLS" />
        </div>
      )}

      {type === 'sftp' && (
        <div className="space-y-4 border border-gray-700/60 rounded-lg p-4">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">SFTP Configuration</p>
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
                className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white placeholder-gray-500 text-xs font-mono focus:outline-none focus:border-brand-500 resize-none"
              />
            </div>
          )}
        </div>
      )}

      {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" onClick={onCancel}>Cancel</Btn>
        <Btn type="submit" disabled={saving}>{saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add target'}</Btn>
      </div>
    </form>
  )
}

function BackupTargetsTab() {
  const qc = useQueryClient()
  const { data: targets = [], isLoading } = useQuery({ queryKey: ['backup-targets'], queryFn: fetchBackupTargets })
  const [modal, setModal] = useState(null) // null | 'new' | { editing: target }
  const [deleting, setDeleting] = useState(null)

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateBackupTarget(id, body) : createBackupTarget(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['backup-targets'] }); setModal(null) },
  })

  const delMut = useMutation({
    mutationFn: (id) => deleteBackupTarget(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['backup-targets'] }); setDeleting(null) },
  })

  function handleSave(body) {
    const id = modal?.editing?.id
    return saveMut.mutateAsync({ id, body })
  }

  if (isLoading) return <div className="py-12 text-center text-gray-500 text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-white">Backup Targets</h2>
          <p className="text-sm text-gray-500 mt-0.5">S3-compatible object storage and SFTP destinations for workspace backups.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add target</Btn>
      </div>

      {targets.length === 0 ? (
        <EmptyState
          icon="🗄"
          title="No backup targets configured"
          description="Add an S3 bucket or SFTP server to enable off-site backups."
          action={<Btn onClick={() => setModal('new')}>＋ Add first target</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {targets.map(t => (
            <div key={t.id} className="flex items-center gap-4 p-4 bg-gray-900 border border-gray-800 rounded-xl">
              <div className="flex-shrink-0">
                <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider
                  ${t.type === 's3' ? 'bg-amber-900/60 text-amber-300' : 'bg-cyan-900/60 text-cyan-300'}`}>
                  {t.type}
                </span>
              </div>
              <div className="flex-1 min-w-0">
                <p className="text-sm font-semibold text-white">{t.name}</p>
                <p className="text-xs text-gray-500 mt-0.5 truncate">
                  {t.type === 's3'
                    ? `${t.config?.endpoint || 's3'} / ${t.config?.bucket || '—'}`
                    : `${t.config?.username || ''}@${t.config?.host || '—'}:${t.config?.port || 22}`
                  }
                </p>
              </div>
              <div className="flex items-center gap-2">
                <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: t })}>Edit</Btn>
                <Btn variant="danger" size="sm" onClick={() => setDeleting(t)}>Delete</Btn>
              </div>
            </div>
          ))}
        </div>
      )}

      {/* Add / Edit modal */}
      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-white">{modal === 'new' ? 'Add backup target' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-gray-500 hover:text-white text-xl">×</button>
            </div>
            <BackupTargetForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {/* Delete confirm */}
      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

// ── Docker Registries ─────────────────────────────────────────────────────────

function RegistryForm({ initial, onSave, onCancel, saving }) {
  const isEdit = !!initial?.id
  const [name, setName]         = useState(initial?.name || '')
  const [url, setUrl]           = useState(initial?.url || '')
  const [username, setUsername] = useState(initial?.username || '')
  const [password, setPassword] = useState('')
  const [error, setError]       = useState('')

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim() || !url.trim() || !username.trim()) { setError('Name, URL, and username are required'); return }
    if (!isEdit && !password) { setError('Password is required'); return }
    try {
      await onSave({ name: name.trim(), url: url.trim(), username: username.trim(), password })
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
          <p className="text-xs text-gray-600 mt-1">e.g. docker.io, ghcr.io, registry.example.com</p>
        </div>
        <div>
          <Label required>Username</Label>
          <Input value={username} onChange={setUsername} placeholder="myuser" />
        </div>
        <div>
          <Label required={!isEdit}>Password / Token</Label>
          <Input value={password} onChange={setPassword} type="password" placeholder={isEdit ? '(unchanged)' : '••••••••'} />
          {isEdit && <p className="text-xs text-gray-600 mt-1">Leave blank to keep existing password</p>}
        </div>
      </div>

      {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" onClick={onCancel}>Cancel</Btn>
        <Btn type="submit" disabled={saving}>{saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add registry'}</Btn>
      </div>
    </form>
  )
}

function RegistriesTab() {
  const qc = useQueryClient()
  const { data: regs = [], isLoading } = useQuery({ queryKey: ['registries'], queryFn: fetchRegistries })
  const [modal, setModal]   = useState(null)
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({}) // id -> { loading, ok, error }

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateRegistry(id, body) : createRegistry(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['registries'] }); setModal(null) },
  })

  const delMut = useMutation({
    mutationFn: (id) => deleteRegistry(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['registries'] }); setDeleting(null) },
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testRegistry(id)
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Login failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 5000)
  }

  function handleSave(body) {
    const id = modal?.editing?.id
    return saveMut.mutateAsync({ id, body })
  }

  if (isLoading) return <div className="py-12 text-center text-gray-500 text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-white">Docker Registries</h2>
          <p className="text-sm text-gray-500 mt-0.5">Pre-authenticated registries available when creating new workspaces.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add registry</Btn>
      </div>

      {regs.length === 0 ? (
        <EmptyState
          icon="📦"
          title="No registries configured"
          description="Add a Docker registry to pull private images when creating workspaces."
          action={<Btn onClick={() => setModal('new')}>＋ Add first registry</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {regs.map(r => {
            const ts = testStatus[r.id]
            return (
              <div key={r.id} className="flex items-center gap-4 p-4 bg-gray-900 border border-gray-800 rounded-xl">
                <div className="flex-shrink-0 w-8 h-8 rounded-lg bg-gray-800 flex items-center justify-center text-sm">
                  📦
                </div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-semibold text-white">{r.name}</p>
                  <p className="text-xs text-gray-500 mt-0.5">{r.url} · {r.username}</p>
                </div>
                <div className="flex items-center gap-2">
                  {ts?.loading && <span className="text-xs text-gray-500">Testing…</span>}
                  {ts?.ok && <span className="text-xs text-green-400">✓ Connected</span>}
                  {ts?.error && <span className="text-xs text-red-400 max-w-[180px] truncate" title={ts.error}>{ts.error}</span>}
                  <Btn variant="ghost" size="sm" onClick={() => handleTest(r.id)} disabled={ts?.loading}>Test</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: r })}>Edit</Btn>
                  <Btn variant="danger" size="sm" onClick={() => setDeleting(r)}>Delete</Btn>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-white">{modal === 'new' ? 'Add registry' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-gray-500 hover:text-white text-xl">×</button>
            </div>
            <RegistryForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

// ── Hosts (Phase 7: Multi-Host Support) ───────────────────────────────────────

function HostForm({ initial, onSave, onCancel, saving }) {
  const isEdit = !!initial?.id
  const [name, setName]       = useState(initial?.name || '')
  const [address, setAddress] = useState(initial?.address || '')
  const [port, setPort]       = useState(initial?.ssh_port || 22)
  const [user, setUser]       = useState(initial?.ssh_user || '')
  const [wsDir, setWsDir]     = useState(initial?.workspaces_dir || '')
  const [key, setKey]         = useState('')
  const [managed, setManaged] = useState(false)
  const [copied, setCopied]   = useState(false)
  const [error, setError]     = useState('')

  // The Rigger-managed public key — fetched lazily when the toggle is turned on.
  const { data: managedKey } = useQuery({
    queryKey: ['managed-host-key'],
    queryFn: fetchManagedHostKey,
    enabled: managed,
    staleTime: Infinity,
  })
  const pubKey = managedKey?.public_key || ''
  const installCmd = pubKey
    ? `mkdir -p ~/.ssh && echo '${pubKey}' >> ~/.ssh/authorized_keys && chmod 600 ~/.ssh/authorized_keys`
    : ''

  function copy(text) {
    navigator.clipboard?.writeText(text)
    setCopied(true); setTimeout(() => setCopied(false), 2000)
  }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim() || !address.trim() || !user.trim()) { setError('Name, address, and SSH user are required'); return }
    if (!isEdit && !managed && !key.trim()) { setError('Paste an SSH private key or enable the Rigger-managed key'); return }
    try {
      await onSave({
        name: name.trim(), address: address.trim(), ssh_port: Number(port) || 22,
        ssh_user: user.trim(), ssh_key: managed ? '' : key, use_managed_key: managed,
        workspaces_dir: wsDir.trim(),
      })
    } catch (err) {
      setError(err.response?.data?.error || 'Failed to save')
    }
  }

  return (
    <form onSubmit={submit} className="space-y-4">
      <div className="grid grid-cols-2 gap-4">
        <div>
          <Label required>Display name</Label>
          <Input value={name} onChange={setName} placeholder="prod-server-1" />
        </div>
        <div>
          <Label required>Address</Label>
          <Input value={address} onChange={setAddress} placeholder="10.0.0.5 or host.example.com" />
        </div>
        <div>
          <Label required>SSH user</Label>
          <Input value={user} onChange={setUser} placeholder="root" />
        </div>
        <div>
          <Label>SSH port</Label>
          <Input value={port} onChange={setPort} type="number" placeholder="22" />
        </div>
      </div>

      <div>
        <Label>Remote workspaces directory</Label>
        <Input value={wsDir} onChange={setWsDir} placeholder="/opt/rigger/workspaces" />
        <p className="text-xs text-gray-600 mt-1">
          Absolute path on the host where workspaces live (for scan/import) and are pushed (for deploy/migrate).
          Leave blank to use the server default (<code className="font-mono">REMOTE_WORKSPACES_DIR</code>).
        </p>
      </div>

      {/* Rigger-managed key toggle */}
      <label className="flex items-center gap-2.5 cursor-pointer select-none">
        <input type="checkbox" checked={managed} onChange={e => setManaged(e.target.checked)} className="accent-brand-500" />
        <span className="text-sm text-gray-200">Use Rigger-managed key</span>
        <span className="text-xs text-gray-500">— Rigger holds the private key; you just install its public key on the host</span>
      </label>

      {managed ? (
        <div className="space-y-2 rounded-lg border border-gray-700 bg-gray-950/60 p-3">
          <p className="text-xs text-gray-400">
            1. Run this on <strong className="text-gray-300">{user || 'the host'}@{address || 'the host'}</strong> to authorize Rigger:
          </p>
          <div className="flex items-start gap-2">
            <pre className="flex-1 overflow-auto bg-gray-950 border border-gray-800 rounded-lg p-2 text-[11px] font-mono text-gray-300 whitespace-pre-wrap">{installCmd || 'Loading Rigger public key…'}</pre>
            <button type="button" onClick={() => copy(installCmd)} disabled={!installCmd}
              className="shrink-0 px-2.5 py-1.5 text-xs bg-gray-800 hover:bg-gray-700 text-gray-300 rounded-lg disabled:opacity-40">
              {copied ? '✓' : 'Copy'}
            </button>
          </div>
          <p className="text-xs text-gray-500">
            2. Then add the host and click <strong className="text-gray-400">Test</strong>. The host only needs Docker + SSH.
            (The private key never leaves Rigger.)
          </p>
        </div>
      ) : (
        <div>
          <Label required={!isEdit}>SSH private key</Label>
          <textarea
            value={key} onChange={e => setKey(e.target.value)}
            rows={6} spellCheck={false}
            placeholder={isEdit ? '(unchanged — paste a new key to replace)' : '-----BEGIN OPENSSH PRIVATE KEY-----'}
            className="w-full bg-gray-950 border border-gray-700 rounded-lg px-3 py-2 text-xs font-mono text-gray-200 focus:border-brand-500 focus:outline-none"
          />
          <div className="text-xs text-gray-600 mt-1 space-y-1">
            <p>
              Paste the <strong>private</strong> key Rigger should log in with — its public half must be in the SSH user's
              <code className="font-mono"> ~/.ssh/authorized_keys</code> on the host. Must have <strong>no passphrase</strong>.
              Stored encrypted at rest.{isEdit && ' Leave blank to keep the existing key.'}
            </p>
            <p className="text-gray-500">
              Generate one with <code className="font-mono">ssh-keygen -t ed25519 -N "" -f rigger_host</code> — paste
              <code className="font-mono"> rigger_host</code> here and install <code className="font-mono">rigger_host.pub</code> on the host.
            </p>
          </div>
        </div>
      )}

      {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" onClick={onCancel}>Cancel</Btn>
        <Btn type="submit" disabled={saving}>{saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add host'}</Btn>
      </div>
    </form>
  )
}

function HostsTab() {
  const qc = useQueryClient()
  const { data: hosts = [], isLoading } = useQuery({ queryKey: ['hosts'], queryFn: fetchHosts })
  const [modal, setModal]       = useState(null)
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({}) // id -> { loading, ok, msg, error }
  const [scanning, setScanning] = useState(null)    // host being scanned (modal)
  const [health, setHealth]     = useState(null)    // host whose health is shown (modal)

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateHost(id, body) : createHost(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['hosts'] }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteHost(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['hosts'] }); setDeleting(null) },
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      const res = await testHost(id)
      if (res.status === 'ok') setTestStatus(s => ({ ...s, [id]: { ok: true, msg: res.message } }))
      else setTestStatus(s => ({ ...s, [id]: { error: res.error || 'Connection failed' } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Connection failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 8000)
  }

  function handleSave(body) {
    return saveMut.mutateAsync({ id: modal?.editing?.id, body })
  }

  if (isLoading) return <div className="py-12 text-center text-gray-500 text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-white">Remote Hosts</h2>
          <p className="text-sm text-gray-500 mt-0.5">Manage Docker workloads on other servers over SSH. Hosts need only Docker + SSH.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add host</Btn>
      </div>

      {hosts.length === 0 ? (
        <EmptyState
          icon="🖥️"
          title="No remote hosts"
          description="Register a server to deploy and operate workspaces on it from this control plane."
          action={<Btn onClick={() => setModal('new')}>＋ Add first host</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {hosts.map(host => {
            const ts = testStatus[host.id]
            return (
              <div key={host.id} className="flex items-center gap-4 p-4 bg-gray-900 border border-gray-800 rounded-xl">
                <div className="flex-shrink-0 w-8 h-8 rounded-lg bg-gray-800 flex items-center justify-center text-sm">🖥️</div>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-semibold text-white">{host.name}</p>
                  <p className="text-xs text-gray-500 mt-0.5">{host.ssh_user}@{host.address}:{host.ssh_port}</p>
                </div>
                <div className="flex items-center gap-2">
                  {ts?.loading && <span className="text-xs text-gray-500">Testing…</span>}
                  {ts?.ok && <span className="text-xs text-green-400 max-w-[200px] truncate" title={ts.msg}>✓ {ts.msg}</span>}
                  {ts?.error && <span className="text-xs text-red-400 max-w-[200px] truncate" title={ts.error}>{ts.error}</span>}
                  <Btn variant="ghost" size="sm" onClick={() => handleTest(host.id)} disabled={ts?.loading}>Test</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setHealth(host)}>Health</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setScanning(host)}>Scan</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: host })}>Edit</Btn>
                  <Btn variant="danger" size="sm" onClick={() => setDeleting(host)}>Delete</Btn>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-white">{modal === 'new' ? 'Add host' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-gray-500 hover:text-white text-xl">×</button>
            </div>
            <HostForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}

      {scanning && (
        <ScanHostModal
          host={scanning}
          onClose={() => setScanning(null)}
          onImported={() => { qc.invalidateQueries({ queryKey: ['workspaces'] }) }}
        />
      )}

      {health && (
        <HostStatsModal host={health} onClose={() => setHealth(null)} />
      )}
    </div>
  )
}

// HostStatsModal fetches and displays a remote host's Docker + system health.
function HostStatsModal({ host, onClose }) {
  const { data, isLoading, error } = useQuery({
    queryKey: ['host-stats', host.id],
    queryFn: () => fetchHostStats(host.id),
    refetchInterval: 5000,
  })
  const failed = error || data?.status === 'error'
  const d = data?.docker || {}
  const hs = data?.host || {}

  const fmtUptime = (s) => {
    if (!s) return '—'
    const days = Math.floor(s / 86400), hrs = Math.floor((s % 86400) / 3600)
    return days > 0 ? `${days}d ${hrs}h` : `${hrs}h ${Math.floor((s % 3600) / 60)}m`
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-5">
          <h3 className="font-semibold text-white">Health · {host.name}</h3>
          <button onClick={onClose} className="text-gray-500 hover:text-white text-xl">×</button>
        </div>

        {isLoading && <div className="py-8 text-center text-gray-500 text-sm">Loading…</div>}
        {!isLoading && failed && (
          <div className="py-3 px-4 bg-red-500/10 border border-red-500/30 rounded-lg text-sm text-red-400">
            {data?.error || error?.response?.data?.error || 'Failed to reach host'}
          </div>
        )}

        {!isLoading && !failed && (
          <div className="space-y-4">
            <div>
              <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">Docker</p>
              {d.error ? (
                <p className="text-sm text-red-400">{d.error}</p>
              ) : (
                <div className="grid grid-cols-2 gap-3">
                  <StatCell label="Server" value={d.server_version || '—'} />
                  <StatCell label="Storage driver" value={d.storage_driver || '—'} />
                  <StatCell label="Containers" value={`${d.containers_running || 0} up · ${d.containers_stopped || 0} stopped`} />
                  <StatCell label="Images" value={d.images_total ?? 0} />
                  <StatCell label="Volumes" value={d.volumes_total ?? 0} />
                  <StatCell label="Networks" value={d.networks_total ?? 0} />
                </div>
              )}
            </div>
            <div>
              <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-2">System</p>
              <div className="grid grid-cols-2 gap-3">
                <StatCell label="OS" value={hs.os || '—'} />
                <StatCell label="Arch · CPUs" value={`${hs.arch || '—'} · ${hs.cpus || 0}`} />
                <StatCell label="Memory" value={`${(hs.mem_used_pct || 0).toFixed(0)}% of ${(hs.mem_total_mb / 1024 || 0).toFixed(1)} GB`} />
                <StatCell label="Disk" value={`${(hs.disk_used_pct || 0).toFixed(0)}% of ${(hs.disk_total_gb || 0).toFixed(0)} GB`} />
                <StatCell label="Uptime" value={fmtUptime(hs.uptime_seconds)} />
              </div>
            </div>
            <div className="flex justify-end pt-1"><Btn variant="ghost" onClick={onClose}>Close</Btn></div>
          </div>
        )}
      </div>
    </div>
  )
}

function StatCell({ label, value }) {
  return (
    <div className="bg-gray-950/50 border border-gray-800 rounded-lg px-3 py-2">
      <p className="text-[11px] text-gray-500 uppercase tracking-wider">{label}</p>
      <p className="text-sm text-white mt-0.5 truncate" title={String(value)}>{value}</p>
    </div>
  )
}

// ScanHostModal lists workspaces discovered on a remote host and imports the
// selected ones (caches config.json locally + associates the workspace).
function ScanHostModal({ host, onClose, onImported }) {
  const [loading, setLoading]   = useState(true)
  const [error, setError]       = useState(null)
  const [rows, setRows]         = useState([])      // discovered workspaces
  const [sel, setSel]           = useState({})      // name -> bool
  const [importing, setImporting] = useState(false)
  const [done, setDone]         = useState(null)    // { imported, errors }

  useState(() => {
    scanHost(host.id)
      .then(res => {
        if (res.status !== 'ok') { setError(res.error || 'Scan failed'); return }
        const ws = res.workspaces || []
        setRows(ws)
        const init = {}
        ws.forEach(w => { if (!w.imported) init[w.name] = true })
        setSel(init)
      })
      .catch(err => setError(err.response?.data?.error || 'Scan failed'))
      .finally(() => setLoading(false))
  })

  const chosen = rows.filter(w => sel[w.name]).map(w => w.name)

  async function doImport() {
    setImporting(true)
    try {
      const res = await importHost(host.id, chosen)
      setDone(res)
      onImported?.()
    } catch (err) {
      setError(err.response?.data?.error || 'Import failed')
    }
    setImporting(false)
  }

  return (
    <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
      <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-lg mx-4 p-6" onClick={e => e.stopPropagation()}>
        <div className="flex items-center justify-between mb-5">
          <h3 className="font-semibold text-white">Scan "{host.name}"</h3>
          <button onClick={onClose} className="text-gray-500 hover:text-white text-xl">×</button>
        </div>

        {loading && <div className="py-8 text-center text-gray-500 text-sm">Scanning host…</div>}
        {error && <div className="py-3 px-4 mb-4 bg-red-500/10 border border-red-500/30 rounded-lg text-sm text-red-400">{error}</div>}

        {!loading && !error && (
          done ? (
            <div className="text-sm text-gray-300 space-y-2">
              <p className="text-green-400">✓ Imported {done.imported?.length || 0} workspace(s).</p>
              {done.errors && Object.keys(done.errors).length > 0 && (
                <ul className="text-red-400 text-xs space-y-1">
                  {Object.entries(done.errors).map(([n, e]) => <li key={n}>{n}: {e}</li>)}
                </ul>
              )}
              <div className="flex justify-end pt-2"><Btn onClick={onClose}>Done</Btn></div>
            </div>
          ) : rows.length === 0 ? (
            <div className="py-8 text-center text-gray-500 text-sm">No workspaces found in the remote workspaces directory.</div>
          ) : (
            <>
              <p className="text-xs text-gray-500 mb-3">Select workspaces to import. Already-imported workspaces are disabled.</p>
              <div className="space-y-1.5 max-h-72 overflow-y-auto">
                {rows.map(w => (
                  <label key={w.name} className={`flex items-center gap-3 p-3 rounded-lg border ${w.imported ? 'border-gray-800 bg-gray-900/50 opacity-60' : 'border-gray-800 bg-gray-900 cursor-pointer hover:border-gray-700'}`}>
                    <input
                      type="checkbox"
                      disabled={w.imported}
                      checked={!!sel[w.name]}
                      onChange={e => setSel(s => ({ ...s, [w.name]: e.target.checked }))}
                    />
                    <div className="flex-1 min-w-0">
                      <p className="text-sm font-medium text-white">{w.name}{w.imported && <span className="ml-2 text-xs text-gray-500">(imported)</span>}</p>
                      <p className="text-xs text-gray-500 mt-0.5">{w.project} · {w.type} · {w.envs?.join(', ') || 'no envs'}</p>
                    </div>
                  </label>
                ))}
              </div>
              <div className="flex justify-end gap-2 pt-4">
                <Btn variant="ghost" onClick={onClose}>Cancel</Btn>
                <Btn onClick={doImport} disabled={importing || chosen.length === 0}>
                  {importing ? 'Importing…' : `Import ${chosen.length || ''}`}
                </Btn>
              </div>
            </>
          )
        )}
      </div>
    </div>
  )
}

// ── General Settings Tab ──────────────────────────────────────────────────────

function GeneralTab() {
  const qc = useQueryClient()
  const { data: cfg = {}, isLoading } = useQuery({
    queryKey: ['general-settings'],
    queryFn: fetchGeneralSettings,
  })
  const [acmeEmail, setAcmeEmail] = useState('')
  const [riggerDomain, setRiggerDomain] = useState('')
  const [confirmDestructive, setConfirmDestructive] = useState(true)

  const saveMut = useMutation({
    mutationFn: () => updateGeneralSettings({
      acme_email: acmeEmail,
      rigger_domain: riggerDomain,
      confirm_destructive: confirmDestructive ? 'true' : 'false',
    }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['general-settings'] }),
  })

  // Sync from loaded data when it arrives
  const [synced, setSynced] = useState(false)
  if (!isLoading && !synced && cfg.acme_email !== undefined) {
    setAcmeEmail(cfg.acme_email || '')
    setRiggerDomain(cfg.rigger_domain || '')
    // Default ON — only an explicit "false" disables confirmations.
    setConfirmDestructive(cfg.confirm_destructive !== 'false')
    setSynced(true)
  }

  if (isLoading) return <div className="py-12 text-center text-gray-500 text-sm">Loading…</div>

  return (
    <div className="space-y-8 max-w-2xl">
      {/* SSL / Let's Encrypt */}
      <div>
        <h2 className="text-base font-semibold text-white mb-1">SSL Certificates — Let's Encrypt</h2>
        <p className="text-sm text-gray-500 mb-4">
          Traefik automatically issues and renews certificates via Let's Encrypt. Set your email
          below — it is sent to Let's Encrypt for cert expiry notifications and account recovery.
        </p>

        <div className="space-y-4 p-4 bg-gray-900 border border-gray-800 rounded-xl">
          <div>
            <Label required>ACME email address</Label>
            <Input
              value={acmeEmail}
              onChange={setAcmeEmail}
              placeholder="admin@example.com"
              type="email"
            />
            <p className="text-xs text-gray-500 mt-1">
              Must match the <code className="font-mono text-xs">ACME_EMAIL</code> value in{' '}
              <code className="font-mono text-xs">src/.env</code>. Traefik reads it from there;
              this field stores it for reference and future automation.
            </p>
          </div>

          <div className="px-4 py-3 bg-amber-950/40 border border-amber-800/50 rounded-lg">
            <p className="text-xs text-amber-300 font-semibold mb-1">Requirements for SSL to work</p>
            <ul className="text-xs text-amber-400 space-y-0.5 list-disc pl-4">
              <li>Port 80 must be publicly reachable (for the HTTP-01 ACME challenge)</li>
              <li>Each domain must have a DNS A record pointing to this server</li>
              <li>Let's Encrypt rate limits: max 5 certs per domain per week</li>
            </ul>
          </div>
        </div>
      </div>

      {/* Rigger domain */}
      <div>
        <h2 className="text-base font-semibold text-white mb-1">Rigger UI Domain</h2>
        <p className="text-sm text-gray-500 mb-4">
          Optionally expose the Rigger UI itself through Traefik with an SSL cert.
          After setting this, uncomment the <code className="font-mono text-xs">labels</code> block
          in <code className="font-mono text-xs">src/docker-compose.yml</code> and rebuild.
        </p>

        <div className="p-4 bg-gray-900 border border-gray-800 rounded-xl">
          <Label>Rigger UI domain</Label>
          <Input
            value={riggerDomain}
            onChange={setRiggerDomain}
            placeholder="rigger.example.com"
          />
          <p className="text-xs text-gray-500 mt-1">
            Leave blank to access Rigger UI on port {' '}
            <code className="font-mono text-xs">RIGGER_PORT</code> only.
          </p>
        </div>
      </div>

      {/* Confirmations */}
      <div>
        <h2 className="text-base font-semibold text-white mb-1">Confirmations</h2>
        <p className="text-sm text-gray-500 mb-4">
          Show a confirmation dialog before destructive actions — removing a service or
          environment, deleting backups/archives, clearing history, inactivating a stack, and the like.
        </p>
        <div className="p-4 bg-gray-900 border border-gray-800 rounded-xl">
          <Toggle
            checked={confirmDestructive}
            onChange={setConfirmDestructive}
            label="Confirm before destructive actions"
          />
          <p className="text-xs text-gray-500 mt-2">
            Recommended (on by default). Turn off to skip these prompts. Stronger safeguards —
            type-to-confirm workspace deletion and the Housekeeping prune flows — always stay on.
          </p>
        </div>
      </div>

      {/* Save */}
      <div className="flex items-center gap-3">
        <Btn onClick={() => saveMut.mutate()} disabled={saveMut.isPending}>
          {saveMut.isPending ? 'Saving…' : 'Save settings'}
        </Btn>
        {saveMut.isSuccess && (
          <span className="text-xs text-green-400">✓ Saved</span>
        )}
      </div>
    </div>
  )
}

// ── Alert Rules (Phase 6a) ──────────────────────────────────────────────────────

const SEVERITY_BADGE = {
  critical: 'bg-red-500/15 text-red-300 border-red-800/50',
  warning:  'bg-amber-500/15 text-amber-300 border-amber-700/50',
  info:     'bg-blue-500/15 text-blue-300 border-blue-700/50',
}

function RuleForm({ initial, meta, workspaces, channels = [], onSave, onCancel, saving }) {
  const conditions = meta?.conditions || []
  const isEdit = !!initial?.id

  const [name, setName]                 = useState(initial?.name || '')
  const [conditionType, setConditionType] = useState(initial?.condition_type || conditions[0]?.value || 'container_down')
  const [threshold, setThreshold]       = useState(initial?.threshold ?? 80)
  const [severity, setSeverity]         = useState(initial?.severity || 'warning')
  const [workspace, setWorkspace]       = useState(initial?.workspace || '')
  const [env, setEnv]                   = useState(initial?.env || '')
  const [cooldown, setCooldown]         = useState(initial?.cooldown_minutes ?? 15)
  const [enabled, setEnabled]           = useState(initial?.enabled ?? true)
  const [notifyIds, setNotifyIds]       = useState(initial?.notify_channel_ids || [])
  const [error, setError]               = useState('')

  function toggleChannel(id) {
    setNotifyIds(ids => ids.includes(id) ? ids.filter(x => x !== id) : [...ids, id])
  }

  const cond      = conditions.find(c => c.value === conditionType) || {}
  const isHost    = cond.scope === 'host'
  const isNumeric = !!cond.numeric

  const wsObj = workspaces.find(w => w.name === workspace)
  const envOptions = [{ value: '', label: 'All environments' },
    ...((wsObj?.envs || []).map(e => ({ value: e, label: e })))]
  const wsOptions = [{ value: '', label: 'All workspaces' },
    ...workspaces.map(w => ({ value: w.name, label: w.name }))]

  function changeWorkspace(v) {
    setWorkspace(v)
    if (!v) setEnv('') // "all workspaces" can't target a specific env
  }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim()) { setError('Name is required'); return }
    if (isNumeric && Number(threshold) <= 0) { setError('Threshold must be greater than 0'); return }
    const body = {
      name: name.trim(),
      condition_type: conditionType,
      threshold: isNumeric ? Number(threshold) : 0,
      workspace: isHost ? '' : workspace,
      env: (isHost || !workspace) ? '' : env,
      severity,
      cooldown_minutes: Number(cooldown) || 15,
      enabled,
      notify_channel_ids: notifyIds,
    }
    try { await onSave(body) }
    catch (err) { setError(err.response?.data?.error || 'Failed to save') }
  }

  return (
    <form onSubmit={submit} className="space-y-5">
      <div>
        <Label required>Rule name</Label>
        <Input value={name} onChange={setName} placeholder="Production stack down" />
      </div>

      <div className="grid grid-cols-2 gap-4">
        <div>
          <Label required>Condition</Label>
          <Select value={conditionType} onChange={setConditionType}
            options={conditions.map(c => ({ value: c.value, label: c.label }))} />
        </div>
        <div>
          <Label required>Severity</Label>
          <Select value={severity} onChange={setSeverity}
            options={(meta?.severities || ['info', 'warning', 'critical']).map(s => ({ value: s, label: s[0].toUpperCase() + s.slice(1) }))} />
        </div>
      </div>

      {isNumeric && (
        <div>
          <Label required>Threshold {cond.unit ? `(${cond.unit})` : ''}</Label>
          <Input value={threshold} onChange={v => setThreshold(v)} type="number" placeholder="80" />
        </div>
      )}

      {/* Targeting — hidden for host-scoped conditions (e.g. disk) */}
      {isHost ? (
        <div className="px-4 py-3 bg-gray-800/40 border border-gray-700/60 rounded-lg">
          <p className="text-xs text-gray-400">This condition is evaluated against the <span className="text-gray-200 font-medium">host</span> and applies globally.</p>
        </div>
      ) : (
        <div className="grid grid-cols-2 gap-4">
          <div>
            <Label>Workspace</Label>
            <Select value={workspace} onChange={changeWorkspace} options={wsOptions} />
          </div>
          <div>
            <Label>Environment</Label>
            <Select value={env} onChange={setEnv} options={envOptions} disabled={!workspace} />
            {!workspace && <p className="text-xs text-gray-600 mt-1">Applies to all environments.</p>}
          </div>
        </div>
      )}

      <div className="grid grid-cols-2 gap-4 items-end">
        <div>
          <Label>Cooldown (minutes)</Label>
          <Input value={cooldown} onChange={v => setCooldown(v)} type="number" placeholder="15" />
          <p className="text-xs text-gray-600 mt-1">Minimum gap before re-firing for the same target.</p>
        </div>
        <div className="pb-2">
          <Toggle checked={enabled} onChange={setEnabled} label={enabled ? 'Enabled' : 'Disabled'} />
        </div>
      </div>

      {/* Notify channels */}
      <div>
        <Label>Notify channels</Label>
        {channels.length === 0 ? (
          <p className="text-xs text-gray-600 mt-1">
            No channels yet — add one on the <span className="text-gray-400">Notifications</span> tab to deliver this alert. The alert still shows in the inbox without a channel.
          </p>
        ) : (
          <div className="flex flex-wrap gap-2 mt-1">
            {channels.map(ch => {
              const on = notifyIds.includes(ch.id)
              return (
                <button
                  key={ch.id} type="button" onClick={() => toggleChannel(ch.id)}
                  className={`px-2.5 py-1 rounded-lg text-xs font-medium border transition-colors ${
                    on ? 'bg-brand-600/20 border-brand-500 text-brand-300'
                       : 'bg-gray-800 border-gray-700 text-gray-400 hover:text-gray-200'
                  }`}
                >
                  {on ? '✓ ' : ''}{ch.name}
                  <span className="ml-1 text-gray-500">{ch.type}</span>
                </button>
              )
            })}
          </div>
        )}
      </div>

      {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" onClick={onCancel}>Cancel</Btn>
        <Btn type="submit" disabled={saving}>{saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add rule'}</Btn>
      </div>
    </form>
  )
}

function RulesTab() {
  const qc = useQueryClient()
  const { data: rules = [], isLoading } = useQuery({ queryKey: ['alert-rules'], queryFn: fetchAlertRules })
  const { data: meta }       = useQuery({ queryKey: ['alert-meta'], queryFn: fetchAlertMeta })
  const { data: workspaces = [] } = useQuery({ queryKey: ['workspaces'], queryFn: fetchWorkspaces })
  const { data: channels = [] } = useQuery({ queryKey: ['notification-channels'], queryFn: fetchNotificationChannels })
  const [modal, setModal]       = useState(null) // null | 'new' | { editing: rule }
  const [deleting, setDeleting] = useState(null)

  const condLabel = (v) => meta?.conditions?.find(c => c.value === v)?.label || v
  const condUnit  = (v) => meta?.conditions?.find(c => c.value === v)?.unit || ''

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateAlertRule(id, body) : createAlertRule(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['alert-rules'] }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteAlertRule(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['alert-rules'] }); setDeleting(null) },
  })
  const toggleMut = useMutation({
    mutationFn: (rule) => updateAlertRule(rule.id, { ...rule, enabled: !rule.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['alert-rules'] }),
  })

  function handleSave(body) {
    return saveMut.mutateAsync({ id: modal?.editing?.id, body })
  }

  function targetLabel(r) {
    if (!r.workspace) return 'All workspaces'
    return r.env ? `${r.workspace} / ${r.env}` : `${r.workspace} (all envs)`
  }

  if (isLoading) return <div className="py-12 text-center text-gray-500 text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-white">Alert Rules</h2>
          <p className="text-sm text-gray-500 mt-0.5">Conditions evaluated every 60s. Matches open an alert in the inbox; clearing auto-resolves it.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add rule</Btn>
      </div>

      {rules.length === 0 ? (
        <EmptyState
          icon="🔔"
          title="No alert rules yet"
          description="Create a rule to be notified when a container goes down, disk fills up, a backup fails, and more."
          action={<Btn onClick={() => setModal('new')}>＋ Add first rule</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {rules.map(r => (
            <div key={r.id} className="flex items-center gap-4 p-4 bg-gray-900 border border-gray-800 rounded-xl">
              <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider border ${SEVERITY_BADGE[r.severity] || SEVERITY_BADGE.warning}`}>
                {r.severity}
              </span>
              <div className="flex-1 min-w-0">
                <p className="text-sm font-semibold text-white truncate">{r.name}</p>
                <p className="text-xs text-gray-500 mt-0.5 truncate">
                  {condLabel(r.condition_type)}
                  {r.threshold > 0 ? ` ${r.threshold}${condUnit(r.condition_type)}` : ''} · {targetLabel(r)}
                  {r.notify_channel_ids?.length > 0 && <span className="text-gray-400"> · 🔔 {r.notify_channel_ids.length}</span>}
                </p>
              </div>
              <Toggle checked={r.enabled} onChange={() => toggleMut.mutate(r)} label="" />
              <div className="flex items-center gap-2">
                <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: r })}>Edit</Btn>
                <Btn variant="danger" size="sm" onClick={() => setDeleting(r)}>Delete</Btn>
              </div>
            </div>
          ))}
        </div>
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-white">{modal === 'new' ? 'Add alert rule' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-gray-500 hover:text-white text-xl">×</button>
            </div>
            <RuleForm
              initial={modal === 'new' ? null : modal.editing}
              meta={meta}
              workspaces={workspaces}
              channels={channels}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

// ── Notification Channels (Phase 6b) ────────────────────────────────────────────

const EMAIL_DEFAULT   = { host: '', port: 587, username: '', password: '', from: '', to: '', use_tls: true }
const APPRISE_DEFAULT = { urls: '' }

const APPRISE_EXAMPLES = `slack://TokenA/TokenB/TokenC/#channel
discord://webhook_id/webhook_token
tgram://bot_token/chat_id
json://hooks.example.com/webhook`

function ChannelForm({ initial, onSave, onCancel, saving }) {
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

  function setField(k, v) { setCfg(c => ({ ...c, [k]: v })) }
  function changeType(t)  { setType(t); setCfg(t === 'email' ? { ...EMAIL_DEFAULT } : { ...APPRISE_DEFAULT }) }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim()) { setError('Name is required'); return }
    try { await onSave({ name: name.trim(), type, config: cfg, enabled }) }
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
        <div className="space-y-2 border border-gray-700/60 rounded-lg p-4">
          <Label required>Apprise URL(s)</Label>
          <textarea
            value={cfg.urls} onChange={e => setField('urls', e.target.value)}
            placeholder={APPRISE_EXAMPLES} rows={4}
            className="w-full px-3 py-2 bg-gray-800 border border-gray-700 rounded-lg text-white placeholder-gray-600 text-xs font-mono focus:outline-none focus:border-brand-500 resize-y"
          />
          <p className="text-xs text-gray-500">
            One Apprise URL per line. Delivered via the Apprise sidecar — see the{' '}
            <a href="https://github.com/caronc/apprise/wiki" target="_blank" rel="noreferrer" className="text-brand-400 hover:underline">Apprise wiki</a>{' '}
            for the URL format of each service.
          </p>
        </div>
      )}

      {type === 'email' && (
        <div className="space-y-4 border border-gray-700/60 rounded-lg p-4">
          <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider">SMTP (sent directly by Rigger)</p>
          <div className="grid grid-cols-2 gap-4">
            <div>
              <Label required>SMTP host</Label>
              <Input value={cfg.host} onChange={v => setField('host', v)} placeholder="smtp.gmail.com" />
            </div>
            <div>
              <Label required>Port</Label>
              <Input value={cfg.port} onChange={v => setField('port', parseInt(v) || 0)} type="number" placeholder="587" />
              <p className="text-xs text-gray-600 mt-1">465 = implicit TLS; 587/25 = STARTTLS</p>
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

      {error && <p className="text-sm text-red-400 bg-red-950/40 border border-red-800/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <Btn variant="secondary" onClick={onCancel}>Cancel</Btn>
        <Btn type="submit" disabled={saving}>{saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add channel'}</Btn>
      </div>
    </form>
  )
}

function NotificationsTab() {
  const qc = useQueryClient()
  const { data: channels = [], isLoading } = useQuery({ queryKey: ['notification-channels'], queryFn: fetchNotificationChannels })
  const [modal, setModal]       = useState(null)
  const [deleting, setDeleting] = useState(null)
  const [testStatus, setTestStatus] = useState({})

  const saveMut = useMutation({
    mutationFn: ({ id, body }) => id ? updateNotificationChannel(id, body) : createNotificationChannel(body),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['notification-channels'] }); setModal(null) },
  })
  const delMut = useMutation({
    mutationFn: (id) => deleteNotificationChannel(id),
    onSuccess: () => { qc.invalidateQueries({ queryKey: ['notification-channels'] }); setDeleting(null) },
  })
  const toggleMut = useMutation({
    mutationFn: (ch) => updateNotificationChannel(ch.id, { ...ch, enabled: !ch.enabled }),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['notification-channels'] }),
  })

  async function handleTest(id) {
    setTestStatus(s => ({ ...s, [id]: { loading: true } }))
    try {
      await testNotificationChannel(id)
      setTestStatus(s => ({ ...s, [id]: { ok: true } }))
    } catch (err) {
      setTestStatus(s => ({ ...s, [id]: { error: err.response?.data?.error || 'Test failed' } }))
    }
    setTimeout(() => setTestStatus(s => { const n = { ...s }; delete n[id]; return n }), 6000)
  }

  function handleSave(body) {
    return saveMut.mutateAsync({ id: modal?.editing?.id, body })
  }

  function summary(ch) {
    const c = typeof ch.config === 'string' ? safeParse(ch.config) : (ch.config || {})
    if (ch.type === 'email') return `${c.from || '—'} → ${c.to || '—'}`
    const urls = (c.urls || '').split(/[\n,]/).map(s => s.trim()).filter(Boolean)
    return urls.length ? `${urls.length} Apprise URL${urls.length > 1 ? 's' : ''}: ${urls[0].split('://')[0]}…` : 'no URLs'
  }

  if (isLoading) return <div className="py-12 text-center text-gray-500 text-sm">Loading…</div>

  return (
    <div>
      <div className="flex items-center justify-between mb-6">
        <div>
          <h2 className="text-base font-semibold text-white">Notification Channels</h2>
          <p className="text-sm text-gray-500 mt-0.5">Where alerts are delivered. Assign channels to rules on the Alert Rules tab.</p>
        </div>
        <Btn onClick={() => setModal('new')}>＋ Add channel</Btn>
      </div>

      {channels.length === 0 ? (
        <EmptyState
          icon="📣"
          title="No notification channels"
          description="Add a channel to deliver alerts to Slack, Discord, email, and more."
          action={<Btn onClick={() => setModal('new')}>＋ Add first channel</Btn>}
        />
      ) : (
        <div className="space-y-2">
          {channels.map(ch => {
            const ts = testStatus[ch.id]
            return (
              <div key={ch.id} className="flex items-center gap-4 p-4 bg-gray-900 border border-gray-800 rounded-xl">
                <span className={`inline-flex items-center px-2 py-0.5 rounded text-xs font-semibold uppercase tracking-wider
                  ${ch.type === 'email' ? 'bg-cyan-900/60 text-cyan-300' : 'bg-purple-900/60 text-purple-300'}`}>
                  {ch.type}
                </span>
                <div className="flex-1 min-w-0">
                  <p className="text-sm font-semibold text-white truncate">{ch.name}</p>
                  <p className="text-xs text-gray-500 mt-0.5 truncate">{summary(ch)}</p>
                </div>
                {ts?.loading && <span className="text-xs text-gray-500">Sending…</span>}
                {ts?.ok && <span className="text-xs text-green-400">✓ Sent</span>}
                {ts?.error && <span className="text-xs text-red-400 max-w-[200px] truncate" title={ts.error}>{ts.error}</span>}
                <Toggle checked={ch.enabled} onChange={() => toggleMut.mutate(ch)} label="" />
                <div className="flex items-center gap-2">
                  <Btn variant="ghost" size="sm" onClick={() => handleTest(ch.id)} disabled={ts?.loading}>Test</Btn>
                  <Btn variant="ghost" size="sm" onClick={() => setModal({ editing: ch })}>Edit</Btn>
                  <Btn variant="danger" size="sm" onClick={() => setDeleting(ch)}>Delete</Btn>
                </div>
              </div>
            )
          })}
        </div>
      )}

      {modal && (
        <div className="fixed inset-0 z-50 flex items-start justify-center bg-black/60 backdrop-blur-sm overflow-y-auto py-8">
          <div className="bg-gray-900 border border-gray-800 rounded-xl w-full max-w-2xl mx-4 p-6" onClick={e => e.stopPropagation()}>
            <div className="flex items-center justify-between mb-5">
              <h3 className="font-semibold text-white">{modal === 'new' ? 'Add notification channel' : `Edit "${modal.editing.name}"`}</h3>
              <button onClick={() => setModal(null)} className="text-gray-500 hover:text-white text-xl">×</button>
            </div>
            <ChannelForm
              initial={modal === 'new' ? null : modal.editing}
              onSave={handleSave}
              onCancel={() => setModal(null)}
              saving={saveMut.isPending}
            />
          </div>
        </div>
      )}

      {deleting && (
        <ConfirmDeleteModal
          name={`"${deleting.name}"`}
          onConfirm={() => delMut.mutate(deleting.id)}
          onClose={() => setDeleting(null)}
          loading={delMut.isPending}
        />
      )}
    </div>
  )
}

function safeParse(s) { try { return JSON.parse(s) } catch { return {} } }

// ── Page ──────────────────────────────────────────────────────────────────────

const TABS = [
  { id: 'general',        label: 'General' },
  { id: 'alerts',         label: 'Alert Rules' },
  { id: 'notifications',  label: 'Notifications' },
  { id: 'registries',     label: 'Docker Registries' },
  { id: 'backup-targets', label: 'Backup Targets' },
  { id: 'hosts',          label: 'Remote Hosts' },
]

export default function SettingsPage() {
  const [tab, setTab] = useState('general')

  return (
    <Layout>
      <div className="max-w-4xl mx-auto px-6 py-8">
        {/* Page header */}
        <div className="mb-6">
          <h1 className="text-xl font-bold text-white">Settings</h1>
          <p className="text-sm text-gray-500 mt-0.5">Configure SSL, integrations, and backup destinations.</p>
        </div>

        {/* Tab bar */}
        <div className="flex gap-1 border-b border-gray-800 mb-6">
          {TABS.map(t => (
            <button
              key={t.id} onClick={() => setTab(t.id)}
              className={`px-4 py-2.5 text-sm font-medium border-b-2 transition-colors -mb-px ${
                tab === t.id
                  ? 'border-brand-500 text-brand-400'
                  : 'border-transparent text-gray-500 hover:text-gray-300'
              }`}
            >
              {t.label}
            </button>
          ))}
        </div>

        {/* Tab content */}
        {tab === 'general'        && <GeneralTab />}
        {tab === 'alerts'         && <RulesTab />}
        {tab === 'notifications'  && <NotificationsTab />}
        {tab === 'registries'     && <RegistriesTab />}
        {tab === 'backup-targets' && <BackupTargetsTab />}
        {tab === 'hosts'          && <HostsTab />}
      </div>
    </Layout>
  )
}
