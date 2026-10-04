import { useEffect, useState } from 'react'
import { useQuery } from '@tanstack/react-query'
import { useSearchParams } from 'react-router-dom'
import { GitBranch, Pause, Play, Plus, RotateCcw } from 'lucide-react'
import { get, patch, post } from '../lib/api'
import { useApp } from '../lib/app'
import { useModels, useProviders, useRoutableModels } from '../lib/queries'
import { fmtAgo, fmtMs, fmtUSD } from '../lib/format'
import { Badge, Button, Confirm, Drawer, Empty, ErrorNote, Field, Input, Loading, Meter, PageHead, Select, Toggle, useAction } from '../components/ui'

interface Rollout {
  id: string; model_id: string; model_name: string; name: string; provider_name: string; candidate_upstream: string; incumbent: string; judge_model: string
  stage: 'shadow' | 'canary' | 'paused' | 'promoted' | 'rolled_back'; paused_stage: string; shadow_pct: number; canary_pct: number; min_samples: number
  win_threshold: number; max_error_rate: number; max_latency_ratio: number; auto_promote: boolean; auto_rollback: boolean
  samples: number; wins: number; ties: number; losses: number; judge_errors: number; cand_requests: number; cand_errors: number
  cand_latency_ms: number; inc_latency_ms: number; cand_cost: number; inc_cost: number; canary_requests: number; canary_errors: number; canary_latency_ms: number
  decision_note: string; decided_at?: string; created_at: string; updated_at: string
  promoted_deployment_id?: string | null; reverted_at?: string | null; reverted_by?: string | null
}

const STAGES = ['shadow', 'canary', 'promoted'] as const

function Pipeline({ r }: { r: Rollout }) {
  const stage = r.stage === 'paused' ? r.paused_stage : r.stage
  const idx = r.stage === 'rolled_back' ? -1 : STAGES.indexOf(stage as any)
  return (
    <div className="chain" style={{ marginTop: 0 }} aria-label={`Stage: ${r.stage}`}>
      {STAGES.map((s, i) => (
        <div key={s} style={{ background: i === idx ? 'var(--accent-wash)' : i < idx ? 'var(--surface)' : undefined, color: i === idx ? 'var(--accent-ink)' : i < idx ? 'var(--ink-2)' : 'var(--muted)', fontWeight: i === idx ? 600 : 400 }}>
          {s === 'shadow' ? 'Shadow & judge' : s === 'canary' ? 'Canary' : 'Promoted'}
          <div style={{ fontWeight: 400, fontSize: 12 }}>{s === 'shadow' ? `${r.shadow_pct}% mirrored` : s === 'canary' ? `${r.canary_pct}% live` : 'primary route'}</div>
        </div>
      ))}
    </div>
  )
}

const stageBadge = (r: Rollout) => r.stage === 'promoted' ? <Badge tone="ok">promoted</Badge> : r.stage === 'rolled_back' ? <Badge tone="bad">{r.reverted_at ? 'promotion rolled back' : 'rolled back'}</Badge> : r.stage === 'paused' ? <Badge tone="warn">paused</Badge> : <Badge tone="accent">{r.stage}</Badge>

