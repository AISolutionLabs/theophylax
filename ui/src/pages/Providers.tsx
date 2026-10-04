import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { CheckCircle2, KeyRound, Plug, Plus, Search, Trash2, XCircle } from 'lucide-react'
import { del, patch, post, type Provider } from '../lib/api'
import { useApp, useToast } from '../lib/app'
import { useProviders } from '../lib/queries'
import { fmtAgo, kindLabel } from '../lib/format'
import { Badge, Button, Confirm, Drawer, Empty, ErrorNote, Field, Input, Loading, PageHead, Segmented, Select, Tabs, Textarea, Toggle, useAction } from '../components/ui'
import { DiscoverDrawer } from './Models'

type Kind = 'anthropic' | 'bedrock' | 'vertex' | 'azure-foundry' | 'azure-openai' | 'vertex-openai' | 'gemini' | 'openai' | 'custom'

const OAUTH_FIELDS = ['token_url', 'client_id', 'client_secret', 'scope']

// Each provider kind offers its auth methods and the fields each needs.
const KINDS: Record<Kind, { host: string; hostLabel: string; hostHint: string; auths: { value: string; label: string; fields: string[] }[] }> = {
  anthropic: { host: 'api.anthropic.com', hostLabel: 'Host', hostHint: 'Leave as api.anthropic.com unless you use a proxy.', auths: [
    { value: 'api_key', label: 'API key (x-api-key)', fields: ['api_key'] },
    { value: 'auth_token', label: 'Bearer token', fields: ['auth_token'] },
    { value: 'oauth', label: 'OAuth 2.0', fields: OAUTH_FIELDS },
  ] },
  bedrock: { host: 'us-east-1', hostLabel: 'Region', hostHint: 'e.g. us-east-1, eu-west-1, eu-central-1.', auths: [
    { value: 'irsa', label: 'IRSA (EKS service account role)', fields: ['role_arn'] },
    { value: 'pod_identity', label: 'EKS Pod Identity', fields: [] },
    { value: 'default', label: 'Default credential chain', fields: [] },
    { value: 'assume_role', label: 'Assume role (cross-account)', fields: ['role_arn', 'external_id'] },
    { value: 'entra', label: 'Entra ID federation (Azure workload identity → IAM role)', fields: ['role_arn', 'audience', 'entra_auth', 'tenant_id', 'client_id'] },
    { value: 'static', label: 'Access keys', fields: ['access_key_id', 'secret_access_key', 'session_token'] },
    { value: 'api_key', label: 'Bedrock API key', fields: ['api_key'] },
  ] },
  vertex: { host: 'us-east5', hostLabel: 'Region', hostHint: 'Claude on Vertex AI: us-east5, europe-west1, global…', auths: [
    { value: 'workload_identity', label: 'Workload identity federation (EKS, AKS…)', fields: ['project', 'audience', 'token_file', 'service_account'] },
    { value: 'metadata', label: 'GKE workload identity / metadata server', fields: ['project'] },
    { value: 'service_account', label: 'Service account key', fields: ['project', 'credentials'] },
  ] },
  'azure-foundry': { host: '', hostLabel: 'Resource', hostHint: 'Your Foundry resource, e.g. my-ai.services.ai.azure.com', auths: [
    { value: 'api_key', label: 'API key', fields: ['api_key'] },
    { value: 'entra', label: 'Entra ID app (client secret)', fields: ['tenant_id', 'client_id', 'client_secret'] },
    { value: 'workload', label: 'Workload identity federation', fields: ['tenant_id', 'client_id'] },
    { value: 'managed', label: 'Managed identity', fields: ['client_id'] },
  ] },
  'azure-openai': { host: '', hostLabel: 'Resource', hostHint: 'e.g. my-openai.openai.azure.com', auths: [
    { value: 'api_key', label: 'API key', fields: ['api_key', 'api_version'] },
    { value: 'entra', label: 'Entra ID app (client secret)', fields: ['tenant_id', 'client_id', 'client_secret', 'api_version'] },
    { value: 'workload', label: 'Workload identity federation', fields: ['tenant_id', 'client_id', 'api_version'] },
    { value: 'managed', label: 'Managed identity', fields: ['client_id', 'api_version'] },
  ] },
  'vertex-openai': { host: 'us-central1', hostLabel: 'Region', hostHint: 'Gemini and partner models on Vertex AI (model IDs like google/gemini-2.5-pro).', auths: [
    { value: 'workload_identity', label: 'Workload identity federation (EKS, AKS…)', fields: ['project', 'audience', 'token_file', 'service_account'] },
    { value: 'metadata', label: 'GKE workload identity / metadata server', fields: ['project'] },
    { value: 'service_account', label: 'Service account key', fields: ['project', 'credentials'] },
  ] },
  gemini: { host: 'generativelanguage.googleapis.com', hostLabel: 'Host', hostHint: 'Google AI Studio (Gemini API).', auths: [
    { value: 'api_key', label: 'API key', fields: ['api_key'] },
  ] },
  openai: { host: 'api.openai.com', hostLabel: 'Host', hostHint: 'Any OpenAI-compatible endpoint. Shorthands also work as connection strings: groq://, mistral://, together://, deepseek://, xai://, openrouter://, ollama://host:11434, vllm://host:8000.', auths: [
    { value: 'api_key', label: 'API key', fields: ['api_key', 'base_url'] },
    { value: 'oauth', label: 'OAuth 2.0', fields: [...OAUTH_FIELDS, 'base_url'] },
  ] },
  custom: { host: 'https://', hostLabel: 'API URL', hostHint: 'OpenAI format: the URL before /chat/completions. Anthropic format: the URL before /v1/messages.', auths: [
    { value: 'basic', label: 'Basic (username and password)', fields: ['username', 'password', 'token_url', 'scope'] },
    { value: 'bearer', label: 'Bearer token', fields: ['token', 'refresh_token', 'token_url', 'client_id', 'client_secret', 'scope'] },
    { value: 'oauth', label: 'OAuth 2.0', fields: OAUTH_FIELDS },
  ] },
}

