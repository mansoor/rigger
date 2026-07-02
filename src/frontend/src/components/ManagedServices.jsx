import { useState } from 'react'
import DatabaseSelect from './DatabaseSelect'
import { Hint } from './ui'

// ManagedServices is the shared editor for a project's managed services (database,
// Redis, object/file storage). They are PROJECT-level — consistent across every
// environment — and surface as services. Used identically by the New Project wizard
// (Services step) and Edit Project (Services tab) so create/edit stay in sync.
//
// `value` is the normalized shape {database, dbVersion, redis, objectStorage,
// storageBucket, storagePath, storageUi, webSql, cloudbeaver}; callers map it to their
// own state. onChange receives the full updated value.

// Connection contract per managed service: what an app needs to talk to it
// in-network. The exact values (db name, credentials, tokens) are auto-generated
// PER ENVIRONMENT and live in each env's .env / the project's Database tab — here we
// document the env-var names + host/port pattern so app authors know what to read.
const SERVICE_META = {
  postgres:        { label: 'PostgreSQL',    port: '5432',      vars: ['POSTGRES_HOST', 'POSTGRES_PORT', 'POSTGRES_DB', 'POSTGRES_USER', 'POSTGRES_PASSWORD'], creds: true },
  mysql:           { label: 'MySQL',         port: '3306',      vars: ['MYSQL_HOST', 'MYSQL_PORT', 'MYSQL_DATABASE', 'MYSQL_USER', 'MYSQL_PASSWORD', 'MYSQL_ROOT_PASSWORD'], creds: true },
  mariadb:         { label: 'MariaDB',       port: '3306',      vars: ['MYSQL_HOST', 'MYSQL_PORT', 'MYSQL_DATABASE', 'MYSQL_USER', 'MYSQL_PASSWORD', 'MYSQL_ROOT_PASSWORD'], creds: true },
  mongodb:         { label: 'MongoDB',       port: '27017',     vars: ['MONGO_HOST', 'MONGO_PORT', 'MONGO_DB', 'MONGO_USER', 'MONGO_PASSWORD', 'MONGO_URI'], creds: true, note: 'Document store. The connection user is the root user (authSource=admin); a ready-to-use MONGO_URI is set in each environment’s .env. No web SQL client (Adminer is SQL-only) — view connection details on the Database tab.' },
  redis:           { label: 'Redis',         port: '6379',      vars: ['REDIS_HOST', 'REDIS_PORT'], note: 'No password by default.' },
  minio:           { label: 'MinIO (S3)',    port: '9000',      vars: ['AWS_ENDPOINT', 'AWS_BUCKET', 'AWS_ACCESS_KEY_ID', 'AWS_SECRET_ACCESS_KEY', 'AWS_USE_PATH_STYLE_ENDPOINT'], note: 'S3-compatible object store. The bucket is auto-created on first deploy; AWS_* credentials are the MinIO root user/password (revealable on the Database/Storage tab).' },
  storage_console: { label: 'MinIO Console', port: '9090',      vars: [], note: 'Browser admin UI for MinIO (opens3/console) — routed at the storage subdomain. Log in with the MinIO root credentials. Not used directly by your app.' },
  storage:         { label: 'Local volume',  volume: true,      vars: [], note: 'Uploads persist to a local named volume mounted at the app’s storage path — no S3 container. FILESYSTEM_DISK=local.' },
  adminer:         { label: 'Adminer',       port: '8978',      vars: [], note: 'Browser SQL client — becomes this project’s web entry; one-click auto-login from Manage Database.' },
  cloudbeaver:     { label: 'CloudBeaver',   port: '8978',      vars: [], note: 'Browser SQL client (legacy projects) — becomes this project’s web entry.' },
}

// managedServiceList derives the synthetic service rows from the picker value
// (mirrors backend workspace.managedDepServices).
export function managedServiceList({ database, redis, storageLocal, storageMinio, storageUi, webSql, cloudbeaver } = {}) {
  const out = []
  if (database && database !== 'none') out.push({ name: database, kind: database })
  if (redis) out.push({ name: 'redis', kind: 'redis' })
  // Object storage backends are independent — both may be on.
  if (storageMinio) {
    out.push({ name: 'minio', kind: 'minio' })
    if (storageUi) out.push({ name: 'storage_console', kind: 'storage_console' })
  }
  if (storageLocal) out.push({ name: 'storage', kind: 'storage' })
  if (webSql || cloudbeaver) out.push({ name: 'adminer', kind: 'adminer' })
  return out
}

