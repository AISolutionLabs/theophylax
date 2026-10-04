import { createContext, useCallback, useContext, useMemo, useState, useSyncExternalStore, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { get, type Me, type Org, type Role, ApiError } from './api'

// ── Session & org context ─────────────────────────────────────────────────
interface AppCtx {
  me: Me
  org: Org
  setOrg: (id: string) => void
  orgRole: Role
  /** effective role at a scope, applying org → team → project inheritance */
  roleAt: (scope: { type: 'org' | 'team' | 'project'; id: string; teamId?: string }) => Role
  can: (scope: { type: 'org' | 'team' | 'project'; id: string; teamId?: string }, min: Role) => boolean
  isAdmin: boolean
  isPlatformAdmin: boolean
}

const Ctx = createContext<AppCtx | null>(null)
const rank: Record<string, number> = { '': 0, viewer: 1, member: 2, admin: 3, owner: 4 }

export function useApp() {
  const c = useContext(Ctx)
  if (!c) throw new Error('useApp outside provider')
  return c
}

export function useMe() {
  return useQuery<Me, ApiError>({ queryKey: ['me'], queryFn: () => get('/api/me'), retry: false, staleTime: 60_000 })
}

export function AppProvider({ me, children }: { me: Me; children: ReactNode }) {
  const qc = useQueryClient()
  const [orgId, setOrgId] = useState<string>(() => localStorageGet('tpx.org') ?? me.orgs[0]?.id ?? '')
  const org = me.orgs.find((o) => o.id === orgId) ?? me.orgs[0]!
  const setOrg = useCallback((id: string) => {
    setOrgId(id)
    localStorageSet('tpx.org', id)
    qc.removeQueries({ predicate: (q) => q.queryKey[0] !== 'me' })
  }, [qc])

  const value = useMemo<AppCtx>(() => {
    const roleAt: AppCtx['roleAt'] = (s) => {
      if (me.is_platform_admin) return 'owner'
      let best: Role = ''
      for (const m of me.memberships) {
        if (m.org_id !== org.id) continue
        const hit = m.scope_type === 'org' || (m.scope_type === 'team' && ((s.type === 'team' && m.scope_id === s.id) || (s.type === 'project' && m.scope_id === s.teamId))) || (m.scope_type === 'project' && s.type === 'project' && m.scope_id === s.id)
        if (hit && rank[m.role]! > rank[best]!) best = m.role
      }
      return best
    }
    const orgRole = roleAt({ type: 'org', id: org.id })
    return {
      me, org, setOrg, roleAt, orgRole,
      can: (s, min) => rank[roleAt(s)]! >= rank[min]!,
      isAdmin: rank[orgRole]! >= rank.admin!,
      isPlatformAdmin: me.is_platform_admin,
    }
  }, [me, org, setOrg])
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>
}

function localStorageGet(k: string) {
  try { return localStorage.getItem(k) } catch { return null }
}
function localStorageSet(k: string, v: string) {
  try { localStorage.setItem(k, v) } catch { /* private mode */ }
}

// ── Theme ─────────────────────────────────────────────────────────────────
// Appearance is a mode (light, dark, or follow the system) plus a palette.
// It is stored per browser and applied before React renders to avoid a flash.
export type ThemeMode = 'light' | 'dark' | 'system'
export const PALETTES = [
  { id: 'verdigris', label: 'Verdigris', swatch: '#0f8a78' },
  { id: 'lapis', label: 'Lapis', swatch: '#2457c5' },
  { id: 'porphyry', label: 'Porphyry', swatch: '#7a3e9d' },
  { id: 'graphite', label: 'Graphite', swatch: '#3b4652' },
  { id: 'contrast', label: 'High contrast', swatch: '#000000' },
] as const
export type Palette = (typeof PALETTES)[number]['id']

const darkQuery = typeof window !== 'undefined' ? window.matchMedia?.('(prefers-color-scheme: dark)') : undefined
let appearance: { mode: ThemeMode; palette: Palette } = {
  mode: (['light', 'dark', 'system'] as const).find((m) => m === localStorageGet('tpx.theme')) ?? 'system',
  palette: PALETTES.find((p) => p.id === localStorageGet('tpx.palette'))?.id ?? 'verdigris',
}
const listeners = new Set<() => void>()
const resolved = () => (appearance.mode === 'system' ? (darkQuery?.matches ? 'dark' : 'light') : appearance.mode)

function applyAppearance() {
  const root = document.documentElement
  root.dataset.theme = resolved()
  if (appearance.palette === 'verdigris') delete root.dataset.palette
  else root.dataset.palette = appearance.palette
}
applyAppearance()
darkQuery?.addEventListener?.('change', () => {
  if (appearance.mode !== 'system') return
  applyAppearance()
  listeners.forEach((l) => l())
})

function setAppearance(next: Partial<typeof appearance>) {
  appearance = { ...appearance, ...next }
  localStorageSet('tpx.theme', appearance.mode)
  localStorageSet('tpx.palette', appearance.palette)
  applyAppearance()
  listeners.forEach((l) => l())
}

export function useTheme() {
  const snap = useSyncExternalStore((l) => { listeners.add(l); return () => { listeners.delete(l) } }, () => `${appearance.mode}|${appearance.palette}|${resolved()}`)
  const [mode, palette, theme] = snap.split('|') as [ThemeMode, Palette, 'light' | 'dark']
  return {
    theme, mode, palette,
    setMode: (m: ThemeMode) => setAppearance({ mode: m }),
    setPalette: (p: Palette) => setAppearance({ palette: p }),
    toggle: () => setAppearance({ mode: theme === 'dark' ? 'light' : 'dark' }),
  }
}

// ── Toasts ────────────────────────────────────────────────────────────────
type Toast = { id: number; text: string; kind: 'info' | 'error' }
const ToastCtx = createContext<(text: string, kind?: 'info' | 'error') => void>(() => {})
export const useToast = () => useContext(ToastCtx)

export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Toast[]>([])
  const push = useCallback((text: string, kind: 'info' | 'error' = 'info') => {
    const id = Date.now() + Math.random()
    setItems((x) => [...x, { id, text, kind }])
    setTimeout(() => setItems((x) => x.filter((t) => t.id !== id)), kind === 'error' ? 6000 : 3200)
  }, [])
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {items.map((t) => <div key={t.id} className={`toast ${t.kind === 'error' ? 'error' : ''}`}>{t.text}</div>)}
      </div>
    </ToastCtx.Provider>
  )
}

export function errMsg(e: unknown) {
  if (e instanceof ApiError) return e.message
  if (e instanceof Error) return e.message
  return String(e)
}
