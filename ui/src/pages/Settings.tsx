import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { get, patch, post, type ApiKey, type ScopeSettings } from '../lib/api'
import { useApp } from '../lib/app'
import { useRoutableModels } from '../lib/queries'
import { fmtAgo } from '../lib/format'
import { Button, ChipsInput, CodeBlock, Confirm, CopyButton, Field, Input, PageHead, Panel, RoleBadge, Select, useAction } from '../components/ui'

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
        {me.has_password && <PasswordPanel />}
        <TokensPanel />
        {orgAdmin && <OrgDefaults />}
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
  return (
    <Panel title="Organization defaults" sub="Apply to every team and project unless they narrow them further." end={<Button variant="primary" busy={save.isPending} onClick={() => save.mutate(undefined)}>Save</Button>}>
      <Field label="Organization name"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
      <Field label="Allowed models" hint="Empty allows every model in the catalog."><ChipsInput value={s.allowed_models ?? []} onChange={(v) => setS({ ...s, allowed_models: v })} placeholder="Any model" suggestions={models.data.map((m) => m.name)} /></Field>
      <div className="grid3">
        <Field label="Requests per minute"><Input type="number" min={0} value={s.rpm ?? ''} onChange={(e) => setS({ ...s, rpm: Number(e.target.value) || 0 })} placeholder="No limit" /></Field>
        <Field label="Tokens per minute"><Input type="number" min={0} value={s.tpm ?? ''} onChange={(e) => setS({ ...s, tpm: Number(e.target.value) || 0 })} placeholder="No limit" /></Field>
        <Field label="Payload logging"><Select value={s.log_payloads ? 'on' : 'off'} onChange={(e) => setS({ ...s, log_payloads: e.target.value === 'on' })} options={[{ value: 'off', label: 'Off (metadata only)' }, { value: 'on', label: 'On (after redaction)' }]} /></Field>
      </div>
    </Panel>
  )
}
