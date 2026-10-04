import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Download, RefreshCw } from 'lucide-react'
import { get, post, qs, type PriceQuote, type PricingStatus } from '../lib/api'
import { useApp } from '../lib/app'
import { download, fmtAgo, fmtCompact, fmtInt, fmtRate, fmtUSD, kindLabel, toCSV } from '../lib/format'
import { AreaChart, BarList } from '../components/charts'
import { Badge, Button, ErrorNote, Loading, PageHead, Panel, Segmented, Select, useAction } from '../components/ui'

interface Line {
  provider_kind: string; provider_name: string; upstream_model: string; price_key: string; requests: number
  input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number; cost: number; rates: PriceQuote | null
}
interface Statement {
  period: { month: string; start: string; end: string }
  totals: { cost: number; requests: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number; unpriced_requests: number; unpriced_tokens: number }
  lines: Line[]; daily: { t: string; cost: number }[]; by_team: { label: string; cost: number }[]; months: string[]; pricing: PricingStatus
}

const monthLabel = (m: string) => new Date(`${m}-01T00:00:00Z`).toLocaleDateString('en-US', { month: 'long', year: 'numeric', timeZone: 'UTC' })
const tokens = (l: Line) => l.input_tokens + l.output_tokens + l.cache_read_tokens + l.cache_write_tokens

