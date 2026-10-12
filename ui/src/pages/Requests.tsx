import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { Activity, CheckCircle2, Download, RefreshCw, XCircle } from 'lucide-react'
import { get, qs, type ReceiptLookup, type RequestLog } from '../lib/api'
import { useApp } from '../lib/app'
import { useProjects, useRoutableModels } from '../lib/queries'
import { download, fmtInt, fmtMs, fmtTime, fmtUSD, toCSV } from '../lib/format'
import { Badge, Button, CodeBlock, CopyButton, Drawer, Empty, ErrorNote, Input, Loading, PageHead, Seal, Select, StatusBadge, Tabs } from '../components/ui'

export default function Requests() {
  const { org } = useApp()
  const [sp, setSp] = useSearchParams()
  const projects = useProjects()
  const models = useRoutableModels()
  const [f, setF] = useState({ status: '', model: '', project_id: '', request_id: sp.get('id') ?? '', tag: '', since: '24h' })
  const [open, setOpen] = useState<string | null>(sp.get('id'))
  const logs = useQuery({
    queryKey: ['logs', org.id, f],
    queryFn: () => get<RequestLog[]>(`/api/orgs/${org.id}/logs${qs({ ...f, limit: 200 })}`),
    refetchInterval: 10_000,
  })
  useEffect(() => { if (sp.get('id')) { sp.delete('id'); setSp(sp, { replace: true }) } }, [sp, setSp])
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setF((x) => ({ ...x, [k]: e.target.value }))

  return (
    <>
      <PageHead title="Requests" lede="Every call through the gateway, with who made it, what it cost, what guardrails did, and a signed receipt you can verify independently."
        actions={<>
          <Button onClick={() => logs.refetch()}><RefreshCw size={14} /> Refresh</Button>
          <Button onClick={() => download(`requests-${new Date().toISOString().slice(0, 10)}.csv`, toCSV((logs.data ?? []).map(({ guardrails, ...r }) => ({ ...r, guardrails: guardrails.map((g) => `${g.rule}:${g.action}`).join(' ') }))))}><Download size={14} /> Export CSV</Button>
        </>} />
      <div className="row wrap" style={{ marginBottom: 14 }}>
        <Input placeholder="Request ID" value={f.request_id} onChange={set('request_id')} style={{ width: 260 }} />
        <Input placeholder="Tags, e.g. feature:search" value={f.tag} onChange={set('tag')} style={{ width: 220 }} aria-label="Filter by tags" />
        <Select value={f.status} onChange={set('status')} style={{ width: 170 }} options={[{ value: '', label: 'Any outcome' }, { value: 'ok', label: 'Succeeded' }, { value: 'error', label: 'Failed' }, { value: 'guardrail', label: 'Guardrail acted' }]} />
        <Select value={f.model} onChange={set('model')} style={{ width: 200 }} options={[{ value: '', label: 'Any model' }, ...models.data.map((m) => ({ value: m.name, label: m.name }))]} />
        <Select value={f.project_id} onChange={set('project_id')} style={{ width: 220 }} options={[{ value: '', label: 'Any project' }, ...(projects.data ?? []).map((p) => ({ value: p.id, label: `${p.team_name} / ${p.name}` }))]} />
        <Select value={f.since} onChange={set('since')} style={{ width: 150 }} options={[{ value: '1h', label: 'Last hour' }, { value: '24h', label: 'Last 24 hours' }, { value: '168h', label: 'Last 7 days' }, { value: '720h', label: 'Last 30 days' }]} />
      </div>
      <div className="panel">
        {logs.isLoading ? <Loading /> : logs.error ? <ErrorNote error={logs.error} /> : !logs.data?.length ? (
          <Empty icon={<Activity size={20} />} title="No requests match">Requests appear here within a second of completing. Try widening the time range or clearing filters.</Empty>
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead><tr><th className="tight" aria-label="Receipt" /><th>When</th><th>Model</th><th>Caller</th><th>Status</th><th className="num">Tokens in / out</th><th className="num">Cost</th><th className="num">Latency</th><th>Guardrails</th></tr></thead>
              <tbody>
                {logs.data.map((l) => (
                  <tr key={l.id} className="click" onClick={() => setOpen(l.id)}>
                    <td className="tight"><Seal size={16} state="signed" /></td>
                    <td className="nowrap">{fmtTime(l.ts)}</td>
                    <td><div className="cell-title mono">{l.model}</div><div className="cell-sub">{l.provider_kind}{l.attempts > 1 ? ` · ${l.attempts} attempts` : ''}{l.stream ? ' · stream' : ''}</div></td>
                    <td><div>{l.key_name ?? l.user_email ?? '—'}</div><div className="cell-sub">{l.project_name}</div></td>
                    <td><StatusBadge status={l.status} /></td>
                    <td className="num">{fmtInt(l.input_tokens + l.cache_read_tokens + l.cache_write_tokens)} / {fmtInt(l.output_tokens)}</td>
                    <td className="num">{fmtUSD(l.cost_usd, true)}</td>
                    <td className="num">{fmtMs(l.latency_ms)}</td>
                    <td>{l.guardrails.length ? <GuardBadges list={l.guardrails} /> : <span className="dim">—</span>}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {open && <RequestDrawer id={open} onClose={() => setOpen(null)} />}
    </>
  )
}

function GuardBadges({ list }: { list: RequestLog['guardrails'] }) {
  const blocked = list.some((g) => g.action === 'block')
  const redacted = list.filter((g) => g.action === 'redact').reduce((n, g) => n + (g.count ?? 1), 0)
  return (
    <span className="row" style={{ gap: 4 }}>
      {blocked && <span className="badge bad">blocked</span>}
      {redacted > 0 && <span className="badge accent">{redacted} redacted</span>}
      {!blocked && !redacted && <span className="badge warn">{list.length} flagged</span>}
    </span>
  )
}

function RequestDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const [tab, setTab] = useState<'details' | 'receipt' | 'payload'>('details')
  const d = useQuery({ queryKey: ['log', id], queryFn: () => get<{ log: RequestLog; request?: string; response?: string }>(`/api/logs/${id}`) })
  const r = useQuery({ queryKey: ['receipt', id], queryFn: () => get<ReceiptLookup>(`/api/receipts/${id}`), retry: false })
  const l = d.data?.log
  return (
    <Drawer open onClose={onClose} wide title={l ? `${l.model} · ${l.status}` : 'Request'} sub={<span className="mono">{id}</span>}>
      {d.isLoading ? <Loading /> : d.error ? <ErrorNote error={d.error} /> : l && (
        <>
          <Tabs value={tab} onChange={setTab} tabs={[{ value: 'details', label: 'Details' }, { value: 'receipt', label: <span className="row" style={{ gap: 6 }}><Seal size={14} state={r.data?.anchored ? 'anchored' : 'signed'} /> Receipt</span> }, ...(d.data?.request || d.data?.response ? [{ value: 'payload' as const, label: 'Payload' }] : [])]} />
          {tab === 'details' && (
            <div className="stack">
              {l.error && <div className="callout bad">{l.error}</div>}
              <dl className="kv">
                <dt>Time</dt><dd>{new Date(l.ts).toLocaleString()}</dd>
                <dt>Endpoint</dt><dd className="mono">{l.endpoint}</dd>
                <dt>Model requested</dt><dd className="mono">{l.model}</dd>
                <dt>Served by</dt><dd><span className="mono">{l.upstream_model || '—'}</span> via {l.provider_kind || '—'}{l.attempts > 1 && <Badge tone="warn">failed over after {l.attempts - 1} attempt(s)</Badge>}</dd>
                <dt>Caller</dt><dd>{l.key_name ? `Key “${l.key_name}”` : l.user_email ?? '—'} · {l.project_name}</dd>
                <dt>Tokens</dt><dd>{fmtInt(l.input_tokens)} input · {fmtInt(l.output_tokens)} output · {fmtInt(l.cache_read_tokens)} cache read · {fmtInt(l.cache_write_tokens)} cache write</dd>
                <dt>Cost</dt><dd>{fmtUSD(l.cost_usd, true)} <span className="muted">{l.price_key === 'mock' ? '· mock provider, not billed' : l.price_key ? <>· list price <span className="mono">{l.price_key}</span></> : l.input_tokens + l.output_tokens > 0 ? '· no upstream list price found' : ''}</span></dd>
                <dt>Latency</dt><dd>{fmtMs(l.latency_ms)} total · {fmtMs(l.ttfb_ms)} to first byte {l.stream && '(streamed)'}</dd>
                <dt>Client</dt><dd>{l.client_ip} · <span className="muted">{l.user_agent}</span></dd>
                {l.tags && Object.keys(l.tags).length > 0 && <><dt>Tags</dt><dd className="row wrap" style={{ gap: 4 }}>{Object.entries(l.tags).sort().map(([k, v]) => <Badge key={k}><span className="mono">{k}={v}</span></Badge>)}</dd></>}
              </dl>
              <div>
                <h3 style={{ marginBottom: 8 }}>Guardrails</h3>
                {!l.guardrails.length ? <p className="muted">No guardrail acted on this request.</p> : (
                  <table className="tbl panel"><thead><tr><th>Policy</th><th>Rule</th><th>Phase</th><th>Action</th><th>Detail</th></tr></thead>
                    <tbody>{l.guardrails.map((g, i) => <tr key={i}><td>{g.policy}</td><td>{g.rule}</td><td>{g.phase === 'input' ? 'pre-call' : 'post-call'}</td><td><Badge tone={g.action === 'block' ? 'bad' : g.action === 'redact' ? 'accent' : 'warn'}>{g.action}</Badge></td><td className="dim">{g.detail}{g.count ? ` ×${g.count}` : ''}</td></tr>)}</tbody>
                  </table>
                )}
              </div>
            </div>
          )}
          {tab === 'receipt' && <ReceiptView q={r} />}
          {tab === 'payload' && (
            <div className="stack">
              <p className="muted">Stored because payload logging is on for this scope. Content is shown after guardrails ran, so redacted values stay redacted. Viewing is recorded in the audit trail.</p>
              <h3>Request</h3><div className="code-block" style={{ whiteSpace: 'pre-wrap', maxHeight: 360 }}>{pretty(d.data?.request)}</div>
              <h3>Response</h3><div className="code-block" style={{ whiteSpace: 'pre-wrap', maxHeight: 360 }}>{pretty(d.data?.response)}</div>
            </div>
          )}
        </>
      )}
    </Drawer>
  )
}

function pretty(s?: string) {
  if (!s) return '(none)'
  try { return JSON.stringify(JSON.parse(s), null, 2) } catch { return s }
}

function ReceiptView({ q }: { q: ReturnType<typeof useQuery<ReceiptLookup>> }) {
  if (q.isLoading) return <Loading />
  if (q.error || !q.data) return <div className="callout">The receipt is still being written. Receipts are persisted within about a second of the response; refresh in a moment.</div>
  const { receipt: rc, signature_valid, anchored, checkpoint } = q.data
  const base = window.location.origin
  return (
    <div className="stack">
      <section className="receipt">
        <div className="receipt-head">
          <Seal size={40} state={!signature_valid ? 'broken' : anchored ? 'anchored' : 'signed'} />
          <div>
            <h3>Proof-of-Inference receipt</h3>
            <div className="sub">Issued by {rc.iss} · key {q.data.kid} · {new Date(rc.iat * 1000).toLocaleString()}</div>
          </div>
        </div>
        <div className="receipt-body">
          <dl className="kv">
            <dt>Caller</dt><dd>{rc.sub.type} <span className="mono">{rc.sub.id}</span></dd>
            <dt>Model</dt><dd>requested <span className="mono">{rc.model_requested}</span>, served <span className="mono">{rc.route.upstream_model || '—'}</span> via {rc.route.provider_kind || '—'} ({rc.route.attempts} attempt{rc.route.attempts === 1 ? '' : 's'})</dd>
            {rc.route.router && <><dt>Router</dt><dd><span className="mono">{rc.route.router.model}</span> scored {rc.route.router.score.toFixed(2)} ({rc.route.router.classifier}) → tier {rc.route.router.tier}, <span className="mono">{rc.route.model}</span></dd></>}
            <dt>Request digest</dt><dd className="digest">sha256:{rc.req_sha256}</dd>
            <dt>Response digest</dt><dd className="digest">sha256:{rc.resp_sha256}</dd>
            <dt>Policies in force</dt><dd>{rc.policies.length ? rc.policies.map((p) => <div key={p.id}>{p.name} <span className="muted">({p.scope}, {p.mode})</span> <span className="digest">{p.digest.slice(0, 19)}…</span></div>) : 'None'}</dd>
            <dt>Actions taken</dt><dd>{rc.actions.length ? rc.actions.map((a, i) => <div key={i}>{a.action} by {a.policy} ({a.rule}, {a.phase === 'input' ? 'pre-call' : 'post-call'}){a.count ? ` ×${a.count}` : ''}</div>) : 'None'}</dd>
            <dt>Usage</dt><dd>{rc.usage.input} in · {rc.usage.output} out · {fmtUSD(rc.cost_usd, true)} · status {rc.status}</dd>
          </dl>
          <div className="chain">
            <div>{signature_valid ? <span className="ok"><CheckCircle2 size={13} /> Signature valid</span> : <span style={{ color: 'var(--bad)' }}><XCircle size={13} /> Signature invalid</span>}<div className="muted">Ed25519, JWS</div></div>
            <div>{anchored ? <span className="ok"><CheckCircle2 size={13} /> Anchored</span> : <span className="muted">Anchoring pending</span>}<div className="muted">{checkpoint ? `Merkle root ${checkpoint.merkle_root.slice(0, 12)}… over ${checkpoint.count} receipts` : 'Batched every few minutes'}</div></div>
            <div>{checkpoint?.audit_id ? <span className="ok"><CheckCircle2 size={13} /> In audit chain</span> : <span className="muted">Awaiting audit entry</span>}<div className="muted">{checkpoint?.audit_id ? <span className="mono">{checkpoint.audit_id.slice(0, 16)}…</span> : 'Hash-chained log'}</div></div>
          </div>
        </div>
      </section>
      <p className="muted">The receipt contains digests, not content, so it can be shared with auditors or customers. Anyone can check it against the published public key:</p>
      <CodeBlock code={`theophylax verify-receipt \\\n  --keys ${base}/.well-known/theophylax-receipt-keys.json \\\n  --request request.json --response response.json @receipt.jws`} />
      <div className="row"><CopyButton text={q.data.jws} label="Copy receipt (JWS)" /><a className="btn sm" href={`${base}/.well-known/theophylax-receipt-keys.json`} target="_blank" rel="noreferrer">Public keys</a></div>
    </div>
  )
}
