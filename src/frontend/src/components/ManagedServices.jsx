import { useState } from 'react'
import DatabaseSelect from './DatabaseSelect'

// ManagedServices is the shared editor for a project's managed services (database,
// Redis, Garage object storage). They are PROJECT-level — consistent across every
// environment — and surface as services. Used identically by the New Project wizard
// (Services step) and Edit Project (Services tab) so create/edit stay in sync.
//
// `value` is the normalized shape {database, dbVersion, redis, garage, cloudbeaver};
// callers map it to their own state. onChange receives the full updated value.

// Connection contract per managed service: what an app needs to talk to it
// in-network. The exact values (db name, credentials, tokens) are auto-generated
// PER ENVIRONMENT and live in each env's .env / the project's Database tab — here we
// document the env-var names + host/port pattern so app authors know what to read.
const SERVICE_META = {
  postgres:     { label: 'PostgreSQL',    port: '5432',      vars: ['POSTGRES_HOST', 'POSTGRES_PORT', 'POSTGRES_DB', 'POSTGRES_USER', 'POSTGRES_PASSWORD'], creds: true },
  mysql:        { label: 'MySQL',         port: '3306',      vars: ['MYSQL_HOST', 'MYSQL_PORT', 'MYSQL_DATABASE', 'MYSQL_USER', 'MYSQL_PASSWORD', 'MYSQL_ROOT_PASSWORD'], creds: true },
  mariadb:      { label: 'MariaDB',       port: '3306',      vars: ['MYSQL_HOST', 'MYSQL_PORT', 'MYSQL_DATABASE', 'MYSQL_USER', 'MYSQL_PASSWORD', 'MYSQL_ROOT_PASSWORD'], creds: true },
  redis:        { label: 'Redis',         port: '6379',      vars: ['REDIS_HOST', 'REDIS_PORT'], note: 'No password by default.' },
  garage:       { label: 'Garage (S3)',   port: '3900 / 3903', vars: ['GARAGE_HOST', 'GARAGE_API_PORT', 'GARAGE_S3_PORT', 'GARAGE_ADMIN_TOKEN', 'GARAGE_KEY_ID', 'GARAGE_SECRET_KEY'] },
  garage_webui: { label: 'Garage Web UI', port: '3909',      vars: [], note: 'Browser admin UI for Garage — not used directly by your app.' },
  adminer:      { label: 'Adminer',       port: '8978',      vars: [], note: 'Browser SQL client — becomes this project’s web entry; one-click auto-login from Manage Database.' },
  cloudbeaver:  { label: 'CloudBeaver',   port: '8978',      vars: [], note: 'Browser SQL client (legacy projects) — becomes this project’s web entry.' },
}

// managedServiceList derives the synthetic service rows from the picker value
// (mirrors backend workspace.managedDepServices).
export function managedServiceList({ database, redis, garage, webSql, cloudbeaver } = {}) {
  const out = []
  if (database && database !== 'none') out.push({ name: database, kind: database })
  if (redis) out.push({ name: 'redis', kind: 'redis' })
  if (garage) { out.push({ name: 'garage', kind: 'garage' }); out.push({ name: 'garage_webui', kind: 'garage_webui' }) }
  if (webSql || cloudbeaver) out.push({ name: 'adminer', kind: 'adminer' })
  return out
}

// enabledDependsOnTargets returns the managed-service names a real service may
// depend_on (excludes UI-only sidecars like garage_webui / adminer).
export function enabledDependsOnTargets({ database, redis, garage } = {}) {
  const out = []
  if (database && database !== 'none') out.push(database)
  if (redis) out.push('redis')
  if (garage) out.push('garage')
  return out
}

function MiniToggle({ label, hint, checked, onChange }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div>
        <p className="text-sm text-content">{label}</p>
        {hint && <p className="text-xs text-content-subtle mt-0.5">{hint}</p>}
      </div>
      <button type="button" onClick={() => onChange(!checked)}
        className={`relative w-10 h-5 rounded-full transition-colors shrink-0 ${checked ? 'bg-brand-600' : 'bg-surface-overlay'}`}>
        <span className={`absolute top-0.5 left-0.5 w-4 h-4 bg-white rounded-full shadow transition-transform ${checked ? 'translate-x-5' : ''}`} />
      </button>
    </div>
  )
}

