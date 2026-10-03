import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { CheckCircle2, Download, ScrollText, XCircle } from 'lucide-react'
import { get, qs, type AuditEntry } from '../lib/api'
import { useApp } from '../lib/app'
import { download, fmtTime } from '../lib/format'
import { Badge, Button, Drawer, Empty, ErrorNote, Input, Loading, PageHead, useAction } from '../components/ui'

export default function Audit() {
  const { org, isPlatformAdmin } = useApp()
  const [action, setAction] = useState('')
  const [actor, setActor] = useState('')
  const [scope, setScope] = useState<'org' | 'platform'>('org')
  const [open, setOpen] = useState<AuditEntry | null>(null)
  const list = useQuery({
    queryKey: ['audit', org.id, scope, action, actor],
    queryFn: () => get<AuditEntry[]>(`${scope === 'org' ? `/api/orgs/${org.id}/audit` : '/api/audit'}${qs({ action, actor })}`),
  })
  const verify = useAction(() => get<{ ok: boolean; checked: number; broken_at?: number; reason?: string }>('/api/audit/verify'))
  return (
    <>
      <PageHead title="Audit trail" lede="Every administrative change, sign-in and receipt checkpoint, in a hash-chained log. Altering or deleting any entry breaks the chain, and verification will say where."
        actions={<>
          {isPlatformAdmin && <Button onClick={() => verify.mutate(undefined)} busy={verify.isPending}>Verify chain integrity</Button>}
          <Button onClick={() => download('audit.json', JSON.stringify(list.data ?? [], null, 2), 'application/json')}><Download size={14} /> Export</Button>
        </>} />
      {verify.data && (
        <div className={`callout ${verify.data.ok ? 'accent' : 'bad'}`} style={{ marginBottom: 14 }}>
          {verify.data.ok ? <CheckCircle2 size={16} /> : <XCircle size={16} />}
          {verify.data.ok ? `Chain intact: all ${verify.data.checked.toLocaleString()} entries verified.` : `Chain broken at entry #${verify.data.broken_at}: ${verify.data.reason}.`}
        </div>
      )}
      <div className="row wrap" style={{ marginBottom: 14 }}>
        <Input placeholder="Action, e.g. key. or guardrail.update" value={action} onChange={(e) => setAction(e.target.value)} style={{ width: 280 }} />
        <Input placeholder="Actor email" value={actor} onChange={(e) => setActor(e.target.value)} style={{ width: 240 }} />
        {isPlatformAdmin && <label className="row muted" style={{ gap: 6 }}><input type="checkbox" checked={scope === 'platform'} onChange={(e) => setScope(e.target.checked ? 'platform' : 'org')} /> All organizations</label>}
      </div>
      <div className="panel">
        {list.isLoading ? <Loading /> : list.error ? <ErrorNote error={list.error} /> : !list.data?.length ? <Empty icon={<ScrollText size={20} />} title="Nothing recorded yet" /> : (
          <table className="tbl">
            <thead><tr><th className="num">#</th><th>When</th><th>Who</th><th>Action</th><th>Target</th><th>From</th></tr></thead>
            <tbody>
              {list.data.map((e) => (
                <tr key={e.id} className="click" onClick={() => setOpen(e)}>
                  <td className="num dim">{e.seq}</td>
                  <td className="nowrap">{fmtTime(e.ts)}</td>
                  <td>{e.actor_email || e.actor_id || e.actor_type} {e.actor_type !== 'user' && <Badge>{e.actor_type}</Badge>}</td>
                  <td className="mono">{e.action}</td>
                  <td className="dim">{e.target_type} <span className="mono">{e.target_id.slice(0, 22)}</span></td>
                  <td className="dim">{e.ip}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {open && (
        <Drawer open onClose={() => setOpen(null)} title={open.action} sub={`Entry #${open.seq} · ${new Date(open.ts).toLocaleString()}`}>
          <dl className="kv" style={{ marginBottom: 16 }}>
            <dt>Actor</dt><dd>{open.actor_email || open.actor_id} ({open.actor_type})</dd>
            <dt>Target</dt><dd>{open.target_type} <span className="mono">{open.target_id}</span></dd>
            <dt>IP / agent</dt><dd>{open.ip} · <span className="muted">{open.user_agent}</span></dd>
          </dl>
          <div className="code-block" style={{ whiteSpace: 'pre-wrap' }}>{JSON.stringify(open.details, null, 2)}</div>
        </Drawer>
      )}
    </>
  )
}