const slug = (s: string) => s.toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^[^a-z0-9]+|-+$/g, '').slice(0, 63)

const FIELD_LABEL: Record<string, string> = {
  api_key: 'API key', auth_token: 'Token', role_arn: 'Role ARN', external_id: 'External ID', access_key_id: 'Access key ID', secret_access_key: 'Secret access key',
  session_token: 'Session token (optional)', project: 'Google Cloud project', audience: 'Audience', token_file: 'Token file', service_account: 'Impersonate service account (optional)',
  credentials: 'Service account key (path or ${file:…})', entra_auth: 'Entra auth (workload, managed or entra)', tenant_id: 'Tenant ID', client_id: 'Client ID', client_secret: 'Client secret', api_version: 'API version', base_url: 'Base URL (optional)',
  username: 'Username', password: 'Password', token: 'Access token', refresh_token: 'Refresh token (optional)', token_url: 'Token URL (optional)', scope: 'Scope (optional)',
}
const SECRET = new Set(['api_key', 'auth_token', 'secret_access_key', 'session_token', 'client_secret', 'password', 'token', 'refresh_token', 'credentials'])
const OAUTH_LABEL: Record<string, string> = { token_url: 'Token URL', client_id: 'Client ID', client_secret: 'Client secret' }
const CUSTOM_HINT: Record<string, string> = {
  token_url: 'OAuth token endpoint. With Basic, the credentials are exchanged here for an access and refresh token; with Bearer, the refresh token is used here to renew the access token.',
  client_id: 'Optional; sent when refreshing the token.',
  client_secret: 'Optional; sent when refreshing the token.',
}