export default function Rollouts() {
  const { isPlatformAdmin } = useApp()
  const [sp, setSp] = useSearchParams()
  const list = useQuery({ queryKey: ['rollouts'], queryFn: () => get<Rollout[]>('/api/rollouts'), refetchInterval: 10_000 })
  const [creating, setCreating] = useState(sp.get('new') === '1')
  const [open, setOpen] = useState<string | null>(null)
  useEffect(() => { if (sp.get('new')) { sp.delete('new'); setSp(sp, { replace: true }) } }, [sp, setSp])
  return (
    <>
      <PageHead title="Model rollouts" lede="Move a model to a new version or provider safely. Shadow mirrors live traffic to the candidate and a judge model compares answers; canary then serves a slice of real traffic. Promotion and rollback happen automatically against your thresholds."
        actions={isPlatformAdmin && <Button variant="primary" onClick={() => setCreating(true)}><Plus size={15} /> Start rollout</Button>} />
      <div className="stack">
        {list.isLoading ? <Loading /> : list.error ? <ErrorNote error={list.error} /> : !list.data?.length ? (
          <div className="panel"><Empty icon={<GitBranch size={20} />} title="No rollouts yet" action={isPlatformAdmin ? <Button variant="primary" onClick={() => setCreating(true)}>Start rollout</Button> : undefined}>When a new model version ships, start a rollout instead of switching everyone at once.</Empty></div>
        ) : list.data.map((r) => {
          const winRate = r.samples ? (r.wins + r.ties) / r.samples : 0
          const errRate = r.cand_requests ? r.cand_errors / r.cand_requests : 0
          const latRatio = r.inc_latency_ms ? r.cand_latency_ms / r.inc_latency_ms : 0
          const costRatio = r.inc_cost ? r.cand_cost / r.inc_cost : 0
          return (
            <section className="panel" key={r.id} style={{ cursor: 'pointer' }} onClick={() => setOpen(r.id)}>
              <div className="panel-head">
                <div><h3><span className="mono">{r.model_name}</span> → <span className="mono">{r.candidate_upstream}</span></h3><div className="sub">via {r.provider_name} · {['promoted', 'rolled_back'].includes(r.stage) ? 'decided' : `replacing ${r.incumbent || 'nothing yet'}`} · started {fmtAgo(r.created_at)}</div></div>
                <div className="end">{stageBadge(r)}</div>
              </div>
              <div className="panel-body stack" style={{ gap: 14 }}>
                <Pipeline r={r} />
                <div className="kpis" style={{ border: 0 }}>
                  <div className="kpi"><div className="label">Judged samples</div><div className="value" style={{ fontSize: 20 }}>{r.samples} / {r.min_samples}</div><Meter progress pct={(r.samples / r.min_samples) * 100} /></div>
                  <div className="kpi"><div className="label">Candidate wins or ties</div><div className="value" style={{ fontSize: 20 }}>{r.samples ? (winRate * 100).toFixed(0) + '%' : '—'}</div><div className="delta">needs {(r.win_threshold * 100).toFixed(0)}% · {r.wins}W {r.ties}T {r.losses}L</div></div>
                  <div className="kpi"><div className="label">Candidate errors</div><div className="value" style={{ fontSize: 20 }}>{r.cand_requests ? (errRate * 100).toFixed(1) + '%' : '—'}</div><div className="delta">limit {(r.max_error_rate * 100).toFixed(0)}%{r.canary_requests ? ` · canary ${r.canary_errors}/${r.canary_requests}` : ''}</div></div>
                  <div className="kpi"><div className="label">Latency · cost vs current</div><div className="value" style={{ fontSize: 20 }}>{latRatio ? `×${latRatio.toFixed(2)}` : '—'}<span className="muted" style={{ fontSize: 15 }}> · {costRatio ? `×${costRatio.toFixed(2)}` : '—'}</span></div><div className="delta">latency limit ×{r.max_latency_ratio}</div></div>
                </div>
                {r.decision_note && <p className="muted" style={{ fontSize: 13 }}>{r.decision_note}</p>}
              </div>
            </section>
          )
        })}
      </div>
      {creating && <RolloutForm onClose={() => setCreating(false)} />}
      {open && <RolloutDrawer id={open} onClose={() => setOpen(null)} />}
    </>
  )
}

