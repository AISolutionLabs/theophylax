import { useEffect, useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { Plus } from 'lucide-react'
import { get, post } from '../lib/api'
import { fmtAgo, fmtTime } from '../lib/format'
import { Button, Drawer, ErrorNote, Field, Input, Loading, PageHead, Panel, Seal, useAction } from '../components/ui'

export default function System() {
  const [sp, setSp] = useSearchParams()
  const [newOrg, setNewOrg] = useState(sp.get('new-org') === '1')
  useEffect(() => { if (sp.get('new-org')) { sp.delete('new-org'); setSp(sp, { replace: true }) } }, [sp, setSp])
  const sys = useQuery({ queryKey: ['system'], queryFn: () => get<any>('/api/system'), refetchInterval: 10_000 })
  const ckp = useQuery({ queryKey: ['checkpoints'], queryFn: () => get<any[]>('/api/receipts/checkpoints') })
  const orgs = useQuery({ queryKey: ['orgs'], queryFn: () => get<any[]>('/api/orgs') })
  return (
    <>
      <PageHead title="System" lede="Health of this gateway replica, organizations, and the receipt anchoring history." actions={<Button variant="primary" onClick={() => setNewOrg(true)}><Plus size={15} /> New organization</Button>} />
      <div className="stack">
        {sys.isLoading ? <Loading /> : sys.error ? <ErrorNote error={sys.error} /> : (
          <div className="kpis">
            <div className="kpi"><div className="label">Version</div><div className="value" style={{ fontSize: 20 }}>{sys.data.version}</div><div className="delta">{sys.data.go}</div></div>
            <div className="kpi"><div className="label">Config loaded</div><div className="value" style={{ fontSize: 20 }}>{fmtAgo(sys.data.config_loaded_at)}</div><div className="delta">{sys.data.providers} providers · {sys.data.routes} routes</div></div>
            <div className="kpi"><div className="label">Memory · goroutines</div><div className="value" style={{ fontSize: 20 }}>{sys.data.heap_mb} MB</div><div className="delta">{sys.data.goroutines} goroutines</div></div>
            <div className="kpi"><div className="label">Database pool</div><div className="value" style={{ fontSize: 20 }}>{sys.data.db.total_conns}/{sys.data.db.max_conns}</div><div className="delta">{sys.data.db.idle_conns} idle</div></div>
          </div>
        )}
        {sys.data && (
          <Panel title="Integrations" sub="Configured with environment variables on the gateway; see docs/operations.md.">
            <dl className="kv">
              <dt>Rate limits</dt><dd>{sys.data.rate_limits === 'fleet-wide' ? 'Enforced fleet-wide (synced through Postgres)' : 'Per replica'}</dd>
              <dt>OpenTelemetry traces</dt><dd className="mono">{sys.data.export?.otlp?.endpoint ?? <span className="dim">not configured (THEOPHYLAX_OTLP_ENDPOINT)</span>}</dd>
              <dt>Log and audit sink</dt><dd className="mono">{sys.data.export?.log_sink ? `${sys.data.export.log_sink.url} (${sys.data.export.log_sink.format}${sys.data.export.log_sink.audit ? ', with audit log' : ''})` : <span className="dim">not configured (THEOPHYLAX_LOG_SINK_URL)</span>}</dd>
            </dl>
          </Panel>
        )}
        <Panel title="Organizations" pad={false}>
          <table className="tbl"><tbody>{(orgs.data ?? []).map((o) => <tr key={o.id}><td className="cell-title">{o.name}</td><td className="mono dim">{o.slug}</td><td className="dim">{fmtTime(o.created_at)}</td></tr>)}</tbody></table>
        </Panel>
        <Panel title="Receipt checkpoints" sub="Each checkpoint is a Merkle root over a batch of receipts, written into the audit chain." pad={false}>
          <table className="tbl">
            <thead><tr><th className="tight" /><th>Anchored</th><th className="num">Receipts</th><th>Covers</th><th>Merkle root</th><th>Audit entry</th></tr></thead>
            <tbody>
              {(ckp.data ?? []).map((c) => (
                <tr key={c.id}>
                  <td className="tight"><Seal size={16} /></td>
                  <td>{fmtTime(c.ts)}</td>
                  <td className="num">{c.count}</td>
                  <td className="dim">{fmtTime(c.first_ts)} – {fmtTime(c.last_ts)}</td>
                  <td className="mono dim">{b64hex(c.merkle_root).slice(0, 20)}…</td>
                  <td className="mono dim">{c.audit_id?.slice(0, 18) ?? 'pending'}</td>
                </tr>
              ))}
              {!ckp.data?.length && <tr><td colSpan={6} className="muted">No checkpoints yet. They are written every couple of minutes once traffic flows.</td></tr>}
            </tbody>
          </table>
        </Panel>
      </div>
      {newOrg && <NewOrg onClose={() => setNewOrg(false)} />}
    </>
  )
}

function b64hex(b64: string) {
  try { return Array.from(atob(b64), (c) => c.charCodeAt(0).toString(16).padStart(2, '0')).join('') } catch { return b64 }
}

function NewOrg({ onClose }: { onClose: () => void }) {
  const qc = useQueryClient()
  const [name, setName] = useState('')
  const create = useAction(() => post('/api/orgs', { name }), { done: 'Organization created', invalidate: [['orgs']], onSuccess: () => { qc.invalidateQueries({ queryKey: ['me'] }); onClose() } })
  return (
    <Drawer open onClose={onClose} title="New organization" sub="You become its first owner." footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!name} busy={create.isPending} onClick={() => create.mutate(undefined)}>Create organization</Button></>}>
      <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} autoFocus /></Field>
    </Drawer>
  )
}
