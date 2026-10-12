import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { cacheTTLs, del, get, patch, post, type ApiKey, type ScopeSettings } from '../lib/api'
import { useApp } from '../lib/app'
import { useRoutableModels } from '../lib/queries'
import { fmtAgo } from '../lib/format'
import { AppearancePicker } from '../components/Appearance'
import { Badge, Button, ChipsInput, CodeBlock, Confirm, CopyButton, Field, Input, PageHead, Panel, RoleBadge, Select, Toggle, useAction } from '../components/ui'

export default function Settings() {
  const { me, org, can } = useApp()
  const orgAdmin = can({ type: 'org', id: org.id }, 'admin')
  return (
    <>
      <PageHead title="Settings" lede="Your profile and access tokens, plus organization-wide defaults if you administer it." />
      <div className="stack">
        <Panel title="Your profile">
          <dl className="kv">
            <dt>Email</dt><dd>{me.user.email}</dd>
            <dt>Name</dt><dd>{me.user.name || '—'}</dd>
            <dt>Access</dt><dd className="row wrap" style={{ gap: 6 }}>{me.is_platform_admin && <RoleBadge role="owner" />}{me.is_platform_admin && 'Platform administrator · '}{me.memberships.map((m) => <span key={m.id}>{m.scope_name} <RoleBadge role={m.role} /></span>)}</dd>
          </dl>
        </Panel>
        <Panel title="Appearance" sub="Saved in this browser. System follows your device's light or dark setting."><div className="appearance" style={{ width: 'auto', padding: 0, maxWidth: 520 }}><AppearancePicker /></div></Panel>
        {me.has_password && <PasswordPanel />}
        <TokensPanel />
        {orgAdmin && <OrgDefaults />}
        {orgAdmin && <AlertChannels />}
      </div>
    </>
  )
}

function PasswordPanel() {
  const [cur, setCur] = useState('')
  const [next, setNext] = useState('')
  const change = useAction(() => post('/api/me/password', { current: cur, new: next }), { done: 'Password changed', onSuccess: () => { setCur(''); setNext('') } })
  return (
    <Panel title="Password" sub="Used only for local sign-in. With single sign-on, your identity provider manages credentials.">
      <div className="grid3">
        <Field label="Current password"><Input type="password" autoComplete="current-password" value={cur} onChange={(e) => setCur(e.target.value)} /></Field>
        <Field label="New password" hint="At least 12 characters."><Input type="password" autoComplete="new-password" value={next} onChange={(e) => setNext(e.target.value)} /></Field>
        <div style={{ alignSelf: 'center' }}><Button variant="primary" disabled={next.length < 12} busy={change.isPending} onClick={() => change.mutate(undefined)}>Change password</Button></div>
      </div>
    </Panel>
  )
}

function TokensPanel() {
  const { org } = useApp()
  const tokens = useQuery({ queryKey: ['pats', org.id], queryFn: () => get<ApiKey[]>(`/api/orgs/${org.id}/keys?mine=true`) })
  const [name, setName] = useState('')
  const [secret, setSecret] = useState('')
  const [revoking, setRevoking] = useState<ApiKey | null>(null)
  const create = useAction(() => post<{ secret: string }>('/api/keys', { kind: 'pat', name, org_id: org.id, expires_in_days: 90 }), { done: 'Token created', invalidate: [['pats']], onSuccess: (r) => { setSecret(r.secret); setName('') } })
  const revoke = useAction((k: ApiKey) => post(`/api/keys/${k.id}/revoke`), { done: 'Token revoked', invalidate: [['pats']], onSuccess: () => setRevoking(null) })
  const list = (tokens.data ?? []).filter((k) => k.kind === 'pat')
  return (
    <Panel title="Personal access tokens" sub="For automation against the admin API (Terraform, CI, GitOps). They act with your permissions and expire after 90 days.">
      <div className="row" style={{ alignItems: 'flex-end' }}>
        <div style={{ flex: 1 }}><Field label="Token name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="gitops-sync" /></Field></div>
        <div style={{ marginBottom: 16 }}><Button variant="primary" disabled={!name} busy={create.isPending} onClick={() => create.mutate(undefined)}><Plus size={14} /> Create token</Button></div>
      </div>
      {secret && (
        <div className="stack" style={{ marginBottom: 16 }}>
          <div className="secret-box"><code>{secret}</code><CopyButton text={secret} /></div>
          <CodeBlock code={`curl -H "Authorization: Bearer ${secret}" ${window.location.origin}/api/orgs/${org.slug}/teams`} />
        </div>
      )}
      <table className="tbl panel">
        <thead><tr><th>Name</th><th>Prefix</th><th>Last used</th><th>Expires</th><th /></tr></thead>
        <tbody>
          {list.map((k) => <tr key={k.id}><td>{k.name}</td><td className="mono dim">{k.prefix}</td><td className="dim">{fmtAgo(k.last_used_at)}</td><td className="dim">{k.expires_at ? new Date(k.expires_at).toLocaleDateString() : 'Never'}</td><td className="tight"><Button size="sm" variant="danger" onClick={() => setRevoking(k)}>Revoke</Button></td></tr>)}
          {!list.length && <tr><td colSpan={5} className="muted">No tokens yet.</td></tr>}
        </tbody>
      </table>
      <Confirm open={!!revoking} danger title={`Revoke ${revoking?.name}?`} body="Automation using it will stop working within 30 seconds." confirmLabel="Revoke token" onClose={() => setRevoking(null)} onConfirm={() => revoking && revoke.mutate(revoking)} busy={revoke.isPending} />
    </Panel>
  )
}

