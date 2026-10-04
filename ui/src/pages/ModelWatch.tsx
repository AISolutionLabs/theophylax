import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Radar, RefreshCw, Sparkles } from 'lucide-react'
import { get, post } from '../lib/api'
import { fmtAgo, fmtCompact } from '../lib/format'
import { Badge, Button, Input, useAction } from '../components/ui'

type Entry = { key: string; input_price?: number; output_price?: number; context_window?: number; max_output?: number; mode?: string; deprecation_date?: string; capabilities?: string[] }
type Found = { id: string; provider_id: string; provider_name: string; provider_kind: string; upstream_model: string; display_name: string; first_seen: string; suggested_name: string; catalog?: Entry }
type Notice = { model_id: string; model_name: string; upstream_model: string; provider_name: string; reason: 'deprecated' | 'deprecating'; catalog: Entry }
type Watch = { new: Found[]; lifecycle: Notice[]; last_run?: { ran_at: string; providers: number; new_models: number; errors: string[] }; interval: string; catalog_models: number }

const usd = (v?: number | null) => (v == null ? '—' : `$${+v.toFixed(4)}`)
const reasonLabel = { deprecated: 'Retired by provider', deprecating: 'Retiring soon' }

// Model watch: new models from connected providers and catalog suggestions.
// Shown to platform admins at the top of Models & routing.
export function ModelWatch() {
  const w = useQuery({ queryKey: ['model-watch'], queryFn: () => get<Watch>('/api/model-watch'), refetchInterval: 60_000 })
  const [names, setNames] = useState<Record<string, string>>({})
  const scan = useAction(() => post<any>('/api/model-watch/scan', {}), { done: (r: any) => (r.new?.length ? `Found ${r.new.length} new model(s)` : 'No new models'), invalidate: [['model-watch']] })
  const add = useAction((f: Found) => post<any>(`/api/model-watch/discoveries/${f.id}/add`, { name: names[f.id] ?? f.suggested_name }),
    { done: (r: any) => `Added ${r.model_name}${r.priced ? ' with catalog pricing' : ''}`, invalidate: [['model-watch'], ['models']] })
  const dismiss = useAction((f: Found) => post(`/api/model-watch/discoveries/${f.id}/dismiss`, {}), { done: 'Dismissed', invalidate: [['model-watch']] })
  if (!w.data) return null
  const d = w.data
  const lifecycle = d.lifecycle
  return (
    <section className="panel" style={{ marginBottom: 16, padding: 18 }}>
      <div className="row" style={{ alignItems: 'flex-start' }}>
        <Radar size={18} style={{ marginTop: 2 }} />
        <div style={{ flex: 1 }}>
          <h3 style={{ margin: 0 }}>Model watch</h3>
          <p className="muted" style={{ margin: '2px 0 0', fontSize: 12.5 }}>
            Checks connected providers every {d.interval} for new models and describes them from the price catalog ({fmtCompact(d.catalog_models)} models).
            {d.last_run ? <> Last checked {fmtAgo(d.last_run.ran_at)} across {d.last_run.providers} provider{d.last_run.providers === 1 ? '' : 's'}.</> : ' Not run yet.'}
          </p>
        </div>
        <Button size="sm" busy={scan.isPending} onClick={() => scan.mutate(undefined)}><RefreshCw size={13} /> Check now</Button>
      </div>
      {d.new.length > 0 && (
        <table className="tbl" style={{ marginTop: 14 }}>
          <thead><tr><th>New from your providers</th><th>Provider</th><th className="num">In / out per 1M</th><th className="num">Context</th><th>Name in catalog</th><th /></tr></thead>
          <tbody>{d.new.map((f) => (
            <tr key={f.id}>
              <td><div className="row" style={{ gap: 6 }}><Sparkles size={13} /><span className="mono">{f.upstream_model}</span></div><div className="cell-sub">first seen {fmtAgo(f.first_seen)}</div></td>
              <td className="dim">{f.provider_name}</td>
              <td className="num">{f.catalog ? `${usd(f.catalog.input_price)} / ${usd(f.catalog.output_price)}` : <span className="dim">not in catalog</span>}</td>
              <td className="num dim">{f.catalog?.context_window ? fmtCompact(f.catalog.context_window) : '—'}</td>
              <td><Input className="code" value={names[f.id] ?? f.suggested_name} onChange={(e) => setNames((n) => ({ ...n, [f.id]: e.target.value }))} style={{ maxWidth: 220 }} /></td>
              <td className="tight"><div className="row" style={{ gap: 6 }}>
                <Button size="sm" variant="primary" onClick={() => add.mutate(f)}>Add</Button>
                <Link className="btn sm" to={`/rollouts?new=1&provider=${encodeURIComponent(f.provider_id)}&upstream=${encodeURIComponent(f.upstream_model)}`} title="Evaluate it against an existing model before switching traffic">Trial as rollout</Link>
                <Button size="sm" variant="ghost" onClick={() => dismiss.mutate(f)}>Dismiss</Button>
              </div></td>
            </tr>))}
          </tbody>
        </table>
      )}
      {lifecycle.length > 0 && (
        <div className="callout warn" style={{ marginTop: 10 }}>
          <div>{lifecycle.map((s) => <div key={s.model_id + s.reason}><Badge tone="warn">{reasonLabel[s.reason]}</Badge> <span className="mono">{s.model_name}</span> <span className="muted">({s.upstream_model} on {s.provider_name}, {s.catalog.deprecation_date})</span></div>)}
            <div className="muted" style={{ marginTop: 6, fontSize: 12.5 }}>Start a rollout to a newer model now; aliases move every client in one change.</div></div>
        </div>
      )}
      {d.last_run?.errors?.length ? <p className="muted" style={{ marginTop: 10, fontSize: 12 }}>Last check had issues: {d.last_run.errors.join('; ')}</p> : null}
    </section>
  )
}
