import type { ReactNode } from 'react'

/** Small, safe Markdown renderer for skill docs: headings, lists, code, inline code, bold, links. */
export function Markdown({ text }: { text: string }) {
  const src = text.replace(/^---\n[\s\S]*?\n---\n?/, '') // strip frontmatter
  const lines = src.split('\n')
  const out: ReactNode[] = []
  let i = 0
  let key = 0
  while (i < lines.length) {
    const l = lines[i]!
    if (l.startsWith('```')) {
      const buf: string[] = []
      i++
      while (i < lines.length && !lines[i]!.startsWith('```')) buf.push(lines[i++]!)
      i++
      out.push(<pre key={key++}>{buf.join('\n')}</pre>)
      continue
    }
    const h = /^(#{1,3})\s+(.*)$/.exec(l)
    if (h) {
      const level = h[1]!.length
      out.push(level === 1 ? <h2 key={key++}>{inline(h[2]!)}</h2> : level === 2 ? <h3 key={key++}>{inline(h[2]!)}</h3> : <h3 key={key++} style={{ fontSize: 14 }}>{inline(h[2]!)}</h3>)
      i++
      continue
    }
    if (/^\s*[-*]\s+/.test(l)) {
      const items: string[] = []
      while (i < lines.length && /^\s*[-*]\s+/.test(lines[i]!)) items.push(lines[i++]!.replace(/^\s*[-*]\s+/, ''))
      out.push(<ul key={key++}>{items.map((t, k) => <li key={k}>{inline(t)}</li>)}</ul>)
      continue
    }
    if (/^\s*\d+\.\s+/.test(l)) {
      const items: string[] = []
      while (i < lines.length && /^\s*\d+\.\s+/.test(lines[i]!)) items.push(lines[i++]!.replace(/^\s*\d+\.\s+/, ''))
      out.push(<ol key={key++}>{items.map((t, k) => <li key={k}>{inline(t)}</li>)}</ol>)
      continue
    }
    if (!l.trim()) { i++; continue }
    const para: string[] = []
    while (i < lines.length && lines[i]!.trim() && !/^(#{1,3}\s|```|\s*[-*]\s|\s*\d+\.\s)/.test(lines[i]!)) para.push(lines[i++]!)
    out.push(<p key={key++} style={{ margin: '0 0 10px' }}>{inline(para.join(' '))}</p>)
  }
  return <div className="md">{out}</div>
}

function inline(s: string): ReactNode[] {
  const parts: ReactNode[] = []
  const re = /(`[^`]+`|\*\*[^*]+\*\*|\[[^\]]+\]\((https?:\/\/[^)\s]+)\))/g
  let last = 0
  let m: RegExpExecArray | null
  let k = 0
  while ((m = re.exec(s))) {
    if (m.index > last) parts.push(s.slice(last, m.index))
    const t = m[0]
    if (t.startsWith('`')) parts.push(<code key={k++}>{t.slice(1, -1)}</code>)
    else if (t.startsWith('**')) parts.push(<b key={k++}>{t.slice(2, -2)}</b>)
    else parts.push(<a key={k++} href={m[2]} target="_blank" rel="noreferrer noopener">{t.slice(1, t.indexOf(']'))}</a>)
    last = m.index + t.length
  }
  if (last < s.length) parts.push(s.slice(last))
  return parts
}