export default function Providers() {
  const { isPlatformAdmin } = useApp()
  const [sp, setSp] = useSearchParams()
  const providers = useProviders()
  const [editing, setEditing] = useState<Provider | 'new' | null>(sp.get('new') === '1' ? 'new' : null)
  const [discover, setDiscover] = useState<Provider | null>(null)
  const toast = useToast()
  useEffect(() => {
    let changed = false
    if (sp.get('new')) { sp.delete('new'); changed = true }
    if (sp.get('oauth') === 'connected') { toast('Provider connected. The gateway will refresh its token automatically.'); sp.delete('oauth'); sp.delete('provider'); changed = true }
    if (sp.get('oauth_error')) { toast(`Couldn't connect the provider: ${sp.get('oauth_error')}`, 'error'); sp.delete('oauth_error'); changed = true }
    if (changed) setSp(sp, { replace: true })
  }, [sp, setSp, toast])
  return (
    <>
      <PageHead title="Providers" lede="Connections to model providers. Credentials are encrypted at rest and never shown again; prefer workload identity (IRSA, Pod Identity, Entra federation) over static keys."
        actions={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={15} /> Connect provider</Button>} />
      <div className="panel">
        {providers.isLoading ? <Loading /> : providers.error ? <ErrorNote error={providers.error} /> : !providers.data?.length ? (
          <Empty icon={<Plug size={20} />} title="No providers connected" action={<Button variant="primary" onClick={() => setEditing('new')}>Connect provider</Button>}>Connect Anthropic, AWS Bedrock, Google Vertex AI, Gemini, Azure AI Foundry, Azure OpenAI, any OpenAI-compatible endpoint, or a custom API with Basic or Bearer credentials.</Empty>
        ) : (
          <table className="tbl">
            <thead><tr><th>Provider</th><th>Type</th><th>Connection</th><th>Scope</th><th className="num">Models</th><th>Status</th><th /></tr></thead>
            <tbody>
              {providers.data.map((p) => (
                <tr key={p.id} className={p.can_manage ? 'click' : ''} onClick={() => p.can_manage && setEditing(p)}>
                  <td className="cell-title">{p.name}</td>
                  <td>{kindLabel[p.kind] ?? p.kind}</td>
                  <td className="mono dim" style={{ maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={p.connection_string}>{p.connection_string}</td>
                  <td>{p.org_id ? <Badge>this organization</Badge> : <Badge tone="accent">platform-wide</Badge>}</td>
                  <td className="num">{p.deployment_count}</td>
                  <td>{p.status === 'ready' ? <Badge tone="ok" title={p.oauth?.connected_by ? `OAuth connected by ${p.oauth.connected_by}` : undefined}>ready</Badge> : p.status === 'not_connected' ? <Badge tone="warn" title="An admin needs to sign in at the provider">not connected</Badge> : p.status === 'disabled' ? <Badge>disabled</Badge> : <Badge tone="bad" title={p.error}>error</Badge>}</td>
                  <td className="tight" onClick={(e) => e.stopPropagation()}>
                    {p.can_manage && p.status === 'not_connected' && <Button size="sm" variant="primary" onClick={() => connectOAuth(p.id)}><KeyRound size={13} /> Connect</Button>}
                    {p.can_manage && p.status === 'ready' && p.kind !== 'azure-foundry' && p.kind !== 'vertex' && <Button size="sm" onClick={() => setDiscover(p)}><Search size={13} /> Discover models</Button>}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {!isPlatformAdmin && <p className="muted" style={{ marginTop: 12, fontSize: 12.5 }}>Org admins can connect providers that only this organization uses. Platform-wide providers are managed by platform admins.</p>}
      {editing && <ProviderForm existing={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
      {discover && <DiscoverDrawer provider={discover} onClose={() => setDiscover(null)} />}
    </>
  )
}

interface OAuthOpts { grant: string; authorizeURL: string; clientAuth: string }

function buildConn(kind: Kind, host: string, auth: string, fields: Record<string, string>, proxy: string, caBundle: string, endpoint: string, custom: { name: string; format: string }, oauth: OAuthOpts) {
  const q = new URLSearchParams()
  if (kind === 'custom') {
    q.set('name', custom.name)
    if (custom.format !== 'openai') q.set('format', custom.format)
    if (host.startsWith('http://')) { q.set('base_url', host.replace(/\/+$/, '')); host = host.slice(7) } else host = host.replace(/^https:\/\//, '')
    host = host.replace(/\/+$/, '')
  }
  if (!(kind === 'anthropic' && auth === 'api_key') && !(kind === 'openai' && auth === 'api_key') && kind !== 'gemini') q.set('auth', auth)
  if (auth === 'oauth') {
    if (oauth.grant === 'authorization_code') { q.set('grant_type', 'authorization_code'); if (oauth.authorizeURL) q.set('authorize_url', oauth.authorizeURL) }
    if (oauth.clientAuth === 'post') q.set('client_auth', 'post')
  }
  for (const f of KINDS[kind].auths.find((a) => a.value === auth)?.fields ?? []) if (fields[f]) q.set(f, fields[f]!)
  if (proxy) q.set('proxy', proxy)
  if (caBundle) q.set('ca_bundle', caBundle)
  if (endpoint && kind === 'bedrock') q.set('endpoint', endpoint)
  const qs = q.toString().replace(/%24%7B/g, '${').replace(/%7D/g, '}').replace(/%3A/g, ':').replace(/%2F/g, '/')
  return `${kind}://${host}${qs ? '?' + qs : ''}`
}

function ProviderForm({ existing, onClose }: { existing?: Provider; onClose: () => void }) {
  const { org, isPlatformAdmin } = useApp()
  const [mode, setMode] = useState<'guided' | 'string'>(existing ? 'string' : 'guided')
  const [name, setName] = useState(existing?.name ?? '')
  const [kind, setKind] = useState<Kind>((existing?.kind as Kind) ?? 'bedrock')
  const [host, setHost] = useState(KINDS.bedrock.host)
  const [auth, setAuth] = useState(KINDS.bedrock.auths[0]!.value)
  const [fields, setFields] = useState<Record<string, string>>({})
  const [proxy, setProxy] = useState('')
  const [ca, setCa] = useState('')
  const [endpoint, setEndpoint] = useState('')
  const [raw, setRaw] = useState('')
  const [credName, setCredName] = useState('')
  const [credFormat, setCredFormat] = useState('openai')
  const [oauth, setOAuth] = useState<OAuthOpts>({ grant: 'client_credentials', authorizeURL: '', clientAuth: 'basic' })
  const [scopeOrg, setScopeOrg] = useState(!isPlatformAdmin)
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [config, setConfig] = useState(JSON.stringify(existing?.config ?? {}, null, 2))
  const [tab, setTab] = useState<'connection' | 'advanced'>('connection')
  const [confirm, setConfirm] = useState(false)
  const cred = credName || slug(name)
  const conn = useMemo(() => mode === 'guided' ? buildConn(kind, host, auth, fields, proxy, ca, endpoint, { name: cred, format: credFormat }, oauth) : raw, [mode, kind, host, auth, fields, proxy, ca, endpoint, raw, cred, credFormat, oauth])
  const spec = KINDS[kind]
  const authSpec = spec.auths.find((a) => a.value === auth) ?? spec.auths[0]!

  const test = useAction(() => post<any>('/api/providers/validate', { connection_string: conn, probe: true, config: safeJSON(config) }))
  const save = useAction(() => {
    const body: any = { name, enabled, config: safeJSON(config) }
    if (conn && (!existing || mode === 'guided' || raw)) body.connection_string = conn
    if (!existing) body.org_id = scopeOrg ? org.id : ''
    return existing ? patch(`/api/providers/${existing.id}`, body) : post('/api/providers', body)
  }, { done: existing ? 'Provider saved' : 'Provider connected', invalidate: [['providers'], ['models']], onSuccess: onClose })
  const remove = useAction(() => del(`/api/providers/${existing!.id}`), { done: 'Provider removed', invalidate: [['providers'], ['models']], onSuccess: onClose })

  return (
    <Drawer open onClose={onClose} wide title={existing ? existing.name : 'Connect a provider'} sub={existing ? <>Current: <span className="mono">{existing.connection_string}</span></> : 'Use the guided form or paste a connection string.'}
      footer={<>{existing && <Button variant="danger" onClick={() => setConfirm(true)}><Trash2 size={14} /> Remove</Button>}<span className="spacer" />
        <Button onClick={() => test.mutate(undefined)} busy={test.isPending} disabled={!conn}>Test connection</Button>
        <Button variant="primary" onClick={() => save.mutate(undefined)} busy={save.isPending} disabled={!name || (!existing && !conn)}>{existing ? 'Save provider' : 'Connect provider'}</Button></>}>
      <div className="grid2">
        <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} placeholder="Bedrock eu-west-1 (prod)" autoFocus={!existing} /></Field>
        {!existing && isPlatformAdmin ? (
          <Field label="Available to"><Select value={scopeOrg ? 'org' : 'platform'} onChange={(e) => setScopeOrg(e.target.value === 'org')} options={[{ value: 'platform', label: 'Every organization' }, { value: 'org', label: `Only ${org.name}` }]} /></Field>
        ) : <Field label="Status"><Toggle checked={enabled} onChange={setEnabled} label={enabled ? 'Enabled' : 'Disabled'} /></Field>}
      </div>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'connection', label: 'Connection' }, { value: 'advanced', label: 'Advanced' }]} />
      {tab === 'connection' && (
        <>
          <div style={{ marginBottom: 16 }}><Segmented value={mode} onChange={setMode} options={[{ value: 'guided', label: 'Guided' }, { value: 'string', label: 'Connection string' }]} /></div>
          {mode === 'guided' ? (
            <>
              <Field label="Provider type">
                <Select value={kind} onChange={(e) => { const k = e.target.value as Kind; setKind(k); setHost(KINDS[k].host); setAuth(KINDS[k].auths[0]!.value); setFields({}) }}
                  options={(Object.keys(KINDS) as Kind[]).map((k) => ({ value: k, label: kindLabel[k] ?? k }))} />
              </Field>
              <div className="grid2">
                <Field label={spec.hostLabel} hint={spec.hostHint}><Input value={host} onChange={(e) => setHost(e.target.value)} /></Field>
                <Field label="Authentication"><Select value={auth} onChange={(e) => setAuth(e.target.value)} options={spec.auths.map((a) => ({ value: a.value, label: a.label }))} /></Field>
              </div>
              {kind === 'custom' && (
                <div className="grid2">
                  <Field label="Credential name" hint={<>Models through this credential are named <span className="mono">&lt;model&gt;@{cred || 'name'}</span>, e.g. <span className="mono">gpt-4o@{cred || 'name'}</span>.</>}>
                    <Input className="code" value={cred} onChange={(e) => setCredName(slug(e.target.value))} placeholder="acme" />
                  </Field>
                  <Field label="API format"><Select value={credFormat} onChange={(e) => setCredFormat(e.target.value)} options={[{ value: 'openai', label: 'OpenAI chat completions' }, { value: 'anthropic', label: 'Anthropic Messages' }]} /></Field>
                </div>
              )}
              {auth === 'oauth' && (
                <>
                  <div className="grid2">
                    <Field label="OAuth grant" hint={oauth.grant === 'authorization_code' ? 'After saving, click Connect to sign in at the provider once. The gateway keeps the refresh token.' : 'Service-to-service: the client ID and secret are exchanged for an access token.'}>
                      <Select value={oauth.grant} onChange={(e) => setOAuth((o) => ({ ...o, grant: e.target.value }))} options={[{ value: 'client_credentials', label: 'Client credentials' }, { value: 'authorization_code', label: 'Authorization code (sign in)' }]} />
                    </Field>
                    <Field label="Client authentication" hint="How the client secret is sent to the token URL.">
                      <Select value={oauth.clientAuth} onChange={(e) => setOAuth((o) => ({ ...o, clientAuth: e.target.value }))} options={[{ value: 'basic', label: 'HTTP Basic (client_secret_basic)' }, { value: 'post', label: 'Request body (client_secret_post)' }]} />
                    </Field>
                  </div>
                  {oauth.grant === 'authorization_code' && (
                    <>
                      <Field label="Authorize URL"><Input className="code" value={oauth.authorizeURL} onChange={(e) => setOAuth((o) => ({ ...o, authorizeURL: e.target.value }))} placeholder="https://auth.example.com/oauth/authorize" /></Field>
                      <Field label="Redirect URI" hint="Register this with the provider's OAuth app."><div className="row"><code style={{ flex: 1 }}>{`${window.location.origin}/auth/provider-oauth/callback`}</code></div></Field>
                    </>
                  )}
                </>
              )}
              <div className="grid2">
                {authSpec.fields.map((f) => (
                  <Field key={f} label={auth === 'oauth' ? (OAUTH_LABEL[f] ?? FIELD_LABEL[f] ?? f) : (FIELD_LABEL[f] ?? f)} hint={auth === 'oauth' && f === 'client_secret' ? 'Optional for public clients using the authorization code grant (PKCE).' : kind === 'custom' && CUSTOM_HINT[f] && auth !== 'oauth' ? CUSTOM_HINT[f] : SECRET.has(f) ? 'Or reference a mounted secret: ${env:NAME} or ${file:/path}' : f === 'role_arn' && auth === 'irsa' ? 'Leave empty to use AWS_ROLE_ARN from the pod.' : f === 'api_version' ? 'v1 (recommended) or a dated version like 2024-10-21' : undefined}>
                    <Input type={SECRET.has(f) && !fields[f]?.startsWith('${') ? 'password' : 'text'} value={fields[f] ?? ''} onChange={(e) => setFields((x) => ({ ...x, [f]: e.target.value }))} autoComplete="off" className={SECRET.has(f) ? 'code' : ''} />
                  </Field>
                ))}
              </div>
              {kind === 'bedrock' && <Field label="VPC endpoint (optional)" hint="PrivateLink runtime endpoint, e.g. https://vpce-…bedrock-runtime.eu-west-1.vpce.amazonaws.com"><Input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} /></Field>}
              <div className="grid2">
                <Field label="Egress proxy (optional)" hint="Per-connection HTTP proxy, or 'none' to bypass the environment proxy."><Input value={proxy} onChange={(e) => setProxy(e.target.value)} placeholder="http://proxy.corp:8080" /></Field>
                <Field label="Custom CA bundle (optional)" hint="Path to a PEM file, for TLS-inspecting proxies."><Input value={ca} onChange={(e) => setCa(e.target.value)} placeholder="/etc/ssl/corp-ca.pem" /></Field>
              </div>
              <Field label="Resulting connection string"><div className="code-block" style={{ whiteSpace: 'pre-wrap' }}>{conn.replace(/(api_key|secret_access_key|client_secret|auth_token|session_token|password|refresh_token|token)=([^&$][^&]*)/g, '$1=••••')}</div></Field>
            </>
          ) : (
            <Field label="Connection string" hint={existing ? 'Leave empty to keep the stored credentials.' : 'URI or key=value form. Values can reference ${env:NAME} or ${file:/path}.'}>
              <Textarea className="code" rows={4} value={raw} onChange={(e) => setRaw(e.target.value)} placeholder={'bedrock://eu-west-1?auth=irsa\nanthropic://api.anthropic.com?api_key=${env:ANTHROPIC_API_KEY}\ncustom://api.acme.com/v1?name=acme&auth=basic&username=…&password=${env:ACME_SECRET}&token_url=https://auth.acme.com/oauth/token\nProvider=AzureOpenAI;Endpoint=https://x.openai.azure.com;Auth=Entra;TenantId=…;ClientId=…;ClientSecret=…'} />
            </Field>
          )}
          {test.data && (
            <div className={`callout ${test.data.ok && !test.data.probe_error ? 'accent' : test.data.ok ? 'warn' : 'bad'}`}>
              {test.data.ok && !test.data.probe_error ? <CheckCircle2 size={16} /> : <XCircle size={16} />}
              <div>{!test.data.ok ? test.data.error : test.data.probe_error ? <>The connection string is valid, but listing models failed: {test.data.probe_error}</> : <>Connected to {kindLabel[test.data.kind]} and found {test.data.models} model(s).</>}</div>
            </div>
          )}
        </>
      )}
      {tab === 'advanced' && (
        <Field label="Options (JSON)" hint={'drop_params: request fields to strip for this provider; headers: extra headers (values may use ${env:…}); timeout_seconds.'}>
          <Textarea className="code" rows={8} value={config} onChange={(e) => setConfig(e.target.value)} placeholder={'{\n  "drop_params": ["metadata"],\n  "headers": {"x-team": "ml"},\n  "timeout_seconds": 600\n}'} />
        </Field>
      )}
      <Confirm open={confirm} danger title={`Remove ${existing?.name}?`} body="Its deployments are removed too. Models that only route here will stop working." confirmLabel="Remove provider" onClose={() => setConfirm(false)} onConfirm={() => remove.mutate(undefined)} busy={remove.isPending} />
      {existing?.oauth && <OAuthPanel p={existing} />}
      {existing && <p className="muted" style={{ marginTop: 14, fontSize: 12.5 }}>Last changed {fmtAgo(existing.updated_at)}.</p>}
    </Drawer>
  )
}

function connectOAuth(id: string) {
  window.location.href = `/auth/provider-oauth/${encodeURIComponent(id)}/start`
}

function safeJSON(s: string) {
  try { return JSON.parse(s || '{}') } catch { return {} }
}

function OAuthPanel({ p }: { p: Provider }) {
  const o = p.oauth!
  const disconnect = useAction(() => del(`/api/providers/${p.id}/oauth`), { done: 'Provider disconnected', invalidate: [['providers']] })
  return (
    <div className="panel" style={{ padding: 14, marginTop: 14 }}>
      <h3 style={{ marginBottom: 6 }}>OAuth 2.0 · {o.grant === 'authorization_code' ? 'authorization code' : 'client credentials'}</h3>
      {o.grant === 'authorization_code' ? (
        <>
          <p className="muted" style={{ fontSize: 13, marginBottom: 10 }}>
            {o.connected ? <>Connected{o.connected_by ? ` by ${o.connected_by}` : ''}{o.connected_at ? ` ${fmtAgo(o.connected_at)}` : ''}. The access token is refreshed automatically.</> : 'Not connected yet. Sign in at the provider to authorize the gateway.'}
            {' '}Redirect URI to register: <span className="mono">{o.redirect_uri}</span>
          </p>
          <div className="row">
            <Button variant={o.connected ? undefined : 'primary'} onClick={() => connectOAuth(p.id)}><KeyRound size={14} /> {o.connected ? 'Reconnect' : 'Connect'}</Button>
            {o.connected && <Button variant="danger" busy={disconnect.isPending} onClick={() => disconnect.mutate(undefined)}>Disconnect</Button>}
          </div>
        </>
      ) : (
        <p className="muted" style={{ fontSize: 13 }}>{o.connected ? `Access token held${o.expires_at ? `, renews before ${new Date(o.expires_at).toLocaleString()}` : ''}.` : 'The first request obtains an access token.'}</p>
      )}
    </div>
  )
}