export default function Billing() {
  const { org, isAdmin, isPlatformAdmin } = useApp()
  const [month, setMonth] = useState('')
  const [mine, setMine] = useState(!isAdmin)
  const q = useQuery({ queryKey: ['billing', org.id, month, mine], queryFn: () => get<Statement>(`/api/orgs/${org.id}/billing${qs({ month, mine })}`) })
  const refresh = useAction(() => post<PricingStatus>('/api/pricing/refresh'), { done: (s) => `Price catalog refreshed · ${fmtInt(s.models)} models`, invalidate: [['billing'], ['models']] })
  const d = q.data
  const t = d?.totals

  const exportCSV = () => d && download(`billing-${d.period.month}.csv`, toCSV(d.lines.map((l) => ({
    provider: l.provider_name || l.provider_kind, provider_kind: l.provider_kind, upstream_model: l.upstream_model, price_key: l.price_key, requests: l.requests,
    input_tokens: l.input_tokens, cache_read_tokens: l.cache_read_tokens, cache_write_tokens: l.cache_write_tokens, output_tokens: l.output_tokens,
    input_per_mtok: l.rates?.input ?? '', output_per_mtok: l.rates?.output ?? '', cache_read_per_mtok: l.rates?.cache_read ?? '', cache_write_per_mtok: l.rates?.cache_write ?? '',
    cost_usd: l.cost.toFixed(6),
  }))))

  return (
    <>
      <PageHead title="Billing" lede="What your traffic actually cost: every request is priced at the upstream provider's list price for the exact model that served it. No markup and no estimates."
        actions={<Button onClick={exportCSV} disabled={!d?.lines.length}><Download size={14} /> Export CSV</Button>} />
      <div className="row wrap" style={{ marginBottom: 16 }}>
        <Select value={month || d?.period.month || ''} onChange={(e) => setMonth(e.target.value)} style={{ width: 200 }} aria-label="Billing month"
          options={(d?.months ?? []).map((m) => ({ value: m, label: monthLabel(m) }))} />
        {isAdmin && <Segmented value={mine ? 'mine' : 'all'} onChange={(v) => setMine(v === 'mine')} options={[{ value: 'all', label: 'Everything I can see' }, { value: 'mine', label: 'Only my traffic' }]} />}
      </div>
      {q.isLoading ? <Loading /> : q.error ? <ErrorNote error={q.error} /> : d && t && (
        <div className="stack">
          {t.unpriced_requests > 0 && (
            <div className="callout warn"><AlertTriangle size={16} /><div>
              {fmtInt(t.unpriced_requests)} request{t.unpriced_requests > 1 ? 's' : ''} ({fmtCompact(t.unpriced_tokens)} tokens) went to upstream models that aren't in the price catalog, so they're not included in the total. They're marked “no list price” below.
            </div></div>
          )}
          <div className="kpis">
            <div className="kpi"><div className="label">Total, {monthLabel(d.period.month)}</div><div className="value">{fmtUSD(t.cost)}</div><div className="delta">{fmtInt(t.requests)} requests</div></div>
            <div className="kpi"><div className="label">Input tokens</div><div className="value">{fmtCompact(t.input_tokens)}</div></div>
            <div className="kpi"><div className="label">Cache read · write</div><div className="value">{fmtCompact(t.cache_read_tokens)}<span className="muted" style={{ fontSize: 16 }}> · {fmtCompact(t.cache_write_tokens)}</span></div></div>
            <div className="kpi"><div className="label">Output tokens</div><div className="value">{fmtCompact(t.output_tokens)}</div></div>
          </div>

          <Panel title="Line items" sub="One line per upstream model. Rates are USD per million tokens from the current catalog; amounts were fixed when each request was served." pad={false}>
            <div className="table-wrap">
              <table className="tbl">
                <thead><tr>
                  <th>Upstream model</th><th className="num">Requests</th>
                  <th className="num">Input</th><th className="num">Cache read</th><th className="num">Cache write</th><th className="num">Output</th>
                  <th className="num">Rate in / out</th><th className="num">Amount</th>
                </tr></thead>
                <tbody>
                  {d.lines.map((l) => {
                    const unpriced = !l.price_key && tokens(l) > 0
                    return (
                      <tr key={`${l.provider_kind}|${l.upstream_model}|${l.price_key}`}>
                        <td>
                          <div className="cell-title mono">{l.upstream_model}</div>
                          <div className="cell-sub">{l.provider_name || kindLabel[l.provider_kind] || l.provider_kind}{l.price_key && l.price_key !== l.upstream_model && l.price_key !== 'mock' && <> · priced as <span className="mono">{l.price_key}</span></>}</div>
                        </td>
                        <td className="num">{fmtInt(l.requests)}</td>
                        <td className="num">{fmtCompact(l.input_tokens)}</td>
                        <td className="num dim">{fmtCompact(l.cache_read_tokens)}</td>
                        <td className="num dim">{fmtCompact(l.cache_write_tokens)}</td>
                        <td className="num">{fmtCompact(l.output_tokens)}</td>
                        <td className="num dim">{l.price_key === 'mock' ? 'not billed' : l.rates ? <span title={`Cache read ${fmtRate(l.rates.cache_read)} · cache write ${fmtRate(l.rates.cache_write)}${l.rates.cache_write_1h != null ? ` (1h ${fmtRate(l.rates.cache_write_1h)})` : ''}${l.rates.tiers?.length ? ` · long-context rates above ${fmtCompact(l.rates.tiers[0])} tokens` : ''}`}>{fmtRate(l.rates.input)} / {fmtRate(l.rates.output)}</span> : unpriced ? <Badge tone="warn">no list price</Badge> : '—'}</td>
                        <td className="num">{unpriced ? '—' : fmtUSD(l.cost, true)}</td>
                      </tr>
                    )
                  })}
                  {!d.lines.length && <tr><td colSpan={8} className="muted" style={{ textAlign: 'center', padding: 28 }}>No traffic in {monthLabel(d.period.month)}.</td></tr>}
                </tbody>
                {d.lines.length > 0 && (
                  <tfoot><tr>
                    <td className="cell-title">Total</td><td className="num">{fmtInt(t.requests)}</td>
                    <td className="num">{fmtCompact(t.input_tokens)}</td><td className="num">{fmtCompact(t.cache_read_tokens)}</td><td className="num">{fmtCompact(t.cache_write_tokens)}</td><td className="num">{fmtCompact(t.output_tokens)}</td>
                    <td /><td className="num cell-title">{fmtUSD(t.cost, true)}</td>
                  </tr></tfoot>
                )}
              </table>
            </div>
          </Panel>

          <div className="grid2" style={{ alignItems: 'start' }}>
            <Panel title="Daily spend">
              <AreaChart data={d.daily.map((b) => ({ t: b.t, v: b.cost }))} format={(v) => fmtUSD(v)} interval="day" height={200} />
            </Panel>
            <Panel title="By team">
              {d.by_team.length ? <BarList items={d.by_team.map((b) => ({ label: b.label, value: b.cost }))} format={(v) => fmtUSD(v)} /> : <p className="muted">No billed spend this month.</p>}
            </Panel>
          </div>

          <Panel title="Price source" end={isPlatformAdmin && <Button size="sm" busy={refresh.isPending} onClick={() => refresh.mutate(undefined)}><RefreshCw size={13} /> Refresh now</Button>}>
            <dl className="kv">
              <dt>Catalog</dt><dd className="mono" style={{ wordBreak: 'break-all' }}>{d.pricing.source}</dd>
              <dt>Last updated</dt><dd>{d.pricing.fetched_at ? `${fmtAgo(d.pricing.fetched_at)} · ${fmtInt(d.pricing.models)} priced models · refreshed every ${d.pricing.refresh_every.replace(/(\d)m0s$/, '$1m').replace(/(\d)h0m$/, '$1h')}` : 'Not loaded yet'}</dd>
              {d.pricing.last_error && <><dt>Last refresh</dt><dd style={{ color: 'var(--bad)' }}>Failed: {d.pricing.last_error}. Previous prices stay in use.</dd></>}
            </dl>
          </Panel>
        </div>
      )}
    </>
  )
}
