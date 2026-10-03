import { useEffect, useRef, useState, type ReactNode, type ButtonHTMLAttributes, type InputHTMLAttributes, type SelectHTMLAttributes, type TextareaHTMLAttributes } from 'react'
import { Check, Copy, X } from 'lucide-react'
import { useMutation, useQueryClient, type QueryKey } from '@tanstack/react-query'
import { errMsg, useToast } from '../lib/app'

// ── Buttons ───────────────────────────────────────────────────────────────
type BtnProps = ButtonHTMLAttributes<HTMLButtonElement> & { variant?: 'primary' | 'danger' | 'ghost'; size?: 'sm'; icon?: boolean; busy?: boolean }
export function Button({ variant, size, icon, busy, className = '', children, disabled, ...rest }: BtnProps) {
  return (
    <button type="button" className={`btn ${variant ?? ''} ${size ?? ''} ${icon ? 'icon' : ''} ${className}`} disabled={disabled || busy} {...rest}>
      {children}
    </button>
  )
}

export function CopyButton({ text, label = 'Copy', size = 'sm' as const }: { text: string; label?: string; size?: 'sm' }) {
  const [done, setDone] = useState(false)
  return (
    <Button size={size} onClick={() => { navigator.clipboard?.writeText(text); setDone(true); setTimeout(() => setDone(false), 1500) }} aria-label={label}>
      {done ? <Check size={14} /> : <Copy size={14} />} {done ? 'Copied' : label}
    </Button>
  )
}

