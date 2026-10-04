import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { PiggyBank, Plus, Trash2 } from 'lucide-react'
import { del, get, patch, post, type Budget, type PersonalLimit, type User } from '../lib/api'
import { useApp } from '../lib/app'
import { useProjects, useTeams } from '../lib/queries'
import { fmtCompact, fmtUSD } from '../lib/format'
import { Badge, Button, Confirm, Drawer, Empty, ErrorNote, Field, Input, Loading, Meter, PageHead, Panel, Segmented, Select, useAction } from '../components/ui'

const pct = (b: { limit_usd?: number | null; limit_tokens?: number | null }, usd: number, tok: number) =>
  Math.max(b.limit_usd ? (usd / b.limit_usd) * 100 : 0, b.limit_tokens ? (tok / b.limit_tokens) * 100 : 0)
const limitText = (b: Budget) => [b.limit_usd != null ? fmtUSD(b.limit_usd) : '', b.limit_tokens != null ? `${fmtCompact(b.limit_tokens)} tokens` : ''].filter(Boolean).join(' / ')
const kindOf = (b: Budget) => (b.scope_type === 'user' ? 'Person' : b.per_user ? `Each person in ${b.scope_type === 'org' ? 'the organization' : 'this team'}` : `${b.scope_type} total`)
const sourceLabel = { user: 'Your own budget', team: 'Team per-person default', org: 'Organization per-person default' }