// ServiceRow is a read-only, expandable summary of one managed service: the
// in-network host/port and the env vars an app uses to reach it.
function ServiceRow({ row, resourcePrefix }) {
  const [open, setOpen] = useState(false)
  const meta = SERVICE_META[row.kind] || { label: row.kind, vars: [] }
  const host = `${resourcePrefix || '<prefix>'}_<env>_${row.name}`
  return (
    <div className="bg-surface-raised/30 border border-border-strong/60 rounded-xl overflow-hidden">
      <button type="button" onClick={() => setOpen(o => !o)}
        className="w-full flex items-center gap-3 px-4 py-3 text-left">
        <span className={`text-content-subtle text-xs transition-transform ${open ? 'rotate-90' : ''}`}>▸</span>
        <span className="text-sm font-semibold text-content-strong">{row.name}</span>
        <span className="text-[11px] px-2 py-0.5 rounded bg-surface-overlay/40 text-content-muted">managed · {meta.label}</span>
        <span className="ml-auto text-[11px] text-content-faint">read-only — remove via the toggles above</span>
      </button>
      {open && (
        <div className="px-4 pb-3 pt-1 text-xs text-content-subtle space-y-2 border-t border-border-strong/40">
          <div>
            In-network host <code className="font-mono text-content">{host}</code>
            {meta.port && <> · port <code className="font-mono text-content">{meta.port}</code></>}
          </div>
          {meta.vars.length > 0 && (
            <div>
              <span className="text-content-muted">App connection env vars</span> (auto-set in each environment’s <code className="font-mono">.env</code>):
              <div className="mt-1 font-mono text-[11px] text-content break-all">{meta.vars.join('  ')}</div>
            </div>
          )}
          {meta.creds && (
            <p className="text-content-faint">Database name &amp; credentials are generated per environment — view/reveal them on the project’s <strong>Database</strong> tab.</p>
          )}
          {meta.note && <p className="text-content-faint">{meta.note}</p>}
        </div>
      )}
    </div>
  )
}

export default function ManagedServices({ value, onChange, showWebSql = false, requireDatabase = false, error = '', resourcePrefix = '' }) {
  const v = value || {}
  const rows = managedServiceList(v)
  const set = (patch) => onChange({ ...v, ...patch })
  return (
    <div className="rounded-xl border border-border bg-surface-raised/40 p-4 space-y-4">
      <div>
        <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider mb-1">Managed services</p>
        <p className="text-xs text-content-subtle">
          Databases, caches and object storage Rigger runs for you — consistent across every
          environment. They appear as services below and are wired into your app via env vars +
          <code className="font-mono"> depends_on</code>. Remove one by setting it back to <em>None</em> / off.
        </p>
      </div>

      <div className="grid grid-cols-3 gap-3 items-end">
        <div>
          <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
            Database{requireDatabase && <span className="text-danger-fg ml-0.5">*</span>}
          </label>
          <DatabaseSelect
            engine={requireDatabase && (v.database || 'none') === 'none' ? '' : (v.database || 'none')}
            version={v.dbVersion}
            onChange={(eng, ver) => set({ database: eng, dbVersion: ver })}
            caption={false}
          />
          {error && <p className="text-danger-fg text-xs mt-1">{error}</p>}
        </div>
        <MiniToggle label="Redis" hint="redis:7-alpine" checked={!!v.redis} onChange={x => set({ redis: x })} />
        <MiniToggle label="Garage S3" hint="self-hosted object store" checked={!!v.garage} onChange={x => set({ garage: x })} />
      </div>

      {(showWebSql || (v.database && v.database !== 'none')) && (
        <div className="pt-1 border-t border-border">
          <MiniToggle
            label="Adminer (web SQL client)"
            hint={showWebSql
              ? "Browser SQL client — becomes this project’s web entry; one-click auto-login from Manage Database. Protect the route (internal/VPN) for production DBs."
              : "Browser SQL client for this project’s database — routed at the adminer subdomain; one-click auto-login from Manage Database. Protect the route (internal/VPN) for production DBs."}
            checked={!!v.webSql}
            onChange={x => set({ webSql: x })}
          />
        </div>
      )}

      {rows.length > 0 && (
        <div className="space-y-2 pt-1">
          {rows.map(r => <ServiceRow key={r.name} row={r} resourcePrefix={resourcePrefix} />)}
        </div>
      )}
    </div>
  )
}
