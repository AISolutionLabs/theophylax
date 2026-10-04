import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { AlertTriangle, Network, Plus, Trash2 } from 'lucide-react'
import { del, patch, post, type Deployment, type Model, type Provider } from '../lib/api'
import { useApp } from '../lib/app'
import { useModels, useProviders } from '../lib/queries'
import { fmtCompact, kindLabel } from '../lib/format'
import { Badge, Button, ChipsInput, Confirm, Drawer, Empty, ErrorNote, Field, Input, Loading, PageHead, Select, Tabs, useAction } from '../components/ui'

import { ModelWatch } from './ModelWatch'

const price = (v?: number | null) => (v == null ? '—' : `$${v}`)

export default function Models() {
  const { isPlatformAdmin } = useApp()
  const models = useModels()
  const [open, setOpen] = useState<Model | 'new' | null>(null)
  const [q, setQ] = useState('')
  const list = (models.data ?? []).filter((m) => !q || (m.name + m.display_name + m.aliases.join(' ')).toLowerCase().includes(q.toLowerCase()))
  const unpriced = (models.data ?? []).filter((m) => m.deployments.length && m.input_price == null).length
  return (
    <>
      <PageHead title="Models & routing" lede="The public model catalog. Each model routes to one or more deployments across providers with automatic failover; aliases let you move everyone to a new version in one change."
        actions={isPlatformAdmin && <Button variant="primary" onClick={() => setOpen('new')}><Plus size={15} /> Add model</Button>} />
      {isPlatformAdmin && <ModelWatch />}
      {unpriced > 0 && <div className="callout warn" style={{ marginBottom: 14 }}><AlertTriangle size={16} /><div>{unpriced} routable model{unpriced > 1 ? 's have' : ' has'} no price set, so spend and dollar budgets can't be calculated for {unpriced > 1 ? 'them' : 'it'}. Token budgets still work.</div></div>}
      <div className="row" style={{ marginBottom: 14 }}><Input placeholder="Filter models" value={q} onChange={(e) => setQ(e.target.value)} style={{ maxWidth: 320 }} /></div>
      <div className="panel">
        {models.isLoading ? <Loading /> : models.error ? <ErrorNote error={models.error} /> : !list.length ? (
          <Empty icon={<Network size={20} />} title="The catalog is empty">Connect a provider, then use “Discover models” to add what it offers.</Empty>
        ) : (
          <table className="tbl">
            <thead><tr><th>Model</th><th>Routes to</th><th>Strategy</th><th className="num">In / out per 1M</th><th className="num">Context</th><th className="num">Requests, 24h</th><th>Status</th></tr></thead>
            <tbody>
              {list.map((m) => (
                <tr key={m.id} className="click" onClick={() => setOpen(m)}>
                  <td><div className="cell-title mono">{m.name}</div><div className="cell-sub">{m.display_name}{m.aliases.length ? ` · aliases: ${m.aliases.join(', ')}` : ''}</div></td>
                  <td>{m.deployments.length ? m.deployments.map((d) => <div key={d.id} className="cell-sub" style={{ color: 'var(--ink-2)' }}>{d.provider_name} <span className="dim">p{d.priority}{d.enabled ? '' : ' · off'}</span></div>) : <span className="dim">No deployments</span>}</td>
                  <td className="dim">{m.strategy}</td>
                  <td className="num">{m.input_price == null ? <Badge tone="warn">no price</Badge> : `${price(m.input_price)} / ${price(m.output_price)}`}</td>
                  <td className="num dim">{m.context_window ? fmtCompact(m.context_window) : '—'}</td>
                  <td className="num">{fmtCompact(m.requests_24h)}</td>
                  <td>{m.status === 'active' ? <Badge tone="ok">active</Badge> : m.status === 'deprecated' ? <Badge tone="warn">deprecated</Badge> : <Badge>{m.status}</Badge>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {open && <ModelDrawer model={open === 'new' ? undefined : open} onClose={() => setOpen(null)} />}
    </>
  )
}

function num(v: string) { return v === '' ? undefined : Number(v) }

function ModelDrawer({ model, onClose }: { model?: Model; onClose: () => void }) {
  const { isPlatformAdmin } = useApp()
  const providers = useProviders()
  const [tab, setTab] = useState<'routing' | 'details'>(model ? 'routing' : 'details')
  const [f, setF] = useState({
    name: model?.name ?? '', display_name: model?.display_name ?? '', family: model?.family ?? '', aliases: model?.aliases ?? [],
    input_price: model?.input_price?.toString() ?? '', output_price: model?.output_price?.toString() ?? '', cache_read_price: model?.cache_read_price?.toString() ?? '', cache_write_price: model?.cache_write_price?.toString() ?? '',
    context_window: model?.context_window?.toString() ?? '', max_output: model?.max_output?.toString() ?? '', status: model?.status ?? 'active', strategy: model?.strategy ?? 'priority',
  })
  const [confirm, setConfirm] = useState(false)
  const [dep, setDep] = useState({ provider_id: '', upstream_model: '', priority: '0', weight: '1' })
  const set = (k: keyof typeof f) => (v: any) => setF((x) => ({ ...x, [k]: v }))
  const save = useAction(() => {
    const body = { ...f, input_price: num(f.input_price), output_price: num(f.output_price), cache_read_price: num(f.cache_read_price), cache_write_price: num(f.cache_write_price),
      context_window: num(f.context_window) ?? 0, max_output: num(f.max_output) ?? 0, clear_prices: f.input_price === '' && f.output_price === '' }
    return model ? patch(`/api/models/${model.id}`, body) : post('/api/models', body)
  }, { done: model ? 'Model saved' : 'Model added', invalidate: [['models']], onSuccess: () => !model && onClose() })
  const remove = useAction(() => del(`/api/models/${model!.id}`), { done: 'Model removed', invalidate: [['models']], onSuccess: onClose })
  const addDep = useAction(() => post(`/api/models/${model!.id}/deployments`, { ...dep, priority: Number(dep.priority), weight: Number(dep.weight) }), { done: 'Deployment added', invalidate: [['models']], onSuccess: () => setDep({ provider_id: '', upstream_model: '', priority: '0', weight: '1' }) })
  const updDep = useAction((x: { d: Deployment; body: any }) => patch(`/api/deployments/${x.d.id}`, x.body), { done: 'Deployment updated', invalidate: [['models']] })
  const delDep = useAction((d: Deployment) => del(`/api/deployments/${d.id}`), { done: 'Deployment removed', invalidate: [['models']] })
  const live = useModels().data?.find((m) => m.id === model?.id) ?? model
  const manageable = (providers.data ?? []).filter((p) => p.can_manage)

  return (
    <Drawer open onClose={onClose} wide title={model ? model.name : 'Add a model'} sub={model?.display_name}
      footer={isPlatformAdmin ? <>{model && <Button variant="danger" onClick={() => setConfirm(true)}><Trash2 size={14} /> Remove</Button>}<span className="spacer" /><Button variant="primary" onClick={() => save.mutate(undefined)} busy={save.isPending} disabled={!f.name}>{model ? 'Save model' : 'Add model'}</Button></> : undefined}>
      {model && <Tabs value={tab} onChange={setTab} tabs={[{ value: 'routing', label: `Routing (${live?.deployments.length ?? 0})` }, { value: 'details', label: 'Details & pricing' }]} />}
      {tab === 'routing' && live && (
        <div className="stack">
          <Field label="Routing strategy" hint="Priority tries lower numbers first and fails over on errors; weighted spreads load; latency prefers the fastest healthy deployment.">
            <Select value={f.strategy} disabled={!isPlatformAdmin} onChange={(e) => { set('strategy')(e.target.value); patch(`/api/models/${model!.id}`, { strategy: e.target.value }) }} options={[{ value: 'priority', label: 'Priority with failover' }, { value: 'weighted', label: 'Weighted load balancing' }, { value: 'latency', label: 'Lowest latency' }]} />
          </Field>
          <table className="tbl panel">
            <thead><tr><th>Provider</th><th>Upstream model</th><th className="num">Priority</th><th className="num">Weight</th><th>On</th><th /></tr></thead>
            <tbody>
              {live.deployments.map((d) => (
                <tr key={d.id}>
                  <td>{d.provider_name}<div className="cell-sub">{kindLabel[d.provider_kind] ?? d.provider_kind}</div></td>
                  <td className="mono">{d.upstream_model}</td>
                  <td className="num"><Input type="number" defaultValue={d.priority} style={{ width: 70 }} onBlur={(e) => Number(e.target.value) !== d.priority && updDep.mutate({ d, body: { priority: Number(e.target.value) } })} /></td>
                  <td className="num"><Input type="number" min={1} defaultValue={d.weight} style={{ width: 70 }} onBlur={(e) => Number(e.target.value) !== d.weight && updDep.mutate({ d, body: { weight: Number(e.target.value) } })} /></td>
                  <td><input type="checkbox" checked={d.enabled} onChange={(e) => updDep.mutate({ d, body: { enabled: e.target.checked } })} aria-label="Enabled" /></td>
                  <td className="tight"><Button size="sm" variant="ghost" onClick={() => delDep.mutate(d)} aria-label="Remove deployment"><Trash2 size={14} /></Button></td>
                </tr>
              ))}
              {!live.deployments.length && <tr><td colSpan={6} className="muted">No deployments yet. Requests for this model fail until one is added.</td></tr>}
            </tbody>
          </table>
          {manageable.length > 0 && (
            <div className="panel" style={{ padding: 14 }}>
              <h3 style={{ marginBottom: 10 }}>Add a deployment</h3>
              <div className="grid2">
                <Field label="Provider"><Select value={dep.provider_id} onChange={(e) => setDep((x) => ({ ...x, provider_id: e.target.value }))} options={[{ value: '', label: 'Choose…' }, ...manageable.map((p) => ({ value: p.id, label: p.name }))]} /></Field>
                <Field label="Upstream model ID" hint="Bedrock: model or inference-profile ID. Azure: deployment name. Use {model} to pass the requested name through."><Input className="code" value={dep.upstream_model} onChange={(e) => setDep((x) => ({ ...x, upstream_model: e.target.value }))} /></Field>
                <Field label="Priority"><Input type="number" value={dep.priority} onChange={(e) => setDep((x) => ({ ...x, priority: e.target.value }))} /></Field>
                <Field label="Weight"><Input type="number" min={1} value={dep.weight} onChange={(e) => setDep((x) => ({ ...x, weight: e.target.value }))} /></Field>
              </div>
              <Button variant="primary" disabled={!dep.provider_id || !dep.upstream_model} busy={addDep.isPending} onClick={() => addDep.mutate(undefined)}>Add deployment</Button>
            </div>
          )}
        </div>
      )}
      {tab === 'details' && (
        <fieldset disabled={!isPlatformAdmin} style={{ border: 0, padding: 0, margin: 0 }}>
          <div className="grid2">
            <Field label="Public name" hint="What clients send as model. End with * to route a whole family, e.g. claude-*."><Input className="code" value={f.name} onChange={(e) => set('name')(e.target.value)} /></Field>
            <Field label="Display name"><Input value={f.display_name} onChange={(e) => set('display_name')(e.target.value)} /></Field>
          </div>
          <Field label="Aliases" hint="Other names that resolve here, e.g. a stable “sonnet-latest” you repoint when a new version ships."><ChipsInput value={f.aliases} onChange={set('aliases')} /></Field>
          <div className="grid2">
            <Field label="Family"><Input value={f.family} onChange={(e) => set('family')(e.target.value)} /></Field>
            <Field label="Status"><Select value={f.status} onChange={(e) => set('status')(e.target.value)} options={[{ value: 'active', label: 'Active' }, { value: 'preview', label: 'Preview' }, { value: 'deprecated', label: 'Deprecated (hidden from model lists)' }, { value: 'disabled', label: 'Disabled' }]} /></Field>
          </div>
          <h3 style={{ margin: '6px 0 4px' }}>Pricing, USD per million tokens</h3>
          <p className="muted" style={{ marginBottom: 12, fontSize: 12.5 }}>Check your provider's current price list. If cache prices are blank, cache reads cost 0.1× and cache writes 1.25× the input price.</p>
          <div className="grid2">
            <Field label="Input"><Input type="number" step="0.0001" value={f.input_price} onChange={(e) => set('input_price')(e.target.value)} /></Field>
            <Field label="Output"><Input type="number" step="0.0001" value={f.output_price} onChange={(e) => set('output_price')(e.target.value)} /></Field>
            <Field label="Cache read"><Input type="number" step="0.0001" value={f.cache_read_price} onChange={(e) => set('cache_read_price')(e.target.value)} /></Field>
            <Field label="Cache write"><Input type="number" step="0.0001" value={f.cache_write_price} onChange={(e) => set('cache_write_price')(e.target.value)} /></Field>
            <Field label="Context window (tokens)"><Input type="number" value={f.context_window} onChange={(e) => set('context_window')(e.target.value)} /></Field>
            <Field label="Max output (tokens)"><Input type="number" value={f.max_output} onChange={(e) => set('max_output')(e.target.value)} /></Field>
          </div>
        </fieldset>
      )}
      <Confirm open={confirm} danger title={`Remove ${model?.name}?`} body="Clients requesting this model will get a 404." confirmLabel="Remove model" onClose={() => setConfirm(false)} onConfirm={() => remove.mutate(undefined)} busy={remove.isPending} />
    </Drawer>
  )
}

export function DiscoverDrawer({ provider, onClose }: { provider: Provider; onClose: () => void }) {
  const d = useQuery({ queryKey: ['discover', provider.id], queryFn: () => post<{ id: string; display_name: string; kind: string; suggested_name: string; configured: boolean }[]>(`/api/providers/${provider.id}/discover`), retry: false })
  const [picked, setPicked] = useState<Record<string, string>>({})
  const imp = useAction(() => post<any[]>('/api/models/import', { provider_id: provider.id, items: Object.entries(picked).map(([upstream_model, name]) => ({ upstream_model, name })) }),
    { done: (r: any[]) => `Added ${r.filter((x) => !x.error).length} model(s)`, invalidate: [['models'], ['providers']], onSuccess: onClose })
  return (
    <Drawer open onClose={onClose} wide title={`Models available from ${provider.name}`} sub="Pick the ones to offer. Each becomes a catalog model (or a new route for an existing one)."
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!Object.keys(picked).length} busy={imp.isPending} onClick={() => imp.mutate(undefined)}>Add {Object.keys(picked).length || ''} to catalog</Button></>}>
      {d.isLoading ? <Loading label="Asking the provider" /> : d.error ? <ErrorNote error={d.error} /> : (
        <table className="tbl panel">
          <thead><tr><th className="tight" /><th>Upstream ID</th><th>Type</th><th>Public name</th></tr></thead>
          <tbody>
            {(d.data ?? []).map((m) => (
              <tr key={m.id}>
                <td className="tight">{m.configured ? <Badge tone="ok">added</Badge> : <input type="checkbox" checked={m.id in picked} onChange={(e) => setPicked((p) => { const n = { ...p }; if (e.target.checked) n[m.id] = m.suggested_name; else delete n[m.id]; return n })} aria-label={`Add ${m.id}`} />}</td>
                <td><div className="mono">{m.id}</div><div className="cell-sub">{m.display_name}</div></td>
                <td className="dim">{m.kind.replace('_', ' ')}</td>
                <td>{m.id in picked ? <Input className="code" value={picked[m.id]} onChange={(e) => setPicked((p) => ({ ...p, [m.id]: e.target.value }))} /> : <span className="mono dim">{m.suggested_name}</span>}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Drawer>
  )
}
