import { useEffect, useMemo, useRef, useState } from 'react'

export interface Point { t: string; v: number }

function niceMax(v: number) {
  if (v <= 0) return 1
  const p = Math.pow(10, Math.floor(Math.log10(v)))
  const n = v / p
  const step = n <= 1 ? 1 : n <= 2 ? 2 : n <= 2.5 ? 2.5 : n <= 5 ? 5 : 10
  return step * p
}

/**
 * Single-series area chart with a crosshair tooltip. The panel title names the
 * series, so no legend is drawn. Text uses ink tokens; only marks use the chart hue.
 */
export function AreaChart({ data, height = 200, format, interval }: { data: Point[]; height?: number; format: (v: number) => string; interval: 'hour' | 'day' }) {
  const ref = useRef<HTMLDivElement>(null)
  const [hover, setHover] = useState<number | null>(null)
  const [w, setW] = useState(720)
  useEffect(() => {
    if (!ref.current) return
    const ro = new ResizeObserver((e) => setW(Math.max(300, Math.round(e[0]!.contentRect.width))))
    ro.observe(ref.current)
    return () => ro.disconnect()
  }, [])
  const W = w, H = height, padL = 48, padR = 12, padT = 12, padB = 26
  const max = niceMax(Math.max(...data.map((d) => d.v), 0))
  const x = (i: number) => padL + (data.length <= 1 ? (W - padL - padR) / 2 : (i / (data.length - 1)) * (W - padL - padR))
  const y = (v: number) => padT + (1 - v / max) * (H - padT - padB)
  const line = data.map((d, i) => `${i ? 'L' : 'M'}${x(i).toFixed(1)},${y(d.v).toFixed(1)}`).join('')
  const area = data.length ? `${line}L${x(data.length - 1)},${y(0)}L${x(0)},${y(0)}Z` : ''
  const ticks = [0, 0.5, 1].map((f) => f * max)
  const label = (t: string) => {
    const d = new Date(t)
    return interval === 'hour' ? d.toLocaleString(undefined, { weekday: 'short', hour: 'numeric' }) : d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
  }
  const xTicks = useMemo(() => {
    if (data.length <= 1) return data.map((_, i) => i)
    const n = Math.min(W < 500 ? 3 : 6, data.length)
    return Array.from({ length: n }, (_, k) => Math.round((k * (data.length - 1)) / (n - 1)))
  }, [data.length, W])

  if (!data.length) return <div ref={ref} className="muted" style={{ height, display: 'grid', placeItems: 'center' }}>No traffic in this period yet.</div>

  const onMove = (e: React.MouseEvent) => {
    const r = ref.current!.getBoundingClientRect()
    const px = ((e.clientX - r.left) / r.width) * W
    let best = 0
    data.forEach((_, i) => { if (Math.abs(x(i) - px) < Math.abs(x(best) - px)) best = i })
    setHover(best)
  }
  const h = hover !== null ? data[hover] : null
  return (
    <div className="chart" ref={ref} onMouseMove={onMove} onMouseLeave={() => setHover(null)}>
      <svg viewBox={`0 0 ${W} ${H}`} role="img" aria-label="Time series chart">
        <g className="grid">{ticks.map((t) => <line key={t} x1={padL} x2={W - padR} y1={y(t)} y2={y(t)} strokeWidth="1" />)}</g>
        <g className="axis">
          {ticks.map((t) => <text key={t} x={padL - 8} y={y(t) + 4} textAnchor="end">{format(t)}</text>)}
          {xTicks.map((i) => <text key={i} x={x(i)} y={H - 6} textAnchor="middle">{label(data[i]!.t)}</text>)}
        </g>
        <path d={area} fill="var(--chart-wash)" />
        <path d={line} fill="none" stroke="var(--chart)" strokeWidth="2" strokeLinejoin="round" strokeLinecap="round" />
        {hover !== null && h && (
          <g>
            <line x1={x(hover)} x2={x(hover)} y1={padT} y2={y(0)} stroke="var(--line-strong)" strokeWidth="1" />
            <circle cx={x(hover)} cy={y(h.v)} r="5" fill="var(--chart)" stroke="var(--surface)" strokeWidth="2" />
          </g>
        )}
        {data.length === 1 && <circle cx={x(0)} cy={y(data[0]!.v)} r="5" fill="var(--chart)" stroke="var(--surface)" strokeWidth="2" />}
      </svg>
      {hover !== null && h && (
        <div className="chart-tip" style={{ left: `${(x(hover) / W) * 100}%`, top: `${(y(h.v) / H) * 100}%` }}>
          <b>{format(h.v)}</b>{label(h.t)}
        </div>
      )}
    </div>
  )
}

/** Ranked horizontal bars; value at the tip, label above, ink-colored text. */
export function BarList({ items, format }: { items: { label: string; value: number; sub?: string }[]; format: (v: number) => string }) {
  const max = Math.max(...items.map((i) => i.value), 0) || 1
  if (!items.length) return <div className="muted">No data yet.</div>
  return (
    <div className="barlist">
      {items.map((it) => (
        <div className="item" key={it.label} title={`${it.label}: ${format(it.value)}`}>
          <span style={{ overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>{it.label}{it.sub && <span className="muted"> {it.sub}</span>}</span>
          <span className="right" style={{ fontWeight: 560 }}>{format(it.value)}</span>
          <div className="bar" style={{ width: `${Math.max(1.5, (it.value / max) * 100)}%` }} />
        </div>
      ))}
    </div>
  )
}
