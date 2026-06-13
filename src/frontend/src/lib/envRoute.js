// resolveEnvRoute mirrors the backend composegen.resolveRoute: the URL an env is
// reachable at when it routes through Traefik (vs. host-port binding). Returns
// { url, domain, ssl, auto } or null when the env isn't domain-routed.
//
// Decision tree (same as the backend):
//   - Traefik off                  → null (host-port binding; use port links)
//   - explicit cfg.domain          → that domain (ssl from cfg.ssl_enabled)
//   - base domain set              → {prefix}-{env}.{base}, HTTPS (Let's Encrypt)
//   - none                         → {prefix}-{env}.localhost, HTTP (or HTTPS if localTLS)
// Underscores in the prefix become hyphens (valid DNS label).
export function resolveEnvRoute(cfg, prefix, envName, baseDomain, localTLS) {
  if (!cfg?.traefik_enabled) return null
  const explicit = (cfg.domain || '').trim()
  if (explicit) {
    const host = cfg.subdomain ? `${cfg.subdomain}.${explicit}` : explicit
    const ssl = !!cfg.ssl_enabled
    return { url: `${ssl ? 'https' : 'http'}://${host}`, domain: host, ssl, auto: false }
  }
  const label = String(prefix || '').replace(/_/g, '-') + '-' + envName
  const base = (baseDomain || '').trim()
  if (base) {
    const host = `${label}.${base}`
    return { url: `https://${host}`, domain: host, ssl: true, auto: true }
  }
  const host = `${label}.localhost`
  const ssl = !!localTLS
  return { url: `${ssl ? 'https' : 'http'}://${host}`, domain: host, ssl, auto: true }
}
