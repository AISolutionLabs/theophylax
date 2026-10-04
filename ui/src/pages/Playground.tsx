import { useEffect, useRef, useState } from 'react'
import { Link } from 'react-router-dom'
import { Eraser, Send, Square } from 'lucide-react'
import { useApp } from '../lib/app'
import { useProjects, useRoutableModels, useAgents } from '../lib/queries'
import { fmtMs, fmtUSD } from '../lib/format'
import { Badge, Button, Empty, Field, Input, PageHead, Seal, Select, Textarea, Toggle } from '../components/ui'

interface Turn { role: 'user' | 'assistant'; text: string; meta?: { requestId?: string; ms?: number; ttfb?: number; inTok?: number; outTok?: number; provider?: string; model?: string; error?: string; receipt?: boolean } }

export default function Playground() {
  const { can } = useApp()
  const projects = useProjects()
  const models = useRoutableModels()
  const agents = useAgents()
  const usable = (projects.data ?? []).filter((p) => can({ type: 'project', id: p.id, teamId: p.team_id }, 'member'))
  const [project, setProject] = useState('')
  const [model, setModel] = useState('')
  const [agent, setAgent] = useState('')
  const [system, setSystem] = useState('')
  const [maxTokens, setMaxTokens] = useState(1024)
  const [stream, setStream] = useState(true)
  const [turns, setTurns] = useState<Turn[]>([])
  const [draft, setDraft] = useState('')
  const [busy, setBusy] = useState(false)
  const abort = useRef<AbortController | null>(null)
  const logRef = useRef<HTMLDivElement>(null)

  useEffect(() => { if (!project && usable[0]) setProject(usable[0].id) }, [usable, project])
  useEffect(() => { if (!model && models.data[0]) setModel(models.data[0].name) }, [models.data, model])
  useEffect(() => { logRef.current?.scrollTo({ top: logRef.current.scrollHeight }) }, [turns])

  const send = async () => {
    if (!draft.trim() || busy) return
    const history = [...turns, { role: 'user' as const, text: draft.trim() }]
    setTurns([...history, { role: 'assistant', text: '' }])
    setDraft('')
    setBusy(true)
    const ctrl = new AbortController()
    abort.current = ctrl
    const t0 = performance.now()
    const body: any = { model, max_tokens: maxTokens, stream, messages: history.map((t) => ({ role: t.role, content: t.text })) }
    if (system.trim()) body.system = system
    const url = agent ? `/v1/agents/${agent}/messages` : '/v1/messages'
    const update = (fn: (t: Turn) => Turn) => setTurns((ts) => [...ts.slice(0, -1), fn(ts[ts.length - 1]!)])
    try {
      const res = await fetch(url, { method: 'POST', signal: ctrl.signal, headers: { 'Content-Type': 'application/json', 'X-Theophylax-Client': 'web', 'X-Theophylax-Project': project, 'anthropic-version': '2023-06-01' }, body: JSON.stringify(body) })
      const meta: Turn['meta'] = { requestId: res.headers.get('X-Request-Id') ?? undefined, provider: res.headers.get('X-Theophylax-Provider') ?? undefined, model: res.headers.get('X-Theophylax-Model') ?? undefined }
      if (!res.ok) {
        const e = await res.json().catch(() => ({}))
        update((t) => ({ ...t, meta: { ...meta, error: e?.error?.message ?? res.statusText, ms: performance.now() - t0 } }))
        return
      }
      if (!stream) {
        const j = await res.json()
        const text = (j.content ?? []).filter((b: any) => b.type === 'text').map((b: any) => b.text).join('')
        update(() => ({ role: 'assistant', text, meta: { ...meta, ms: performance.now() - t0, inTok: j.usage?.input_tokens, outTok: j.usage?.output_tokens, receipt: true } }))
        return
      }
      const reader = res.body!.getReader()
      const dec = new TextDecoder()
      let buf = '', ttfb = 0, inTok = 0, outTok = 0, receipt = false
      for (;;) {
        const { value, done } = await reader.read()
        if (done) break
        buf += dec.decode(value, { stream: true })
        let idx
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const chunk = buf.slice(0, idx)
          buf = buf.slice(idx + 2)
          if (chunk.startsWith(': theophylax-receipt')) { receipt = true; continue }
          const data = chunk.split('\n').filter((l) => l.startsWith('data: ')).map((l) => l.slice(6)).join('')
          if (!data) continue
          const ev = JSON.parse(data)
          if (ev.type === 'message_start') inTok = ev.message?.usage?.input_tokens ?? 0
          if (ev.type === 'content_block_delta' && ev.delta?.type === 'text_delta') {
            if (!ttfb) ttfb = performance.now() - t0
            update((t) => ({ ...t, text: t.text + ev.delta.text }))
          }
          if (ev.type === 'message_delta') outTok = ev.usage?.output_tokens ?? outTok
          if (ev.type === 'error') update((t) => ({ ...t, meta: { ...t.meta, error: ev.error?.message } }))
        }
      }
      update((t) => ({ ...t, meta: { ...meta, ...t.meta, ms: performance.now() - t0, ttfb, inTok, outTok, receipt } }))
    } catch (e: any) {
      if (e?.name !== 'AbortError') update((t) => ({ ...t, meta: { error: String(e) } }))
    } finally {
      setBusy(false)
      abort.current = null
    }
  }

  // Estimate from the serving deployment's list price; the exact figure is on the request log.
  const deps = models.data.find((m) => m.name === model)?.deployments ?? []
  const cost = (m?: Turn['meta']) => {
    const p = deps.find((d) => d.provider_name === m?.provider)?.pricing
    return p?.input != null && m?.inTok != null ? ((m.inTok ?? 0) * p.input + (m.outTok ?? 0) * (p.output ?? 0)) / 1e6 : null
  }

  if (!projects.isLoading && !usable.length) {
    return (<><PageHead title="Playground" /><Empty icon={<Send size={20} />} title="You need a project to try models" action={<Link className="btn primary" to="/teams">Go to Teams & projects</Link>}>Playground traffic is attributed to a project, just like API keys. Ask a team admin to add you to one.</Empty></>)
  }

  return (
    <>
      <PageHead title="Playground" lede="Try any routable model with your organization's guardrails and budgets applied. Requests are logged and receipted like any other." />
      <div className="pg">
        <div className="panel" style={{ padding: 16, overflowY: 'auto' }}>
          <Field label="Project" hint="Usage is attributed here.">
            <Select value={project} onChange={(e) => setProject(e.target.value)} options={usable.map((p) => ({ value: p.id, label: `${p.team_name} / ${p.name}` }))} />
          </Field>
          <Field label="Agent" hint="Optional. Applies the agent's prompt, skills and model.">
            <Select value={agent} onChange={(e) => setAgent(e.target.value)} options={[{ value: '', label: 'None' }, ...(agents.data ?? []).filter((a) => a.status === 'active').map((a) => ({ value: a.slug, label: a.name }))]} />
          </Field>
          {!agent && (
            <Field label="Model">
              <Select value={model} onChange={(e) => setModel(e.target.value)} options={models.data.map((m) => ({ value: m.name, label: m.display_name || m.name }))} />
            </Field>
          )}
          <Field label="System prompt"><Textarea value={system} onChange={(e) => setSystem(e.target.value)} placeholder="You are a concise assistant." /></Field>
          <Field label="Max output tokens"><Input type="number" min={1} value={maxTokens} onChange={(e) => setMaxTokens(Number(e.target.value))} /></Field>
          <Toggle checked={stream} onChange={setStream} label="Stream responses" />
        </div>
        <div className="panel chat">
          <div className="chat-log" ref={logRef} aria-live="polite">
            {!turns.length && <div className="muted" style={{ margin: 'auto', textAlign: 'center', maxWidth: '44ch' }}>Send a message to start. Try including an email address to see the data-protection guardrail redact it before it leaves the network.</div>}
            {turns.map((t, i) => (
              <div key={i} className={`msg ${t.role}`}>
                <div className="who">{t.role === 'user' ? 'You' : (t.meta?.model ?? (agent || model))}</div>
                <div className="body">{t.text || (t.role === 'assistant' && busy && i === turns.length - 1 ? '…' : '')}</div>
                {t.meta && (
                  <div className="meta">
                    {t.meta.error && <Badge tone="bad">{t.meta.error}</Badge>}
                    {t.meta.receipt && t.meta.requestId && <Link to={`/requests?id=${t.meta.requestId}`} className="row" style={{ gap: 5 }}><Seal size={15} state="signed" /> Receipt</Link>}
                    {t.meta.provider && <span>via {t.meta.provider}</span>}
                    {t.meta.ms != null && <span>{fmtMs(t.meta.ms)}{t.meta.ttfb ? ` · first token ${fmtMs(t.meta.ttfb)}` : ''}</span>}
                    {t.meta.inTok != null && <span>{t.meta.inTok} in · {t.meta.outTok ?? 0} out</span>}
                    {cost(t.meta) != null && <span>{fmtUSD(cost(t.meta), true)}</span>}
                  </div>
                )}
              </div>
            ))}
          </div>
          <div className="composer">
            <Button variant="ghost" icon onClick={() => setTurns([])} aria-label="Clear conversation" title="Clear conversation"><Eraser size={16} /></Button>
            <Textarea value={draft} onChange={(e) => setDraft(e.target.value)} placeholder="Message (Enter to send, Shift+Enter for a new line)" rows={1}
              onKeyDown={(e) => { if (e.key === 'Enter' && !e.shiftKey) { e.preventDefault(); send() } }} aria-label="Message" />
            {busy ? <Button onClick={() => abort.current?.abort()}><Square size={14} /> Stop</Button> : <Button variant="primary" onClick={send} disabled={!draft.trim() || !project}><Send size={14} /> Send</Button>}
          </div>
        </div>
      </div>
    </>
  )
}
