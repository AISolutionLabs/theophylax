import { useEffect, useMemo, useState, type ReactNode } from 'react'
import { NavLink, useLocation, useNavigate } from 'react-router-dom'
import { useQuery } from '@tanstack/react-query'
import {
  Activity, BadgeCheck, Bot, Boxes, Building2, ChevronsUpDown, CircleGauge, FlaskConical, GitBranch, KeyRound, LayoutGrid, LogOut,
  Menu, Network, PiggyBank, Plug, Receipt, ScrollText, Search, Server, Settings, ShieldCheck, Store, Upload, Users, Wallet,
} from 'lucide-react'
import { useApp } from '../lib/app'
import { get, post } from '../lib/api'
import { initials } from '../lib/format'
import { Logo } from './ui'
import { AppearanceMenu } from './Appearance'

interface NavDef { to: string; label: string; icon: ReactNode; show?: boolean; count?: number; keywords?: string }

export function Shell({ children }: { children: ReactNode }) {
  const { me, org, setOrg, isAdmin, isPlatformAdmin, orgRole } = useApp()
  const [railOpen, setRailOpen] = useState(false)
  const [palette, setPalette] = useState(false)
  const [orgMenu, setOrgMenu] = useState(false)
  const loc = useLocation()
  const nav = useNavigate()
  const isOwner = orgRole === 'owner'

  const pending = useQuery({ queryKey: ['review-count', org.id], queryFn: () => get<any[]>(`/api/orgs/${org.id}/registry?view=review`), enabled: isAdmin, staleTime: 60_000 })

  const groups: { label: string; items: NavDef[] }[] = [
    { label: 'Use', items: [
      { to: '/', label: 'Overview', icon: <LayoutGrid size={17} /> },
      { to: '/playground', label: 'Playground', icon: <FlaskConical size={17} />, keywords: 'chat try test' },
      { to: '/keys', label: 'API keys', icon: <KeyRound size={17} />, keywords: 'tokens credentials claude code' },
      { to: '/agents', label: 'Agents', icon: <Bot size={17} /> },
      { to: '/marketplace', label: 'Marketplace', icon: <Store size={17} />, keywords: 'skills mcp install' },
    ] },
    { label: 'Govern', items: [
      { to: '/guardrails', label: 'Guardrails', icon: <ShieldCheck size={17} />, keywords: 'pii redact policy' },
      { to: '/budgets', label: 'Budgets', icon: <PiggyBank size={17} />, keywords: 'spend limits cost' },
      { to: '/models', label: 'Models & routing', icon: <Network size={17} />, keywords: 'catalog deployments failover' },
      { to: '/rollouts', label: 'Model rollouts', icon: <GitBranch size={17} />, keywords: 'shadow canary eval promote' },
      { to: '/providers', label: 'Providers', icon: <Plug size={17} />, show: isAdmin, keywords: 'bedrock azure anthropic connection string' },
    ] },
    { label: 'Organization', items: [
      { to: '/teams', label: 'Teams & projects', icon: <Boxes size={17} /> },
      { to: '/people', label: 'People', icon: <Users size={17} />, keywords: 'members roles owners' },
      { to: '/registry', label: 'Publishing', icon: <Upload size={17} />, count: pending.data?.length || undefined, keywords: 'registry review skills' },
      { to: '/sso', label: 'Single sign-on', icon: <Building2 size={17} />, show: isOwner, keywords: 'oidc scim entra okta idp' },
    ] },
    { label: 'Evidence', items: [
      { to: '/requests', label: 'Requests', icon: <Activity size={17} />, keywords: 'logs receipts' },
      { to: '/usage', label: 'Usage & spend', icon: <Wallet size={17} />, keywords: 'analytics chargeback' },
      { to: '/billing', label: 'Billing', icon: <Receipt size={17} />, keywords: 'invoice cost price statement spend' },
      { to: '/audit', label: 'Audit trail', icon: <ScrollText size={17} />, show: isAdmin },
    ] },
    { label: 'Platform', items: [
      { to: '/platform/users', label: 'All users', icon: <BadgeCheck size={17} />, show: isPlatformAdmin },
      { to: '/platform/system', label: 'System', icon: <Server size={17} />, show: isPlatformAdmin },
    ] },
  ]
  const visible = groups.map((g) => ({ ...g, items: g.items.filter((i) => i.show !== false) })).filter((g) => g.items.length)
  const flat = visible.flatMap((g) => g.items)
  const current = flat.find((i) => (i.to === '/' ? loc.pathname === '/' : loc.pathname.startsWith(i.to)))

  useEffect(() => {
    const h = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'k') { e.preventDefault(); setPalette((p) => !p) }
    }
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [])
  useEffect(() => setRailOpen(false), [loc.pathname])

  const logout = async () => { await post('/api/auth/logout'); window.location.href = '/login' }

  return (
    <div className="shell">
      <nav className={`rail ${railOpen ? 'open' : ''}`} aria-label="Primary">
        <div className="brand"><Logo /><div><b>Theophylax</b><small>AI gateway</small></div></div>
        <div style={{ position: 'relative' }}>
          <button className="org-switch" onClick={() => setOrgMenu((v) => !v)} aria-haspopup="listbox" aria-expanded={orgMenu}>
            <span className="avatar" style={{ width: 24, height: 24, fontSize: 11, borderRadius: 6, background: 'var(--accent)' }}>{initials(org.name)}</span>
            <span className="grow">{org.name}</span><ChevronsUpDown size={15} />
          </button>
          {orgMenu && (
            <div className="panel" role="listbox" style={{ position: 'absolute', left: 12, right: 12, top: '100%', zIndex: 5, padding: 4, boxShadow: 'var(--shadow-pop)' }}>
              {me.orgs.map((o) => (
                <button key={o.id} role="option" aria-selected={o.id === org.id} className="btn ghost" style={{ width: '100%', justifyContent: 'flex-start' }} onClick={() => { setOrg(o.id); setOrgMenu(false); nav('/') }}>
                  {o.name} {o.id === org.id && <span className="muted">· current</span>}
                </button>
              ))}
              {isPlatformAdmin && <button className="btn ghost" style={{ width: '100%', justifyContent: 'flex-start' }} onClick={() => { setOrgMenu(false); nav('/platform/system?new-org=1') }}>Create organization</button>}
            </div>
          )}
        </div>
        {visible.map((g) => (
          <div className="nav-group" key={g.label}>
            <span>{g.label}</span>
            {g.items.map((i) => (
              <NavLink key={i.to} to={i.to} end={i.to === '/'} className={({ isActive }) => `nav-item ${isActive ? 'active' : ''}`}>
                {i.icon}{i.label}{i.count ? <span className="count" aria-label={`${i.count} pending`}>{i.count}</span> : null}
              </NavLink>
            ))}
          </div>
        ))}
        <div className="rail-foot">
          <div className="avatar">{initials(me.user.name || me.user.email)}</div>
          <div className="who"><div>{me.user.name || me.user.email}</div><span>{me.is_platform_admin ? 'Platform admin' : orgRole || 'member'}</span></div>
          <NavLink to="/settings" className="icon-btn-rail" aria-label="Settings"><Settings size={16} /></NavLink>
          <button className="icon-btn-rail" onClick={logout} aria-label="Sign out"><LogOut size={16} /></button>
        </div>
      </nav>
      {railOpen && <div className="scrim" style={{ zIndex: 40 }} onClick={() => setRailOpen(false)} />}
      <div className="main">
        <header className="topbar">
          <button className="btn ghost icon mobile-menu" onClick={() => setRailOpen(true)} aria-label="Open navigation"><Menu size={18} /></button>
          <div className="crumbs"><span>{org.name}</span><span>/</span><b>{current?.label ?? 'Settings'}</b></div>
          <button className="search-trigger" onClick={() => setPalette(true)}><Search size={15} /><span>Jump to a page or action</span><kbd>⌘K</kbd></button>
          <AppearanceMenu />
        </header>
        <main className="page">{children}</main>
      </div>
      {palette && <CommandPalette items={flat} onClose={() => setPalette(false)} />}
    </div>
  )
}

