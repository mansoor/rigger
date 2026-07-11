import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { fetchManagedHostKey } from '../lib/api'
import { Hint } from './ui'

// Shared Remote Host add/edit form, used by both the admin Settings page (global
// hosts, with a workspace allowlist) and Manage Workspace (workspace-owned hosts).
//
// Props:
//   initial    – host being edited, or null for create
//   onSave     – async (body) => void; throws to surface a server error
//   onCancel   – () => void
//   saving     – bool
//   showGrants – render the "Offered to" workspace allowlist (admin/global only)
//   workspaces – [{ key, name }] for the allowlist picker (when showGrants)

function Label({ children, required }) {
  return (
    <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
      {children}{required && <span className="text-danger-fg ml-0.5">*</span>}
    </label>
  )
}

function Input({ value, onChange, placeholder, type = 'text', disabled, ...rest }) {
  return (
    <input
      type={type} value={value ?? ''} onChange={e => onChange(e.target.value)}
      placeholder={placeholder} disabled={disabled}
      className="w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle text-sm focus:outline-none focus:border-brand-500 transition-colors disabled:opacity-50"
      {...rest}
    />
  )
}

export default function HostForm({ initial, onSave, onCancel, saving, showGrants = false, showBuildOnly = false, workspaces = [] }) {
  const isEdit = !!initial?.id
  const [name, setName]       = useState(initial?.name || '')
  const [address, setAddress] = useState(initial?.address || '')
  const [port, setPort]       = useState(initial?.ssh_port || 22)
  const [user, setUser]       = useState(initial?.ssh_user || '')
  const [wsDir, setWsDir]     = useState(initial?.workspaces_dir || '')
  const [key, setKey]         = useState('')
  const [managed, setManaged] = useState(false)
  const [buildOnly, setBuildOnly] = useState(!!initial?.build_only)
  const [reachability, setReachability] = useState(initial?.reachability === 'private' ? 'private' : 'public')
  const [publicAddress, setPublicAddress] = useState(initial?.public_address || '')
  const [copied, setCopied]   = useState(false)
  const [error, setError]     = useState('')

  // Grant state (admin only). A host with grants ['*'] (or none yet) is offered to
  // every workspace; otherwise to the listed workspace keys.
  const initialGrants = initial?.grants
  const startAll = !initialGrants || initialGrants.includes('*')
  const [grantMode, setGrantMode] = useState(startAll ? 'all' : 'selected')
  const [grantKeys, setGrantKeys] = useState(startAll ? [] : initialGrants.filter(g => g !== '*'))

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

  // navigator.clipboard is only available in secure contexts (HTTPS/localhost) —
  // over plain HTTP (a self-hosted box on a LAN IP) it's undefined and the copy
  // silently no-ops. Fall back to a hidden textarea + execCommand so Copy works
  // everywhere; surface an error if even that fails so the user knows to select
  // the command manually.
  async function copy(text) {
    let ok = false
    try {
      if (navigator.clipboard?.writeText) { await navigator.clipboard.writeText(text); ok = true }
    } catch { /* fall through to the legacy path */ }
    if (!ok) {
      try {
        const ta = document.createElement('textarea')
        ta.value = text
        ta.style.position = 'fixed'; ta.style.top = '-1000px'; ta.style.opacity = '0'
        document.body.appendChild(ta)
        ta.focus(); ta.select()
        ok = document.execCommand('copy')
        ta.remove()
      } catch { ok = false }
    }
    if (ok) { setError(''); setCopied(true); setTimeout(() => setCopied(false), 2000) }
    else { setError('Couldn’t copy automatically — select the command above and copy it manually.') }
  }

  function toggleGrant(k) {
    setGrantKeys(cur => cur.includes(k) ? cur.filter(x => x !== k) : [...cur, k])
  }

  async function submit(e) {
    e.preventDefault()
    setError('')
    if (!name.trim() || !address.trim() || !user.trim()) { setError('Name, address, and SSH user are required'); return }
    if (!isEdit && !managed && !key.trim()) { setError('Paste an SSH private key or enable the Rigger-managed key'); return }
    const body = {
      name: name.trim(), address: address.trim(), ssh_port: Number(port) || 22,
      ssh_user: user.trim(), ssh_key: managed ? '' : key, use_managed_key: managed,
      workspaces_dir: wsDir.trim(),
    }
    if (showBuildOnly) body.build_only = buildOnly
    body.reachability = reachability
    body.public_address = reachability === 'public' ? publicAddress.trim() : ''
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
        <Hint tone="faint">
          Absolute path on the host where workspaces live (for scan/import) and are pushed (for deploy/migrate).
          Leave blank to use the server default (<code className="font-mono">REMOTE_WORKSPACES_DIR</code>).
        </Hint>
      </div>

      {/* Rigger-managed key toggle */}
      <label className="flex items-center gap-2.5 cursor-pointer select-none">
        <input type="checkbox" checked={managed} onChange={e => setManaged(e.target.checked)} className="accent-brand-500" />
        <span className="text-sm text-content">Use Rigger-managed key</span>
        <span className="text-xs text-content-subtle">— Rigger holds the private key; you just install its public key on the host</span>
      </label>

      {managed ? (
        <div className="space-y-2 rounded-lg border border-border-strong bg-canvas/60 p-3">
          <p className="text-xs text-content-muted">
            1. Run this on <strong className="text-content">{user || 'the host'}@{address || 'the host'}</strong> to authorize Rigger:
          </p>
          <div className="flex items-start gap-2">
            <pre className="flex-1 overflow-auto bg-canvas border border-border rounded-lg p-2 text-[11px] font-mono text-content whitespace-pre-wrap">{installCmd || 'Loading Rigger public key…'}</pre>
            <button type="button" onClick={() => copy(installCmd)} disabled={!installCmd}
              className="shrink-0 px-2.5 py-1.5 text-xs bg-surface-raised hover:bg-surface-overlay text-content rounded-lg disabled:opacity-40">
              {copied ? '✓' : 'Copy'}
            </button>
          </div>
          <Hint>
            2. Then add the host and click <strong className="text-content-muted">Test</strong>. The host only needs Docker + SSH.
            (The private key never leaves Rigger.)
          </Hint>
        </div>
      ) : (
        <div>
          <Label required={!isEdit}>SSH private key</Label>
          <textarea
            value={key} onChange={e => setKey(e.target.value)}
            rows={6} spellCheck={false}
            placeholder={isEdit ? '(unchanged — paste a new key to replace)' : '-----BEGIN OPENSSH PRIVATE KEY-----'}
            className="w-full bg-canvas border border-border-strong rounded-lg px-3 py-2 text-xs font-mono text-content focus:border-brand-500 focus:outline-none"
          />
          <div className="text-xs text-content-faint mt-1 space-y-1">
            <p>
              Paste the <strong>private</strong> key Rigger should log in with — its public half must be in the SSH user's
              <code className="font-mono"> ~/.ssh/authorized_keys</code> on the host. Must have <strong>no passphrase</strong>.
              Stored encrypted at rest.{isEdit && ' Leave blank to keep the existing key.'}
            </p>
            <p className="text-content-subtle">
              Generate one with <code className="font-mono">ssh-keygen -t ed25519 -N "" -f rigger_host</code> — paste
              <code className="font-mono"> rigger_host</code> here and install <code className="font-mono">rigger_host.pub</code> on the host.
            </p>
          </div>
        </div>
      )}

      <div className="rounded-lg border border-border-strong bg-canvas/60 p-3 space-y-2">
        <Label>How is this host reached?</Label>
        <div className="space-y-1.5 text-sm text-content">
          <label className="flex items-start gap-2 cursor-pointer">
            <input type="radio" checked={reachability === 'public'} onChange={() => setReachability('public')} className="accent-brand-500 mt-0.5" />
            <span><strong className="text-content">Public</strong> — this host is its own front door (own public IP / DNS). Apps on it are reached directly at the host.</span>
          </label>
          <label className="flex items-start gap-2 cursor-pointer">
            <input type="radio" checked={reachability === 'private'} onChange={() => setReachability('private')} className="accent-brand-500 mt-0.5" />
            <span><strong className="text-content">Private (behind gateway)</strong> — only this Rigger control plane is exposed; it routes traffic to this host's apps. Use for LAN / homelab boxes with no public IP.</span>
          </label>
        </div>
        <Hint className="mt-0.5">The control plane must be reachable at :80/:443 for private hosts. Public apps get their own TLS; private apps are served under the control plane's cert.</Hint>
        {reachability === 'public' && (
          <div className="pt-1">
            <Label>Public web address <span className="text-content-faint font-normal">(optional)</span></Label>
            <Input value={publicAddress} onChange={setPublicAddress} placeholder="same as SSH address" />
            <Hint className="mt-0.5">Only if this host serves web traffic on a different IP/hostname than its SSH address (e.g. you SSH via a bastion). Used for the app's DNS record and magic-DNS URL.</Hint>
          </div>
        )}
      </div>

      {showBuildOnly && (
        <div className="rounded-lg border border-border-strong bg-canvas/60 p-3">
          <label className="flex items-start gap-2.5 cursor-pointer select-none">
            <input type="checkbox" checked={buildOnly} onChange={e => setBuildOnly(e.target.checked)} className="accent-brand-500 mt-0.5" />
            <span>
              <span className="text-sm text-content">Build-only (dedicated builder)</span>
              <Hint className="mt-0.5">
                Use this host only to build &amp; push images — it's hidden from deploy-host pickers and runs no workloads,
                so it can be torn down at any time. Leave unchecked for a normal <strong className="text-content-muted">Build + Deploy</strong> host.
                {isEdit && ' A host that is still a deploy target for an environment can\'t be switched to build-only until those environments are moved.'}
              </Hint>
            </span>
          </label>
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
            Controls which workspaces can pick this host for their environments. Workspace-owned hosts are private and aren't listed here.
          </Hint>
        </div>
      )}

      {error && <p className="text-sm text-danger-fg bg-danger-subtle/40 border border-danger-border/50 rounded-lg px-3 py-2">{error}</p>}

      <div className="flex gap-2 justify-end pt-2">
        <button type="button" onClick={onCancel}
          className="font-semibold rounded-lg transition-colors px-4 py-2 text-sm bg-surface-overlay hover:bg-surface-overlay text-content disabled:opacity-50">Cancel</button>
        <button type="submit" disabled={saving}
          className="font-semibold rounded-lg transition-colors px-4 py-2 text-sm bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50">
          {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Add host'}
        </button>
      </div>
    </form>
  )
}
