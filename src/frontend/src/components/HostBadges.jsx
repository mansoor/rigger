// HostCapabilityBadges — small status chips for a host's probed Swarm capability
// (from its last Test) and its build-only marker (image-distribution Phase 5).
// Swarm chips only appear once the host has been probed (swarm_state set).
export default function HostCapabilityBadges({ host, showRole = false }) {
  if (!host) return null
  const chip = 'text-[10px] px-1.5 py-0.5 rounded border'
  return (
    <>
      {showRole ? (
        host.build_only ? (
          <span className={`${chip} bg-amber-100/70 text-amber-700 border-amber-200 dark:bg-amber-950/60 dark:text-amber-300 dark:border-amber-800/40`}
            title="Dedicated builder — builds & pushes images, excluded from deploy targets">Build only</span>
        ) : (
          <span className={`${chip} bg-emerald-100/70 text-emerald-700 border-emerald-200 dark:bg-emerald-950/60 dark:text-emerald-300 dark:border-emerald-800/40`}
            title="Builds images and runs deployments">Build + Deploy</span>
        )
      ) : host.build_only && (
        <span className={`${chip} bg-amber-100/70 text-amber-700 border-amber-200 dark:bg-amber-950/60 dark:text-amber-300 dark:border-amber-800/40`}
          title="Dedicated builder — excluded from deploy-host pickers">build-only</span>
      )}
      {host.swarm_manager ? (
        <span className={`${chip} bg-sky-100/70 text-sky-700 border-sky-200 dark:bg-sky-950/60 dark:text-sky-300 dark:border-sky-800/40`}
          title="Swarm manager — can run stack deploys">swarm manager</span>
      ) : host.swarm_state === 'active' ? (
        <span className={`${chip} bg-surface-raised border-border-strong text-content-faint`}
          title="Swarm worker — not a manager; can't run stack deploys">swarm worker</span>
      ) : null}
    </>
  )
}
