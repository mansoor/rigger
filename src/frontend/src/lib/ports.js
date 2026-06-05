// Port-mapping validation shared by the New- and Edit-workspace forms.
//
// A host port can only be bound once per host, so the same host port mapped by
// more than one service (or twice by one service) will fail at deploy time. We
// surface these as non-blocking warnings — the user can still save.

// portConflicts takes services of the shape { name, ports: [hostPort,...] } and
// returns human-readable warning strings for any host port used more than once.
export function portConflicts(services) {
  const occ = new Map() // host port -> [service name, ...]
  for (const s of services || []) {
    for (const p of s.ports || []) {
      const port = String(p).trim()
      if (!/^\d+$/.test(port)) continue // ignore blanks / ${VAR} / ranges
      if (!occ.has(port)) occ.set(port, [])
      occ.get(port).push(s.name || '(unnamed)')
    }
  }
  const warnings = []
  for (const [port, names] of occ) {
    if (names.length > 1) {
      warnings.push(`Host port ${port} is mapped ${names.length}× (${names.join(', ')}) — each host port must be unique.`)
    }
  }
  return warnings
}

// hostPortsFromMappings extracts host ports from the New-workspace image shape
// (img.portMappings = [{ host, container, link }]).
export function hostPortsFromMappings(img) {
  return (img.portMappings || []).map(p => p.host).filter(Boolean)
}

// hostPortsFromConfig extracts host ports from the stored/Edit image shape
// (host_port + extra_ports like "8080:80").
export function hostPortsFromConfig(img) {
  const out = []
  if (img.host_port) out.push(String(img.host_port))
  for (const ep of img.extra_ports || []) {
    const h = String(ep).split(':')[0]
    if (h) out.push(h)
  }
  return out
}