function CommandPalette({ items, onClose }: { items: NavDef[]; onClose: () => void }) {
  const nav = useNavigate()
  const { isAdmin } = useApp()
  const [q, setQ] = useState('')
  const [i, setI] = useState(0)
  const actions = useMemo(() => {
    const a = [
      { label: 'Create an API key', to: '/keys?new=1', hint: 'Action', icon: <KeyRound size={16} /> },
      { label: 'Try a model in the playground', to: '/playground', hint: 'Action', icon: <FlaskConical size={16} /> },
      { label: 'Create an agent', to: '/agents?new=1', hint: 'Action', icon: <Bot size={16} /> },
      { label: 'Publish a skill', to: '/registry?new=1', hint: 'Action', icon: <Upload size={16} /> },
      { label: 'Look up a request or receipt', to: '/requests', hint: 'Action', icon: <Activity size={16} /> },
    ]
    if (isAdmin) a.push(
      { label: 'Add a guardrail policy', to: '/guardrails?new=1', hint: 'Action', icon: <ShieldCheck size={16} /> },
      { label: 'Connect a provider', to: '/providers?new=1', hint: 'Action', icon: <Plug size={16} /> },
      { label: 'Start a model rollout', to: '/rollouts?new=1', hint: 'Action', icon: <GitBranch size={16} /> },
      { label: 'Set a budget', to: '/budgets?new=1', hint: 'Action', icon: <CircleGauge size={16} /> },
    )
    return [...items.map((x) => ({ label: x.label, to: x.to, hint: 'Page', icon: x.icon, kw: x.keywords })), ...a]
  }, [items, isAdmin])
  const results = actions.filter((a) => (a.label + ' ' + ((a as any).kw ?? '')).toLowerCase().includes(q.toLowerCase()))
  const go = (to: string) => { onClose(); nav(to) }
  return (
    <>
      <div className="scrim" onClick={onClose} style={{ zIndex: 79 }} />
      <div className="palette" role="dialog" aria-label="Command palette">
        <input autoFocus placeholder="Search pages and actions" value={q} onChange={(e) => { setQ(e.target.value); setI(0) }}
          onKeyDown={(e) => {
            if (e.key === 'Escape') onClose()
            if (e.key === 'ArrowDown') { e.preventDefault(); setI((x) => Math.min(x + 1, results.length - 1)) }
            if (e.key === 'ArrowUp') { e.preventDefault(); setI((x) => Math.max(x - 1, 0)) }
            if (e.key === 'Enter' && results[i]) go(results[i]!.to)
          }} />
        <ul role="listbox">
          {results.map((r, k) => (
            <li key={r.label} role="option" aria-selected={k === i} className={k === i ? 'on' : ''} onMouseEnter={() => setI(k)} onClick={() => go(r.to)}>
              {r.icon}{r.label}<small>{r.hint}</small>
            </li>
          ))}
          {!results.length && <li className="muted">Nothing matches “{q}”.</li>}
        </ul>
      </div>
    </>
  )
}
