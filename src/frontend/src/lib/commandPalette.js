// Command-palette search: a tiny dependency-free fuzzy matcher plus the registry
// that turns the current workspace's projects and Rigger's navigation surface into
// jump-to entries. Everything here is pure — the palette component feeds it the
// already-cached projects list (no new backend endpoint) and the caller's role, so
// results are scoped to the current workspace and to what the user may see.

// scoreText returns a match score for `query` against `text` (higher = better), or
// null when `query` isn't a subsequence of `text`. It rewards contiguous runs and
// word-boundary starts so "apik" ranks "API Keys" above an incidental scatter, and a
// whole-substring hit gets a big boost so exact typing wins.
export function scoreText(query, text) {
  if (!query) return 0
  if (!text) return null
  const q = query.toLowerCase()
  const t = text.toLowerCase()
  const sub = t.indexOf(q)
  let score = 0
  let qi = 0
  let streak = 0
  let prev = -2
  for (let ti = 0; ti < t.length && qi < q.length; ti++) {
    if (t[ti] === q[qi]) {
      let bonus = 1
      if (ti === 0 || /[\s\-_./:]/.test(t[ti - 1])) bonus += 8 // start / word boundary
      if (prev === ti - 1) { streak += 1; bonus += streak * 3 } else { streak = 0 }
      score += bonus
      prev = ti
      qi += 1
    }
  }
  if (qi < q.length) return null // not all query chars present, in order
  if (sub >= 0) score += 25 + (sub === 0 ? 15 : 0) // contiguous substring (prefix even better)
  score += Math.max(0, 24 - (t.length - q.length)) * 0.2 // prefer tighter matches
  return score
}

// scoreEntry scores a query against an entry's title (primary) and its keywords
// (secondary, discounted), returning the better of the two or null if neither hits.
export function scoreEntry(query, entry) {
  if (!query) return 0
  const title = scoreText(query, entry.title)
  const kw = entry.keywords ? scoreText(query, entry.keywords) : null
  const best = Math.max(title ?? -Infinity, kw != null ? kw * 0.55 : -Infinity)
  return best === -Infinity ? null : best
}

// Admin-settings tabs (mirrors SettingsPage TABS) with a few search synonyms so
// "certificate" finds Domains & TLS, "token" finds API Keys, etc.
const ADMIN_TABS = [
  { id: 'general', label: 'General', kw: 'key length password policy metrics storage' },
  { id: 'preferences', label: 'Preferences', kw: 'appearance theme confirm defaults' },
  { id: 'users', label: 'Users', kw: 'accounts people members invite' },
  { id: 'access-requests', label: 'Access Requests', kw: 'invites approval inbox' },
  { id: 'system-email', label: 'System Email', kw: 'smtp mail sender' },
  { id: 'api-keys', label: 'API Keys', kw: 'token rest api integration' },
  { id: 'updates', label: 'Updates', kw: 'version upgrade self-update' },
  { id: 'domains', label: 'Domains & TLS', kw: 'ssl certificate https lets encrypt cloudflare dns auto url' },
  { id: 'hosts', label: 'Remote Hosts', kw: 'ssh server docker host' },
  { id: 'registries', label: 'Docker Registries', kw: 'image registry push pull ghcr' },
  { id: 'backup-targets', label: 'Backup Targets', kw: 's3 sftp destination' },
  { id: 'notifications', label: 'Notifications', kw: 'slack webhook email channel alert' },
  { id: 'alerts', label: 'Alert Rules', kw: 'cpu memory threshold monitoring' },
]

// Workspace-management tabs (mirrors ManageWorkspacePage — already deep-links via ?tab=).
const WS_TABS = [
  { id: 'general', label: 'General', kw: 'rename tier order defaults' },
  { id: 'preferences', label: 'Preferences', kw: 'appearance theme confirm' },
  { id: 'members', label: 'Members', kw: 'team roles people access' },
  { id: 'access-requests', label: 'Access Requests', kw: 'invites approval inbox' },
  { id: 'api-keys', label: 'API Keys', kw: 'token rest api' },
  { id: 'domains', label: 'Domains & TLS', kw: 'ssl certificate https override' },
  { id: 'hosts', label: 'Remote Hosts', kw: 'ssh server docker' },
  { id: 'registries', label: 'Docker Registries', kw: 'image registry' },
  { id: 'git', label: 'Git', kw: 'provider github gitlab token deploy key' },
  { id: 'backup-targets', label: 'Backup Targets', kw: 's3 sftp' },
  { id: 'notifications', label: 'Notifications', kw: 'slack webhook email channel' },
  { id: 'access-lists', label: 'Access Lists', kw: 'proxy basic auth ip geoip' },
  { id: 'alerts', label: 'Alert Rules', kw: 'cpu memory threshold' },
  { id: 'danger', label: 'Danger Zone', kw: 'delete transfer destroy' },
]

