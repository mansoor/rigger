import { useState } from 'react'
import { Hint, Btn } from './ui'

// Sentinel category value for "templates with no categories".
export const UNCATEGORIZED = '__uncat__'

// TemplateCard — a single template tile (label + optional website link + container
// count + description + tags). Shared by the new-project wizard and the Template
// Manager so both render templates identically.
export function TemplateCard({ tmpl, selected, onClick, onDoubleClick }) {
  const tagColors = ['bg-info-subtle text-info-fg', 'bg-purple-100 text-purple-700 dark:bg-purple-950 dark:text-purple-300', 'bg-success-subtle text-success-fg']
  return (
    <button
      type="button" onClick={onClick} onDoubleClick={onDoubleClick}
      className={`text-left w-full p-4 rounded-xl border transition-all ${
        selected
          ? 'border-brand-500 bg-brand-950/30'
          : 'border-border-strong bg-surface-raised/40 hover:border-border-strong'
      }`}
    >
      <div className="flex items-start justify-between gap-2 mb-1">
        <p className="font-medium text-content-strong text-sm flex items-center gap-1.5 min-w-0">
          <span className="truncate">{tmpl.label}</span>
          {tmpl.website && (
            <span
              role="link" tabIndex={0}
              title={`Open ${tmpl.website} in a new tab`}
              onClick={e => { e.stopPropagation(); window.open(tmpl.website, '_blank', 'noopener,noreferrer') }}
              onKeyDown={e => { if (e.key === 'Enter' || e.key === ' ') { e.stopPropagation(); e.preventDefault(); window.open(tmpl.website, '_blank', 'noopener,noreferrer') } }}
              className="text-content-faint hover:text-accent-text cursor-pointer shrink-0"
              aria-label="Open project website"
            >
              <svg viewBox="0 0 20 20" className="w-3.5 h-3.5 inline-block align-middle" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                <path d="M11 3h6v6" /><path d="M17 3l-8 8" /><path d="M15 12v4a1 1 0 01-1 1H4a1 1 0 01-1-1V6a1 1 0 011-1h4" />
              </svg>
            </span>
          )}
        </p>
        <span className="text-xs text-content-subtle shrink-0">{tmpl.image_count} container{tmpl.image_count !== 1 ? 's' : ''}</span>
      </div>
      {tmpl.description && <p className="text-xs text-content-muted mb-2">{tmpl.description}</p>}
      <div className="flex flex-wrap gap-1">
        {(tmpl.tags || []).slice(0, 3).map((tag, i) => (
          <span key={tag} className={`text-xs px-1.5 py-0.5 rounded ${tagColors[i % tagColors.length]}`}>{tag}</span>
        ))}
      </div>
    </button>
  )
}

