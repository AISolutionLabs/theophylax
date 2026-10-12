import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Download } from 'lucide-react'
import { get, qs } from '../lib/api'
import { useApp } from '../lib/app'
import { download, fmtCompact, fmtInt, fmtMs, fmtUSD, toCSV } from '../lib/format'
import { AreaChart } from '../components/charts'
import { Button, ErrorNote, Input, Loading, PageHead, Panel, Segmented, Select } from '../components/ui'

type Metric = 'cost' | 'requests' | 'tokens'

export default function Usage() {
  const { org, isAdmin } = useApp()
  const [days, setDays] = useState('30')
  const [groupBy, setGroupBy] = useState('model')
  const [metric, setMetric] = useState<Metric>('cost')
  const [mine, setMine] = useState(!isAdmin)
  const [tagKey, setTagKey] = useState('')
  const [tagFilter, setTagFilter] = useState('')
  const groupParam = groupBy === 'tag' ? (tagKey.trim() ? `tag:${tagKey.trim().toLowerCase()}` : 'model') : groupBy
  const u = useQuery({ queryKey: ['usage', org.id, days, groupParam, mine, tagFilter], queryFn: () => get<any>(`/api/orgs/${org.id}/usage${qs({ days, group_by: groupParam, mine, tag: tagFilter.trim() })}`) })
  const t = u.data?.totals
  const fmt = metric === 'cost' ? (v: number) => fmtUSD(v) : fmtCompact
  const label = { cost: 'Spend', requests: 'Requests', tokens: 'Tokens' }[metric]
  return (
    <>
      <PageHead title="Usage & spend" lede="Chargeback-ready breakdowns by team, project, key, person, model, provider, agent or request tag."
        actions={<Button onClick={() => download(`usage-${groupBy}-${days}d.csv`, toCSV(u.data?.breakdown ?? []))}><Download size={14} /> Export CSV</Button>} />
      <div className="row wrap" style={{ marginBottom: 16 }}>
        <Segmented value={days} onChange={setDays} options={[{ value: '1', label: '24h' }, { value: '7', label: '7 days' }, { value: '30', label: '30 days' }, { value: '90', label: '90 days' }]} />
        <Select value={groupBy} onChange={(e) => setGroupBy(e.target.value)} style={{ width: 200 }} options={[{ value: 'model', label: 'By model' }, { value: 'team', label: 'By team' }, { value: 'project', label: 'By project' }, { value: 'key', label: 'By API key' }, { value: 'user', label: 'By person' }, { value: 'provider', label: 'By provider' }, { value: 'agent', label: 'By agent' }, { value: 'tag', label: 'By tag…' }]} />
        {groupBy === 'tag' && <Input placeholder="Tag key, e.g. feature" value={tagKey} onChange={(e) => setTagKey(e.target.value)} style={{ width: 180 }} aria-label="Tag key to group by" />}
        <Input placeholder="Filter tags, e.g. env:prod" value={tagFilter} onChange={(e) => setTagFilter(e.target.value)} style={{ width: 220 }} aria-label="Filter by tags" />
        {isAdmin && <Segmented value={mine ? 'mine' : 'all'} onChange={(v) => setMine(v === 'mine')} options={[{ value: 'all', label: 'Everything I can see' }, { value: 'mine', label: 'Only my traffic' }]} />}
      </div>
      {u.isLoading ? <Loading /> : u.error ? <ErrorNote error={u.error} /> : (
        <div className="stack">
          <div className="kpis">
            <div className="kpi"><div className="label">Spend</div><div className="value">{fmtUSD(t.cost)}</div></div>
            <div className="kpi"><div className="label">Requests</div><div className="value">{fmtCompact(t.requests)}</div><div className="delta">{t.requests ? ((t.errors / t.requests) * 100).toFixed(1) : 0}% failed</div></div>
            <div className="kpi"><div className="label">Tokens</div><div className="value">{fmtCompact(t.tokens)}</div></div>
            <div className="kpi"><div className="label">Latency p50 · p95</div><div className="value">{fmtMs(t.p50_ms)}<span className="muted" style={{ fontSize: 16 }}> · {fmtMs(t.p95_ms)}</span></div><div className="delta">first byte avg {fmtMs(t.avg_ttfb_ms)}</div></div>
          </div>
          <Panel title={`${label} per ${u.data.interval}`} end={<Segmented value={metric} onChange={setMetric} options={[{ value: 'cost', label: 'Spend' }, { value: 'requests', label: 'Requests' }, { value: 'tokens', label: 'Tokens' }]} />}>
            <AreaChart data={u.data.series.map((s: any) => ({ t: s.t, v: s[metric] }))} format={fmt} interval={u.data.interval} height={220} />
          </Panel>
          <Panel title="Breakdown" pad={false}>
            <div className="table-wrap">
              <table className="tbl">
                <thead><tr><th>{groupBy === 'user' ? 'Person' : groupBy === 'tag' ? (tagKey.trim() ? `Tag: ${tagKey.trim()}` : 'Model') : groupBy[0]!.toUpperCase() + groupBy.slice(1)}</th><th className="num">Requests</th><th className="num">Failed</th><th className="num">Input</th><th className="num">Cached</th><th className="num">Output</th><th className="num">p50 / p95</th><th className="num">Spend</th><th style={{ width: 140 }} aria-label="Share of spend" /></tr></thead>
                <tbody>
                  {u.data.breakdown.map((b: any) => (
                    <tr key={b.id + b.label}>
                      <td className="cell-title">{b.label || '—'}</td>
                      <td className="num">{fmtInt(b.requests)}</td>
                      <td className="num dim">{fmtInt(b.errors)}</td>
                      <td className="num">{fmtCompact(b.input_tokens)}</td>
                      <td className="num dim">{fmtCompact(b.cached_tokens)}</td>
                      <td className="num">{fmtCompact(b.output_tokens)}</td>
                      <td className="num dim">{fmtMs(b.p50_ms)} / {fmtMs(b.p95_ms)}</td>
                      <td className="num">{fmtUSD(b.cost)}</td>
                      <td><div className="meter"><i style={{ width: `${t.cost ? Math.max(1, (b.cost / t.cost) * 100) : 0}%`, background: 'var(--chart)' }} /></div></td>
                    </tr>
                  ))}
                  {!u.data.breakdown.length && <tr><td colSpan={9} className="muted" style={{ textAlign: 'center', padding: 28 }}>No traffic in this period.</td></tr>}
                </tbody>
              </table>
            </div>
          </Panel>
        </div>
      )}
    </>
  )
}
