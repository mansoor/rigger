import { WS_ROLES, GLOBAL_ROLES } from '../lib/roles'

// RoleHelp renders a compact legend explaining what each role can do, so the
// person granting access knows what they're handing out. `scope` selects the
// workspace-tier ladder ('workspace', default) or the global account roles.
export default function RoleHelp({ scope = 'workspace', className = '' }) {
  const roles = scope === 'global' ? GLOBAL_ROLES : WS_ROLES
  return (
    <div className={`rounded-lg border border-border bg-surface/40 p-3 ${className}`}>
      <p className="text-[11px] font-semibold uppercase tracking-wider text-content-subtle mb-1.5">
        What each role can do
      </p>
      <dl className="space-y-1.5">
        {roles.map(r => (
          <div key={r.value} className="flex gap-2 text-xs leading-snug">
            <dt className="shrink-0 w-24 font-semibold text-content">{r.label}</dt>
            <dd className="flex-1 text-content-muted">{r.desc}</dd>
          </div>
        ))}
      </dl>
    </div>
  )
}
