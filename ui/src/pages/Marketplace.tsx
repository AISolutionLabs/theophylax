import { useState, type ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Link } from 'react-router-dom'
import { Bot, Download, FileCode2, Puzzle, Server, Star, Store, Wand2 } from 'lucide-react'
import { get, patch, post, type Installation, type RegistryItem, type RegistryVersion } from '../lib/api'
import { useApp } from '../lib/app'
import { useScopeOptions } from '../lib/queries'
import { fmtAgo, fmtCompact, kindLabel } from '../lib/format'
import { Markdown } from '../components/Markdown'
import { Badge, Button, CodeBlock, Drawer, Empty, ErrorNote, Field, Input, Loading, PageHead, Segmented, Select, Tabs, useAction } from '../components/ui'

export const kindIcon = (k: string, size = 16) => ({ skill: <Wand2 size={size} />, mcp_server: <Server size={size} />, agent: <Bot size={size} />, prompt: <FileCode2 size={size} />, plugin: <Puzzle size={size} /> } as Record<string, ReactNode>)[k] ?? <Puzzle size={size} />

export default function Marketplace() {
  const { org } = useApp()
  const [kind, setKind] = useState('')
  const [q, setQ] = useState('')
  const items = useQuery({ queryKey: ['marketplace', org.id, kind, q], queryFn: () => get<RegistryItem[]>(`/api/orgs/${org.id}/registry?view=marketplace${kind ? `&kind=${kind}` : ''}${q ? `&q=${encodeURIComponent(q)}` : ''}`) })
  const [open, setOpen] = useState<RegistryItem | null>(null)
  const featured = !q && !kind ? items.data?.[0] : undefined
  const rest = featured ? items.data!.slice(1) : items.data ?? []
  return (
    <>
      <PageHead title="Marketplace" lede={`Skills, MCP servers, agent templates and prompts reviewed and approved for ${org.name}. Install them for a team or project, or download skills straight into Claude Code.`}
        actions={<Link className="btn" to="/registry?new=1">Publish to the marketplace</Link>} />
      <div className="row wrap" style={{ marginBottom: 18 }}>
        <Segmented value={kind} onChange={setKind} options={[{ value: '', label: 'Everything' }, { value: 'skill', label: 'Skills' }, { value: 'mcp_server', label: 'MCP servers' }, { value: 'agent', label: 'Agent templates' }, { value: 'prompt', label: 'Prompts' }]} />
        <Input placeholder="Search by name, tag or description" value={q} onChange={(e) => setQ(e.target.value)} style={{ maxWidth: 340 }} />
      </div>
      {items.isLoading ? <Loading /> : items.error ? <ErrorNote error={items.error} /> : !items.data?.length ? (
        <div className="panel"><Empty icon={<Store size={20} />} title={q || kind ? 'Nothing matches' : 'The marketplace is empty'} action={<Link className="btn primary" to="/registry?new=1">Publish the first item</Link>}>Anyone in the organization can publish. Admins review submissions before they appear here.</Empty></div>
      ) : (
        <>
          {featured && <Featured item={featured} onOpen={() => setOpen(featured)} />}
          <div className="market">
            {rest.map((it) => (
              <button key={it.id} className="listing" onClick={() => setOpen(it)}>
                <div className="kind">{kindIcon(it.kind)}{kindLabel[it.kind]}<span className="spacer" />v{it.approved_version}</div>
                <h3>{it.name}</h3>
                <p>{it.description || 'No description.'}</p>
                <div className="row wrap" style={{ gap: 4 }}>{it.tags.slice(0, 4).map((t) => <Badge key={t}>{t}</Badge>)}</div>
                <div className="foot">
                  <span>{it.publisher}</span><span className="spacer" />
                  <span title="Installs">{fmtCompact(it.installs)} install{it.installs === 1 ? '' : 's'}</span>
                  {it.rating_count > 0 && <span className="row" style={{ gap: 3 }}><Star size={12} /> {(it.rating_sum / it.rating_count).toFixed(1)}</span>}
                </div>
              </button>
            ))}
          </div>
        </>
      )}
      {open && <ItemDrawer item={open} onClose={() => setOpen(null)} />}
    </>
  )
}

