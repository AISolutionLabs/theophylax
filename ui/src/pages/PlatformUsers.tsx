import { useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { Plus } from 'lucide-react'
import { get, patch, post, type User } from '../lib/api'
import { useApp } from '../lib/app'
import { fmtAgo } from '../lib/format'
import { Badge, Button, Drawer, ErrorNote, Field, Input, Loading, PageHead, Toggle, useAction } from '../components/ui'

export default function PlatformUsers() {
  const { me } = useApp()
  const users = useQuery({ queryKey: ['all-users'], queryFn: () => get<User[]>('/api/users') })
  const [q, setQ] = useState('')
  const [open, setOpen] = useState<User | 'new' | null>(null)
  const list = (users.data ?? []).filter((u) => !q || (u.email + u.name).toLowerCase().includes(q.toLowerCase()))
  return (
    <>
      <PageHead title="All users" lede="Every account across organizations. Platform administrators manage providers, the model catalog and organizations."
        actions={<Button variant="primary" onClick={() => setOpen('new')}><Plus size={15} /> Add user</Button>} />
      <div className="row" style={{ marginBottom: 14 }}><Input placeholder="Search" value={q} onChange={(e) => setQ(e.target.value)} style={{ maxWidth: 320 }} /></div>
      <div className="panel">
        {users.isLoading ? <Loading /> : users.error ? <ErrorNote error={users.error} /> : (
          <table className="tbl">
            <thead><tr><th>Person</th><th>Role</th><th>Sign-in</th><th>Last sign-in</th><th>Status</th></tr></thead>
            <tbody>
              {list.map((u) => (
                <tr key={u.id} className={u.id === me.user.id ? '' : 'click'} onClick={() => u.id !== me.user.id && setOpen(u)}>
                  <td><div className="cell-title">{u.name || u.email}</div><div className="cell-sub">{u.email}</div></td>
                  <td>{u.is_platform_admin ? <Badge tone="bronze">platform admin</Badge> : <span className="dim">user</span>}</td>
                  <td className="dim">{(u as any).idp_id ? 'SSO' : 'Password / invited'}</td>
                  <td className="dim">{fmtAgo(u.last_login_at)}</td>
                  <td>{u.status === 'active' ? <Badge tone="ok">active</Badge> : <Badge tone="bad">suspended</Badge>}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
      {open && <UserDrawer user={open === 'new' ? undefined : open} onClose={() => setOpen(null)} />}
    </>
  )
}

function UserDrawer({ user, onClose }: { user?: User; onClose: () => void }) {
  const [email, setEmail] = useState(user?.email ?? '')
  const [name, setName] = useState(user?.name ?? '')
  const [password, setPassword] = useState('')
  const [admin, setAdmin] = useState(user?.is_platform_admin ?? false)
  const [active, setActive] = useState((user?.status ?? 'active') === 'active')
  const save = useAction(() => user
    ? patch(`/api/users/${user.id}`, { name, is_platform_admin: admin, status: active ? 'active' : 'suspended', ...(password ? { password } : {}) })
    : post('/api/users', { email, name, password, is_platform_admin: admin }), { done: user ? 'User saved' : 'User added', invalidate: [['all-users']], onSuccess: onClose })
  return (
    <Drawer open onClose={onClose} title={user ? user.email : 'Add a user'} footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" busy={save.isPending} onClick={() => save.mutate(undefined)}>{user ? 'Save user' : 'Add user'}</Button></>}>
      {!user && <Field label="Email"><Input type="email" value={email} onChange={(e) => setEmail(e.target.value)} /></Field>}
      <Field label="Name"><Input value={name} onChange={(e) => setName(e.target.value)} /></Field>
      <Field label={user ? 'Reset password' : 'Password'} hint="Optional when the person signs in with SSO. At least 12 characters."><Input type="password" autoComplete="new-password" value={password} onChange={(e) => setPassword(e.target.value)} /></Field>
      <div className="stack" style={{ gap: 12 }}>
        <Toggle checked={admin} onChange={setAdmin} label="Platform administrator" />
        {user && <Toggle checked={active} onChange={setActive} label={active ? 'Active' : 'Suspended: sign-in and all keys rejected'} />}
      </div>
    </Drawer>
  )
}
