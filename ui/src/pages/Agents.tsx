import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { Bot, Plus } from 'lucide-react'
import { del, get, patch, post, type Agent } from '../lib/api'
import { useApp } from '../lib/app'
import { useAgents, useMarketplace, useRoutableModels, useTeams } from '../lib/queries'
import { fmtAgo } from '../lib/format'
import { Badge, Button, CodeBlock, Confirm, Drawer, Empty, ErrorNote, Field, Input, Loading, MultiPick, PageHead, Select, Textarea, Toggle, useAction } from '../components/ui'

export default function Agents() {
  const { org, isAdmin } = useApp()
  const [sp, setSp] = useSearchParams()
  const agents = useAgents()
  const [editing, setEditing] = useState<Agent | 'new' | null>(sp.get('new') === '1' ? 'new' : null)
  const [viewing, setViewing] = useState<Agent | null>(null)
  useEffect(() => { if (sp.get('new')) { sp.delete('new'); setSp(sp, { replace: true }) } }, [sp, setSp])

  return (
    <>
      <PageHead title="Agents" lede="Managed agents bundle a model, a system prompt, approved skills and MCP servers behind one endpoint. Change the definition here and every caller gets it, with no client redeploys."
        actions={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={15} /> New agent</Button>} />
      <div className="panel">
        {agents.isLoading ? <Loading /> : agents.error ? <ErrorNote error={agents.error} /> : !agents.data?.length ? (
          <Empty icon={<Bot size={20} />} title="No agents yet" action={<Button variant="primary" onClick={() => setEditing('new')}><Plus size={15} /> New agent</Button>}>
            Create one from scratch, or install an agent template from the Marketplace.
          </Empty>
        ) : (
          <table className="tbl">
            <thead><tr><th>Agent</th><th>Team</th><th>Model</th><th>Skills</th><th className="num">Requests, 24h</th><th>Status</th></tr></thead>
            <tbody>
              {agents.data.map((a) => (
                <tr key={a.id} className="click" onClick={() => setViewing(a)}>
                  <td><div className="cell-title">{a.name}</div><div className="cell-sub">/v1/agents/{a.slug}/messages</div></td>
                  <td>{a.team_name}</td>
                  <td className="mono">{a.model}</td>
                  <td className="dim">{a.skills.length + a.mcp_servers.length || '—'}</td>
                  <td className="num">{a.requests_24h ?? 0}</td>
                  <td>{a.status === 'active' ? <Badge tone="ok">active</Badge> : <Badge>disabled</Badge>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {viewing && <AgentDetail agent={viewing} onClose={() => setViewing(null)} onEdit={() => { setEditing(viewing); setViewing(null) }} />}
      {editing && <AgentForm existing={editing === 'new' ? undefined : editing} onClose={() => setEditing(null)} />}
      <p className="muted" style={{ marginTop: 12, fontSize: 12.5 }}>{isAdmin ? 'Team admins can create and edit agents for their team.' : 'Team admins can create agents.'} Org: {org.name}.</p>
    </>
  )
}

function AgentDetail({ agent, onClose, onEdit }: { agent: Agent; onClose: () => void; onEdit: () => void }) {
  const d = useQuery({ queryKey: ['agent', agent.id], queryFn: () => get<{ agent: Agent; keys: any[]; endpoint: string; can_edit: boolean }>(`/api/agents/${agent.id}`) })
  const [confirm, setConfirm] = useState(false)
  const remove = useAction(() => del(`/api/agents/${agent.id}`), { done: 'Agent deleted', invalidate: [['agents']], onSuccess: onClose })
  const base = window.location.origin
  return (
    <Drawer open onClose={onClose} title={agent.name} sub={agent.description || `Owned by ${agent.owner_email ?? 'unknown'}`} wide
      footer={d.data?.can_edit ? <><Button variant="danger" onClick={() => setConfirm(true)}>Delete</Button><span className="spacer" /><Button variant="primary" onClick={onEdit}>Edit agent</Button></> : undefined}>
      <dl className="kv" style={{ marginBottom: 20 }}>
        <dt>Endpoint</dt><dd className="mono">{base}/v1/agents/{agent.slug}/messages</dd>
        <dt>Model</dt><dd className="mono">{agent.model}</dd>
        <dt>Team</dt><dd>{agent.team_name}</dd>
        <dt>Client system prompts</dt><dd>{agent.allow_client_system ? 'Appended after the agent prompt' : 'Ignored'}</dd>
        <dt>Dedicated keys</dt><dd>{d.data?.keys.length ?? '…'}</dd>
        <dt>Last changed</dt><dd>{fmtAgo(agent.updated_at)}</dd>
      </dl>
      <h3 style={{ marginBottom: 8 }}>Call it</h3>
      <CodeBlock code={`curl ${base}/v1/agents/${agent.slug}/messages \\\n  -H "x-api-key: $THEOPHYLAX_KEY" -H "content-type: application/json" \\\n  -d '{"max_tokens":1024,"messages":[{"role":"user","content":"Hello"}]}'`} />
      <h3 style={{ margin: '20px 0 8px' }}>System prompt</h3>
      <div className="code-block" style={{ whiteSpace: 'pre-wrap' }}>{agent.system_prompt || '(none)'}</div>
      <Confirm open={confirm} danger title={`Delete ${agent.name}?`} body="Callers of this endpoint will start receiving 404s and keys bound to it are revoked." confirmLabel="Delete agent" onClose={() => setConfirm(false)} onConfirm={() => remove.mutate(undefined)} busy={remove.isPending} />
    </Drawer>
  )
}

function AgentForm({ existing, onClose }: { existing?: Agent; onClose: () => void }) {
  const { can, org } = useApp()
  const teams = useTeams()
  const models = useRoutableModels()
  const skills = useMarketplace('skill')
  const mcps = useMarketplace('mcp_server')
  const adminTeams = (teams.data ?? []).filter((t) => can({ type: 'team', id: t.id }, 'admin'))
  const [f, setF] = useState({
    name: existing?.name ?? '', description: existing?.description ?? '', team_id: existing?.team_id ?? '', model: existing?.model ?? '',
    system_prompt: existing?.system_prompt ?? '', skills: existing?.skills ?? [], mcp_servers: existing?.mcp_servers ?? [],
    params: JSON.stringify(existing?.params ?? { max_tokens: 4096 }, null, 2), allow_client_system: existing?.allow_client_system ?? true, status: existing?.status ?? 'active',
  })
  const [paramErr, setParamErr] = useState('')
  useEffect(() => { if (!f.team_id && adminTeams[0]) setF((x) => ({ ...x, team_id: adminTeams[0]!.id })) }, [adminTeams, f.team_id])
  useEffect(() => { if (!f.model && models.data[0]) setF((x) => ({ ...x, model: models.data[0]!.name })) }, [models.data, f.model])
  const set = (k: keyof typeof f) => (v: any) => setF((x) => ({ ...x, [k]: v }))
  const save = useAction(() => {
    let params
    try { params = JSON.parse(f.params || '{}') } catch { setParamErr('Must be valid JSON'); return Promise.reject(new Error('Parameters must be valid JSON')) }
    const body = { ...f, params }
    return existing ? patch(`/api/agents/${existing.id}`, body) : post(`/api/orgs/${org.id}/agents`, body)
  }, { done: existing ? 'Agent saved' : 'Agent created', invalidate: [['agents'], ['agent']], onSuccess: onClose })
  if (!existing && !teams.isLoading && !adminTeams.length) {
    return <Drawer open onClose={onClose} title="New agent"><div className="callout">You need to be an admin of a team to create agents for it.</div></Drawer>
  }
  return (
    <Drawer open onClose={onClose} title={existing ? `Edit ${existing.name}` : 'New agent'} wide
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" busy={save.isPending} disabled={!f.name || !f.model} onClick={() => save.mutate(undefined)}>{existing ? 'Save agent' : 'Create agent'}</Button></>}>
      <div className="grid2">
        <Field label="Name"><Input value={f.name} onChange={(e) => set('name')(e.target.value)} autoFocus /></Field>
        {!existing ? <Field label="Team"><Select value={f.team_id} onChange={(e) => set('team_id')(e.target.value)} options={adminTeams.map((t) => ({ value: t.id, label: t.name }))} /></Field>
          : <Field label="Status"><Select value={f.status} onChange={(e) => set('status')(e.target.value)} options={[{ value: 'active', label: 'Active' }, { value: 'disabled', label: 'Disabled' }]} /></Field>}
      </div>
      <Field label="Description"><Input value={f.description} onChange={(e) => set('description')(e.target.value)} placeholder="What it does and who it's for" /></Field>
      <Field label="Model"><Select value={f.model} onChange={(e) => set('model')(e.target.value)} options={models.data.map((m) => ({ value: m.name, label: m.display_name || m.name }))} /></Field>
      <Field label="System prompt"><Textarea rows={8} value={f.system_prompt} onChange={(e) => set('system_prompt')(e.target.value)} /></Field>
      <Field label="Skills" hint="Approved skills from the marketplace. Their instructions are added to the system prompt.">
        <MultiPick value={f.skills} onChange={set('skills')} options={(skills.data ?? []).map((s) => ({ value: s.id, label: s.name, sub: `v${s.approved_version}` }))} empty="No approved skills yet. Publish one under Publishing." />
      </Field>
      <Field label="MCP servers" hint="Attached as remote MCP servers when the request is served by the Anthropic API.">
        <MultiPick value={f.mcp_servers} onChange={set('mcp_servers')} options={(mcps.data ?? []).map((s) => ({ value: s.id, label: s.name }))} empty="No approved MCP servers in the registry yet." />
      </Field>
      <Field label="Default parameters" hint="Applied when the caller doesn't set them, e.g. max_tokens or temperature." error={paramErr}>
        <Textarea className="code" rows={4} value={f.params} onChange={(e) => { set('params')(e.target.value); setParamErr('') }} />
      </Field>
      <Toggle checked={f.allow_client_system} onChange={set('allow_client_system')} label="Let callers add their own system prompt after the agent's" />
    </Drawer>
  )
}
