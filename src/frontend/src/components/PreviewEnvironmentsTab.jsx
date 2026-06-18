import { useState, useEffect } from 'react'
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query'
import {
  fetchPreviewSettings, setPreviewConfig,
  createPreviewWebhook, deletePreviewWebhook,
  redeployPreview, teardownPreview, setPreviewWritebackToken,
} from '../lib/api'

// Preview / PR environments tab (inside Edit Project). Opt-in per project: a
// signed webhook drives ephemeral pr{n} envs cloned from a template env, deployed
// on PR open/sync and torn down on close. See docs/design/preview-environments.md.

const PROVIDERS = [
  { value: 'github', label: 'GitHub' },
  { value: 'gitea', label: 'Gitea' },
]
const FORK_POLICIES = [
  { value: 'off', label: 'Off — never auto-deploy fork PRs (recommended)' },
  { value: 'approved', label: 'Approved — only after manual approval' },
  { value: 'on', label: 'On — deploy all fork PRs (use a dedicated host)' },
]
const DB_STRATEGIES = [
  { value: 'isolated-empty', label: 'Isolated, empty — fresh DB, migrations on deploy' },
  { value: 'isolated-seed', label: 'Isolated, seeded — import the project SQL seed' },
  { value: 'clone-from', label: 'Clone from an env — real data, isolated (recommended for data apps)' },
  { value: 'shared', label: 'Shared with an env — point at a live DB (non-prod)' },
]

const DEFAULTS = {
  enabled: false, template_env: '', provider: 'github', branch_filter: '',
  max_concurrent: 0, ttl_hours: 0, protect_auth: false,
  auto_deploy_forks: 'off', write_back: false, db_strategy: 'isolated-empty', db_source: '',
}

const STATUS_STYLES = {
  running: 'bg-success-subtle/50 text-success-fg',
  creating: 'bg-brand-500/20 text-brand-300',
  updating: 'bg-brand-500/20 text-brand-300',
  failed: 'bg-danger-subtle/50 text-danger-fg',
  torn_down: 'bg-surface-raised text-content-faint',
}

function Field({ label, hint, children }) {
  return (
    <label className="block">
      <span className="text-xs font-semibold text-content-subtle uppercase tracking-wider">{label}</span>
      <div className="mt-1">{children}</div>
      {hint && <p className="text-xs text-content-subtle mt-1">{hint}</p>}
    </label>
  )
}

const inputCls = 'w-full px-3 py-2 bg-surface-raised border border-border rounded-lg text-sm text-content focus:border-brand-400 focus:outline-none'