function OrgDefaults() {
  const { org } = useApp()
  const qc = useQueryClient()
  const models = useRoutableModels()
  const [name, setName] = useState(org.name)
  const [s, setS] = useState<ScopeSettings>(org.settings ?? {})
  const save = useAction(() => patch(`/api/orgs/${org.id}`, { name, settings: s }), { done: 'Organization saved', onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }) })
  const purge = useAction(() => del<{ deleted: number }>(`/api/orgs/${org.id}/cache`), { done: (r) => `Cleared ${r.deleted} cached response(s)` })
  return (
    <Panel title="Organization defaults" sub="Apply to every team and project unless they narrow them further." end={<Button variant="primary" busy={save.isPending} onClick={() => save.mutate(undefined)}>Save</Button>}>
      <Field label="Organization name"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
      <Field label="Allowed models" hint="Empty allows every model in the catalog."><ChipsInput value={s.allowed_models ?? []} onChange={(v) => setS({ ...s, allowed_models: v })} placeholder="Any model" suggestions={models.data.map((m) => m.name)} /></Field>
      <div className="grid3">
        <Field label="Requests per minute"><Input type="number" min={0} value={s.rpm ?? ''} onChange={(e) => setS({ ...s, rpm: Number(e.target.value) || 0 })} placeholder="No limit" /></Field>
        <Field label="Tokens per minute"><Input type="number" min={0} value={s.tpm ?? ''} onChange={(e) => setS({ ...s, tpm: Number(e.target.value) || 0 })} placeholder="No limit" /></Field>
        <Field label="Payload logging"><Select value={s.log_payloads ? 'on' : 'off'} onChange={(e) => setS({ ...s, log_payloads: e.target.value === 'on' })} options={[{ value: 'off', label: 'Off (metadata only)' }, { value: 'on', label: 'On (after redaction)' }]} /></Field>
      </div>
      <Field label="Response cache" hint={<>Answers identical requests from cache without calling the provider, at no cost. Entries are kept per project and encrypted; clients can opt out with <span className="mono">X-Theophylax-Cache: off</span>. {(s.cache_ttl_seconds ?? 0) > 0 && <Button size="sm" busy={purge.isPending} onClick={() => purge.mutate(undefined)}>Clear cache</Button>}</>}>
        <Select value={String(Math.max(s.cache_ttl_seconds ?? 0, 0))} onChange={(e) => setS({ ...s, cache_ttl_seconds: Number(e.target.value) })} options={[{ value: '0', label: 'Off' }, ...cacheTTLs]} style={{ maxWidth: 240 }} />
      </Field>
    </Panel>
  )
}

interface Channel { id: string; name: string; type: 'slack' | 'teams' | 'webhook' | 'email'; target: string; has_secret: boolean; events: string[]; enabled: boolean }

const channelTypes = [{ value: 'slack', label: 'Slack (incoming webhook)' }, { value: 'teams', label: 'Microsoft Teams (workflow webhook)' }, { value: 'webhook', label: 'Webhook (signed JSON)' }, { value: 'email', label: 'Email' }]
const eventLabels: Record<string, string> = { 'budget.threshold': 'Budget passes its alert threshold', 'budget.exhausted': 'Budget exhausted', 'guardrail.blocked': 'Guardrail blocks (5-minute summaries)' }

