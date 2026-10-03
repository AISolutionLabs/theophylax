import { useEffect, useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { CheckCircle2, Plug, Plus, Search, Trash2, XCircle } from 'lucide-react'
import { del, patch, post, type Provider } from '../lib/api'
import { useApp } from '../lib/app'
import { useProviders } from '../lib/queries'
import { fmtAgo, kindLabel } from '../lib/format'
import { Badge, Button, Confirm, Drawer, Empty, ErrorNote, Field, Input, Loading, PageHead, Segmented, Select, Tabs, Textarea, Toggle, useAction } from '../components/ui'
import { DiscoverDrawer } from './Models'

type Kind = 'anthropic' | 'bedrock' | 'azure-foundry' | 'azure-openai' | 'openai'

// Each provider kind offers its auth methods and the fields each needs.
const KINDS: Record<Kind, { host: string; hostLabel: string; hostHint: string; auths: { value: string; label: string; fields: string[] }[] }> = {
  anthropic: { host: 'api.anthropic.com', hostLabel: 'Host', hostHint: 'Leave as api.anthropic.com unless you use a proxy.', auths: [
    { value: 'api_key', label: 'API key (x-api-key)', fields: ['api_key'] },
    { value: 'auth_token', label: 'Bearer token', fields: ['auth_token'] },
  ] },
  bedrock: { host: 'us-east-1', hostLabel: 'Region', hostHint: 'e.g. us-east-1, eu-west-1, eu-central-1.', auths: [
    { value: 'irsa', label: 'IRSA (EKS service account role)', fields: ['role_arn'] },
    { value: 'pod_identity', label: 'EKS Pod Identity', fields: [] },
    { value: 'default', label: 'Default credential chain', fields: [] },
    { value: 'assume_role', label: 'Assume role (cross-account)', fields: ['role_arn', 'external_id'] },
    { value: 'static', label: 'Access keys', fields: ['access_key_id', 'secret_access_key', 'session_token'] },
    { value: 'api_key', label: 'Bedrock API key', fields: ['api_key'] },
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
  openai: { host: 'api.openai.com', hostLabel: 'Host', hostHint: 'Any OpenAI-compatible endpoint (vLLM, Ollama, LM Studio…).', auths: [
    { value: 'api_key', label: 'API key', fields: ['api_key', 'base_url'] },
  ] },
}

const FIELD_LABEL: Record<string, string> = {
  api_key: 'API key', auth_token: 'Token', role_arn: 'Role ARN', external_id: 'External ID', access_key_id: 'Access key ID', secret_access_key: 'Secret access key',
  session_token: 'Session token (optional)', tenant_id: 'Tenant ID', client_id: 'Client ID', client_secret: 'Client secret', api_version: 'API version', base_url: 'Base URL (optional)',
}
const SECRET = new Set(['api_key', 'auth_token', 'secret_access_key', 'session_token', 'client_secret'])

export default function Providers() {
  const { isPlatformAdmin } = useApp()
  const [sp, setSp] = useSearchParams()
  const providers = useProviders()
  const [editing, setEditing] = useState<Provider | 'new' | null>(sp.get('new') === '1' ? 'new' : null)
  const [discover, setDiscover] = useState<Provider | null>(null)
  useEffect(() => { if (sp.get('new')) { sp.delete('new'); setSp(sp, { replace: true }) } }, [sp, setSp])
  return (
    <>
      <PageHead title="Providers" lede="Connections to model providers. Credentials are encrypted at rest and never shown again; prefer workload identity (IRSA, Pod Identity, Entra federation) over static keys."
        actions={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={15} /> Connect provider</Button>} />
      <div className="panel">
        {providers.isLoading ? <Loading /> : providers.error ? <ErrorNote error={providers.error} /> : !providers.data?.length ? (
          <Empty icon={<Plug size={20} />} title="No providers connected" action={<Button variant="primary" onClick={() => setEditing('new')}>Connect provider</Button>}>Connect Anthropic, AWS Bedrock, Azure AI Foundry, Azure OpenAI or any OpenAI-compatible endpoint.</Empty>
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
                  <td>{p.status === 'ready' ? <Badge tone="ok">ready</Badge> : p.status === 'disabled' ? <Badge>disabled</Badge> : <Badge tone="bad" title={p.error}>error</Badge>}</td>
                  <td className="tight" onClick={(e) => e.stopPropagation()}>{p.can_manage && p.status === 'ready' && p.kind !== 'azure-foundry' && <Button size="sm" onClick={() => setDiscover(p)}><Search size={13} /> Discover models</Button>}</td>
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

function buildConn(kind: Kind, host: string, auth: string, fields: Record<string, string>, proxy: string, caBundle: string, endpoint: string) {
  const q = new URLSearchParams()
  if (!(kind === 'anthropic' && auth === 'api_key') && !(kind === 'openai')) q.set('auth', auth)
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
  const [scopeOrg, setScopeOrg] = useState(!isPlatformAdmin)
  const [enabled, setEnabled] = useState(existing?.enabled ?? true)
  const [config, setConfig] = useState(JSON.stringify(existing?.config ?? {}, null, 2))
  const [tab, setTab] = useState<'connection' | 'advanced'>('connection')
  const [confirm, setConfirm] = useState(false)
  const conn = useMemo(() => mode === 'guided' ? buildConn(kind, host, auth, fields, proxy, ca, endpoint) : raw, [mode, kind, host, auth, fields, proxy, ca, endpoint, raw])
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
              <div className="grid2">
                {authSpec.fields.map((f) => (
                  <Field key={f} label={FIELD_LABEL[f] ?? f} hint={SECRET.has(f) ? 'Or reference a mounted secret: ${env:NAME} or ${file:/path}' : f === 'role_arn' && auth === 'irsa' ? 'Leave empty to use AWS_ROLE_ARN from the pod.' : f === 'api_version' ? 'v1 (recommended) or a dated version like 2024-10-21' : undefined}>
                    <Input type={SECRET.has(f) && !fields[f]?.startsWith('${') ? 'password' : 'text'} value={fields[f] ?? ''} onChange={(e) => setFields((x) => ({ ...x, [f]: e.target.value }))} autoComplete="off" className={SECRET.has(f) ? 'code' : ''} />
                  </Field>
                ))}
              </div>
              {kind === 'bedrock' && <Field label="VPC endpoint (optional)" hint="PrivateLink runtime endpoint, e.g. https://vpce-…bedrock-runtime.eu-west-1.vpce.amazonaws.com"><Input value={endpoint} onChange={(e) => setEndpoint(e.target.value)} /></Field>}
              <div className="grid2">
                <Field label="Egress proxy (optional)" hint="Per-connection HTTP proxy, or 'none' to bypass the environment proxy."><Input value={proxy} onChange={(e) => setProxy(e.target.value)} placeholder="http://proxy.corp:8080" /></Field>
                <Field label="Custom CA bundle (optional)" hint="Path to a PEM file, for TLS-inspecting proxies."><Input value={ca} onChange={(e) => setCa(e.target.value)} placeholder="/etc/ssl/corp-ca.pem" /></Field>
              </div>
              <Field label="Resulting connection string"><div className="code-block" style={{ whiteSpace: 'pre-wrap' }}>{conn.replace(/(api_key|secret_access_key|client_secret|auth_token|session_token)=([^&$][^&]*)/g, '$1=••••')}</div></Field>
            </>
          ) : (
            <Field label="Connection string" hint={existing ? 'Leave empty to keep the stored credentials.' : 'URI or key=value form. Values can reference ${env:NAME} or ${file:/path}.'}>
              <Textarea className="code" rows={4} value={raw} onChange={(e) => setRaw(e.target.value)} placeholder={'bedrock://eu-west-1?auth=irsa\nanthropic://api.anthropic.com?api_key=${env:ANTHROPIC_API_KEY}\nProvider=AzureOpenAI;Endpoint=https://x.openai.azure.com;Auth=Entra;TenantId=…;ClientId=…;ClientSecret=…'} />
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
      {existing && <p className="muted" style={{ marginTop: 14, fontSize: 12.5 }}>Last changed {fmtAgo(existing.updated_at)}.</p>}
    </Drawer>
  )
}

function safeJSON(s: string) {
  try { return JSON.parse(s || '{}') } catch { return {} }
}
