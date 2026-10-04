import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Building2, Plus, Trash2 } from 'lucide-react'
import { del, get, patch, post, type IdP } from '../lib/api'
import { useApp } from '../lib/app'
import { useScopeOptions } from '../lib/queries'
import { Badge, Button, ChipsInput, CodeBlock, Confirm, CopyButton, Drawer, Empty, ErrorNote, Field, Input, Loading, PageHead, Select, Tabs, Toggle, useAction } from '../components/ui'

const PRESETS = [
  { label: 'Microsoft Entra ID', issuer: 'https://login.microsoftonline.com/<tenant-id>/v2.0', groups: 'groups' },
  { label: 'Okta', issuer: 'https://<your-org>.okta.com/oauth2/default', groups: 'groups' },
  { label: 'Google Workspace', issuer: 'https://accounts.google.com', groups: 'groups' },
  { label: 'Auth0', issuer: 'https://<tenant>.auth0.com/', groups: 'https://your-namespace/groups' },
  { label: 'Keycloak', issuer: 'https://<host>/realms/<realm>', groups: 'groups' },
  { label: 'Ping Identity', issuer: 'https://auth.pingone.com/<env-id>/as', groups: 'groups' },
]

export default function SSO() {
  const { org } = useApp()
  const list = useQuery({ queryKey: ['idps', org.id], queryFn: () => get<IdP[]>(`/api/orgs/${org.id}/idps`) })
  const [open, setOpen] = useState<IdP | 'new' | null>(null)
  return (
    <>
      <PageHead title="Single sign-on" lede="Connect your identity provider with OpenID Connect. Group claims map to roles at the organization, team or project level, and SCIM keeps people and teams in sync."
        actions={<Button variant="primary" onClick={() => setOpen('new')}><Plus size={15} /> Add identity provider</Button>} />
      <div className="panel">
        {list.isLoading ? <Loading /> : list.error ? <ErrorNote error={list.error} /> : !list.data?.length ? (
          <Empty icon={<Building2 size={20} />} title="No identity provider yet" action={<Button variant="primary" onClick={() => setOpen('new')}>Add identity provider</Button>}>Works with Entra ID, Okta, Google, Auth0, Keycloak, Ping and any OIDC provider.</Empty>
        ) : (
          <table className="tbl">
            <thead><tr><th>Provider</th><th>Issuer</th><th>Provisioning</th><th>Gateway tokens</th><th>Status</th></tr></thead>
            <tbody>
              {list.data.map((i) => (
                <tr key={i.id} className="click" onClick={() => setOpen(i)}>
                  <td className="cell-title">{i.name}</td>
                  <td className="mono dim">{i.issuer}</td>
                  <td>{[i.jit && 'Just-in-time', i.has_scim_token && 'SCIM'].filter(Boolean).join(' + ') || 'Invite only'}</td>
                  <td>{i.gateway_tokens ? <Badge tone="accent">accepted</Badge> : <span className="dim">off</span>}</td>
                  <td>{i.enabled ? <Badge tone="ok">enabled</Badge> : <Badge>disabled</Badge>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {open && <IdPDrawer idp={open === 'new' ? undefined : open} onClose={() => setOpen(null)} />}
    </>
  )
}

function IdPDrawer({ idp, onClose }: { idp?: IdP; onClose: () => void }) {
  const { org } = useApp()
  const scopes = useScopeOptions()
  const [tab, setTab] = useState<'connection' | 'access' | 'scim'>('connection')
  const [f, setF] = useState({
    name: idp?.name ?? '', issuer: idp?.issuer ?? '', client_id: idp?.client_id ?? '', client_secret: '', scopes: idp?.scopes ?? ['openid', 'email', 'profile'],
    clear_secret: false, groups_claim: idp?.groups_claim ?? 'groups', email_domains: idp?.email_domains ?? [], jit: idp?.jit ?? true, default_role: idp?.default_role ?? 'member',
    role_mappings: idp?.role_mappings ?? [], gateway_tokens: idp?.gateway_tokens ?? false, audience: idp?.audience ?? '', enabled: idp?.enabled ?? true,
  })
  const [scim, setScim] = useState<{ token: string; scim_url: string } | null>(null)
  const [confirm, setConfirm] = useState(false)
  const set = (k: keyof typeof f) => (v: any) => setF((x) => ({ ...x, [k]: v }))
  const save = useAction(() => {
    const body: any = { ...f }
    if (!body.client_secret) delete body.client_secret
    if (!body.clear_secret) delete body.clear_secret
    return idp ? patch(`/api/idps/${idp.id}`, body) : post(`/api/orgs/${org.id}/idps`, body)
  }, { done: idp ? 'Identity provider saved' : 'Identity provider added', invalidate: [['idps']], onSuccess: () => !idp && onClose() })
  const remove = useAction(() => del(`/api/idps/${idp!.id}`), { done: 'Identity provider removed', invalidate: [['idps']], onSuccess: onClose })
  const genScim = useAction(() => post<{ token: string; scim_url: string }>(`/api/idps/${idp!.id}/scim-token`), { done: 'SCIM token issued', invalidate: [['idps']], onSuccess: setScim })
  const redirect = idp?.redirect_uri ?? `${window.location.origin}/auth/oidc/callback`

  return (
    <Drawer open onClose={onClose} wide title={idp ? idp.name : 'Add an identity provider'}
      footer={<>{idp && <Button variant="danger" onClick={() => setConfirm(true)}><Trash2 size={14} /> Remove</Button>}<span className="spacer" /><Button variant="primary" busy={save.isPending} disabled={!f.name || !f.issuer || !f.client_id} onClick={() => save.mutate(undefined)}>{idp ? 'Save' : 'Add provider'}</Button></>}>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'connection', label: 'Connection' }, { value: 'access', label: 'Access & roles' }, ...(idp ? [{ value: 'scim' as const, label: 'SCIM provisioning' }] : [])]} />
      {tab === 'connection' && (
        <>
          {!idp && (
            <Field label="Start from">
              <Select value="" onChange={(e) => { const p = PRESETS.find((x) => x.label === e.target.value); if (p) setF((x) => ({ ...x, name: x.name || p.label, issuer: p.issuer, groups_claim: p.groups })) }}
                options={[{ value: '', label: 'Choose a provider…' }, ...PRESETS.map((p) => ({ value: p.label, label: p.label }))]} />
            </Field>
          )}
          <Field label="Redirect URI" hint="Register this in your IdP's application settings."><div className="row"><code style={{ flex: 1 }}>{redirect}</code><CopyButton text={redirect} /></div></Field>
          <div className="grid2">
            <Field label="Name" hint="Shown on the sign-in button."><Input value={f.name} onChange={(e) => set('name')(e.target.value)} /></Field>
            <Field label="Issuer URL"><Input className="code" value={f.issuer} onChange={(e) => set('issuer')(e.target.value)} /></Field>
            <Field label="Client ID"><Input className="code" value={f.client_id} onChange={(e) => set('client_id')(e.target.value)} /></Field>
            <Field label="Client secret" hint={f.clear_secret ? 'The stored secret will be removed when you save; sign-in will use PKCE only.' : idp?.has_secret ? <>A secret is stored. Leave empty to keep it, or <a href="#" onClick={(e) => { e.preventDefault(); setF((x) => ({ ...x, client_secret: '', clear_secret: true })) }}>remove it</a> for a public client.</> : 'Optional for public clients using PKCE.'}><Input type="password" value={f.client_secret} disabled={f.clear_secret} onChange={(e) => set('client_secret')(e.target.value)} autoComplete="off" /></Field>
          </div>
          <Field label="Scopes"><ChipsInput value={f.scopes} onChange={set('scopes')} /></Field>
          <Toggle checked={f.enabled} onChange={set('enabled')} label="Show this provider on the sign-in page" />
        </>
      )}
      {tab === 'access' && (
        <>
          <div className="grid2">
            <Field label="Allowed email domains" hint="Empty allows any domain the IdP issues."><ChipsInput value={f.email_domains} onChange={set('email_domains')} placeholder="company.com" /></Field>
            <Field label="Groups claim" hint="Entra: groups (object IDs) or roles. Okta: groups."><Input className="code" value={f.groups_claim} onChange={(e) => set('groups_claim')(e.target.value)} /></Field>
          </div>
          <div className="grid2">
            <Field label="Create accounts on first sign-in"><Toggle checked={f.jit} onChange={set('jit')} label={f.jit ? 'Just-in-time provisioning on' : 'Only pre-provisioned people'} /></Field>
            <Field label="Default organization role"><Select value={f.default_role} onChange={(e) => set('default_role')(e.target.value)} options={[{ value: 'member', label: 'Member' }, { value: 'viewer', label: 'Viewer' }, { value: 'none', label: 'None (groups decide)' }]} /></Field>
          </div>
          <h3 style={{ margin: '4px 0 8px' }}>Group → role mappings</h3>
          <p className="muted" style={{ fontSize: 12.5, marginBottom: 10 }}>Applied at every sign-in. Removing someone from a group removes the mapped role next time they sign in (or immediately with SCIM).</p>
          {f.role_mappings.map((m, i) => (
            <div className="row" key={i} style={{ marginBottom: 8 }}>
              <Input className="code" placeholder="Group name or ID" value={m.group} onChange={(e) => set('role_mappings')(f.role_mappings.map((x, k) => k === i ? { ...x, group: e.target.value } : x))} />
              <Select style={{ width: 300 }} value={`${m.scope_type}:${m.scope_id}`} onChange={(e) => { const [st, sid] = e.target.value.split(':'); set('role_mappings')(f.role_mappings.map((x, k) => k === i ? { ...x, scope_type: st!, scope_id: sid! } : x)) }} options={scopes} />
              <Select style={{ width: 120 }} value={m.role} onChange={(e) => set('role_mappings')(f.role_mappings.map((x, k) => k === i ? { ...x, role: e.target.value } : x))} options={['owner', 'admin', 'member', 'viewer'].map((r) => ({ value: r, label: r }))} />
              <Button variant="ghost" icon onClick={() => set('role_mappings')(f.role_mappings.filter((_, k) => k !== i))} aria-label="Remove mapping"><Trash2 size={14} /></Button>
            </div>
          ))}
          <Button size="sm" onClick={() => set('role_mappings')([...f.role_mappings, { group: '', scope_type: 'org', scope_id: org.id, role: 'member' }])}><Plus size={13} /> Add mapping</Button>
          <hr style={{ border: 0, borderTop: '1px solid var(--line)', margin: '20px 0' }} />
          <Toggle checked={f.gateway_tokens} onChange={set('gateway_tokens')} label="Accept this IdP's access tokens as gateway credentials" />
          <p className="muted" style={{ fontSize: 12.5, margin: '6px 0 12px' }}>Lets people call the gateway with their own SSO identity (e.g. via Claude Code's apiKeyHelper) instead of a shared key. They choose a project with the X-Theophylax-Project header.</p>
          {f.gateway_tokens && <Field label="Accepted audiences" hint="Comma-separated. The client ID is always accepted."><Input className="code" value={f.audience} onChange={(e) => set('audience')(e.target.value)} placeholder="api://theophylax" /></Field>}
        </>
      )}
      {tab === 'scim' && idp && (
        <div className="stack">
          <p>Point your IdP's SCIM 2.0 provisioning at this endpoint. Users are created and deactivated automatically (deactivation also revokes their keys); groups become teams.</p>
          <Field label="SCIM base URL"><div className="row"><code style={{ flex: 1 }}>{idp.scim_url}</code><CopyButton text={idp.scim_url} /></div></Field>
          {scim ? (
            <div className="secret-box"><code>{scim.token}</code><CopyButton text={scim.token} /></div>
          ) : (
            <Button variant="primary" onClick={() => genScim.mutate(undefined)} busy={genScim.isPending} style={{ alignSelf: 'flex-start' }}>{idp.has_scim_token ? 'Rotate SCIM token' : 'Generate SCIM token'}</Button>
          )}
          {scim && <p className="muted">Copy the token now; it won't be shown again. The previous token stopped working.</p>}
          <CodeBlock code={`curl -H "Authorization: Bearer <token>" ${idp.scim_url}/Users?filter=userName%20eq%20%22jane@company.com%22`} />
        </div>
      )}
      <Confirm open={confirm} danger title={`Remove ${idp?.name}?`} body="People who only sign in through it lose access until another method is configured." confirmLabel="Remove provider" onClose={() => setConfirm(false)} onConfirm={() => remove.mutate(undefined)} busy={remove.isPending} />
    </Drawer>
  )
}