function AlertChannels() {
  const { org } = useApp()
  const q = useQuery({ queryKey: ['channels', org.id], queryFn: () => get<{ channels: Channel[]; event_types: string[]; email_configured: boolean }>(`/api/orgs/${org.id}/notifications`) })
  const blank = { name: '', type: 'slack', target: '', secret: '', events: [] as string[] }
  const [f, setF] = useState(blank)
  const [adding, setAdding] = useState(false)
  const [removing, setRemoving] = useState<Channel | null>(null)
  const inv = { invalidate: [['channels', org.id]] }
  const add = useAction(() => post(`/api/orgs/${org.id}/notifications`, { ...f, secret: f.type === 'webhook' ? f.secret : undefined }), { ...inv, done: 'Channel added', onSuccess: () => { setF(blank); setAdding(false) } })
  const toggle = useAction((c: Channel) => patch(`/api/notifications/${c.id}`, { enabled: !c.enabled }), inv)
  const test = useAction((c: Channel) => post<{ ok: boolean; error?: string }>(`/api/notifications/${c.id}/test`, {}), { done: (r) => (r.ok ? 'Test alert sent' : `Delivery failed: ${r.error}`) })
  const remove = useAction((c: Channel) => del(`/api/notifications/${c.id}`), { ...inv, done: 'Channel removed', onSuccess: () => setRemoving(null) })
  const types = q.data?.event_types ?? Object.keys(eventLabels)
  const placeholder = { slack: 'https://hooks.slack.com/services/…', teams: 'https://…logic.azure.com/workflows/…', webhook: 'https://ops.example.com/theophylax', email: 'oncall@example.com, finops@example.com' }[f.type as Channel['type']]
  return (
    <Panel title="Alerts" sub="Tell people when a budget is running out or a guardrail starts blocking traffic. Alerts never include prompt or response content." end={!adding && <Button onClick={() => setAdding(true)}><Plus size={14} /> Add channel</Button>}>
      {(q.data?.channels ?? []).length === 0 && !adding && <p className="muted">No alert channels yet.</p>}
      {(q.data?.channels ?? []).map((c) => (
        <div key={c.id} className="row wrap" style={{ justifyContent: 'space-between', gap: 8, padding: '8px 0', borderBottom: '1px solid var(--line)' }}>
          <div>
            <strong>{c.name}</strong> <Badge>{c.type}</Badge> <span className="muted mono">{c.target}</span>
            <div className="muted" style={{ fontSize: 12 }}>{c.events.length ? c.events.map((e) => eventLabels[e] ?? e).join(' · ') : 'All alerts'}{c.has_secret ? ' · signed' : ''}</div>
          </div>
          <div className="row" style={{ gap: 8 }}>
            <Toggle checked={c.enabled} onChange={() => toggle.mutate(c)} label={c.enabled ? 'On' : 'Paused'} />
            <Button size="sm" busy={test.isPending} onClick={() => test.mutate(c)}>Send test</Button>
            <Button size="sm" variant="danger" onClick={() => setRemoving(c)}>Remove</Button>
          </div>
        </div>
      ))}
      {adding && (
        <div className="stack" style={{ marginTop: 12 }}>
          <div className="grid3">
            <Field label="Name"><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} placeholder="e.g. #ai-platform" /></Field>
            <Field label="Type"><Select value={f.type} onChange={(e) => setF({ ...f, type: e.target.value })} options={channelTypes.filter((t) => t.value !== 'email' || q.data?.email_configured)} /></Field>
            {f.type === 'webhook' && <Field label="Signing secret" hint="Optional. Requests carry an X-Theophylax-Signature HMAC."><Input value={f.secret} onChange={(e) => setF({ ...f, secret: e.target.value })} type="password" autoComplete="new-password" /></Field>}
          </div>
          <Field label={f.type === 'email' ? 'Recipients' : 'Webhook URL'} hint={f.type === 'email' ? 'Comma-separated addresses.' : 'Stored encrypted; only the host is shown afterwards.'}><Input value={f.target} onChange={(e) => setF({ ...f, target: e.target.value })} placeholder={placeholder} /></Field>
          <Field label="Send" hint="Leave all unticked to receive every alert.">
            <div className="stack" style={{ gap: 4 }}>{types.map((t) => (
              <label key={t} className="row" style={{ gap: 6 }}><input type="checkbox" checked={f.events.includes(t)} onChange={(e) => setF({ ...f, events: e.target.checked ? [...f.events, t] : f.events.filter((x) => x !== t) })} />{eventLabels[t] ?? t}</label>
            ))}</div>
          </Field>
          <div className="row" style={{ gap: 8 }}><Button variant="primary" busy={add.isPending} onClick={() => add.mutate(undefined)} disabled={!f.name || !f.target}>Add channel</Button><Button onClick={() => { setAdding(false); setF(blank) }}>Cancel</Button></div>
        </div>
      )}
      <Confirm open={!!removing} danger title={`Remove ${removing?.name}?`} body="Alerts will stop going to this channel." confirmLabel="Remove channel" onClose={() => setRemoving(null)} onConfirm={() => removing && remove.mutate(removing)} busy={remove.isPending} />
    </Panel>
  )
}