function PersonalLimits() {
  const { org } = useApp()
  const q = useQuery({ queryKey: ['budgets-personal', org.id], queryFn: () => get<PersonalLimit[]>(`/api/orgs/${org.id}/budgets/personal`), refetchInterval: 15_000 })
  if (!q.data?.length) return null
  return (
    <Panel pad={false} title="Your personal limits" sub="The per-person budget that applies to you in each team. Pooled team and organization budgets apply on top.">
      <table className="tbl">
        <thead><tr><th>Team</th><th>Period</th><th>Set by</th><th style={{ width: '32%' }}>Used</th><th>Limit</th></tr></thead>
        <tbody>
          {q.data.map((l) => {
            const p = pct(l.budget, l.spent_usd, l.spent_tokens)
            return (
              <tr key={`${l.team_id}|${l.period}`}>
                <td className="cell-title">{l.team_name || '—'}</td>
                <td className="dim">{l.period}</td>
                <td>{sourceLabel[l.source]}</td>
                <td><Meter pct={p} /><div className="cell-sub" style={{ marginTop: 4 }}>{p.toFixed(0)}% · {l.budget.limit_usd != null && fmtUSD(l.spent_usd)}{l.budget.limit_usd != null && l.budget.limit_tokens != null && ' · '}{l.budget.limit_tokens != null && `${fmtCompact(l.spent_tokens)} tokens`}</div></td>
                <td>{limitText(l.budget)}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </Panel>
  )
}

export default function Budgets() {
  const { org } = useApp()
  const [sp, setSp] = useSearchParams()
  const list = useQuery({ queryKey: ['budgets', org.id], queryFn: () => get<Budget[]>(`/api/orgs/${org.id}/budgets`), refetchInterval: 15_000 })
  const [editing, setEditing] = useState<Budget | 'new' | null>(sp.get('new') === '1' ? 'new' : null)
  useEffect(() => { if (sp.get('new')) { sp.delete('new'); setSp(sp, { replace: true }) } }, [sp, setSp])
  return (
    <>
      <PageHead title="Budgets" lede={<>Cap spend or tokens for the organization, a team, a project, a key or a person. A blocking budget rejects requests once it is used up, until the period resets. Each person's own limit comes from <b>their personal budget</b>, else <b>their team's per-person budget</b>, else <b>the organization's per-person budget</b>.</>}
        actions={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={15} /> Set budget</Button>} />
      <div className="stack">
      <PersonalLimits />
      <Panel pad={false} title="Budgets" sub="Usage refreshes every few seconds across all gateway replicas.">
        {list.isLoading ? <Loading /> : list.error ? <ErrorNote error={list.error} /> : !list.data?.length ? (
          <Empty icon={<PiggyBank size={20} />} title="No budgets yet" action={<Button variant="primary" onClick={() => setEditing('new')}>Set budget</Button>}>Budgets work in dollars (from upstream list prices) or tokens.</Empty>
        ) : (
          <table className="tbl">
            <thead><tr><th>Applies to</th><th>Period</th><th style={{ width: '32%' }}>Used</th><th>Limit</th><th>When used up</th></tr></thead>
            <tbody>
              {list.data.map((b) => {
                const p = pct(b, b.spent_usd, b.spent_tokens)
                return (
                  <tr key={b.id} className={b.can_edit ? 'click' : ''} onClick={() => b.can_edit && setEditing(b)}>
                    <td><div className="cell-title">{b.scope_name}</div><div className="cell-sub">{kindOf(b)}</div></td>
                    <td className="dim">{b.period}</td>
                    <td>{b.per_user ? <span className="muted">Tracked for each person</span> : <><Meter pct={p} /><div className="cell-sub" style={{ marginTop: 4 }}>{p.toFixed(0)}% · {b.limit_usd != null && fmtUSD(b.spent_usd)}{b.limit_usd != null && b.limit_tokens != null && ' · '}{b.limit_tokens != null && `${fmtCompact(b.spent_tokens)} tokens`}</div></>}</td>
                    <td>{limitText(b)}{b.per_user && <span className="dim"> each</span>}</td>
                    <td>{b.action === 'block' ? <Badge tone="bad">block requests</Badge> : <Badge tone="warn">alert only</Badge>}</td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </Panel>
      </div>
      {editing && <BudgetForm existing={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
    </>
  )
}

function BudgetForm({ existing, onClose }: { existing?: Budget; onClose: () => void }) {
  const { org, can } = useApp()
  const teams = useTeams()
  const projects = useProjects()
  const users = useQuery({ queryKey: ['org-users', org.id], queryFn: () => get<User[]>(`/api/orgs/${org.id}/users`), enabled: can({ type: 'org', id: org.id }, 'admin') })
  // scope values: "<type>:<id>" for pooled budgets, "<type>-each:<id>" for per-person defaults
  const scopes = [
    { value: `org-each:${org.id}`, label: 'Each person in the organization (default)' },
    ...(teams.data ?? []).map((t) => ({ value: `team-each:${t.id}`, label: `Each person in team ${t.name}` })),
    ...(users.data ?? []).map((u) => ({ value: `user:${u.id}`, label: `Person · ${u.email}` })),
    { value: `org:${org.id}`, label: `Organization total (${org.name})` },
    ...(teams.data ?? []).map((t) => ({ value: `team:${t.id}`, label: `Team total · ${t.name}` })),
    ...(projects.data ?? []).map((p) => ({ value: `project:${p.id}`, label: `Project total · ${p.team_name} / ${p.name}` })),
  ]
  const initial = existing ? `${existing.scope_type}${existing.per_user ? '-each' : ''}:${existing.scope_id}` : `org-each:${org.id}`
  if (existing && !scopes.some((s) => s.value === initial)) scopes.push({ value: initial, label: existing.scope_name })
  const [scope, setScope] = useState(initial)
  const [period, setPeriod] = useState(existing?.period ?? 'monthly')
  const [usd, setUsd] = useState(existing?.limit_usd != null ? String(existing.limit_usd) : '')
  const [tokens, setTokens] = useState(existing?.limit_tokens != null ? String(existing.limit_tokens) : '')
  const [alertPct, setAlertPct] = useState(String(existing?.alert_pct ?? 80))
  const [action, setAction] = useState(existing?.action ?? 'block')
  const [confirm, setConfirm] = useState(false)
  const save = useAction(() => {
    const [kind, sid] = scope.split(':')
    const body: any = { period, alert_pct: Number(alertPct), action, scope_type: kind!.replace('-each', ''), scope_id: sid, per_user: kind!.endsWith('-each') }
    if (usd) body.limit_usd = Number(usd); else body.clear_usd = true
    if (tokens) body.limit_tokens = Number(tokens); else body.clear_tokens = true
    return existing ? patch(`/api/budgets/${existing.id}`, body) : post(`/api/orgs/${org.id}/budgets`, body)
  }, { done: existing ? 'Budget saved' : 'Budget set', invalidate: [['budgets'], ['overview']], onSuccess: onClose })
  const remove = useAction(() => del(`/api/budgets/${existing!.id}`), { done: 'Budget removed', invalidate: [['budgets']], onSuccess: onClose })
  return (
    <Drawer open onClose={onClose} title={existing ? `Budget for ${existing.scope_name}` : 'Set a budget'}
      footer={<>{existing && <Button variant="danger" onClick={() => setConfirm(true)}><Trash2 size={14} /> Remove</Button>}<span className="spacer" /><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!usd && !tokens} busy={save.isPending} onClick={() => save.mutate(undefined)}>{existing ? 'Save budget' : 'Set budget'}</Button></>}>
      <Field label="Applies to" hint={scope.includes('-each') ? 'Each person gets this limit for their own spend, unless they have a personal budget (or, for the organization default, a team default) for the same period.' : scope.startsWith('user:') ? "Overrides the team and organization per-person defaults for this person." : 'A shared cap on everyone\'s combined spend.'}>
        <Select value={scope} disabled={!!existing} onChange={(e) => setScope(e.target.value)} options={scopes} /></Field>
      <Field label="Period"><Segmented value={period} onChange={setPeriod} options={[{ value: 'daily', label: 'Daily' }, { value: 'monthly', label: 'Monthly' }, { value: 'total', label: 'All time' }]} /></Field>
      <div className="grid2">
        <Field label="Spend limit (USD)" hint="Spend is the upstream provider's list price for the tokens consumed."><Input type="number" min={0} step="0.01" value={usd} onChange={(e) => setUsd(e.target.value)} placeholder="No dollar limit" /></Field>
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
