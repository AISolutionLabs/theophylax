import { useQuery } from '@tanstack/react-query'
import { get, type Agent, type Model, type Project, type Provider, type RegistryItem, type Team } from './api'
import { useApp } from './app'

export function useTeams() {
  const { org } = useApp()
  return useQuery({ queryKey: ['teams', org.id], queryFn: () => get<Team[]>(`/api/orgs/${org.id}/teams`) })
}
export function useProjects() {
  const { org } = useApp()
  return useQuery({ queryKey: ['projects', org.id], queryFn: () => get<Project[]>(`/api/orgs/${org.id}/projects`) })
}
export function useModels() {
  return useQuery({ queryKey: ['models'], queryFn: () => get<Model[]>('/api/models') })
}
export function useProviders() {
  return useQuery({ queryKey: ['providers'], queryFn: () => get<Provider[]>('/api/providers') })
}
export function useAgents() {
  const { org } = useApp()
  return useQuery({ queryKey: ['agents', org.id], queryFn: () => get<Agent[]>(`/api/orgs/${org.id}/agents`) })
}
export function useMarketplace(kind?: string) {
  const { org } = useApp()
  return useQuery({ queryKey: ['marketplace', org.id, kind ?? ''], queryFn: () => get<RegistryItem[]>(`/api/orgs/${org.id}/registry?view=marketplace${kind ? `&kind=${kind}` : ''}`) })
}

/** Models that can actually be routed (have at least one deployment). */
export function useRoutableModels() {
  const q = useModels()
  return { ...q, data: (q.data ?? []).filter((m) => m.deployments.length > 0 && m.status !== 'disabled') }
}

/** Scope options for pickers: org, every team, every project. */
export function useScopeOptions() {
  const { org } = useApp()
  const teams = useTeams()
  const projects = useProjects()
  const opts = [{ value: `org:${org.id}`, label: `Whole organization (${org.name})` }]
  for (const t of teams.data ?? []) opts.push({ value: `team:${t.id}`, label: `Team · ${t.name}` })
  for (const p of projects.data ?? []) opts.push({ value: `project:${p.id}`, label: `Project · ${p.team_name} / ${p.name}` })
  return opts
}