const HK_TABS = [
  { id: 'dashboard', label: 'Dashboard', kw: 'disk usage docker cleanup' },
  { id: 'safety', label: 'Safety Center', kw: 'dangling unused images volumes build cache prune' },
  { id: 'migrations', label: 'Migration Leftovers', kw: 'source host cleanup wipe' },
  { id: 'automation', label: 'Automation & Logs', kw: 'journal kernel apt tmp' },
]

// buildEntries assembles every jump-to target for the palette from the static
// navigation surface (role-gated) and the current workspace's projects. `projects`
// is the already-fetched, already-RBAC-filtered list the sidebar renders.
export function buildEntries({ projects = [], currentWs = '', isAdmin = false, canAdminWs = false }) {
  const out = []
  const push = (e) => out.push(e)

  // ── Navigation (everyone) ──────────────────────────────────────────────────
  push({ id: 'nav-dashboard', title: 'Dashboard', subtitle: 'Home', category: 'Navigation', icon: '▚', url: '/', keywords: 'home overview projects' })
  push({ id: 'nav-tools', title: 'Tools', subtitle: 'Templates, backups, migration', category: 'Navigation', icon: '⚒', url: '/tools', keywords: 'templates backup restore migration import' })
  if (canAdminWs && currentWs) {
    push({ id: 'nav-newproject', title: 'New project', subtitle: 'Create a project in this workspace', category: 'Navigation', icon: '＋', url: `/workspaces/${currentWs}/projects/new`, keywords: 'create add deploy app' })
  }

  // ── Admin (super-admin only) ───────────────────────────────────────────────
  if (isAdmin) {
    push({ id: 'nav-proxy', title: 'Proxy Service', subtitle: 'Reverse-proxy routes', category: 'Admin', icon: '⇄', url: '/proxy', keywords: 'reverse proxy route traefik access list waf' })
    push({ id: 'nav-settings', title: 'Admin settings', subtitle: 'Global control-plane settings', category: 'Admin', icon: '⚙', url: '/settings', keywords: 'admin global configuration' })
    for (const t of ADMIN_TABS) {
      push({ id: `admin-${t.id}`, title: t.label, subtitle: 'Admin settings', category: 'Admin', icon: '⚙', url: `/settings?tab=${t.id}`, keywords: `admin settings ${t.kw}` })
    }
    push({ id: 'nav-housekeeping', title: 'Housekeeping', subtitle: 'Docker & host maintenance', category: 'Admin', icon: '🧹', url: '/housekeeping', keywords: 'cleanup prune disk maintenance' })
    for (const t of HK_TABS) {
      push({ id: `hk-${t.id}`, title: t.label, subtitle: 'Housekeeping', category: 'Admin', icon: '🧹', url: `/housekeeping?tab=${t.id}`, keywords: `housekeeping ${t.kw}` })
    }
  }

  // ── Workspace (workspace admins) ───────────────────────────────────────────
  if (canAdminWs && currentWs) {
    push({ id: 'nav-manage', title: 'Manage workspace', subtitle: 'Workspace settings', category: 'Workspace', icon: '◫', url: `/workspaces/${currentWs}/manage`, keywords: 'workspace settings members' })
    for (const t of WS_TABS) {
      push({ id: `ws-${t.id}`, title: t.label, subtitle: 'Manage workspace', category: 'Workspace', icon: '◫', url: `/workspaces/${currentWs}/manage?tab=${t.id}`, keywords: `workspace ${t.kw}` })
    }
  }

  // ── Projects & environments (current workspace) ────────────────────────────
  for (const p of projects) {
    const cfg = p.config || {}
    const display = cfg.project?.name || p.name
    const type = cfg.project?.type || 'custom'
    const services = (cfg.services || []).map(s => s.name).filter(Boolean)
    const envs = p.envs || []
    push({
      id: `proj-${p.name}`,
      title: display,
      subtitle: `Project · ${p.name}`,
      category: 'Project',
      icon: '▢',
      url: `/workspaces/${currentWs}/projects/${p.name}`,
      keywords: `${p.name} ${type} ${services.join(' ')} ${envs.join(' ')}`,
    })
    for (const env of envs) {
      push({
        id: `env-${p.name}-${env}`,
        title: `${display} · ${env}`,
        subtitle: `Environment · ${p.name}`,
        category: 'Environment',
        icon: '◆',
        url: `/workspaces/${currentWs}/projects/${p.name}`,
        keywords: `${env} ${p.name} environment ${type}`,
      })
    }
  }

  return out
}

// rank filters entries by the query and returns the best matches (empty query keeps
// the natural registry order, projects first, capped). Stable: ties keep input order.
export function rank(entries, query, limit = 40) {
  const q = query.trim()
  if (!q) return entries.slice(0, limit)
  const scored = []
  for (let i = 0; i < entries.length; i++) {
    const s = scoreEntry(q, entries[i])
    if (s != null) scored.push({ e: entries[i], s, i })
  }
  scored.sort((a, b) => (b.s - a.s) || (a.i - b.i))
  return scored.slice(0, limit).map(x => x.e)
}