function RolloutForm({ onClose }: { onClose: () => void }) {
  const models = useModels()
  const routable = useRoutableModels()
  const providers = useProviders()
  const [f, setF] = useState({ model_id: '', candidate_provider_id: '', candidate_upstream: '', judge_model: '', shadow_pct: '10', canary_pct: '10', min_samples: '50', win_threshold: '50', max_error_rate: '5', max_latency_ratio: '1.5', auto_promote: true, auto_rollback: true, skip_shadow: false })
  const set = (k: keyof typeof f) => (v: any) => setF((x) => ({ ...x, [k]: v }))
  const create = useAction(() => post('/api/rollouts', {
    ...f, shadow_pct: Number(f.shadow_pct), canary_pct: Number(f.canary_pct), min_samples: Number(f.min_samples),
    win_threshold: Number(f.win_threshold) / 100, max_error_rate: Number(f.max_error_rate) / 100, max_latency_ratio: Number(f.max_latency_ratio),
  }), { done: 'Rollout started', invalidate: [['rollouts']], onSuccess: onClose })
  return (
    <Drawer open onClose={onClose} wide title="Start a model rollout" sub="Clients keep calling the same model name throughout; only the routing changes."
      footer={<><Button onClick={onClose}>Cancel</Button><Button variant="primary" busy={create.isPending} disabled={!f.model_id || !f.candidate_provider_id || !f.candidate_upstream || (!f.skip_shadow && !f.judge_model)} onClick={() => create.mutate(undefined)}>Start rollout</Button></>}>
      <Field label="Model to evolve" hint="The public name your clients use."><Select value={f.model_id} onChange={(e) => set('model_id')(e.target.value)} options={[{ value: '', label: 'Choose…' }, ...(models.data ?? []).filter((m) => !m.name.includes('*')).map((m) => ({ value: m.id, label: m.name }))]} /></Field>
      <div className="grid2">
        <Field label="Candidate provider"><Select value={f.candidate_provider_id} onChange={(e) => set('candidate_provider_id')(e.target.value)} options={[{ value: '', label: 'Choose…' }, ...(providers.data ?? []).filter((p) => p.status === 'ready').map((p) => ({ value: p.id, label: p.name }))]} /></Field>
        <Field label="Candidate upstream model ID" hint="The new version, e.g. a Bedrock inference profile or Azure deployment."><Input className="code" value={f.candidate_upstream} onChange={(e) => set('candidate_upstream')(e.target.value)} /></Field>
      </div>
      <Toggle checked={f.skip_shadow} onChange={set('skip_shadow')} label="Skip shadow evaluation and go straight to canary" />
      {!f.skip_shadow && (
        <div className="grid2" style={{ marginTop: 14 }}>
          <Field label="Judge model" hint="Compares incumbent and candidate answers with randomized order."><Select value={f.judge_model} onChange={(e) => set('judge_model')(e.target.value)} options={[{ value: '', label: 'Choose…' }, ...routable.data.map((m) => ({ value: m.name, label: m.name }))]} /></Field>
          <Field label="Traffic mirrored in shadow (%)" hint="Mirrored calls cost money but are never shown to users."><Input type="number" min={1} max={100} value={f.shadow_pct} onChange={(e) => set('shadow_pct')(e.target.value)} /></Field>
        </div>
      )}
      <div className="grid3" style={{ marginTop: f.skip_shadow ? 14 : 0 }}>
        <Field label="Live canary traffic (%)" hint="0 promotes directly after shadow."><Input type="number" min={0} max={100} value={f.canary_pct} onChange={(e) => set('canary_pct')(e.target.value)} /></Field>
        <Field label="Samples per stage"><Input type="number" min={5} value={f.min_samples} onChange={(e) => set('min_samples')(e.target.value)} /></Field>
        <Field label="Win-or-tie needed (%)"><Input type="number" min={0} max={100} value={f.win_threshold} onChange={(e) => set('win_threshold')(e.target.value)} /></Field>
        <Field label="Max error rate (%)"><Input type="number" min={0} max={100} value={f.max_error_rate} onChange={(e) => set('max_error_rate')(e.target.value)} /></Field>
        <Field label="Max latency vs current (×)"><Input type="number" step="0.1" min={1} value={f.max_latency_ratio} onChange={(e) => set('max_latency_ratio')(e.target.value)} /></Field>
      </div>
      <div className="stack" style={{ gap: 10 }}>
        <Toggle checked={f.auto_promote} onChange={set('auto_promote')} label="Advance and promote automatically when thresholds are met" />
        <Toggle checked={f.auto_rollback} onChange={set('auto_rollback')} label="Roll back automatically when the candidate underperforms or errors" />
      </div>
      <div className="callout" style={{ marginTop: 16 }}>Shadow traffic sends the same (already redacted) prompts to the candidate provider and judge model. Make sure both are approved for this data.</div>
    </Drawer>
  )
}

