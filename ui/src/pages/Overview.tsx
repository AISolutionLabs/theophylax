import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { AlertTriangle, KeyRound, Plug, Sparkles } from 'lucide-react'
import { get } from '../lib/api'
import { useApp } from '../lib/app'
import { fmtCompact, fmtMs, fmtUSD, pctChange } from '../lib/format'
import { AreaChart, BarList } from '../components/charts'
import { Badge, CodeBlock, Empty, ErrorNote, Loading, PageHead, Panel, Seal } from '../components/ui'

interface OverviewData {
  last_24h: { requests: number; errors: number; tokens: number; cost: number; p95_ms: number }
  prev_24h: { requests: number; errors: number; tokens: number; cost: number; p95_ms: number }
  counts: Record<string, number>
  top_models: { model: string; requests: number; cost: number }[] | null
  guardrail_events_24h: number
  budget_alerts: { budget_id: string; scope_name: string; period: string; pct: number }[] | null
  unhealthy_providers: { name: string; error: string }[] | null
  role: string
}

function Delta({ cur, prev, invert }: { cur: number; prev: number; invert?: boolean }) {
  const d = pctChange(cur, prev)
  if (d === null) return <div className="delta">new in the last 24h</div>
  if (Math.abs(d) < 0.5) return <div className="delta">flat vs previous 24h</div>
  const up = d > 0
  return <div className="delta" style={{ color: (up !== !!invert) ? 'var(--ink-2)' : 'var(--muted)' }}>{up ? '▲' : '▼'} {Math.abs(d).toFixed(0)}% vs previous 24h</div>
}

export default function Overview() {
  const { org, isAdmin, me } = useApp()
  const ov = useQuery({ queryKey: ['overview', org.id], queryFn: () => get<OverviewData>(`/api/orgs/${org.id}/overview`), refetchInterval: 30_000 })
  const usage = useQuery({ queryKey: ['usage', org.id, 2, 'model'], queryFn: () => get<any>(`/api/orgs/${org.id}/usage?days=2`) })
  if (ov.isLoading) return <Loading />
  if (ov.error) return <ErrorNote error={ov.error} />
  const d = ov.data!
  const firstName = (me.user.name || me.user.email).split(/[\s@]/)[0]
  const noTraffic = d.last_24h.requests === 0 && d.prev_24h.requests === 0

  return (
    <>
      <PageHead title={`Good to see you, ${firstName}`} lede={`What's flowing through ${org.name} right now, and anything that needs a decision.`} />

      {(d.unhealthy_providers?.length || d.budget_alerts?.length || (isAdmin && d.counts.pending_reviews)) ? (
        <div className="stack" style={{ marginBottom: 18 }}>
          {d.unhealthy_providers?.map((p) => (
            <div className="callout bad" key={p.name}><AlertTriangle size={16} /><div><b>{p.name}</b> can't be used: {p.error}. <Link to="/providers">Check the connection</Link></div></div>
          ))}
          {d.budget_alerts?.map((b) => (
            <div className="callout warn" key={b.budget_id}><AlertTriangle size={16} /><div><b>{b.scope_name}</b> has used {b.pct.toFixed(0)}% of its {b.period} budget. <Link to="/budgets">Review budgets</Link></div></div>
          ))}
          {isAdmin && !!d.counts.pending_reviews && (
            <div className="callout accent"><Sparkles size={16} /><div>{d.counts.pending_reviews} marketplace submission{d.counts.pending_reviews > 1 ? 's are' : ' is'} waiting for review. <Link to="/registry?tab=review">Review now</Link></div></div>
          )}
        </div>
      ) : null}

      <div className="kpis" style={{ marginBottom: 18 }}>
        <div className="kpi"><div className="label">Requests, last 24h</div><div className="value">{fmtCompact(d.last_24h.requests)}</div><Delta cur={d.last_24h.requests} prev={d.prev_24h.requests} /></div>
        <div className="kpi"><div className="label">Spend, last 24h</div><div className="value">{fmtUSD(d.last_24h.cost)}</div><Delta cur={d.last_24h.cost} prev={d.prev_24h.cost} /></div>
        <div className="kpi"><div className="label">Tokens, last 24h</div><div className="value">{fmtCompact(d.last_24h.tokens)}</div><Delta cur={d.last_24h.tokens} prev={d.prev_24h.tokens} /></div>
        <div className="kpi">
          <div className="label">Error rate · p95 latency</div>
          <div className="value">{d.last_24h.requests ? ((d.last_24h.errors / d.last_24h.requests) * 100).toFixed(1) : '0.0'}%<span className="muted" style={{ fontSize: 16, fontWeight: 500 }}> · {fmtMs(d.last_24h.p95_ms)}</span></div>
          <div className="delta">{d.guardrail_events_24h} guardrail event{d.guardrail_events_24h === 1 ? '' : 's'}</div>
        </div>
      </div>

      {noTraffic ? (
        <GettingStarted hasKeys={d.counts.keys > 0} models={d.counts.models} />
      ) : (
        <div className="split">
          <Panel title="Requests per hour" sub="Last 48 hours">
            {usage.data ? <AreaChart data={(usage.data.series ?? []).map((s: any) => ({ t: s.t, v: s.requests }))} format={fmtCompact} interval={usage.data.interval} /> : <Loading />}
          </Panel>
          <Panel title="Busiest models" sub="Last 24 hours">
            <BarList items={(d.top_models ?? []).map((m) => ({ label: m.model, value: m.requests, sub: fmtUSD(m.cost) }))} format={fmtCompact} />
          </Panel>
        </div>
      )}

      <div className="kpis six" style={{ marginTop: 18 }}>
        {([['teams', 'Teams', '/teams'], ['projects', 'Projects', '/teams'], ['members', 'People', '/people'], ['keys', 'Active keys', '/keys'], ['agents', 'Agents', '/agents'], ['models', 'Routable models', '/models']] as const).map(([k, label, to]) => (
          <Link className="kpi" key={k} to={to} style={{ color: 'inherit', textDecoration: 'none' }}><div className="label">{label}</div><div className="value" style={{ fontSize: 22 }}>{d.counts[k] ?? 0}</div></Link>
        ))}
      </div>
    </>
  )
}

