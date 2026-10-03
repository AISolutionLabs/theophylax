// Typed client for the Theophylax control-plane API.

export class ApiError extends Error {
  status: number
  type: string
  constructor(status: number, type: string, message: string) {
    super(message)
    this.status = status
    this.type = type
  }
}

export async function api<T = any>(path: string, opts: { method?: string; body?: unknown; headers?: Record<string, string> } = {}): Promise<T> {
  const res = await fetch(path, {
    method: opts.method ?? (opts.body !== undefined ? 'POST' : 'GET'),
    credentials: 'same-origin',
    headers: { 'X-Theophylax-Client': 'web', ...(opts.body !== undefined ? { 'Content-Type': 'application/json' } : {}), ...opts.headers },
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  })
  if (res.status === 204) return undefined as T
  const text = await res.text()
  let data: any = undefined
  try { data = text ? JSON.parse(text) : undefined } catch { /* non-JSON */ }
  if (!res.ok) {
    const e = data?.error ?? {}
    throw new ApiError(res.status, e.type ?? 'error', e.message ?? text ?? res.statusText)
  }
  return data as T
}

export const get = <T = any>(p: string) => api<T>(p)
export const post = <T = any>(p: string, body: unknown = {}) => api<T>(p, { method: 'POST', body })
export const patch = <T = any>(p: string, body: unknown) => api<T>(p, { method: 'PATCH', body })
export const del = <T = any>(p: string) => api<T>(p, { method: 'DELETE' })

export function qs(params: Record<string, string | number | boolean | undefined | null>) {
  const s = new URLSearchParams()
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== null && v !== '') s.set(k, String(v))
  const q = s.toString()
  return q ? `?${q}` : ''
}

// ── Types ─────────────────────────────────────────────────────────────────
export type Role = 'owner' | 'admin' | 'member' | 'viewer' | ''

