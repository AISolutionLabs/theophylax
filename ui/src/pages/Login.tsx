import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearchParams } from 'react-router-dom'
import { Building2 } from 'lucide-react'
import { get, post } from '../lib/api'
import { errMsg } from '../lib/app'
import { Button, Field, Input, Logo, Seal } from '../components/ui'

export default function Login() {
  const [sp] = useSearchParams()
  const nav = useNavigate()
  const qc = useQueryClient()
  const cfg = useQuery({ queryKey: ['auth-config'], queryFn: () => get<{ local_login: boolean; providers: { id: string; name: string; org: string }[] }>('/api/auth/config') })
  const [email, setEmail] = useState('')
  const [password, setPassword] = useState('')
  const [err, setErr] = useState(sp.get('error') ?? '')
  const [busy, setBusy] = useState(false)
  const next = sp.get('next') || '/'

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setErr('')
    try {
      await post('/api/auth/login', { email, password })
      await qc.invalidateQueries({ queryKey: ['me'] })
      nav(next.startsWith('/') ? next : '/', { replace: true })
    } catch (e) {
      setErr(errMsg(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login">
      <section className="art" aria-hidden="false">
        <div className="row" style={{ color: '#fff', gap: 10 }}><Logo size={30} /><b style={{ fontVariationSettings: "'wdth' 118", fontSize: 18 }}>Theophylax</b></div>
        <div>
          <h1>Every model call, governed and provable.</h1>
          <p>One gateway for Anthropic, AWS Bedrock and Azure, with your identity provider, policies and budgets applied to every request. Each response comes with a signed receipt you can hand to an auditor.</p>
          <div className="row" style={{ marginTop: 28, gap: 14, color: 'var(--rail-ink)' }}>
            <span style={{ color: '#d2a15e' }}><Seal size={34} /></span>
            <span style={{ fontSize: 13 }}>Receipt verified · request and response digests match · anchored in the audit chain</span>
          </div>
        </div>
        <small style={{ color: 'var(--rail-ink-dim)' }}>Sign-in activity is recorded in the audit trail.</small>
      </section>
      <section className="form">
        <div className="card">
          <h2 style={{ marginBottom: 6 }}>Sign in</h2>
          <p className="muted" style={{ marginBottom: 22 }}>Use your company account, or the administrator credentials set at install.</p>
          {err && <div className="callout bad" style={{ marginBottom: 16 }}>{err}</div>}
          {(cfg.data?.providers ?? []).map((p) => (
            <a key={p.id} className="btn" style={{ width: '100%', justifyContent: 'center', height: 40, marginBottom: 8 }} href={`/auth/oidc/${p.id}/start?redirect=${encodeURIComponent(next)}`}>
              <Building2 size={16} /> Continue with {p.name}
            </a>
          ))}
          {cfg.data?.local_login !== false && (
            <>
              {!!cfg.data?.providers.length && <div className="divider">or with a password</div>}
              <form onSubmit={submit}>
                <Field label="Email" htmlFor="email"><Input id="email" type="email" autoComplete="username" required value={email} onChange={(e) => setEmail(e.target.value)} /></Field>
                <Field label="Password" htmlFor="pw"><Input id="pw" type="password" autoComplete="current-password" required value={password} onChange={(e) => setPassword(e.target.value)} /></Field>
                <Button type="submit" variant="primary" busy={busy} style={{ width: '100%', justifyContent: 'center', height: 40 }}>{busy ? 'Signing in' : 'Sign in'}</Button>
              </form>
            </>
          )}
        </div>
      </section>
    </div>
  )
}
