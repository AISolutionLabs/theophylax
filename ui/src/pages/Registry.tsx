import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { Plus, Upload } from 'lucide-react'
import { get, patch, post, type RegistryItem, type RegistryVersion } from '../lib/api'
import { useApp } from '../lib/app'
import { useRoutableModels, useTeams } from '../lib/queries'
import { fmtAgo, kindLabel } from '../lib/format'
import { Badge, Button, ChipsInput, Drawer, Empty, ErrorNote, Field, Input, Loading, PageHead, Select, Tabs, Textarea, Toggle, useAction } from '../components/ui'
import { ItemDrawer, kindIcon } from './Marketplace'

const statusTone: Record<string, any> = { approved: 'ok', pending_review: 'warn', rejected: 'bad', draft: undefined, deprecated: undefined }
const statusLabel: Record<string, string> = { approved: 'live', pending_review: 'in review', rejected: 'changes requested', draft: 'draft', deprecated: 'deprecated' }

const SKILL_TEMPLATE = `---
name: my-skill
description: One sentence on when Claude should use this skill.
---

# My skill

## When to use
- Describe the situations this applies to.

## How to do it
1. Step one.
2. Step two.
`

export default function Registry() {
  const { org, isAdmin } = useApp()
  const [sp, setSp] = useSearchParams()
  const [tab, setTab] = useState<'mine' | 'review'>(sp.get('tab') === 'review' ? 'review' : 'mine')
  const [creating, setCreating] = useState(sp.get('new') === '1')
  const [open, setOpen] = useState<RegistryItem | null>(null)
  const [reviewing, setReviewing] = useState<RegistryItem | null>(null)
  useEffect(() => { if (sp.get('new') || sp.get('tab')) { sp.delete('new'); sp.delete('tab'); setSp(sp, { replace: true }) } }, [sp, setSp])
  const items = useQuery({ queryKey: ['registry', org.id, tab], queryFn: () => get<RegistryItem[]>(`/api/orgs/${org.id}/registry${tab === 'review' ? '?view=review' : ''}`) })
  return (
    <>
      <PageHead title="Publishing" lede="Package what your team has learned as skills, MCP servers, agent templates and prompts. Submissions are reviewed by an org admin before they reach the marketplace; versions are immutable and content-addressed."
        actions={<Button variant="primary" onClick={() => setCreating(true)}><Plus size={15} /> New item</Button>} />
      {isAdmin && <Tabs value={tab} onChange={setTab} tabs={[{ value: 'mine', label: 'All items' }, { value: 'review', label: 'Review queue' }]} />}
      <div className="panel">
        {items.isLoading ? <Loading /> : items.error ? <ErrorNote error={items.error} /> : !items.data?.length ? (
          tab === 'review' ? <Empty icon={<Upload size={20} />} title="Nothing waiting for review">Submissions appear here when someone asks for an item to go live.</Empty>
            : <Empty icon={<Upload size={20} />} title="Nothing published yet" action={<Button variant="primary" onClick={() => setCreating(true)}>New item</Button>}>Start with a skill: a SKILL.md that teaches Claude how your team does something.</Empty>
        ) : (
          <table className="tbl">
            <thead><tr><th>Item</th><th>Type</th><th>Owner</th><th>Latest</th><th>Status</th><th>Updated</th><th /></tr></thead>
            <tbody>
              {items.data.map((it) => (
                <tr key={it.id} className="click" onClick={() => setOpen(it)}>
                  <td><div className="cell-title">{it.name}</div><div className="cell-sub">{it.description}</div></td>
                  <td><span className="row" style={{ gap: 6 }}>{kindIcon(it.kind, 14)}{kindLabel[it.kind]}</span></td>
                  <td className="dim">{it.owner_team ?? it.creator_email}</td>
                  <td>v{it.latest_version}{it.approved_version && it.approved_version !== it.latest_version && <div className="cell-sub">live: v{it.approved_version}</div>}</td>
                  <td><Badge tone={statusTone[it.status]}>{statusLabel[it.status]}</Badge></td>
                  <td className="dim">{fmtAgo(it.updated_at)}</td>
                  <td className="tight" onClick={(e) => e.stopPropagation()}>{tab === 'review' && <Button size="sm" variant="primary" onClick={() => setReviewing(it)}>Review</Button>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {creating && <ItemEditor onClose={() => setCreating(false)} />}
      {open && <PublisherDrawer item={open} onClose={() => setOpen(null)} />}
      {reviewing && <ReviewDrawer item={reviewing} onClose={() => setReviewing(null)} />}
    </>
  )
}

function ContentEditor({ kind, value, onChange }: { kind: string; value: any; onChange: (v: any) => void }) {
  const models = useRoutableModels()
  const set = (k: string) => (v: any) => onChange({ ...value, [k]: v })
  switch (kind) {
    case 'skill':
      return (
        <>
          <Field label="SKILL.md" hint="Frontmatter name and description tell Claude when to use it."><Textarea className="code" rows={16} value={value.skill_md ?? ''} onChange={(e) => set('skill_md')(e.target.value)} /></Field>
          <Field label="Extra files (JSON, optional)" hint={'{"scripts/check.sh": "#!/bin/sh …"} — packaged alongside SKILL.md.'}><Textarea className="code" rows={4} value={value._files ?? (value.files ? JSON.stringify(value.files, null, 2) : '')} onChange={(e) => set('_files')(e.target.value)} /></Field>
        </>
      )
    case 'mcp_server':
      return (
        <>
          <div className="grid2">
            <Field label="Transport"><Select value={value.transport ?? 'http'} onChange={(e) => set('transport')(e.target.value)} options={[{ value: 'http', label: 'Streamable HTTP' }, { value: 'sse', label: 'SSE' }, { value: 'stdio', label: 'stdio (local)' }]} /></Field>
            {(value.transport ?? 'http') === 'stdio'
              ? <Field label="Command"><Input className="code" value={value.command ?? ''} onChange={(e) => set('command')(e.target.value)} placeholder="npx -y @org/mcp-server" /></Field>
              : <Field label="URL"><Input className="code" value={value.url ?? ''} onChange={(e) => set('url')(e.target.value)} placeholder="https://mcp.internal/jira" /></Field>}
          </div>
          {(value.transport ?? 'http') !== 'stdio' && <Field label="Authorization token (optional)" hint="Encrypted at rest. Injected by the gateway's MCP proxy and for agents; never shown to users."><Input type="password" value={value.authorization_token ?? ''} onChange={(e) => set('authorization_token')(e.target.value)} autoComplete="off" /></Field>}
          {(value.transport ?? 'http') === 'http' && (
            <div className="grid2">
              {(['allow', 'deny'] as const).map((k) => (
                <Field key={k} label={k === 'allow' ? 'Allowed tools (optional)' : 'Denied tools (optional)'} hint={k === 'allow' ? 'Comma-separated. When set, only these tools are offered. A trailing * matches a prefix.' : 'Comma-separated, e.g. delete_*, admin_reset. Hidden from tools/list and refused on call.'}>
                  <Input className="code" value={(value.tool_policy?.[k] ?? []).join(', ')}
                    onChange={(e) => {
                      const list = e.target.value.split(',').map((x: string) => x.trim()).filter(Boolean)
                      const tp = { ...(value.tool_policy ?? {}), [k]: list }
                      if (!list.length) delete tp[k]
                      set('tool_policy')(Object.keys(tp).length ? tp : undefined)
                    }} />
                </Field>
              ))}
            </div>
          )}
        </>
      )
    case 'agent':
      return (
        <>
          <Field label="Model"><Select value={value.model ?? ''} onChange={(e) => set('model')(e.target.value)} options={[{ value: '', label: 'Choose…' }, ...models.data.map((m) => ({ value: m.name, label: m.name }))]} /></Field>
          <Field label="System prompt"><Textarea rows={10} value={value.system_prompt ?? ''} onChange={(e) => set('system_prompt')(e.target.value)} /></Field>
        </>
      )
    case 'prompt':
      return <Field label="Template" hint="Use {{variable}} placeholders."><Textarea rows={10} value={value.template ?? ''} onChange={(e) => set('template')(e.target.value)} /></Field>
  }
  return <Field label="Content (JSON)"><Textarea className="code" rows={10} value={value._raw ?? JSON.stringify(value, null, 2)} onChange={(e) => set('_raw')(e.target.value)} /></Field>
}

function finalize(kind: string, c: any) {
  const out = { ...c }
  if (kind === 'skill' && out._files) { out.files = JSON.parse(out._files) }
  delete out._files
  if (out._raw) return JSON.parse(out._raw)
  return out
}

function ItemEditor({ onClose }: { onClose: () => void }) {
  const { org, can } = useApp()
  const teams = useTeams()
  const [kind, setKind] = useState('skill')
  const [f, setF] = useState({ name: '', description: '', tags: [] as string[], owner_team_id: '', visibility: 'org', version: '1.0.0', changelog: 'Initial version' })
  const [content, setContent] = useState<any>({ skill_md: SKILL_TEMPLATE })
  const [submit, setSubmit] = useState(true)
  const save = useAction(() => post(`/api/orgs/${org.id}/registry`, { ...f, kind, content: finalize(kind, content), submit }),
    { done: submit ? (can({ type: 'org', id: org.id }, 'admin') ? 'Published' : 'Submitted for review') : 'Saved as draft', invalidate: [['registry'], ['marketplace']], onSuccess: onClose })
  return (
    <Drawer open onClose={onClose} wide title="New marketplace item"
      footer={<><Toggle checked={submit} onChange={setSubmit} label={can({ type: 'org', id: org.id }, 'admin') ? 'Publish now (you are an approver)' : 'Submit for review'} /><span className="spacer" /><Button onClick={onClose}>Cancel</Button><Button variant="primary" disabled={!f.name} busy={save.isPending} onClick={() => save.mutate(undefined)}>{submit ? 'Publish' : 'Save draft'}</Button></>}>
      <div className="grid2">
        <Field label="Type"><Select value={kind} onChange={(e) => { setKind(e.target.value); setContent(e.target.value === 'skill' ? { skill_md: SKILL_TEMPLATE } : {}) }} options={['skill', 'mcp_server', 'agent', 'prompt'].map((k) => ({ value: k, label: kindLabel[k]! }))} /></Field>
        <Field label="Name"><Input value={f.name} onChange={(e) => setF({ ...f, name: e.target.value })} autoFocus /></Field>
      </div>
      <Field label="Description" hint="Shown on the marketplace card."><Input value={f.description} onChange={(e) => setF({ ...f, description: e.target.value })} /></Field>
      <div className="grid3">
        <Field label="Owning team"><Select value={f.owner_team_id} onChange={(e) => setF({ ...f, owner_team_id: e.target.value })} options={[{ value: '', label: 'Just me' }, ...(teams.data ?? []).filter((t) => can({ type: 'team', id: t.id }, 'member')).map((t) => ({ value: t.id, label: t.name }))]} /></Field>
        <Field label="Visible to"><Select value={f.visibility} onChange={(e) => setF({ ...f, visibility: e.target.value })} options={[{ value: 'org', label: 'Whole organization' }, { value: 'team', label: 'Owning team only' }]} /></Field>
        <Field label="Version"><Input value={f.version} onChange={(e) => setF({ ...f, version: e.target.value })} /></Field>
      </div>
      <Field label="Tags"><ChipsInput value={f.tags} onChange={(v) => setF({ ...f, tags: v })} placeholder="testing, terraform, security" /></Field>
      <ContentEditor kind={kind} value={content} onChange={setContent} />
    </Drawer>
  )
}

function PublisherDrawer({ item, onClose }: { item: RegistryItem; onClose: () => void }) {
  const d = useQuery({ queryKey: ['registry-item', item.id], queryFn: () => get<{ item: RegistryItem; versions: RegistryVersion[]; can_publish: boolean; can_review: boolean }>(`/api/registry/${item.id}`) })
  const [tab, setTab] = useState<'version' | 'view'>('view')
  const latest = d.data?.versions[0]
  const [content, setContent] = useState<any>(null)
  const [version, setVersion] = useState('')
  const [changelog, setChangelog] = useState('')
  const [submit, setSubmit] = useState(true)
  useEffect(() => {
    if (latest && content === null) {
      setContent({ ...latest.content })
      const [a, b, c] = latest.version.split('.').map(Number)
      setVersion(`${a}.${b}.${(c ?? 0) + 1}`)
    }
  }, [latest, content])
  const publish = useAction(() => post(`/api/registry/${item.id}/versions`, { version, changelog, content: finalize(item.kind, content), submit }), { done: `Published v${version}`, invalidate: [['registry'], ['registry-item'], ['marketplace']], onSuccess: onClose })
  const deprecate = useAction(() => patch(`/api/registry/${item.id}`, { deprecated: item.status !== 'deprecated' }), { done: item.status === 'deprecated' ? 'Restored' : 'Deprecated', invalidate: [['registry'], ['marketplace']], onSuccess: onClose })
  if (tab === 'view' && d.data && !d.data.can_publish) return <ItemDrawer item={item} onClose={onClose} />
  return (
    <Drawer open onClose={onClose} wide title={item.name} sub={<>{kindLabel[item.kind]} · <Badge tone={statusTone[item.status]}>{statusLabel[item.status]}</Badge>{item.review_note && <span> · Reviewer: “{item.review_note}”</span>}</>}
      footer={tab === 'version' ? <><Toggle checked={submit} onChange={setSubmit} label="Submit for review" /><span className="spacer" /><Button variant="primary" busy={publish.isPending} onClick={() => publish.mutate(undefined)}>Publish v{version}</Button></> : <><Button onClick={() => deprecate.mutate(undefined)}>{item.status === 'deprecated' ? 'Restore' : 'Deprecate'}</Button><span className="spacer" /><Button variant="primary" onClick={() => setTab('version')}>Publish a new version</Button></>}>
      <Tabs value={tab} onChange={setTab} tabs={[{ value: 'view', label: 'Overview' }, { value: 'version', label: 'New version' }]} />
      {d.isLoading ? <Loading /> : tab === 'view' ? (
        <table className="tbl panel"><thead><tr><th>Version</th><th>Changes</th><th>Published</th></tr></thead>
          <tbody>{d.data?.versions.map((v) => <tr key={v.id}><td>v{v.version} {v.version === item.approved_version && <Badge tone="ok">live</Badge>}</td><td>{v.changelog}</td><td className="dim">{fmtAgo(v.created_at)}</td></tr>)}</tbody></table>
      ) : content && (
        <>
          <div className="grid2">
            <Field label="Version" hint="Semantic version. Existing versions can't be changed."><Input value={version} onChange={(e) => setVersion(e.target.value)} /></Field>
            <Field label="What changed"><Input value={changelog} onChange={(e) => setChangelog(e.target.value)} /></Field>
          </div>
          <ContentEditor kind={item.kind} value={content} onChange={setContent} />
        </>
      )}
    </Drawer>
  )
}

function ReviewDrawer({ item, onClose }: { item: RegistryItem; onClose: () => void }) {
  const d = useQuery({ queryKey: ['registry-item', item.id], queryFn: () => get<{ versions: RegistryVersion[] }>(`/api/registry/${item.id}`) })
  const [note, setNote] = useState('')
  const decide = useAction((decision: string) => post(`/api/registry/${item.id}/review`, { decision, note }), { done: (r: any) => (r.status === 'approved' ? 'Approved and live' : 'Changes requested'), invalidate: [['registry'], ['marketplace'], ['review-count']], onSuccess: onClose })
  const v = d.data?.versions.find((x) => x.version === item.latest_version)
  return (
    <Drawer open onClose={onClose} wide title={`Review ${item.name} v${item.latest_version}`} sub={`Submitted by ${item.creator_email ?? item.publisher}`}
      footer={<><Button variant="danger" busy={decide.isPending} onClick={() => decide.mutate('reject')}>Request changes</Button><span className="spacer" /><Button variant="primary" busy={decide.isPending} onClick={() => decide.mutate('approve')}>Approve and publish</Button></>}>
      <p className="muted" style={{ marginBottom: 12 }}>Check for embedded secrets, unsafe commands and instructions that conflict with policy. The exact content below is what will be served; its digest is {v?.digest.slice(0, 23)}….</p>
      {d.isLoading ? <Loading /> : <div className="code-block" style={{ whiteSpace: 'pre-wrap', maxHeight: '50vh' }}>{v?.content.skill_md ?? JSON.stringify(v?.content, null, 2)}</div>}
      <Field label="Note to the publisher"><Textarea rows={3} value={note} onChange={(e) => setNote(e.target.value)} /></Field>
    </Drawer>
  )
}
