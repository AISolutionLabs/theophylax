import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { PiggyBank, Plus, Trash2 } from 'lucide-react'
import { del, get, patch, post, type Budget } from '../lib/api'
import { useApp } from '../lib/app'
import { useScopeOptions } from '../lib/queries'
import { fmtCompact, fmtUSD } from '../lib/format'
import { Badge, Button, Confirm, Drawer, Empty, ErrorNote, Field, Input, Loading, Meter, PageHead, Panel, Segmented, Select, useAction } from '../components/ui'

const pct = (b: Budget) => Math.max(b.limit_usd ? (b.spent_usd / b.limit_usd) * 100 : 0, b.limit_tokens ? (b.spent_tokens / b.limit_tokens) * 100 : 0)

export default function Budgets() {
  const { org } = useApp()
  const [sp, setSp] = useSearchParams()
  const list = useQuery({ queryKey: ['budgets', org.id], queryFn: () => get<Budget[]>(`/api/orgs/${org.id}/budgets`), refetchInterval: 15_000 })
  const [editing, setEditing] = useState<Budget | 'new' | null>(sp.get('new') === '1' ? 'new' : null)
  useEffect(() => { if (sp.get('new')) { sp.delete('new'); setSp(sp, { replace: true }) } }, [sp, setSp])
  return (
    <>
      <PageHead title="Budgets" lede="Cap spend or tokens for the organization, a team, a project or a single key. A blocking budget rejects requests once it is used up, until the period resets."
        actions={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={15} /> Set budget</Button>} />
      <Panel pad={false} title="Budgets" sub="Usage refreshes every few seconds across all gateway replicas.">
        {list.isLoading ? <Loading /> : list.error ? <ErrorNote error={list.error} /> : !list.data?.length ? (
          <Empty icon={<PiggyBank size={20} />} title="No budgets yet" action={<Button variant="primary" onClick={() => setEditing('new')}>Set budget</Button>}>Budgets work in dollars (needs model pricing) or tokens (always available).</Empty>
        ) : (
          <table className="tbl">
            <thead><tr><th>Applies to</th><th>Period</th><th style={{ width: '32%' }}>Used</th><th>Limit</th><th>When used up</th></tr></thead>
            <tbody>
              {list.data.map((b) => {
                const p = pct(b)
                return (
                  <tr key={b.id} className={b.can_edit ? 'click' : ''} onClick={() => b.can_edit && setEditing(b)}>
                    <td><div className="cell-title">{b.scope_name}</div><div className="cell-sub">{b.scope_type}</div></td>
                    <td className="dim">{b.period}</td>
                    <td><Meter pct={p} /><div className="cell-sub" style={{ marginTop: 4 }}>{p.toFixed(0)}% · {b.limit_usd != null && fmtUSD(b.spent_usd)}{b.limit_usd != null && b.limit_tokens != null && ' · '}{b.limit_tokens != null && `${fmtCompact(b.spent_tokens)} tokens`}</div></td>
                    <td>{[b.limit_usd != null ? fmtUSD(b.limit_usd) : '', b.limit_tokens != null ? `${fmtCompact(b.limit_tokens)} tokens` : ''].filter(Boolean).join(' / ')}</td>
                    <td>{b.action === 'block' ? <Badge tone="bad">block requests</Badge> : <Badge tone="warn">alert only</Badge>}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Panel>
      {editing && <BudgetForm existing={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
    </>
  )
}

function BudgetForm({ existing, onClose }: { existing?: Budget; onClose: () => void }) {
  const { org } = useApp()
  const scopes = useScopeOptions()
  const [scope, setScope] = useState(existing ? `${existing.scope_type}:${existing.scope_id}` : `org:${org.id}`)
  const [period, setPeriod] = useState(existing?.period ?? 'monthly')
  const [usd, setUsd] = useState(existing?.limit_usd != null ? String(existing.limit_usd) : '')
  const [tokens, setTokens] = useState(existing?.limit_tokens != null ? String(existing.limit_tokens) : '')
  const [alertPct, setAlertPct] = useState(String(existing?.alert_pct ?? 80))
  const [action, setAction] = useState(existing?.action ?? 'block')
  const [confirm, setConfirm] = useState(false)
  const save = useAction(() => {
    const [st, sid] = scope.split(':')
    const body: any = { period, alert_pct: Number(alertPct), action, scope_type: st, scope_id: sid }
    if (usd) body.limit_usd = Number(usd); else body.clear_usd = true
    if (tokens) body.limit_tokens = Number(tokens); else body.clear_tokens = true
    return existing ? patch(`/api/budgets/${existing.id}`, body) : post(`/api/orgs/${org.id}/budgets`, body)
  }, { done: existing ? 'Budget saved' : 'Budget set', invalidate: [['budgets'], ['overview']], onSuccess: onClose })
  const remove = useAction(() => del(`/api/budgets/${existing!.id}`), { done: 'Budget removed', invalidate: [['budgets']], onSuccess: onClose })
  return (
    <Drawer open onClose={onClose} title={existing ? `Budget for ${existing.scope_name}` : 'Set a budget'}
      footer={<>{existing && <Button variant="danger" onClick={() => setConfirm(true)}><Trash2 size={14} /> Remove</Button>}<span className="spacer" /><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!usd && !tokens} busy={save.isPending} onClick={() => save.mutate(undefined)}>{existing ? 'Save budget' : 'Set budget'}</Button></>}>
      <Field label="Applies to"><Select value={scope} disabled={!!existing} onChange={(e) => setScope(e.target.value)} options={scopes} /></Field>
      <Field label="Period"><Segmented value={period} onChange={setPeriod} options={[{ value: 'daily', label: 'Daily' }, { value: 'monthly', label: 'Monthly' }, { value: 'total', label: 'All time' }]} /></Field>
      <div className="grid2">
        <Field label="Spend limit (USD)" hint="Requires prices on the models in use."><Input type="number" min={0} step="0.01" value={usd} onChange={(e) => setUsd(e.target.value)} placeholder="No dollar limit" /></Field>
        <Field label="Token limit"><Input type="number" min={0} value={tokens} onChange={(e) => setTokens(e.target.value)} placeholder="No token limit" /></Field>
      </div>
      <div className="grid2">
        <Field label="Warn at (%)" hint="Shown on the overview page."><Input type="number" min={1} max={100} value={alertPct} onChange={(e) => setAlertPct(e.target.value)} /></Field>
        <Field label="When used up"><Select value={action} onChange={(e) => setAction(e.target.value)} options={[{ value: 'block', label: 'Block further requests' }, { value: 'alert', label: 'Alert only' }]} /></Field>
      </div>
      <Confirm open={confirm} danger title="Remove this budget?" body="Spend for this scope will no longer be limited." confirmLabel="Remove budget" onClose={() => setConfirm(false)} onConfirm={() => remove.mutate(undefined)} busy={remove.isPending} />
    </Drawer>
  )
}
