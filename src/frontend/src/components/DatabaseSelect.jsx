import { useQuery } from '@tanstack/react-query'
import { fetchDatabases } from '../lib/api'
import { Hint } from './ui'

// DatabaseSelect — catalog-driven managed-database picker shared by the New Project
// wizard and Edit Project, so the engine + version choices stay in sync everywhere.
// Renders an engine dropdown (None + the catalog) and, when the chosen engine ships
// more than one version, a version dropdown beside it (seeded to the engine default).
// onChange(engine, version) fires for both selections; the parent renders its own
// "Database" label so it matches each page's form styling.

const selCls = 'px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500'

export default function DatabaseSelect({ engine, version, onChange, caption = true, category = 'database', noneLabel = 'None' }) {
  const { data: all = [] } = useQuery({ queryKey: ['databases'], queryFn: fetchDatabases, staleTime: 5 * 60_000 })
  // Only offer engines of the requested category: the primary DB picker excludes the
  // auxiliary search/tsdb engines (they run alongside a DB, chosen in their own selectors).
  const catalog = all.filter(e => (e.category || 'database') === category)
  const sel = catalog.find(e => e.id === engine)
  const multi = sel && (sel.versions || []).length > 1
  const pickEngine = (id) => {
    const e = catalog.find(x => x.id === id)
    onChange(id, e ? e.default_version : '') // seed the version with the engine's default
  }
  return (
    <div>
      <div className="flex gap-2">
        <select value={engine || 'none'} onChange={e => pickEngine(e.target.value)} className={`${selCls} flex-1`}>
          <option value="none">{noneLabel}</option>
          {catalog.map(e => <option key={e.id} value={e.id}>{e.label}</option>)}
        </select>
        {multi && (
          <select value={version || sel.default_version} onChange={e => onChange(engine, e.target.value)} className={`${selCls} w-36`}>
            {sel.versions.map(v => <option key={v} value={v}>{v}</option>)}
          </select>
        )}
      </div>
      {caption && sel && (
        <Hint>{sel.label} {version || sel.default_version} — managed, with auto-generated credentials.</Hint>
      )}
    </div>
  )
}