// enabledDependsOnTargets returns the managed-service names a real service may
// depend_on (excludes UI-only sidecars / the local-volume pseudo-service).
export function enabledDependsOnTargets({ database, redis, storageMinio } = {}) {
  const out = []
  if (database && database !== 'none') out.push(database)
  if (redis) out.push('redis')
  if (storageMinio) out.push('minio')
  return out
}

// SQL_ENGINES are the relational engines that the Adminer web SQL client + the
// SQL management surface (schemas/users) apply to. Document stores (MongoDB) are
// excluded — they get connection info only (a mongo-express console is planned).
const SQL_ENGINES = ['postgres', 'mysql', 'mariadb']

// Group is a labelled subsection inside the Managed Services card, so related
// controls (Database/Redis/Adminer, Storage, Mail) read as one block.
function Group({ title, children }) {
  return (
    <div className="rounded-lg border border-border-strong/50 bg-surface-raised/30 p-3 space-y-3">
      <p className="text-[11px] font-semibold text-content-muted uppercase tracking-wider">{title}</p>
      {children}
    </div>
  )
}

function MiniToggle({ label, hint, checked, onChange }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div>
        <p className="text-sm text-content">{label}</p>
        {hint && <Hint className="mt-0.5">{hint}</Hint>}
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
  const ident = `${resourcePrefix || '<prefix>'}_<env>_${row.name}`
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
            {meta.volume ? <>Persistent volume </> : <>In-network host </>}
            <code className="font-mono text-content">{ident}</code>
            {meta.port && <> · port <code className="font-mono text-content">{meta.port}</code></>}
          </div>
          {meta.vars.length > 0 && (
            <div>
              <span className="text-content-muted">App connection env vars</span> (auto-set in each environment’s <code className="font-mono">.env</code>):
              <div className="mt-1 font-mono text-[11px] text-content break-all">{meta.vars.join('  ')}</div>
            </div>
          )}
          {meta.creds && (
            <Hint tone="faint">Database name &amp; credentials are generated per environment — view/reveal them on the project’s <strong>Database</strong> tab.</Hint>
          )}
          {meta.note && <Hint tone="faint">{meta.note}</Hint>}
        </div>
      )}
    </div>
  )
}

