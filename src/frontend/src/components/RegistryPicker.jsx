import { useState, useEffect } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { fetchWorkspaceRegistries, createWorkspaceRegistry, testRegistryCredentials } from '../lib/api'
import { Hint } from './ui'

// RegistryPicker — the container-registry selector shared by the New Project
// wizard and Edit Project. It lists the workspace's saved registries in a
// dropdown; choosing "Other" opens an inline form to add a NEW workspace registry
// (name/URL/username/password) with Test + Save, so credentials are captured at
// the point of entry (the build needs them to push). An "anonymous" toggle keeps
// the old bare-URL behaviour for registries that allow unauthenticated push
// (e.g. a local/insecure registry). The project itself only stores the URL string
// (cfg.Project.Registry); the saved record supplies the push credentials.

const CUSTOM = '__custom__'
const LOCAL = '__local__' // empty value — inherit the workspace/global SYSTEM registry (image distribution)
const inputCls = 'w-full px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong placeholder-content-subtle text-sm focus:outline-none focus:border-brand-500 transition-colors'

function FieldLabel({ children }) {
  return <label className="block text-[11px] font-semibold text-content-muted uppercase tracking-wide mb-1">{children}</label>
}

export default function RegistryPicker({ workspace, value, onChange, defaultRegistryId = '', error }) {
  const qc = useQueryClient()
  const { data: registries = [], isLoading } = useQuery({
    queryKey: ['ws-registries', workspace],
    queryFn: () => fetchWorkspaceRegistries(workspace),
    enabled: !!workspace,
  })

  const hasRegistries = !isLoading && registries.length > 0
  const matched = registries.find(r => r.url === value)
  const isCustomValue = !!value && !matched && !isLoading

  // `manual` = the inline add-registry form is open (user picked "Other", or the
  // project already references a URL that isn't a saved registry).
  const [manual, setManual] = useState(false)
  const [didInit, setDidInit] = useState(false)

  const [anon, setAnon] = useState(false)
  const [name, setName] = useState('')
  const [url, setUrl] = useState('')
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [busy, setBusy] = useState('') // '' | 'test' | 'save'
  const [msg, setMsg] = useState(null) // { ok, text }

  // Auto-select the workspace's DEFAULT registry once the pool loads and nothing is
  // chosen — but only when a default is actually configured (create flow inheriting
  // it). Never force registries[0]: an empty value is a valid, deliberate choice
  // (inherit the system registry, or local-only when none is configured), and
  // force-picking the first pool entry made it impossible to keep a project on the
  // system/local default (it silently reverted to a random registry).
  const wsDefault = registries.find(r => String(r.id) === String(defaultRegistryId))
  useEffect(() => {
    if (!isLoading && !value && wsDefault) {
      onChange(wsDefault.url)
    }
  }, [isLoading, registries.length]) // eslint-disable-line react-hooks/exhaustive-deps

  // If the project already references an unsaved (custom) URL, open the manual form
  // pre-filled. A stored URL with no matching saved record means it was entered as
  // an *anonymous* registry (credentialed ones are saved as records and match), so
  // restore the anonymous toggle — otherwise Edit Project showed the credentialed
  // form on reload and the "Anonymous registry" choice appeared lost.
  useEffect(() => {
    if (!didInit && isCustomValue) {
      setManual(true)
      setUrl(value)
      setAnon(true)
      setDidInit(true)
    }
  }, [isCustomValue, didInit, value])

  const showManual = manual || (!hasRegistries && !isLoading)
  // An empty value with no manual form open means "inherit the system registry"
  // (or local-only when no system registry is configured).
  const selectValue = manual ? CUSTOM : matched ? value : value ? '' : LOCAL

  function chooseSelect(v) {
    setMsg(null)
    if (v === LOCAL) {
      setManual(false)
      onChange('') // build images locally; never push/pull a registry
    } else if (v === CUSTOM) {
      setManual(true)
      setUrl('')
      onChange('') // force a deliberate Save / anonymous URL before proceeding
    } else {
      setManual(false)
      onChange(v)
    }
  }

  function toggleAnon(on) {
    setAnon(on)
    setMsg(null)
    onChange(on ? url.trim() : '') // anonymous → URL is authoritative; credentialed → set on Save
  }

  async function doTest() {
    setBusy('test'); setMsg(null)
    try {
      await testRegistryCredentials(workspace, { url: url.trim(), username: username.trim(), password })
      setMsg({ ok: true, text: 'Login succeeded' })
    } catch (e) {
      setMsg({ ok: false, text: e?.response?.data?.error || 'Login failed' })
    } finally { setBusy('') }
  }

  async function doSave() {
    setBusy('save'); setMsg(null)
    try {
      await createWorkspaceRegistry(workspace, { name: name.trim(), url: url.trim(), username: username.trim(), password })
      await qc.invalidateQueries({ queryKey: ['ws-registries', workspace] })
      onChange(url.trim()) // project references the URL; now backed by a credentialed record
      setManual(false)
      setPassword('')
      setMsg(null)
    } catch (e) {
      setMsg({ ok: false, text: e?.response?.data?.error || 'Failed to save registry' })
    } finally { setBusy('') }
  }

  const canTest = !!(url.trim() && username.trim() && password) && !busy
  const canSave = !!(name.trim() && url.trim() && username.trim() && password) && !busy

  return (
    <div>
      {hasRegistries && (
        <select value={selectValue} onChange={e => chooseSelect(e.target.value)}
          className={`${inputCls} ${error ? 'border-danger' : ''}`}>
          <option value={LOCAL}>System registry (workspace / global default)</option>
          {registries.map(r => <option key={r.id} value={r.url}>{r.name} — {r.url}{r.system ? ' · system' : ''}</option>)}
          <option value={CUSTOM}>Other (enter manually)…</option>
        </select>
      )}
      {hasRegistries && selectValue === LOCAL && (
        <Hint tone="faint">Uses the system registry configured for this workspace (or the global default). If none is set, images stay local — fine for a single-node compose deploy, but a Swarm or remote-host deploy will be blocked until a system registry is configured.</Hint>
      )}
      {hasRegistries && wsDefault && value === wsDefault.url && (
        <Hint tone="faint">Inherited from this workspace's default.</Hint>
      )}

      {showManual && (
        <div className={`${hasRegistries ? 'mt-3' : ''} rounded-lg border border-border-strong bg-surface-raised/40 p-3 space-y-3`}>
          <label className="flex items-center gap-2 text-xs text-content-muted cursor-pointer">
            <input type="checkbox" checked={anon} onChange={e => toggleAnon(e.target.checked)} className="w-3.5 h-3.5 accent-brand-500" />
            Anonymous registry (no credentials)
          </label>

          {anon ? (
            <div>
              <FieldLabel>Registry URL</FieldLabel>
              <input value={url} onChange={e => { setUrl(e.target.value); onChange(e.target.value) }}
                placeholder="localhost:5000" className={inputCls} />
              <Hint tone="faint">
                Stored as a plain URL with no credentials — only works for registries that allow anonymous push (e.g. a local/insecure registry).
              </Hint>
            </div>
          ) : (
            <>
              <div className="grid grid-cols-2 gap-3">
                <div>
                  <FieldLabel>Display name</FieldLabel>
                  <input value={name} onChange={e => setName(e.target.value)} placeholder="My Registry" className={inputCls} />
                </div>
                <div>
                  <FieldLabel>Registry URL</FieldLabel>
                  <input value={url} onChange={e => setUrl(e.target.value)} placeholder="ghcr.io / registry.example.com" className={inputCls} />
                </div>
                <div>
                  <FieldLabel>Username</FieldLabel>
                  <input value={username} onChange={e => setUsername(e.target.value)} placeholder="myuser" className={inputCls} />
                </div>
                <div>
                  <FieldLabel>Password / Token</FieldLabel>
                  <input type="password" value={password} onChange={e => setPassword(e.target.value)} placeholder="••••••••" className={inputCls} />
                </div>
              </div>
              <Hint tone="faint">
                Saved to this workspace so the build can authenticate and push. The password field accepts a token / API key.
              </Hint>
              <div className="flex items-center gap-2">
                <button type="button" onClick={doTest} disabled={!canTest}
                  className="text-xs font-semibold px-3 py-1.5 rounded-lg border border-border-strong text-content hover:bg-surface-raised disabled:opacity-50 transition-colors">
                  {busy === 'test' ? 'Testing…' : 'Test'}
                </button>
                <button type="button" onClick={doSave} disabled={!canSave}
                  className="text-xs font-semibold px-3 py-1.5 rounded-lg bg-brand-600 hover:bg-brand-700 text-white disabled:opacity-50 transition-colors">
                  {busy === 'save' ? 'Saving…' : 'Save & use'}
                </button>
              </div>
            </>
          )}

          {msg && (
            <p className={`text-xs ${msg.ok ? 'text-success-fg' : 'text-danger-fg'}`}>{msg.ok ? '✓ ' : '✗ '}{msg.text}</p>
          )}
        </div>
      )}

      {error && !showManual && <p className="text-danger-fg text-xs mt-1">{error}</p>}
    </div>
  )
}