function RolloutDrawer({ id, onClose }: { id: string; onClose: () => void }) {
  const { isPlatformAdmin } = useApp()
  const d = useQuery({ queryKey: ['rollout', id], queryFn: () => get<{ rollout: Rollout; samples: any[] }>(`/api/rollouts/${id}`), refetchInterval: 8000 })
  const [confirm, setConfirm] = useState<'promote' | 'rollback' | null>(null)
  const act = useAction((a: string) => post<any>(`/api/rollouts/${id}/${a}`), { done: (res: any) => res?.reverted ? `Promotion rolled back · previous routing restored${res.reverted.kept_newer ? ` · ${res.reverted.kept_newer} newer route(s) kept` : ''}` : 'Rollout updated', invalidate: [['rollouts'], ['rollout'], ['models']], onSuccess: () => setConfirm(null) })
  const upd = useAction((body: any) => patch(`/api/rollouts/${id}`, body), { done: 'Rollout updated', invalidate: [['rollouts'], ['rollout']] })
  const r = d.data?.rollout
  const active = r && ['shadow', 'canary', 'paused'].includes(r.stage)
  return (
    <Drawer open onClose={onClose} wide title={r ? `${r.model_name} → ${r.candidate_upstream}` : 'Rollout'} sub={r && <>{stageBadge(r)} {r.decision_note}</>}
      footer={isPlatformAdmin && active ? <>
        {r!.stage === 'paused' ? <Button onClick={() => act.mutate('resume')}><Play size={14} /> Resume</Button> : <Button onClick={() => act.mutate('pause')}><Pause size={14} /> Pause</Button>}
        {r!.stage === 'shadow' && <Button onClick={() => act.mutate('advance')}>Advance to canary</Button>}
        <span className="spacer" />
        <Button variant="danger" onClick={() => setConfirm('rollback')}>Roll back</Button>
        <Button variant="primary" onClick={() => setConfirm('promote')}>Promote now</Button>
      </> : isPlatformAdmin && r?.stage === 'promoted' ? <>
        <span className="spacer" />
        <Button variant="danger" onClick={() => setConfirm('rollback')}><RotateCcw size={14} /> Roll back promotion</Button>
      </> : undefined}>
      {d.isLoading ? <Loading /> : d.error ? <ErrorNote error={d.error} /> : r && (
        <div className="stack">
          <Pipeline r={r} />
          {isPlatformAdmin && active && (
            <div className="grid3">
              <Field label="Shadow %"><Input type="number" defaultValue={r.shadow_pct} onBlur={(e) => Number(e.target.value) !== r.shadow_pct && upd.mutate({ shadow_pct: Number(e.target.value) })} /></Field>
              <Field label="Canary %"><Input type="number" defaultValue={r.canary_pct} onBlur={(e) => Number(e.target.value) !== r.canary_pct && upd.mutate({ canary_pct: Number(e.target.value) })} /></Field>
              <Field label="Samples per stage"><Input type="number" defaultValue={r.min_samples} onBlur={(e) => Number(e.target.value) !== r.min_samples && upd.mutate({ min_samples: Number(e.target.value) })} /></Field>
            </div>
          )}
          <h3>Judge verdicts</h3>
          <table className="tbl panel">
            <thead><tr><th>When</th><th>Verdict</th><th>Reason</th><th className="num">Latency cand / curr</th><th className="num">Cost cand / curr</th></tr></thead>
            <tbody>
              {d.data!.samples.map((s) => (
                <tr key={s.id}>
                  <td className="dim nowrap">{fmtAgo(s.ts)}</td>
                  <td>{s.verdict === 'candidate' ? <Badge tone="ok">candidate better</Badge> : s.verdict === 'incumbent' ? <Badge tone="warn">current better</Badge> : s.verdict === 'tie' ? <Badge>tie</Badge> : <Badge tone="bad">{s.verdict.replace('_', ' ')}</Badge>}</td>
                  <td style={{ maxWidth: 360 }}>{s.reason}</td>
                  <td className="num dim">{fmtMs(s.cand_latency_ms)} / {fmtMs(s.inc_latency_ms)}</td>
                  <td className="num dim">{fmtUSD(s.cand_cost, true)} / {fmtUSD(s.inc_cost, true)}</td>
                </tr>
              ))}
              {!d.data!.samples.length && <tr><td colSpan={5} className="muted">{r.stage === 'shadow' ? 'Waiting for mirrored traffic.' : 'No judged samples.'}</td></tr>}
            </tbody>
          </table>
        </div>
      )}
      <Confirm open={!!confirm} danger={confirm === 'rollback'} title={confirm === 'promote' ? 'Promote the candidate now?' : r?.stage === 'promoted' ? `Roll back the promotion of ${r.candidate_upstream}?` : 'Roll back this rollout?'}
        body={confirm === 'promote' ? 'The candidate becomes the primary route for this model. Current deployments stay as failover.'
          : r?.stage === 'promoted' ? <>Removes the <span className="mono">{r.provider_name} · {r.candidate_upstream}</span> route this promotion added and restores each earlier route's priority, weight and on/off setting to what it was just before the promotion. Routes added since then are kept. Clients keep using the same model name.</>
          : 'The candidate stops receiving shadow and canary traffic. Routing returns to the current deployments.'}
        confirmLabel={confirm === 'promote' ? 'Promote' : 'Roll back'} onClose={() => setConfirm(null)} onConfirm={() => confirm && act.mutate(confirm)} busy={act.isPending} />
    </Drawer>
  )
}