export default function PreviewEnvironmentsTab({ workspace, name, envNames = [] }) {
  const qc = useQueryClient()
  const queryKey = ['preview-settings', workspace, name]
  const { data, isLoading } = useQuery({ queryKey, queryFn: () => fetchPreviewSettings(workspace, name) })

  const [draft, setDraft] = useState(DEFAULTS)
  useEffect(() => {
    if (data) setDraft({ ...DEFAULTS, ...(data.config || {}) })
  }, [data])
  const upd = (k, v) => setDraft(d => ({ ...d, [k]: v }))

  const saveMut = useMutation({
    mutationFn: () => setPreviewConfig(workspace, name, draft),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  })

  const [newUrl, setNewUrl] = useState(null) // full hook URL shown once after create
  const [copied, setCopied] = useState(false)
  const addHook = useMutation({
    mutationFn: () => createPreviewWebhook(workspace, name, { secret: '', provider: draft.provider }),
    onSuccess: (res) => {
      setNewUrl(`${window.location.origin}${res.hook_path}`)
      qc.invalidateQueries({ queryKey })
    },
  })
  const delHook = useMutation({
    mutationFn: (id) => deletePreviewWebhook(workspace, name, id),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  })
  async function copyUrl() {
    try { await navigator.clipboard.writeText(newUrl); setCopied(true); setTimeout(() => setCopied(false), 1500) } catch { /* clipboard unavailable */ }
  }

  const [tokenInput, setTokenInput] = useState('')
  const saveToken = useMutation({
    mutationFn: (tok) => setPreviewWritebackToken(workspace, name, tok),
    onSuccess: () => { setTokenInput(''); qc.invalidateQueries({ queryKey }) },
  })

  const redeploy = useMutation({
    mutationFn: (pr) => redeployPreview(workspace, name, pr),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  })
  const teardown = useMutation({
    mutationFn: (pr) => teardownPreview(workspace, name, pr),
    onSuccess: () => qc.invalidateQueries({ queryKey }),
  })

  if (isLoading) return <p className="text-sm text-content-subtle">Loading…</p>

  const hooks = data?.webhooks || []
  const previews = data?.previews || []
  const needsSource = draft.db_strategy === 'clone-from' || draft.db_strategy === 'shared'

  return (
    <section className="mb-6 space-y-6">
      <div>
        <h2 className="text-sm font-semibold text-content">Preview Environments</h2>
        <p className="text-xs text-content-subtle mt-1">
          Auto-deploy a short-lived environment for each pull request, cloned from a template
          environment and torn down when the PR closes. Opt-in: enable it, then add the webhook to your repo.
        </p>
      </div>

      {/* ── Configuration ── */}
      <div className="bg-surface border border-border rounded-xl p-5 space-y-4">
        <label className="flex items-center gap-2 text-sm text-content">
          <input type="checkbox" checked={draft.enabled} onChange={e => upd('enabled', e.target.checked)} />
          <span className="font-semibold">Enable preview environments for this project</span>
        </label>

        <div className="grid sm:grid-cols-2 gap-4">
          <Field label="Template environment" hint="The env each preview is cloned from (services, deps, vars).">
            <select className={inputCls} value={draft.template_env} onChange={e => upd('template_env', e.target.value)}>
              <option value="">— select —</option>
              {envNames.map(en => <option key={en} value={en}>{en}</option>)}
            </select>
          </Field>
          <Field label="Provider">
            <select className={inputCls} value={draft.provider} onChange={e => upd('provider', e.target.value)}>
              {PROVIDERS.map(p => <option key={p.value} value={p.value}>{p.label}</option>)}
            </select>
          </Field>
          <Field label="Branch filter" hint="Optional glob (e.g. feature/*). Blank = all branches.">
            <input className={inputCls} value={draft.branch_filter} placeholder="feature/*"
              onChange={e => upd('branch_filter', e.target.value)} />
          </Field>
          <Field label="Fork PRs">
            <select className={inputCls} value={draft.auto_deploy_forks} onChange={e => upd('auto_deploy_forks', e.target.value)}>
              {FORK_POLICIES.map(p => <option key={p.value} value={p.value}>{p.label}</option>)}
            </select>
          </Field>
          <Field label="Max concurrent" hint="Cap active previews (0 = unlimited).">
            <input type="number" min="0" className={inputCls} value={draft.max_concurrent}
              onChange={e => upd('max_concurrent', parseInt(e.target.value, 10) || 0)} />
          </Field>
          <Field label="TTL (hours)" hint="Auto-tear-down after this many hours of inactivity (0 = until PR closes).">
            <input type="number" min="0" className={inputCls} value={draft.ttl_hours}
              onChange={e => upd('ttl_hours', parseInt(e.target.value, 10) || 0)} />
          </Field>
          <Field label="Database strategy" hint="How the preview's database is populated.">
            <select className={inputCls} value={draft.db_strategy} onChange={e => upd('db_strategy', e.target.value)}>
              {DB_STRATEGIES.map(s => <option key={s.value} value={s.value}>{s.label}</option>)}
            </select>
          </Field>
          {needsSource && (
            <Field label="Source environment" hint={draft.db_strategy === 'shared'
              ? '⚠ Previews write to this env’s live DB — use a non-prod env; migrations affect it.'
              : 'Restore the latest backup of this env into each preview (once, on create).'}>
              <select className={inputCls} value={draft.db_source} onChange={e => upd('db_source', e.target.value)}>
                <option value="">— select —</option>
                {envNames.map(en => <option key={en} value={en}>{en}</option>)}
              </select>
            </Field>
          )}
        </div>

        <label className="flex items-center gap-2 text-sm text-content">
          <input type="checkbox" checked={draft.protect_auth} onChange={e => upd('protect_auth', e.target.checked)} />
          <span>Protect preview URLs with basic auth</span>
        </label>

        <label className="flex items-center gap-2 text-sm text-content">
          <input type="checkbox" checked={draft.write_back} onChange={e => upd('write_back', e.target.checked)} />
          <span>Post the preview URL &amp; status back to the PR <span className="text-content-faint">(GitHub commit status + comment)</span></span>
        </label>

        <div className="flex items-center gap-3">
          <button onClick={() => saveMut.mutate()} disabled={saveMut.isPending}
            className="px-4 py-2 rounded-lg bg-brand-500 hover:bg-brand-400 text-white text-sm font-semibold disabled:opacity-40">
            {saveMut.isPending ? 'Saving…' : 'Save settings'}
          </button>
          {saveMut.isSuccess && <span className="text-xs text-success-fg">Saved.</span>}
          {saveMut.isError && <span className="text-xs text-danger-fg">{saveMut.error?.response?.data?.error || 'Save failed.'}</span>}
        </div>
      </div>

      {/* ── Webhook ── */}
      <div className="bg-surface border border-border rounded-xl p-5 space-y-3">
        <div className="flex items-center justify-between">
          <div>
            <h3 className="text-sm font-semibold text-content">Webhook</h3>
            <p className="text-xs text-content-subtle">Add this URL as a <code className="font-mono">pull_request</code> webhook in your repo settings.</p>
          </div>
          <button onClick={() => addHook.mutate()} disabled={addHook.isPending}
            className="text-xs font-semibold text-brand-400 hover:text-brand-300 disabled:opacity-40">+ Add webhook</button>
        </div>

        {newUrl && (
          <div className="px-3 py-2 rounded-lg bg-warning-subtle/40 border border-warning-border/60 text-xs">
            <p className="text-warning-fg font-semibold mb-1">Copy this URL now — it won't be shown again:</p>
            <div className="flex items-start gap-2">
              <code className="flex-1 font-mono break-all text-content-strong select-all">{newUrl}</code>
              <button onClick={copyUrl} title="Copy URL"
                className="shrink-0 px-2 py-1 rounded bg-surface-raised hover:bg-surface-overlay text-content transition-colors">
                {copied ? '✓ Copied' : '⧉ Copy'}
              </button>
            </div>
          </div>
        )}

        {hooks.length === 0 ? (
          <p className="text-xs text-content-faint">No webhooks yet.</p>
        ) : hooks.map(h => (
          <div key={h.id} className="flex items-center gap-2 text-xs">
            <span className="font-mono text-content-muted">hook #{h.id}</span>
            <span className="text-content-faint">{h.provider}</span>
            <span className="text-content-faint">· created {new Date(h.created_at).toLocaleDateString()}</span>
            {h.last_triggered_at && <span className="text-content-faint">· last fired {new Date(h.last_triggered_at).toLocaleString()}</span>}
            <button onClick={() => delHook.mutate(h.id)} className="ml-auto text-danger-fg hover:bg-danger-subtle/40 px-1.5 py-0.5 rounded">Delete</button>
          </div>
        ))}
      </div>

      {/* ── Write-back token ── */}
      {draft.write_back && (
        <div className="bg-surface border border-border rounded-xl p-5 space-y-3">
          <div>
            <h3 className="text-sm font-semibold text-content">Write-back token</h3>
            <p className="text-xs text-content-subtle">
              A GitHub token with <code className="font-mono">repo:status</code> + PR-comment scope. Stored
              encrypted; never shown again. {data?.has_writeback_token
                ? <span className="text-success-fg">A token is configured.</span>
                : <span className="text-warning-fg">No token set — write-back is inactive until you add one.</span>}
            </p>
          </div>
          <div className="flex items-center gap-2">
            <input type="password" autoComplete="off" className={inputCls} placeholder={data?.has_writeback_token ? '•••••••• (leave blank to keep, type to replace)' : 'ghp_…'}
              value={tokenInput} onChange={e => setTokenInput(e.target.value)} />
            <button onClick={() => saveToken.mutate(tokenInput)} disabled={saveToken.isPending || !tokenInput}
              className="shrink-0 px-3 py-2 rounded-lg bg-brand-500 hover:bg-brand-400 text-white text-sm font-semibold disabled:opacity-40">Save token</button>
            {data?.has_writeback_token && (
              <button onClick={() => saveToken.mutate('')} disabled={saveToken.isPending}
                className="shrink-0 px-3 py-2 rounded-lg text-danger-fg hover:bg-danger-subtle/40 text-sm">Clear</button>
            )}
          </div>
        </div>
      )}

      {/* ── Active previews ── */}
      <div className="bg-surface border border-border rounded-xl p-5 space-y-3">
        <h3 className="text-sm font-semibold text-content">Active previews <span className="text-content-faint font-normal">({previews.length})</span></h3>
        {previews.length === 0 ? (
          <p className="text-xs text-content-faint">No preview environments yet. They appear here when a PR opens.</p>
        ) : previews.map(p => (
          <div key={p.id} className="flex items-center gap-3 text-xs border-t border-border pt-2 first:border-0 first:pt-0">
            <span className="font-mono text-content font-semibold">PR #{p.pr_number}</span>
            <span className="font-mono text-content-muted truncate max-w-[10rem]" title={p.branch}>{p.branch}</span>
            <span className={`px-1.5 py-0.5 rounded ${STATUS_STYLES[p.status] || 'bg-surface-raised text-content-faint'}`}>{p.status}</span>
            {p.url && <a href={p.url} target="_blank" rel="noreferrer" className="text-brand-400 hover:text-brand-300 truncate max-w-[14rem]">{p.url}</a>}
            <div className="ml-auto flex items-center gap-2">
              <button onClick={() => redeploy.mutate(p.pr_number)} disabled={redeploy.isPending}
                className="px-1.5 py-0.5 rounded bg-surface-raised hover:bg-surface-overlay text-content disabled:opacity-40">Redeploy</button>
              <button onClick={() => teardown.mutate(p.pr_number)} disabled={teardown.isPending}
                className="px-1.5 py-0.5 rounded text-danger-fg hover:bg-danger-subtle/40">Tear down</button>
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}