// ── Form fields ───────────────────────────────────────────────────────────
export function Field({ label, hint, error, children, htmlFor }: { label: ReactNode; hint?: ReactNode; error?: string; children: ReactNode; htmlFor?: string }) {
  return (
    <div className="field">
      <label htmlFor={htmlFor}>{label}</label>
      {children}
      {error ? <div className="err">{error}</div> : hint ? <div className="hint">{hint}</div> : null}
    </div>
  )
}
export const Input = (p: InputHTMLAttributes<HTMLInputElement>) => <input {...p} className={`input ${p.className ?? ''}`} />
export const Textarea = (p: TextareaHTMLAttributes<HTMLTextAreaElement>) => <textarea {...p} className={`textarea ${p.className ?? ''}`} />
export function Select({ options, ...p }: SelectHTMLAttributes<HTMLSelectElement> & { options: { value: string; label: string }[] }) {
  return (
    <select {...p} className={`select ${p.className ?? ''}`}>
      {options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  )
}

export function Toggle({ checked, onChange, label, disabled }: { checked: boolean; onChange: (v: boolean) => void; label?: ReactNode; disabled?: boolean }) {
  return (
    <label className="toggle">
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="track" />
      {label && <span>{label}</span>}
    </label>
  )
}

export function Segmented<T extends string>({ value, onChange, options }: { value: T; onChange: (v: T) => void; options: { value: T; label: string }[] }) {
  return (
    <div className="segmented" role="radiogroup">
      {options.map((o) => (
        <button key={o.value} role="radio" aria-checked={value === o.value} className={value === o.value ? 'on' : ''} onClick={() => onChange(o.value)}>{o.label}</button>
      ))}
    </div>
  )
}

/** Free-text tag entry (Enter or comma to add). */
export function ChipsInput({ value, onChange, placeholder, suggestions }: { value: string[]; onChange: (v: string[]) => void; placeholder?: string; suggestions?: string[] }) {
  const [draft, setDraft] = useState('')
  const listId = useRef('dl' + Math.random().toString(36).slice(2)).current
  const add = (s: string) => {
    const v = s.trim()
    if (v && !value.includes(v)) onChange([...value, v])
    setDraft('')
  }
  return (
    <div className="chips">
      {value.map((v) => (
        <span className="chip" key={v}>{v}<button aria-label={`Remove ${v}`} onClick={() => onChange(value.filter((x) => x !== v))}><X size={12} /></button></span>
      ))}
      <input value={draft} list={suggestions ? listId : undefined} placeholder={value.length ? '' : placeholder}
        onChange={(e) => setDraft(e.target.value)}
        onKeyDown={(e) => {
          if ((e.key === 'Enter' || e.key === ',') && draft.trim()) { e.preventDefault(); add(draft) }
          if (e.key === 'Backspace' && !draft && value.length) onChange(value.slice(0, -1))
        }}
        onBlur={() => draft && add(draft)} />
      {suggestions && <datalist id={listId}>{suggestions.map((s) => <option key={s} value={s} />)}</datalist>}
    </div>
  )
}

/** Pick several from a known list. */
export function MultiPick({ value, onChange, options, empty }: { value: string[]; onChange: (v: string[]) => void; options: { value: string; label: string; sub?: string }[]; empty?: string }) {
  if (!options.length) return <div className="hint muted">{empty ?? 'Nothing to choose from yet.'}</div>
  return (
    <div className="panel" style={{ maxHeight: 220, overflowY: 'auto' }}>
      {options.map((o) => (
        <label key={o.value} className="row" style={{ padding: '8px 12px', borderBottom: '1px solid var(--line)', cursor: 'pointer' }}>
          <input type="checkbox" checked={value.includes(o.value)} onChange={(e) => onChange(e.target.checked ? [...value, o.value] : value.filter((x) => x !== o.value))} />
          <span>{o.label}{o.sub && <span className="muted"> — {o.sub}</span>}</span>
        </label>
      ))}
    </div>
  )
}

// ── Layout pieces ─────────────────────────────────────────────────────────
export function PageHead({ title, lede, actions }: { title: string; lede?: ReactNode; actions?: ReactNode }) {
  useEffect(() => { document.title = `${title} · Theophylax` }, [title])
  return (
    <div className="page-head">
      <div>
        <h1>{title}</h1>
        {lede && <p className="lede">{lede}</p>}
      </div>
      {actions && <div className="actions">{actions}</div>}
    </div>
  )
}

export function Panel({ title, sub, end, children, pad = true }: { title?: ReactNode; sub?: ReactNode; end?: ReactNode; children: ReactNode; pad?: boolean }) {
  return (
    <section className="panel">
      {(title || end) && (
        <div className="panel-head">
          <div><h3>{title}</h3>{sub && <div className="sub">{sub}</div>}</div>
          {end && <div className="end">{end}</div>}
        </div>
      )}
      {pad ? <div className="panel-body">{children}</div> : children}
    </section>
  )
}

export function Empty({ icon, title, children, action }: { icon: ReactNode; title: string; children?: ReactNode; action?: ReactNode }) {
  return (
    <div className="empty">
      <div className="glyph">{icon}</div>
      <h3>{title}</h3>
      {children && <p>{children}</p>}
      {action}
    </div>
  )
}

export function Badge({ tone, children, title }: { tone?: 'ok' | 'warn' | 'bad' | 'accent' | 'bronze'; children: ReactNode; title?: string }) {
  return <span className={`badge ${tone ?? ''}`} title={title}>{children}</span>
}

export function StatusBadge({ status }: { status: number }) {
  if (status < 400) return <Badge tone="ok">{status}</Badge>
  if (status === 429) return <Badge tone="warn">{status}</Badge>
  return <Badge tone="bad">{status}</Badge>
}

export function RoleBadge({ role }: { role: string }) {
  const tone = role === 'owner' ? 'bronze' : role === 'admin' ? 'accent' : undefined
  return <Badge tone={tone as any}>{role || 'none'}</Badge>
}

export function Tabs<T extends string>({ value, onChange, tabs }: { value: T; onChange: (v: T) => void; tabs: { value: T; label: ReactNode }[] }) {
  return (
    <div className="tabs" role="tablist">
      {tabs.map((t) => <button key={t.value} role="tab" aria-selected={t.value === value} className={t.value === value ? 'on' : ''} onClick={() => onChange(t.value)}>{t.label}</button>)}
    </div>
  )
}

export function Meter({ pct, progress }: { pct: number; progress?: boolean }) {
  const tone = progress ? '' : pct >= 100 ? 'bad' : pct >= 80 ? 'warn' : ''
  return <div className={`meter ${tone}`} role="meter" aria-valuenow={Math.round(pct)} aria-valuemin={0} aria-valuemax={100}><i style={{ width: `${Math.min(100, Math.max(1, pct))}%` }} /></div>
}

export function CodeBlock({ code }: { code: string }) {
  return (
    <div className="code-block">
      <span className="copy"><CopyButton text={code} /></span>
      {code}
    </div>
  )
}

// ── Overlays ──────────────────────────────────────────────────────────────
export function Drawer({ open, onClose, title, sub, children, footer, wide }: { open: boolean; onClose: () => void; title: ReactNode; sub?: ReactNode; children: ReactNode; footer?: ReactNode; wide?: boolean }) {
  useEffect(() => {
    if (!open) return
    const h = (e: KeyboardEvent) => e.key === 'Escape' && onClose()
    window.addEventListener('keydown', h)
    return () => window.removeEventListener('keydown', h)
  }, [open, onClose])
  if (!open) return null
  return (
    <>
      <div className="scrim" onClick={onClose} />
      <aside className={`drawer ${wide ? 'wide' : ''}`} role="dialog" aria-modal="true">
        <div className="drawer-head">
          <div style={{ flex: 1, minWidth: 0 }}><h2>{title}</h2>{sub && <div className="sub">{sub}</div>}</div>
          <Button variant="ghost" icon onClick={onClose} aria-label="Close"><X size={18} /></Button>
        </div>
        <div className="drawer-body">{children}</div>
        {footer && <div className="drawer-foot">{footer}</div>}
      </aside>
    </>
  )
}

export function Confirm({ open, title, body, confirmLabel, danger, onConfirm, onClose, busy }: { open: boolean; title: string; body: ReactNode; confirmLabel: string; danger?: boolean; onConfirm: () => void; onClose: () => void; busy?: boolean }) {
  if (!open) return null
  return (
    <>
      <div className="scrim" onClick={onClose} style={{ zIndex: 69 }} />
      <div className="dialog" role="alertdialog" aria-modal="true">
        <h2>{title}</h2>
        <p>{body}</p>
        <div className="row" style={{ justifyContent: 'flex-end' }}>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant={danger ? 'danger' : 'primary'} onClick={onConfirm} busy={busy} autoFocus>{confirmLabel}</Button>
        </div>
      </div>
    </>
  )
}

/** Mutation that toasts a past-tense confirmation and refreshes queries. */
export function useAction<TVars, TRes = unknown>(fn: (v: TVars) => Promise<TRes>, opts: { done?: string | ((r: TRes) => string); invalidate?: QueryKey[]; onSuccess?: (r: TRes) => void } = {}) {
  const toast = useToast()
  const qc = useQueryClient()
  return useMutation({
    mutationFn: fn,
    onSuccess: (r) => {
      if (opts.done) toast(typeof opts.done === 'function' ? opts.done(r) : opts.done)
      opts.invalidate?.forEach((k) => qc.invalidateQueries({ queryKey: k }))
      opts.onSuccess?.(r)
    },
    onError: (e) => toast(errMsg(e), 'error'),
  })
}

export function Loading({ label = 'Loading' }: { label?: string }) {
  return <div className="muted" style={{ padding: 24 }}>{label}…</div>
}

export function ErrorNote({ error }: { error: unknown }) {
  return <div className="callout bad" style={{ margin: 16 }}>{errMsg(error)}</div>
}

// ── The seal: Theophylax's mark of attestation ─────────────────────────────
export function Seal({ size = 18, state = 'anchored', title }: { size?: number; state?: 'anchored' | 'signed' | 'pending' | 'broken'; title?: string }) {
  const cls = state === 'pending' ? 'pending' : state === 'broken' ? 'broken' : ''
  const filled = state === 'anchored'
  return (
    <span className={`seal ${cls}`} title={title ?? { anchored: 'Signed and anchored', signed: 'Signed receipt', pending: 'Receipt pending', broken: 'Receipt failed verification' }[state]}>
      <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true">
        <path d="M8 1.5h8L22.5 8v8L16 22.5H8L1.5 16V8z" fill={filled ? 'currentColor' : 'none'} stroke="currentColor" strokeWidth="1.6" />
        <ellipse cx="12" cy="12" rx="4.2" ry="5.8" fill="none" stroke={filled ? 'var(--surface)' : 'currentColor'} strokeWidth="1.7" />
        <path d="M8.2 12h7.6" stroke={filled ? 'var(--surface)' : 'currentColor'} strokeWidth="1.7" />
      </svg>
    </span>
  )
}

export function Logo({ size = 26 }: { size?: number }) {
  return (
    <svg width={size} height={size} viewBox="0 0 32 32" aria-hidden="true">
      <path d="M10.6 2h10.8L30 10.6v10.8L21.4 30H10.6L2 21.4V10.6z" fill="var(--accent)" />
      <ellipse cx="16" cy="16" rx="6.2" ry="8.4" fill="none" stroke="#fff" strokeWidth="2.4" />
      <path d="M10.4 16h11.2" stroke="#fff" strokeWidth="2.4" />
    </svg>
  )
}
