// Shared vertical left-rail tab layout used by the settings-style screens
// (Admin, Manage Workspace, Edit Project, Housekeeping, Tools). Renders a sticky
// rail of tabs on the left and the active panel on the right.
//
// `tabs` is a flat array; an entry is either a group divider `{ group: 'Label' }`
// or a tab `{ id, label, icon?, count?, warn?, danger? }`. Pages keep their own
// page header above <VerticalTabs> and pass the active panel as children.
export default function VerticalTabs({ tabs, active, onChange, children }) {
  return (
    <div className="grid grid-cols-[200px_1fr] gap-7 items-start">
      <nav className="sticky top-4 flex flex-col gap-0.5">
        {tabs.map((t, i) =>
          t.group ? (
            <div
              key={`g-${i}`}
              className="px-3 pt-3 pb-1.5 text-[10px] font-semibold uppercase tracking-wider text-content-faint"
            >
              {t.group}
            </div>
          ) : (
            <button
              key={t.id}
              onClick={() => onChange(t.id)}
              className={`flex items-center gap-2.5 w-full text-left px-3 py-2 rounded-lg text-sm font-medium border-l-2 transition-colors ${
                active === t.id
                  ? t.danger
                    ? 'bg-surface-raised text-danger-fg border-danger'
                    : 'bg-surface-raised text-content-strong border-brand-500'
                  : 'text-content-subtle border-transparent hover:bg-surface hover:text-content'
              }`}
            >
              {t.icon && <span className="w-4 text-center opacity-90 shrink-0">{t.icon}</span>}
              <span className="truncate">{t.label}</span>
              {t.count != null && (
                <span
                  className={`ml-auto text-[11px] leading-none rounded-full px-1.5 py-0.5 ${
                    active === t.id
                      ? 'bg-brand-500 text-white'
                      : t.warn
                        ? 'bg-warning-subtle text-warning-fg'
                        : 'bg-surface-raised text-content-subtle'
                  }`}
                >
                  {t.count}
                </span>
              )}
            </button>
          )
        )}
      </nav>
      <div className="min-w-0">{children}</div>
    </div>
  )
}
