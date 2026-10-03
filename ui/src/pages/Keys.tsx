import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { KeyRound, Plus, RotateCw } from 'lucide-react'
import { get, patch, post, qs, type ApiKey } from '../lib/api'
import { useApp } from '../lib/app'
import { useAgents, useProjects, useRoutableModels } from '../lib/queries'
import { fmtAgo, fmtTime } from '../lib/format'
import { Badge, Button, ChipsInput, CodeBlock, Confirm, CopyButton, Drawer, Empty, ErrorNote, Field, Input, Loading, PageHead, Segmented, Select, Tabs, useAction } from '../components/ui'

export default function Keys() {
  const { org, isAdmin } = useApp()
  const [sp, setSp] = useSearchParams()
  const [scope, setScope] = useState<'mine' | 'all'>('mine')
  const [showRevoked, setShowRevoked] = useState(false)
  const [creating, setCreating] = useState(sp.get('new') === '1')
  const [editing, setEditing] = useState<ApiKey | null>(null)
  const [secret, setSecret] = useState<{ key: ApiKey; secret: string } | null>(null)
  const [confirm, setConfirm] = useState<{ kind: 'revoke' | 'rotate'; key: ApiKey } | null>(null)
  const keys = useQuery({ queryKey: ['keys', org.id, scope, showRevoked], queryFn: () => get<ApiKey[]>(`/api/orgs/${org.id}/keys${qs({ mine: scope === 'mine', include_revoked: showRevoked })}`) })

  useEffect(() => { if (sp.get('new')) { sp.delete('new'); setSp(sp, { replace: true }) } }, [sp, setSp])
  const revoke = useAction((k: ApiKey) => post(`/api/keys/${k.id}/revoke`), { done: 'Key revoked', invalidate: [['keys']], onSuccess: () => setConfirm(null) })
  const rotate = useAction((k: ApiKey) => post<{ key: ApiKey; secret: string }>(`/api/keys/${k.id}/rotate`), { done: 'Key rotated', invalidate: [['keys']], onSuccess: (r) => { setConfirm(null); setSecret(r) } })
  const list = (keys.data ?? []).filter((k) => k.kind === 'gateway')

  return (
    <>
      <PageHead title="API keys" lede="Gateway keys authenticate apps, CI jobs and Claude Code. Each key belongs to a project, so usage, budgets and guardrails follow it."
        actions={<Button variant="primary" onClick={() => setCreating(true)}><Plus size={15} /> Create key</Button>} />
      <div className="row wrap" style={{ marginBottom: 14 }}>
        {isAdmin && <Segmented value={scope} onChange={setScope} options={[{ value: 'mine', label: 'My keys' }, { value: 'all', label: 'All keys I can manage' }]} />}
        <span className="spacer" />
        <label className="row muted" style={{ gap: 6, fontSize: 13 }}><input type="checkbox" checked={showRevoked} onChange={(e) => setShowRevoked(e.target.checked)} /> Show revoked</label>
      </div>
      <div className="panel">
        {keys.isLoading ? <Loading /> : keys.error ? <ErrorNote error={keys.error} /> : !list.length ? (
          <Empty icon={<KeyRound size={20} />} title="No keys yet" action={<Button variant="primary" onClick={() => setCreating(true)}><Plus size={15} /> Create key</Button>}>Create a key for a project, then use it as ANTHROPIC_AUTH_TOKEN or an OpenAI API key.</Empty>
        ) : (
          <div className="table-wrap">
            <table className="tbl">
              <thead><tr><th>Name</th><th>Project</th><th>Owner</th><th>Limits</th><th>Last used</th><th>Expires</th><th /></tr></thead>
              <tbody>
                {list.map((k) => (
                  <tr key={k.id} className="click" onClick={() => !k.revoked_at && setEditing(k)}>
                    <td><div className="cell-title">{k.name} {k.revoked_at && <Badge tone="bad">revoked</Badge>} {k.agent_name && <Badge tone="accent">agent: {k.agent_name}</Badge>}</div><div className="cell-sub mono">{k.prefix}</div></td>
                    <td>{k.team_name} / {k.project_name}</td>
                    <td className="dim">{k.owner_email ?? '—'}</td>
                    <td className="dim">{[k.rpm ? `${k.rpm} rpm` : '', k.tpm ? `${k.tpm.toLocaleString()} tpm` : '', k.allowed_models.length ? `${k.allowed_models.length} model${k.allowed_models.length > 1 ? 's' : ''}` : ''].filter(Boolean).join(', ') || 'Inherited'}</td>
                    <td className="dim">{fmtAgo(k.last_used_at)}</td>
                    <td className="dim">{k.expires_at ? fmtTime(k.expires_at) : 'Never'}</td>
                    <td className="tight" onClick={(e) => e.stopPropagation()}>
                      {!k.revoked_at && <div className="row" style={{ gap: 6 }}>
                        <Button size="sm" onClick={() => setConfirm({ kind: 'rotate', key: k })}><RotateCw size={13} /> Rotate</Button>
                        <Button size="sm" variant="danger" onClick={() => setConfirm({ kind: 'revoke', key: k })}>Revoke</Button>
                      </div>}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
      {creating && <KeyForm onClose={() => setCreating(false)} onCreated={(r) => { setCreating(false); setSecret(r) }} />}
      {editing && <KeyForm existing={editing} onClose={() => setEditing(null)} onCreated={() => setEditing(null)} />}
      {secret && <SecretReveal data={secret} onClose={() => setSecret(null)} />}
      <Confirm open={!!confirm} onClose={() => setConfirm(null)} danger={confirm?.kind === 'revoke'}
        title={confirm?.kind === 'revoke' ? `Revoke ${confirm.key.name}?` : `Rotate ${confirm?.key.name}?`}
        body={confirm?.kind === 'revoke' ? 'Requests using this key will be rejected within 30 seconds. This cannot be undone.' : 'A new secret is issued and the current one stops working within 30 seconds. Update every place that uses it.'}
        confirmLabel={confirm?.kind === 'revoke' ? 'Revoke key' : 'Rotate key'} busy={revoke.isPending || rotate.isPending}
        onConfirm={() => confirm && (confirm.kind === 'revoke' ? revoke.mutate(confirm.key) : rotate.mutate(confirm.key))} />
    </>
  )
}

function KeyForm({ existing, onClose, onCreated }: { existing?: ApiKey; onClose: () => void; onCreated: (r: { key: ApiKey; secret: string }) => void }) {
  const { can } = useApp()
  const projects = useProjects()
  const models = useRoutableModels()
  const agents = useAgents()
  const usable = (projects.data ?? []).filter((p) => can({ type: 'project', id: p.id, teamId: p.team_id }, 'member'))
  const [name, setName] = useState(existing?.name ?? '')
  const [project, setProject] = useState(existing?.project_id ?? '')
  const [allowed, setAllowed] = useState<string[]>(existing?.allowed_models ?? [])
  const [rpm, setRpm] = useState(String(existing?.rpm || ''))
  const [tpm, setTpm] = useState(String(existing?.tpm || ''))
  const [expires, setExpires] = useState(existing ? '' : '90')
  const [agent, setAgent] = useState(existing?.agent_id ?? '')
  useEffect(() => { if (!project && usable[0]) setProject(usable[0].id) }, [usable, project])
  const save = useAction(() => {
    const body: any = { name, allowed_models: allowed, rpm: Number(rpm) || 0, tpm: Number(tpm) || 0 }
    if (expires !== '') body.expires_in_days = Number(expires)
    return existing ? patch(`/api/keys/${existing.id}`, body) : post('/api/keys', { ...body, kind: 'gateway', project_id: project, agent_id: agent || undefined })
  }, { done: existing ? 'Key updated' : 'Key created', invalidate: [['keys']], onSuccess: (r: any) => onCreated(r) })
  return (
    <Drawer open onClose={onClose} title={existing ? `Edit ${existing.name}` : 'Create a gateway key'} sub={existing ? existing.prefix : 'The secret is shown once, right after you create the key.'}
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" busy={save.isPending} disabled={!name || !project} onClick={() => save.mutate(undefined)}>{existing ? 'Save changes' : 'Create key'}</Button></>}>
      <Field label="Name" hint="Say where it's used, e.g. “claude-code laptop” or “billing-service prod”."><Input value={name} onChange={(e) => setName(e.target.value)} autoFocus /></Field>
      {!existing && (
        <Field label="Project">
          <Select value={project} onChange={(e) => setProject(e.target.value)} options={usable.map((p) => ({ value: p.id, label: `${p.team_name} / ${p.name}` }))} />
        </Field>
      )}
      <Field label="Allowed models" hint="Leave empty to allow everything the project allows. Wildcards such as claude-* work.">
        <ChipsInput value={allowed} onChange={setAllowed} placeholder="Any model" suggestions={models.data.map((m) => m.name)} />
      </Field>
      <div className="grid2">
        <Field label="Requests per minute" hint="0 or empty = no key-level limit"><Input type="number" min={0} value={rpm} onChange={(e) => setRpm(e.target.value)} /></Field>
        <Field label="Tokens per minute"><Input type="number" min={0} value={tpm} onChange={(e) => setTpm(e.target.value)} /></Field>
      </div>
      <Field label="Expiry" hint={existing?.expires_at ? `Currently expires ${fmtTime(existing.expires_at)}.` : undefined}>
        <Select value={expires} onChange={(e) => setExpires(e.target.value)} options={[...(existing ? [{ value: '', label: 'Keep current expiry' }] : []), { value: '30', label: '30 days' }, { value: '90', label: '90 days' }, { value: '365', label: '1 year' }, { value: '0', label: 'Never expires' }]} />
      </Field>
      {!existing && (agents.data ?? []).length > 0 && (
        <Field label="Bind to an agent" hint="The key can only call this agent. Useful for giving a service its own identity.">
          <Select value={agent} onChange={(e) => setAgent(e.target.value)} options={[{ value: '', label: 'Not bound' }, ...(agents.data ?? []).map((a) => ({ value: a.id, label: a.name }))]} />
        </Field>
      )}
    </Drawer>
  )
}

function SecretReveal({ data, onClose }: { data: { key: ApiKey; secret: string }; onClose: () => void }) {
  const [tab, setTab] = useState<'claude' | 'curl' | 'python' | 'openai'>('claude')
  const base = window.location.origin
  const s = data.secret
  const snippets = {
    claude: `# Claude Code: add to your shell profile\nexport ANTHROPIC_BASE_URL=${base}\nexport ANTHROPIC_AUTH_TOKEN=${s}\n\n# Optional: sync your team's approved skills\ncurl -s -H "x-api-key: $ANTHROPIC_AUTH_TOKEN" ${base}/v1/skills`,
    curl: `curl ${base}/v1/messages \\\n  -H "x-api-key: ${s}" \\\n  -H "anthropic-version: 2023-06-01" \\\n  -H "content-type: application/json" \\\n  -d '{"model":"<model>","max_tokens":256,"messages":[{"role":"user","content":"Hello"}]}'`,
    python: `from anthropic import Anthropic\n\nclient = Anthropic(base_url="${base}", api_key="${s}")\nmsg = client.messages.create(model="<model>", max_tokens=256,\n    messages=[{"role": "user", "content": "Hello"}])\nprint(msg.content[0].text)`,
    openai: `from openai import OpenAI\n\nclient = OpenAI(base_url="${base}/v1", api_key="${s}")\nr = client.chat.completions.create(model="<model>",\n    messages=[{"role": "user", "content": "Hello"}])\nprint(r.choices[0].message.content)`,
  }
  return (
    <Drawer open onClose={onClose} title={`${data.key.name} is ready`} sub="Copy the secret now. For your security it won't be shown again." footer={<Button variant="primary" onClick={onClose}>I've stored the secret</Button>}>
      <div className="secret-box" style={{ marginBottom: 22 }}><code>{s}</code><CopyButton text={s} /></div>
      <h3 style={{ marginBottom: 10 }}>Use it</h3>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'claude', label: 'Claude Code' }, { value: 'curl', label: 'curl' }, { value: 'python', label: 'Anthropic SDK' }, { value: 'openai', label: 'OpenAI SDK' }]} />
      <CodeBlock code={snippets[tab]} />
    </Drawer>
  )
}
