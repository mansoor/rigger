// PortWarnings — a non-blocking amber banner listing duplicate host-port
// mappings detected in a workspace's services. Shown in the New- and
// Edit-workspace forms; the user can still save.
export default function PortWarnings({ warnings }) {
  if (!warnings || warnings.length === 0) return null
  return (
    <div className="mt-2 rounded-lg border border-amber-500/30 bg-amber-500/10 px-3 py-2 text-xs text-amber-300">
      <p className="font-medium mb-0.5">⚠ Possible port conflict</p>
      <ul className="list-disc list-inside space-y-0.5 text-amber-200/90">
        {warnings.map((w, i) => <li key={i}>{w}</li>)}
      </ul>
      <p className="text-amber-300/70 mt-1">You can still save — but the deploy may fail until each host port is unique.</p>
    </div>
  )
}