function GettingStarted({ hasKeys, models }: { hasKeys: boolean; models: number }) {
  const { isAdmin } = useApp()
  const base = window.location.origin
  return (
    <div className="split">
      <Panel title="Send your first request" sub="Point any Anthropic or OpenAI client at this gateway.">
        {models === 0 ? (
          <Empty icon={<Plug size={20} />} title="No models are routable yet" action={isAdmin ? <Link className="btn primary" to="/providers?new=1">Connect a provider</Link> : undefined}>
            {isAdmin ? 'Connect Anthropic, Bedrock or Azure, then add models to the catalog.' : 'Ask a platform administrator to connect a provider.'}
          </Empty>
        ) : (
          <div className="stack">
            <p className="muted">Claude Code works unchanged. Export these two variables, then run <code>claude</code> as usual:</p>
            <CodeBlock code={`export ANTHROPIC_BASE_URL=${base}\nexport ANTHROPIC_AUTH_TOKEN=tpx_…   # your gateway key`} />
            {!hasKeys && <Link className="btn primary" to="/keys?new=1" style={{ alignSelf: 'flex-start' }}><KeyRound size={15} /> Create a key</Link>}
          </div>
        )}
      </Panel>
      <Panel title="What makes this gateway different">
        <div className="row" style={{ alignItems: 'flex-start', gap: 14 }}>
          <Seal size={30} />
          <div>
            <p>Every response is issued a <b>signed receipt</b>. It records digests of the exact request and response, the model that served it and the policy versions in force, and contains no prompt content.</p>
            <p className="muted" style={{ marginTop: 8 }}>Open any row in <Link to="/requests">Requests</Link> to inspect and verify its receipt.</p>
            <div style={{ marginTop: 10 }}><Badge tone="bronze">Proof-of-Inference</Badge></div>
          </div>
        </div>
      </Panel>
    </div>
  )
}
