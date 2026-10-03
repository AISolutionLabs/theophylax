import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { get, type User } from '../lib/api'
import { useApp } from '../lib/app'
import { fmtAgo } from '../lib/format'
import { ErrorNote, Input, Loading, PageHead, Panel, RoleBadge } from '../components/ui'
import { Members } from './Teams'

interface OrgUser extends User { org_role: string; team_count: number }

export default function People() {
  const { org, can } = useApp()
  const [q, setQ] = useState('')
  const users = useQuery({ queryKey: ['org-users', org.id], queryFn: () => get<OrgUser[]>(`/api/orgs/${org.id}/users`), enabled: can({ type: 'org', id: org.id }, 'viewer') })
  const list = (users.data ?? []).filter((u) => !q || (u.email + u.name).toLowerCase().includes(q.toLowerCase()))
  return (
    <>
      <PageHead title="People" lede={`Who has access to ${org.name}, and at what level. Owners can manage other owners; admins manage everyone else.`} />
      <div className="stack">
        <Panel title="Organization roles" sub="Owners and admins here have that role in every team and project.">
          <Members scopeType="org" scopeId={org.id} canManage={can({ type: 'org', id: org.id }, 'admin')} canOwn={can({ type: 'org', id: org.id }, 'owner')} />
        </Panel>
        {users.data && (
          <Panel title="Everyone in the organization" sub="Including people who only belong to a team or project." pad={false} end={<Input placeholder="Search" value={q} onChange={(e) => setQ(e.target.value)} style={{ width: 220 }} />}>
            {users.isLoading ? <Loading /> : users.error ? <ErrorNote error={users.error} /> : (
              <table className="tbl">
                <thead><tr><th>Person</th><th>Org role</th><th className="num">Teams</th><th>Last sign-in</th><th>Status</th></tr></thead>
                <tbody>
                  {list.map((u) => (
                    <tr key={u.id}>
                      <td><div className="cell-title">{u.name || u.email}</div><div className="cell-sub">{u.email}</div></td>
                      <td>{u.org_role ? <RoleBadge role={u.org_role} /> : <span className="dim">team/project only</span>}</td>
                      <td className="num">{u.team_count}</td>
                      <td className="dim">{fmtAgo(u.last_login_at)}</td>
                      <td>{u.status === 'active' ? <span className="dim">active</span> : <span className="badge bad">suspended</span>}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Panel>
        )}
      </div>
    </>
  )
}
