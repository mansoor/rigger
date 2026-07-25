import { useState } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import { fetchWorkspaceGitProviders, createWorkspaceGitProvider } from '../lib/api'
import { Hint, Btn } from './ui'

// GitProviderPicker selects the workspace Git provider used to clone a PRIVATE
// source repo (sets the project's git_provider_id). It can inline-create a provider
// (token or a Rigger-generated SSH deploy key) which persists to the SAME workspace
// store as Manage Workspace → Git, then auto-selects it. value is the numeric id (0 =
// none / public repo).
export default function GitProviderPicker({ workspace, value, onChange }) {
  const qc = useQueryClient()
  const gpKey = ['ws-git-providers', workspace]
  const { data: providers = [] } = useQuery({
    queryKey: gpKey, queryFn: () => fetchWorkspaceGitProviders(workspace), enabled: !!workspace,
  })
  const [adding, setAdding] = useState(false)
  const [createdKey, setCreatedKey] = useState(null) // { name, public_key }

  const createMut = useMutation({
    mutationFn: (body) => createWorkspaceGitProvider(workspace, body),
    onSuccess: (saved) => {
      qc.invalidateQueries({ queryKey: gpKey })
      onChange(saved.id)
      setAdding(false)
      if (saved?.kind === 'ssh_key' && saved?.public_key) setCreatedKey({ name: saved.name, public_key: saved.public_key })
    },
  })

  return (
    <div>
      <div className="flex gap-2">
        <select
          value={String(value || 0)}
          onChange={e => onChange(Number(e.target.value))}
          className="flex-1 bg-surface-raised border border-border rounded-lg px-3 py-2 text-sm text-content focus:outline-none focus:border-brand-500">
          <option value="0">Public repo — no credentials</option>
          {providers.map(p => (
            <option key={p.id} value={String(p.id)}>
              {p.name} ({p.kind === 'ssh_key' ? 'SSH key' : p.kind === 'github_app' ? 'GitHub App' : 'token'})
            </option>
          ))}
        </select>
        <Btn variant="secondary" size="md" onClick={() => setAdding(a => !a)}
          className="shrink-0">
          {adding ? 'Cancel' : '＋ New'}
        </Btn>
      </div>
      <Hint>For a private repository, pick (or add) a Git provider. Manage them in Workspace → Git.</Hint>

      {adding && (
        <InlineCreate
          saving={createMut.isPending}
          error={createMut.error?.response?.data?.error}
          onCreate={(body) => createMut.mutate(body)}
        />
      )}

      {createdKey && (
        <div className="mt-2 rounded-lg border border-border-strong bg-surface-raised/50 p-3 space-y-2">
          <p className="text-xs text-content-muted">Add this <strong>read-only deploy key</strong> to your repo/provider (the private key stays in Rigger):</p>
          <textarea readOnly value={createdKey.public_key} rows={3} onFocus={e => e.target.select()}
            className="w-full bg-surface border border-border rounded px-2 py-1.5 text-[11px] font-mono text-content break-all" />
          <div className="flex justify-end gap-2">
            <Btn variant="secondary" size="xs" onClick={() => navigator.clipboard?.writeText(createdKey.public_key)}
              >Copy</Btn>
            <Btn variant="primary" size="xs" onClick={() => setCreatedKey(null)}
              >Done</Btn>
          </div>
        </div>
      )}
    </div>
  )
}

// InlineCreate is a compact provider-create form embedded under the picker.
function InlineCreate({ onCreate, saving, error }) {
  const [kind, setKind] = useState('token')
  const [name, setName] = useState('')
  const [host, setHost] = useState('')
  const [secret, setSecret] = useState('')
  const canSave = name.trim() && (kind === 'ssh_key' || secret.trim())
  const inp = 'w-full bg-surface border border-border rounded-lg px-3 py-2 text-sm text-content focus:outline-none focus:border-brand-500'
  return (
    <div className="mt-2 rounded-lg border border-border-strong bg-surface-raised/40 p-3 space-y-2">
      <div className="grid grid-cols-2 gap-2">
        <button type="button" onClick={() => setKind('token')}
          className={`px-2 py-1.5 text-xs rounded-lg border ${kind === 'token' ? 'border-brand-500 text-content-strong bg-surface' : 'border-border text-content-muted'}`}>🔒 HTTPS token</button>
        <button type="button" onClick={() => setKind('ssh_key')}
          className={`px-2 py-1.5 text-xs rounded-lg border ${kind === 'ssh_key' ? 'border-brand-500 text-content-strong bg-surface' : 'border-border text-content-muted'}`}>🔑 SSH deploy key</button>
      </div>
      <input className={inp} value={name} onChange={e => setName(e.target.value)} placeholder="Name (e.g. GitHub acme)" />
      <input className={inp} value={host} onChange={e => setHost(e.target.value)} placeholder="Host (optional, e.g. github.com)" />
      {kind === 'token'
        ? <input className={inp} type="password" value={secret} onChange={e => setSecret(e.target.value)} placeholder="Access token (read access to the repo)" />
        : <Hint tone="faint" className="text-[11px]">Rigger generates a deploy keypair; the public key shows after saving — add it to your provider as a read-only deploy key.</Hint>}
      {error && <p className="text-xs text-danger-fg">{error}</p>}
      <div className="flex justify-end">
        <Btn variant="primary" size="xs" disabled={!canSave || saving} onClick={() => onCreate({ name: name.trim(), kind, host: host.trim(), secret })}
          >
          {saving ? 'Saving…' : kind === 'ssh_key' ? 'Generate & use' : 'Add & use'}
        </Btn>
      </div>
    </div>
  )
}