// TemplateBrowserModal — the wide, searchable, category-filtered template grid
// (1→4 columns by viewport). Shared by the new-project wizard's "Browse all
// templates" and the Template Manager's "Open existing template". The consumer's
// onSelect(tmpl) decides what happens (select into the wizard / load into the
// editor) and is responsible for closing if needed; onClose handles the ✕/backdrop.
//
// confirmSelect (opt-in): when true, a single card click only HIGHLIGHTS a pending
// choice (no accidental commit while scrolling) and the footer gains explicit
// OK/Cancel buttons — OK fires onSelect(pending), Cancel closes. Double-clicking a
// card is a shortcut that confirms immediately. When false (default, e.g. the
// Template Manager), a card click fires onSelect right away as before.
export default function TemplateBrowserModal({ templates = [], selected, onSelect, onClose, title = 'All templates', subtitle, footer, confirmSelect = false }) {
  const [search, setSearch] = useState('')
  const [category, setCategory] = useState('') // '' = all; UNCATEGORIZED = no categories
  // In confirm mode, the highlighted-but-not-yet-committed template (seeded from
  // the current selection so reopening shows what's already chosen).
  const [pending, setPending] = useState(() => templates.find(t => t.name === selected) || null)

  const activeName = confirmSelect ? pending?.name : selected
  const handleCardClick = confirmSelect ? (tmpl => setPending(tmpl)) : onSelect
  const confirm = () => { if (pending) onSelect(pending) }

  const allCategories = [...new Set(templates.flatMap(t => t.categories || []))].sort((a, b) => a.localeCompare(b))
  const hasUncategorized = templates.some(t => !(t.categories || []).length)

  const filtered = templates.filter(t => {
    const s = search.toLowerCase()
    const matchesSearch = !search ||
      (t.label || '').toLowerCase().includes(s) ||
      (t.name || '').toLowerCase().includes(s) ||
      (t.tags || []).some(tag => tag.toLowerCase().includes(s)) ||
      (t.categories || []).some(c => c.toLowerCase().includes(s))
    const cats = t.categories || []
    const matchesCategory = !category || (category === UNCATEGORIZED ? cats.length === 0 : cats.includes(category))
    return matchesSearch && matchesCategory
  })

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center p-4 bg-black/70" onClick={onClose}>
      <div className="bg-surface border border-border-strong rounded-2xl w-full max-w-5xl max-h-[85vh] flex flex-col shadow-2xl" onClick={e => e.stopPropagation()}>
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-4 border-b border-border gap-3">
          <div className="min-w-0">
            <h3 className="text-base font-semibold text-content-strong">{title} <span className="font-normal text-content-faint">({filtered.length})</span></h3>
            {subtitle && <Hint className="mt-0.5">{subtitle}</Hint>}
          </div>
          <button type="button" onClick={onClose} className="text-content-subtle hover:text-content-strong transition-colors text-xl leading-none shrink-0">×</button>
        </div>
        {/* Search + category filter */}
        <div className="px-5 py-3 border-b border-border flex items-center gap-2">
          <input
            type="text" value={search} onChange={e => setSearch(e.target.value)} placeholder="Search templates…" autoFocus
            className="flex-1 px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm placeholder-content-subtle focus:outline-none focus:border-brand-500"
          />
          {(allCategories.length > 0 || hasUncategorized) && (
            <select
              value={category} onChange={e => setCategory(e.target.value)}
              className="px-3 py-2 bg-surface-raised border border-border-strong rounded-lg text-content-strong text-sm focus:outline-none focus:border-brand-500 shrink-0 max-w-[12rem]"
            >
              <option value="">All categories</option>
              {allCategories.map(c => <option key={c} value={c}>{c}</option>)}
              {hasUncategorized && <option value={UNCATEGORIZED}>Uncategorized</option>}
            </select>
          )}
        </div>
        {/* Grid */}
        <div className="overflow-y-auto p-5 flex-1">
          {filtered.length === 0 ? (
            <p className="text-sm text-content-subtle text-center py-8">No templates match your search{category ? ' in this category' : ''}.</p>
          ) : (
            <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 xl:grid-cols-4 gap-3">
              {filtered.map(tmpl => (
                <TemplateCard
                  key={tmpl.name} tmpl={tmpl}
                  selected={activeName === tmpl.name}
                  onClick={() => handleCardClick(tmpl)}
                  onDoubleClick={confirmSelect ? () => onSelect(tmpl) : undefined}
                />
              ))}
            </div>
          )}
        </div>
        {confirmSelect ? (
          <div className="px-5 py-3 border-t border-border bg-surface-raised/30 flex items-center gap-3">
            <div className="min-w-0 flex-1">
              {pending
                ? <p className="text-sm text-content-muted truncate"><span className="text-accent-text">✓</span> <strong className="text-content-strong">{pending.label}</strong> — click OK to use it.</p>
                : (footer || <p className="text-sm text-content-subtle">Pick a template, then click OK.</p>)}
            </div>
            <Btn variant="outline" size="md" onClick={onClose} className="shrink-0">Cancel</Btn>
            <Btn variant="primary" size="md" onClick={confirm} disabled={!pending} className="shrink-0">OK</Btn>
          </div>
        ) : (
          footer && <div className="px-5 py-3 border-t border-border bg-surface-raised/30">{footer}</div>
        )}
      </div>
    </div>
  )
}