export interface User { id: string; email: string; name: string; is_platform_admin: boolean; status: string; last_login_at?: string; created_at: string }
export interface Org { id: string; slug: string; name: string; settings: ScopeSettings; created_at: string }
export interface ScopeSettings { allowed_models?: string[]; log_payloads?: boolean | null; rpm?: number; tpm?: number }
export interface Membership { id: string; user_id: string; org_id: string; scope_type: 'org' | 'team' | 'project'; scope_id: string; role: Role; source: string; scope_name?: string; org_name?: string; email?: string; name?: string }
export interface Me { user: User; memberships: Membership[]; orgs: Org[]; is_platform_admin: boolean; has_password: boolean }
export interface Team { id: string; org_id: string; slug: string; name: string; description: string; settings: ScopeSettings; project_count: number; member_count: number; created_at: string }
export interface Project { id: string; org_id: string; team_id: string; slug: string; name: string; description: string; settings: ScopeSettings; team_name: string; key_count: number; created_at: string }
export interface ApiKey { id: string; kind: 'gateway' | 'pat'; org_id: string; team_id?: string; project_id?: string; user_id?: string; agent_id?: string; name: string; prefix: string; allowed_models: string[]; rpm: number; tpm: number; expires_at?: string; revoked_at?: string; last_used_at?: string; created_at: string; project_name?: string; team_name?: string; owner_email?: string; agent_name?: string }
export interface Provider { id: string; org_id?: string; name: string; kind: string; connection_string: string; config: any; enabled: boolean; status: string; error?: string; can_manage: boolean; deployment_count: number; updated_at: string }
export interface Deployment { id: string; model_id: string; provider_id: string; upstream_model: string; weight: number; priority: number; enabled: boolean; provider_name: string; provider_kind: string }
export interface Model { id: string; name: string; display_name: string; family: string; aliases: string[]; input_price?: number | null; output_price?: number | null; cache_read_price?: number | null; cache_write_price?: number | null; context_window: number; max_output: number; capabilities: string[]; status: string; strategy: string; source: string; deployments: Deployment[]; requests_24h: number }
export interface Guardrail { id: string; org_id: string; scope_type: string; scope_id: string; scope_name: string; name: string; description: string; enabled: boolean; mode: 'enforce' | 'monitor'; rules: Rule[]; can_edit: boolean; updated_at: string }
export interface Rule { type: string; action?: string; apply_to?: string; entities?: string[]; patterns?: string[]; words?: string[]; models?: string[]; max?: number; threshold?: number; message?: string; scope?: string; url?: string; timeout_ms?: number; fail_open?: boolean; provider_id?: string; guardrail_id?: string; version?: string; restore?: boolean; detector?: string; judge_model?: string; canary?: boolean; exfil_guard?: boolean; tool_guard?: boolean; allow_domains?: string[] }
export interface Budget { id: string; org_id: string; scope_type: string; scope_id: string; scope_name: string; period: string; limit_usd?: number | null; limit_tokens?: number | null; alert_pct: number; action: string; spent_usd: number; spent_tokens: number; can_edit: boolean }
export interface IdP { id: string; org_id: string; name: string; issuer: string; client_id: string; scopes: string[]; groups_claim: string; email_domains: string[]; jit: boolean; default_role: string; role_mappings: { group: string; scope_type: string; scope_id: string; role: string }[]; gateway_tokens: boolean; audience: string; enabled: boolean; has_secret: boolean; has_scim_token: boolean; redirect_uri: string; scim_url: string }
export interface RegistryItem { id: string; org_id: string; kind: 'skill' | 'mcp_server' | 'agent' | 'prompt' | 'plugin'; slug: string; name: string; description: string; publisher: string; owner_team_id?: string; owner_team?: string; tags: string[]; visibility: string; status: string; latest_version: string; approved_version: string; installs: number; rating_sum: number; rating_count: number; created_by?: string; creator_email?: string; review_note: string; updated_at: string; installed_in_scopes: number }
export interface RegistryVersion { id: string; version: string; content: any; digest: string; changelog: string; created_at: string }
export interface Agent { id: string; org_id: string; team_id: string; project_id?: string; slug: string; name: string; description: string; model: string; system_prompt: string; skills: string[]; mcp_servers: string[]; tools: any; params: any; allow_client_system: boolean; status: string; team_name?: string; owner_email?: string; requests_24h?: number; updated_at: string }
export interface Finding { policy: string; rule: string; action: string; detail?: string; count?: number; phase: string }
export interface RequestLog { id: string; ts: string; org_id: string; team_id?: string; project_id?: string; user_id?: string; key_id?: string; agent_id?: string; endpoint: string; model: string; upstream_model: string; provider_kind: string; status: number; error: string; stream: boolean; latency_ms: number; ttfb_ms: number; input_tokens: number; output_tokens: number; cache_read_tokens: number; cache_write_tokens: number; cost_usd: number; attempts: number; guardrails: Finding[]; client_ip: string; user_agent: string; key_name?: string; project_name?: string; user_email?: string; has_payload: boolean }
export interface AuditEntry { seq: number; id: string; ts: string; actor_type: string; actor_id: string; actor_email: string; org_id?: string; action: string; target_type: string; target_id: string; details: any; ip: string; user_agent: string }
export interface Receipt {
  v: number; rid: string; iss: string; iat: number; org: string; team?: string; project?: string; sub: { type: string; id: string }; agent?: string; endpoint: string; model_requested: string
  route: { model: string; provider_id?: string; provider_kind?: string; upstream_model?: string; attempts: number }
  req_sha256: string; resp_sha256: string; stream: boolean; status: number
  policies: { id: string; name: string; scope: string; mode: string; digest: string }[]
  actions: { policy: string; rule: string; action: string; phase: string; count?: number }[]
  usage: { input: number; output: number; cache_read: number; cache_write: number }; cost_usd: number
}
export interface ReceiptLookup { jws: string; kid: string; signature_valid: boolean; anchored: boolean; anchor_status?: string; checkpoint?: { id: string; merkle_root: string; ts: string; audit_id?: string; count: number }; receipt: Receipt; public_keys_url: string; error?: string }
