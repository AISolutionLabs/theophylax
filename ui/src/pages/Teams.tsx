import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Boxes, Plus, Trash2 } from 'lucide-react'
import { del, get, patch, post, type Membership, type Project, type ScopeSettings, type Team } from '../lib/api'
import { useApp } from '../lib/app'
import { useProjects, useRoutableModels, useTeams } from '../lib/queries'
import { Badge, Button, ChipsInput, Confirm, Drawer, Empty, ErrorNote, Field, Input, Loading, PageHead, RoleBadge, Select, Tabs, Textarea, useAction } from '../components/ui'

export default function Teams() {
  const { org, can } = useApp()
  const teams = useTeams()
  const projects = useProjects()
  const [team, setTeam] = useState<Team | 'new' | null>(null)
  const [project, setProject] = useState<Project | { team: Team } | null>(null)
  const orgAdmin = can({ type: 'org', id: org.id }, 'admin')
  return (
    <>
      <PageHead title="Teams & projects" lede="Teams own projects; projects own keys, agents' traffic and spend. Owners and admins at the organization or team level manage everything beneath them."
        actions={orgAdmin && <Button variant="primary" onClick={() => setTeam('new')}><Plus size={15} /> New team</Button>} />
      {teams.isLoading ? <Loading /> : teams.error ? <ErrorNote error={teams.error} /> : !teams.data?.length ? (
        <div className="panel"><Empty icon={<Boxes size={20} />} title="No teams yet" action={orgAdmin ? <Button variant="primary" onClick={() => setTeam('new')}>New team</Button> : undefined}>Create a team for each group that builds with AI, then a project per application or environment.</Empty></div>
      ) : (
        <div className="stack">
          {teams.data.map((t) => {
            const ps = (projects.data ?? []).filter((p) => p.team_id === t.id)
            const tAdmin = can({ type: 'team', id: t.id }, 'admin')
            return (
              <section className="panel" key={t.id}>
                <div className="panel-head">
                  <div><h3>{t.name}</h3><div className="sub">{t.description || `${t.member_count} member${t.member_count === 1 ? '' : 's'} · ${ps.length} project${ps.length === 1 ? '' : 's'}`}</div></div>
                  <div className="end">
                    <Button size="sm" onClick={() => setTeam(t)}>{tAdmin ? 'Manage team' : 'View team'}</Button>
                    {tAdmin && <Button size="sm" variant="primary" onClick={() => setProject({ team: t })}><Plus size={13} /> Project</Button>}
                  </div>
                </div>
                {ps.length ? (
                  <table className="tbl">
                    <tbody>
                      {ps.map((p) => (
                        <tr key={p.id} className="click" onClick={() => setProject(p)}>
                          <td><div className="cell-title">{p.name}</div><div className="cell-sub">{p.description}</div></td>
                          <td className="dim">{p.key_count} active key{p.key_count === 1 ? '' : 's'}</td>
                          <td className="dim">{settingsSummary(p.settings)}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : <div className="panel-body muted">No projects yet.</div>}
              </section>
            )
          })}
        </div>
      )}
      {team && <TeamDrawer team={team === 'new' ? undefined : team} onClose={() => setTeam(null)} />}
      {project && <ProjectDrawer project={'team' in project ? undefined : project} team={'team' in project ? project.team : undefined} onClose={() => setProject(null)} />}
    </>
  )
}

function settingsSummary(s: ScopeSettings) {
  const parts = []
  if (s?.allowed_models?.length) parts.push(`${s.allowed_models.length} allowed model(s)`)
  if (s?.rpm) parts.push(`${s.rpm} rpm`)
  if (s?.tpm) parts.push(`${s.tpm.toLocaleString()} tpm`)
  if (s?.log_payloads) parts.push('payload logging on')
  return parts.join(' · ') || 'Inherits limits'
}

function SettingsFields({ value, onChange, disabled }: { value: ScopeSettings; onChange: (s: ScopeSettings) => void; disabled?: boolean }) {
  const models = useRoutableModels()
  return (
    <fieldset disabled={disabled} style={{ border: 0, padding: 0, margin: 0 }}>
      <Field label="Allowed models" hint="Empty means everything allowed above. Narrows, never widens, what the parent allows."><ChipsInput value={value.allowed_models ?? []} onChange={(v) => onChange({ ...value, allowed_models: v })} placeholder="Any model" suggestions={models.data.map((m) => m.name)} /></Field>
      <div className="grid2">
        <Field label="Requests per minute"><Input type="number" min={0} value={value.rpm ?? ''} onChange={(e) => onChange({ ...value, rpm: Number(e.target.value) || 0 })} placeholder="No limit" /></Field>
        <Field label="Tokens per minute"><Input type="number" min={0} value={value.tpm ?? ''} onChange={(e) => onChange({ ...value, tpm: Number(e.target.value) || 0 })} placeholder="No limit" /></Field>
      </div>
      <Field label="Payload logging" hint="Stores prompts and responses (after redaction) for debugging. Leave inherited unless you need it.">
        <Select value={value.log_payloads == null ? '' : value.log_payloads ? 'on' : 'off'} onChange={(e) => onChange({ ...value, log_payloads: e.target.value === '' ? null : e.target.value === 'on' })}
          options={[{ value: '', label: 'Inherit' }, { value: 'on', label: 'On' }, { value: 'off', label: 'Off' }]} />
      </Field>
    </fieldset>
  )
}

export function Members({ scopeType, scopeId, canManage, canOwn }: { scopeType: 'org' | 'team' | 'project'; scopeId: string; canManage: boolean; canOwn: boolean }) {
  const m = useQuery({ queryKey: ['members', scopeType, scopeId], queryFn: () => get<Membership[]>(`/api/members?scope_type=${scopeType}&scope_id=${scopeId}`) })
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('member')
  const add = useAction(() => post('/api/members', { email, role, scope_type: scopeType, scope_id: scopeId }), { done: `Added ${email}`, invalidate: [['members'], ['teams'], ['org-users']], onSuccess: () => setEmail('') })
  const upd = useAction((x: { id: string; role: string }) => patch(`/api/members/${x.id}`, { role: x.role }), { done: 'Role updated', invalidate: [['members']] })
  const rm = useAction((id: string) => del(`/api/members/${id}`), { done: 'Member removed', invalidate: [['members'], ['teams']] })
  const roles = [...(canOwn ? ['owner'] : []), 'admin', 'member', 'viewer']
  return (
    <div className="stack">
      {canManage && (
        <div className="row" style={{ alignItems: 'flex-end' }}>
          <div style={{ flex: 1 }}><Field label="Add by email" hint="People who haven't signed in yet are pre-provisioned and join on first SSO login."><Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} placeholder="name@company.com" /></Field></div>
          <div style={{ width: 130 }}><Field label="Role"><Select value={role} onChange={(e) => setRole(e.target.value)} options={roles.map((r) => ({ value: r, label: r }))} /></Field></div>
          <div style={{ marginBottom: 16 }}><Button variant="primary" disabled={!email.includes('@')} busy={add.isPending} onClick={() => add.mutate(undefined)}>Add</Button></div>
        </div>
      )}
      {m.isLoading ? <Loading /> : (
        <table className="tbl panel">
          <thead><tr><th>Person</th><th>Role</th><th>Source</th><th /></tr></thead>
          <tbody>
            {(m.data ?? []).map((x) => (
              <tr key={x.id}>
                <td><div className="cell-title">{x.name || x.email}</div><div className="cell-sub">{x.email}</div></td>
                <td>{canManage && (x.role !== 'owner' || canOwn) ? <Select value={x.role} onChange={(e) => upd.mutate({ id: x.id, role: e.target.value })} options={roles.map((r) => ({ value: r, label: r }))} style={{ width: 120 }} /> : <RoleBadge role={x.role} />}</td>
                <td className="dim">{x.source === 'idp' ? 'SSO group' : x.source === 'scim' ? 'SCIM' : 'Added manually'}</td>
                <td className="tight">{canManage && (x.role !== 'owner' || canOwn) && <Button size="sm" variant="ghost" onClick={() => rm.mutate(x.id)} aria-label={`Remove ${x.email}`}><Trash2 size={14} /></Button>}</td>
              </tr>
            ))}
            {!m.data?.length && <tr><td colSpan={4} className="muted">No one is assigned directly here. Org-level owners and admins still have access.</td></tr>}
          </tbody>
        </table>
      )}
    </div>
  )
}

function TeamDrawer({ team, onClose }: { team?: Team; onClose: () => void }) {
  const { org, can } = useApp()
  const [tab, setTab] = useState<'members' | 'settings'>(team ? 'members' : 'settings')
  const [name, setName] = useState(team?.name ?? '')
  const [desc, setDesc] = useState(team?.description ?? '')
  const [settings, setSettings] = useState<ScopeSettings>(team?.settings ?? {})
  const [confirm, setConfirm] = useState(false)
  const admin = team ? can({ type: 'team', id: team.id }, 'admin') : true
  const owner = team ? can({ type: 'team', id: team.id }, 'owner') : true
  const save = useAction(() => team ? patch(`/api/teams/${team.id}`, { name, description: desc, settings }) : post(`/api/orgs/${org.id}/teams`, { name, description: desc, settings }),
    { done: team ? 'Team saved' : 'Team created', invalidate: [['teams']], onSuccess: () => !team && onClose() })
  const remove = useAction(() => del(`/api/teams/${team!.id}`), { done: 'Team deleted', invalidate: [['teams'], ['projects']], onSuccess: onClose })
  return (
    <Drawer open onClose={onClose} wide title={team ? team.name : 'New team'}
      footer={admin ? <>{team && owner && <Button variant="danger" onClick={() => setConfirm(true)}><Trash2 size={14} /> Delete team</Button>}<span className="spacer" />{tab === 'settings' && <Button variant="primary" disabled={!name} busy={save.isPending} onClick={() => save.mutate(undefined)}>{team ? 'Save team' : 'Create team'}</Button>}</> : undefined}>
      {team && <Tabs value={tab} onChange={setTab} tabs={[{ value: 'members', label: 'Members & owners' }, { value: 'settings', label: 'Settings' }]} />}
      {tab === 'members' && team && <Members scopeType="team" scopeId={team.id} canManage={admin} canOwn={owner} />}
      {tab === 'settings' && (
        <fieldset disabled={!admin} style={{ border: 0, padding: 0, margin: 0 }}>
          <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} autoFocus={!team} /></Field>
          <Field label="Description"><Textarea rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} /></Field>
          <SettingsFields value={settings} onChange={setSettings} />
        </fieldset>
      )}
      <Confirm open={confirm} danger title={`Delete ${team?.name}?`} body="All of its projects, keys and agents are deleted. Request logs and receipts are kept." confirmLabel="Delete team" onClose={() => setConfirm(false)} onConfirm={() => remove.mutate(undefined)} busy={remove.isPending} />
    </Drawer>
  )
}

function ProjectDrawer({ project, team, onClose }: { project?: Project; team?: Team; onClose: () => void }) {
  const { can } = useApp()
  const [tab, setTab] = useState<'members' | 'settings'>(project ? 'members' : 'settings')
  const [name, setName] = useState(project?.name ?? '')
  const [desc, setDesc] = useState(project?.description ?? '')
  const [settings, setSettings] = useState<ScopeSettings>(project?.settings ?? {})
  const [confirm, setConfirm] = useState(false)
  const admin = project ? can({ type: 'project', id: project.id, teamId: project.team_id }, 'admin') : true
  const save = useAction(() => project ? patch(`/api/projects/${project.id}`, { name, description: desc, settings }) : post(`/api/teams/${team!.id}/projects`, { name, description: desc, settings }),
    { done: project ? 'Project saved' : 'Project created', invalidate: [['projects'], ['teams']], onSuccess: () => !project && onClose() })
  const remove = useAction(() => del(`/api/projects/${project!.id}`), { done: 'Project deleted', invalidate: [['projects']], onSuccess: onClose })
  return (
    <Drawer open onClose={onClose} wide title={project ? project.name : `New project in ${team?.name}`} sub={project ? `Team: ${project.team_name}` : undefined}
      footer={admin ? <>{project && <Button variant="danger" onClick={() => setConfirm(true)}><Trash2 size={14} /> Delete project</Button>}<span className="spacer" />{tab === 'settings' && <Button variant="primary" disabled={!name} busy={save.isPending} onClick={() => save.mutate(undefined)}>{project ? 'Save project' : 'Create project'}</Button>}</> : undefined}>
      {project && <Tabs value={tab} onChange={setTab} tabs={[{ value: 'members', label: 'Members' }, { value: 'settings', label: 'Settings' }]} />}
      {tab === 'members' && project && <Members scopeType="project" scopeId={project.id} canManage={admin} canOwn={false} />}
      {tab === 'settings' && (
        <fieldset disabled={!admin} style={{ border: 0, padding: 0, margin: 0 }}>
          <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} autoFocus={!project} /></Field>
          <Field label="Description"><Textarea rows={2} value={desc} onChange={(e) => setDesc(e.target.value)} /></Field>
          <SettingsFields value={settings} onChange={setSettings} />
        </fieldset>
      )}
      {project && <p className="muted" style={{ marginTop: 10, fontSize: 12.5 }}>Project ID <span className="mono">{project.id}</span> — send it as X-Theophylax-Project when calling with an SSO token. <Badge>{project.slug}</Badge></p>}
      <Confirm open={confirm} danger title={`Delete ${project?.name}?`} body="Its keys stop working immediately. Request logs and receipts are kept." confirmLabel="Delete project" onClose={() => setConfirm(false)} onConfirm={() => remove.mutate(undefined)} busy={remove.isPending} />
    </Drawer>
  )
}