export default function ManagedServices({ value, onChange, showWebSql = false, requireDatabase = false, error = '', resourcePrefix = '' }) {
  const v = value || {}
  const rows = managedServiceList(v)
  const set = (patch) => onChange({ ...v, ...patch })
  const localOn = !!v.storageLocal
  const minioOn = !!v.storageMinio
  const isSql = SQL_ENGINES.includes(v.database)
  return (
    <div className="rounded-xl border border-border bg-surface-raised/40 p-4 space-y-4">
      <div>
        <p className="text-xs font-semibold text-content-subtle uppercase tracking-wider mb-1">Managed services</p>
        <Hint>
          Databases, caches and object storage Rigger runs for you — consistent across every
          environment. They appear as services below and are wired into your app via env vars +
          <code className="font-mono"> depends_on</code>. Remove one by setting it back to <em>None</em> / off.
          Adminer &amp; the MinIO console are <strong>defaults</strong> — each environment can override
          them (e.g. on in dev/stage, off in prod) in Edit Project → Environments → Tooling.
        </Hint>
      </div>

      {/* ── Database group: engine + Redis cache + the web SQL client ── */}
      <Group title="Database">
        <div className="grid grid-cols-2 gap-3 items-end">
          <div>
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">
              Engine{requireDatabase && <span className="text-danger-fg ml-0.5">*</span>}
            </label>
            <DatabaseSelect
              engine={requireDatabase && (v.database || 'none') === 'none' ? '' : (v.database || 'none')}
              version={v.dbVersion}
              onChange={(eng, ver) => set({ database: eng, dbVersion: ver })}
              caption={false}
            />
            {error && <p className="text-danger-fg text-xs mt-1">{error}</p>}
          </div>
          <MiniToggle label="Redis" hint="redis:7-alpine — in-network cache / queue" checked={!!v.redis} onChange={x => set({ redis: x })} />
        </div>
        {(showWebSql || isSql) && (
          <MiniToggle
            label="Adminer (web SQL client)"
            hint={showWebSql
              ? "Browser SQL client — becomes this project’s web entry; one-click auto-login from Manage Database. Protect the route (internal/VPN) for production DBs."
              : "Browser SQL client for this project’s database — routed at the adminer subdomain; one-click auto-login from Manage Database. Protect the route (internal/VPN) for production DBs."}
            checked={!!v.webSql}
            onChange={x => set({ webSql: x })}
          />
        )}
        {!isSql && (v.database && v.database !== 'none') && (
          <Hint tone="faint">{SERVICE_META[v.database]?.label || v.database} is not SQL — Adminer doesn’t apply. Connection details are on the Database tab.</Hint>
        )}
      </Group>

      {/* ── Storage group: local volume and/or MinIO (S3) + its admin console ── */}
      <Group title="Storage">
        <div className="grid grid-cols-2 gap-3">
          <MiniToggle label="Local volume" hint="persistent disk" checked={localOn} onChange={x => set({ storageLocal: x })} />
          <MiniToggle label="MinIO (S3)" hint="S3-compatible object store" checked={minioOn} onChange={x => set({ storageMinio: x })} />
        </div>

        {localOn && minioOn && (
          <Hint>Both backends are on — the local volume is mounted <em>and</em> MinIO runs; <code className="font-mono">FILESYSTEM_DISK</code> defaults to <code className="font-mono">s3</code> (override in the app’s env if you want local primary).</Hint>
        )}

        {localOn && (
          <div>
            <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Storage path (in container)</label>
            <input
              type="text"
              value={v.storagePath || ''}
              placeholder="/var/www/html/storage"
              onChange={e => set({ storagePath: e.target.value })}
              className="w-full bg-surface-raised border border-border rounded-lg px-3 py-2 text-sm font-mono text-content focus:outline-none focus:border-brand-500"
            />
            <Hint>A persistent volume is mounted here so uploads survive redeploys. Default suits Laravel; change it to match your app’s upload/storage directory.</Hint>
          </div>
        )}

        {minioOn && (
          <div className="space-y-3">
            <div>
              <label className="block text-xs font-semibold text-content-muted uppercase tracking-wider mb-1">Bucket name</label>
              <input
                type="text"
                value={v.storageBucket || ''}
                placeholder={`${resourcePrefix || '<project>'}-<env>  (auto)`}
                onChange={e => set({ storageBucket: e.target.value })}
                className="w-full bg-surface-raised border border-border rounded-lg px-3 py-2 text-sm font-mono text-content focus:outline-none focus:border-brand-500"
              />
              <Hint>Created automatically on first deploy. Leave blank to derive it as <code className="font-mono">{`{project}-{env}`}</code>; the environment is always appended.</Hint>
            </div>
            <MiniToggle
              label="MinIO admin console"
              hint="Optional browser admin UI (opens3/console) at the storage subdomain. Leave off for S3 only. Protect the route (internal/VPN, or the per-env basic-auth) in production."
              checked={!!v.storageUi}
              onChange={x => set({ storageUi: x })}
            />
          </div>
        )}
      </Group>

      {/* ── Mail group ── */}
      <Group title="Mail">
        <MiniToggle
          label="Mailpit (test SMTP)"
          hint="Catch-all SMTP + web inbox (axllent/mailpit) for testing outbound mail — routed at the mail subdomain. Default for new envs; each environment can override it (typically on in dev/stage, off in prod). Protect the route in production."
          checked={!!v.mailpit}
          onChange={x => set({ mailpit: x })}
        />
      </Group>

      {rows.length > 0 && (
        <div className="space-y-2 pt-1">
          {rows.map(r => <ServiceRow key={r.name} row={r} resourcePrefix={resourcePrefix} />)}
        </div>
      )}
    </div>
  )
}
