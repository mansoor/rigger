// Dependency-free inline sparkline. Fills its container's width responsively
// (viewBox + preserveAspectRatio="none") with a fixed pixel height. Used on env
// cards for the metrics history (Phase 6d).
export default function Sparkline({ values, height = 24, stroke = '#22d3ee', fill = true }) {
  const pts = (values || []).filter(v => typeof v === 'number' && !isNaN(v))
  if (pts.length < 2) {
    return <div style={{ height }} className="w-full flex items-center justify-center text-[10px] text-gray-600">—</div>
  }
  const W = 100 // viewBox width units; the SVG scales horizontally to its container
  const max = Math.max(...pts)
  const min = Math.min(...pts)
  const range = max - min || 1
  const stepX = W / (pts.length - 1)
  const coords = pts.map((v, i) => [i * stepX, height - ((v - min) / range) * (height - 2) - 1])
  const line = coords.map(([x, y]) => `${x.toFixed(2)},${y.toFixed(2)}`).join(' ')
  const area = `0,${height} ${line} ${W},${height}`
  return (
    <svg viewBox={`0 0 ${W} ${height}`} preserveAspectRatio="none"
      width="100%" height={height} className="block w-full">
      {fill && <polygon points={area} fill={stroke} opacity="0.12" />}
      <polyline points={line} fill="none" stroke={stroke} strokeWidth="1.5"
        vectorEffect="non-scaling-stroke" strokeLinejoin="round" strokeLinecap="round" />
    </svg>
  )
}