function Featured({ item, onOpen }: { item: RegistryItem; onOpen: () => void }) {
  const d = useQuery({ queryKey: ['registry-item', item.id], queryFn: () => get<{ versions: RegistryVersion[] }>(`/api/registry/${item.id}`) })
  const content = d.data?.versions.find((v) => v.version === item.approved_version)?.content
  const preview = content?.skill_md ?? content?.template ?? (content ? JSON.stringify(content, null, 2) : '')
  return (
    <section className="featured">
      <div className="left">
        <div className="row muted" style={{ gap: 8, fontSize: 12.5 }}>{kindIcon(item.kind)} Most installed {kindLabel[item.kind]?.toLowerCase()}</div>
        <h2 style={{ fontSize: 22, margin: '8px 0 6px' }}>{item.name}</h2>
        <p className="muted" style={{ maxWidth: '60ch' }}>{item.description}</p>
        <div className="row" style={{ marginTop: 16 }}><Button variant="primary" onClick={onOpen}>View and install</Button><span className="muted">{fmtCompact(item.installs)} install{item.installs === 1 ? '' : 's'} · by {item.publisher}</span></div>
      </div>
      <div className="preview" aria-hidden="true">{preview.slice(0, 700)}</div>
    </section>
  )
}

export function ItemDrawer({ item, onClose }: { item: RegistryItem; onClose: () => void }) {
  const d = useQuery({ queryKey: ['registry-item', item.id], queryFn: () => get<{ item: RegistryItem; versions: RegistryVersion[]; installations: Installation[]; my_rating?: number; can_publish: boolean; can_review: boolean }>(`/api/registry/${item.id}`) })
  const scopes = useScopeOptions()
  const { can } = useApp()
  const [tab, setTab] = useState<'about' | 'install' | 'versions'>('about')
  const [scope, setScope] = useState('')
  const [pin, setPin] = useState('')
  const install = useAction(() => { const [st, sid] = scope.split(':'); return post<any>(`/api/registry/${item.id}/install`, { scope_type: st, scope_id: sid, version: pin }) },
    { done: (r: any) => (r.agent_id ? 'Installed and created an agent' : 'Installed'), invalidate: [['registry-item'], ['marketplace'], ['agents']] })
  const repin = useAction((x: { id: string; version: string }) => patch<Installation>(`/api/installations/${x.id}`, { version: x.version }),
    { done: (r) => (r.pinned ? `Pinned to v${r.version}` : 'Now follows the approved version'), invalidate: [['registry-item']] })
  // Versions an installation may pin: the approved one and any approved before it.
  const approved = (d.data?.versions ?? []).filter((x) => x.approved_at || x.version === item.approved_version)
  const pinOptions = [{ value: '', label: `Follow approved (v${item.approved_version})` }, ...approved.map((x) => ({ value: x.version, label: `Pin v${x.version}` }))]
  const rate = useAction((n: number) => post(`/api/registry/${item.id}/rate`, { rating: n }), { done: 'Thanks for rating', invalidate: [['registry-item'], ['marketplace']] })
  const v = d.data?.versions.find((x) => x.version === (item.approved_version || item.latest_version))
  const c = v?.content ?? {}
  const base = window.location.origin
  const installable = scopes.filter((s) => { const [t, id] = s.value.split(':'); return can({ type: t as any, id: id! }, 'admin') })
  return (
    <Drawer open onClose={onClose} wide title={item.name} sub={<>{kindLabel[item.kind]} · v{item.approved_version || item.latest_version} · by {item.publisher}</>}
      footer={item.kind === 'skill' ? <a className="btn" href={`/api/registry/${item.id}/download`}><Download size={14} /> Download skill (.zip)</a> : undefined}>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'about', label: 'About' }, { value: 'install', label: `Install${d.data?.installations.length ? ` (${d.data.installations.length})` : ''}` }, { value: 'versions', label: 'Versions' }]} />
      {d.isLoading ? <Loading /> : d.error ? <ErrorNote error={d.error} /> : (
        <>
          {tab === 'about' && (
            <div className="stack">
              <p>{item.description}</p>
              {item.kind === 'skill' && c.skill_md && <div className="panel" style={{ padding: 18 }}><Markdown text={c.skill_md} /></div>}
              {item.kind === 'mcp_server' && <dl className="kv"><dt>Transport</dt><dd>{c.transport ?? (c.url ? 'http' : 'stdio')}</dd><dt>{c.url ? 'URL' : 'Command'}</dt><dd className="mono">{c.url ?? `${c.command} ${(c.args ?? []).join(' ')}`}</dd><dt>Credentials</dt><dd>{c.has_authorization_token ? 'Stored encrypted, injected by the gateway' : 'None'}</dd></dl>}
              {item.kind === 'agent' && <dl className="kv"><dt>Model</dt><dd className="mono">{c.model}</dd><dt>System prompt</dt><dd style={{ whiteSpace: 'pre-wrap' }}>{c.system_prompt}</dd></dl>}
              {item.kind === 'prompt' && <div className="code-block" style={{ whiteSpace: 'pre-wrap' }}>{c.template}</div>}
              <div className="row"><span className="muted">Your rating</span>{[1, 2, 3, 4, 5].map((n) => <button key={n} className="btn ghost icon sm" onClick={() => rate.mutate(n)} aria-label={`Rate ${n}`} style={{ color: (d.data?.my_rating ?? 0) >= n ? 'var(--bronze)' : 'var(--muted)' }}><Star size={16} fill={(d.data?.my_rating ?? 0) >= n ? 'currentColor' : 'none'} /></button>)}</div>
            </div>
          )}
          {tab === 'install' && (
            <div className="stack">
              {installable.length ? (
                <div className="row" style={{ alignItems: 'flex-end' }}>
                  <div style={{ flex: 1 }}><Field label="Install for" hint={item.kind === 'agent' ? 'Installing into a team or project creates a managed agent there.' : item.kind === 'mcp_server' ? 'Installed MCP servers are listed to clients in that scope via /v1/mcp/servers.' : 'Installed skills are listed to clients in that scope via /v1/skills.'}>
                    <Select value={scope} onChange={(e) => setScope(e.target.value)} options={[{ value: '', label: 'Choose where…' }, ...installable]} /></Field></div>
                  <div style={{ flex: 1 }}><Field label="Version" hint="Following picks up each newly approved version. Pin to hold this scope on one version.">
                    <Select value={pin} onChange={(e) => setPin(e.target.value)} options={pinOptions} /></Field></div>
                  <div style={{ marginBottom: 16 }}><Button variant="primary" disabled={!scope} busy={install.isPending} onClick={() => install.mutate(undefined)}>Install</Button></div>
                </div>
              ) : <div className="callout">Ask a team or project admin to install this for you.</div>}
              {item.kind === 'skill' && (
                <>
                  <h3>Sync into Claude Code</h3>
                  <p className="muted">Installed skills can be pulled into a developer's machine with their gateway key:</p>
                  <CodeBlock code={`mkdir -p ~/.claude/skills && cd ~/.claude/skills\ncurl -s -H "x-api-key: $ANTHROPIC_AUTH_TOKEN" ${base}/v1/skills \\\n  | jq -r '.data[].download_url' \\\n  | while read u; do curl -s -H "x-api-key: $ANTHROPIC_AUTH_TOKEN" "$u" -o s.zip && unzip -oq s.zip && rm s.zip; done`} />
                </>
              )}
              {item.kind === 'mcp_server' && (
                <>
                  <h3>Sync into Claude Code</h3>
                  <p className="muted">Writes the MCP servers installed for a developer's project, team and org into a project's <code>.mcp.json</code>. Stored tokens stay in the gateway; servers that need one are marked <code>requires_authorization</code>.</p>
                  <CodeBlock code={`curl -s -H "x-api-key: $ANTHROPIC_AUTH_TOKEN" ${base}/v1/mcp/servers | jq '.mcp_json' > .mcp.json`} />
                </>
              )}
              <h3>Installed in</h3>
              {d.data?.installations.length ? (
                <>
                  <p className="muted">The most specific installation wins: a project's overrides its team's, which overrides the org's. To stage a rollout, pin the org, then pin a team to the new version.</p>
                  <table className="tbl panel"><tbody>{d.data.installations.map((x) => (
                    <tr key={x.id}>
                      <td>{x.scope_name} <span className="dim">· {x.scope_type}</span></td>
                      <td>{x.pinned ? <Badge>pinned v{x.version}</Badge> : <span className="dim">follows approved (v{item.approved_version})</span>}</td>
                      <td>{can({ type: x.scope_type, id: x.scope_id }, 'admin') && <Select value={x.pinned ? x.version : ''} onChange={(e) => repin.mutate({ id: x.id, version: e.target.value })} options={pinOptions} aria-label={`Version for ${x.scope_name}`} />}</td>
                      <td className="dim">{fmtAgo(x.created_at)}</td>
                    </tr>
                  ))}</tbody></table>
                </>
              ) : <p className="muted">Not installed anywhere yet.</p>}
            </div>
          )}
          {tab === 'versions' && (
            <table className="tbl panel">
              <thead><tr><th>Version</th><th>Changes</th><th>Digest</th><th>Published</th></tr></thead>
              <tbody>{d.data?.versions.map((x) => <tr key={x.id}><td>v{x.version} {x.version === item.approved_version ? <Badge tone="ok">live</Badge> : x.approved_at ? <Badge>approved</Badge> : null}</td><td>{x.changelog || <span className="dim">—</span>}</td><td className="mono dim" title={x.digest}>{x.digest.slice(0, 19)}…</td><td className="dim">{fmtAgo(x.created_at)}</td></tr>)}</tbody>
            </table>
          )}
        </>
      )}
    </Drawer>
  )
}
