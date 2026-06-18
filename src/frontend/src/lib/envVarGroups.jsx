// Shared classification + label for grouping environment variables as
// "Application" (the app's own config) vs "System" (Rigger-managed). Used by both
// the Edit Project → Environments editor and the env-card Env-vars popup so the two
// views group identically.
//
// System = the keys Rigger generates: a managed dependency's connection vars
// (POSTGRES_*/MYSQL_*/MARIADB_*/REDIS_*/GARAGE_*), per-service image pointers
// (*_IMAGE), and a few platform vars. Everything else is the app's own config.
const SYS_VAR_PREFIXES = ['POSTGRES_', 'MYSQL_', 'MARIADB_', 'REDIS_', 'GARAGE_']
const SYS_VAR_EXACT = new Set(['ADMINER_LOGIN_SECRET', 'PROJECT_NAME', 'RESOURCE_PREFIX', 'REGISTRY', 'COMPOSE_PROJECT_NAME', 'MAIL_HOST'])

export function isSystemVar(k) {
  if (SYS_VAR_EXACT.has(k)) return true
  if (k.endsWith('_IMAGE')) return true
  return SYS_VAR_PREFIXES.some(p => k.startsWith(p))
}

// EnvVarGroupLabel is the small subheader shown above the Application / System
// groups when both are present.
export function EnvVarGroupLabel({ children }) {
  return <p className="text-[10px] font-semibold uppercase tracking-wider text-content-faint pt-1 first:pt-0">{children}</p>
}
