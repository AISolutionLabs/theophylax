export const fmtInt = (n: number | undefined | null) => (n ?? 0).toLocaleString('en-US')

export function fmtCompact(n: number | undefined | null) {
  const v = n ?? 0
  if (Math.abs(v) >= 1e9) return (v / 1e9).toFixed(1).replace(/\.0$/, '') + 'B'
  if (Math.abs(v) >= 1e6) return (v / 1e6).toFixed(1).replace(/\.0$/, '') + 'M'
  if (Math.abs(v) >= 1e4) return (v / 1e3).toFixed(1).replace(/\.0$/, '') + 'k'
  return v.toLocaleString('en-US')
}

export function fmtUSD(n: number | undefined | null, precise = false) {
  const v = n ?? 0
  if (v === 0) return '$0'
  if (v < 0.01) return precise ? '$' + v.toFixed(6) : '<$0.01'
  if (v < 100) return '$' + v.toFixed(2)
  return '$' + Math.round(v).toLocaleString('en-US')
}

/** USD per million tokens, e.g. "$3" or "$0.075". */
export const fmtRate = (n: number | null | undefined) => (n == null ? '—' : `$${+n.toPrecision(6)}`)

export function fmtMs(ms: number | undefined | null) {
  const v = ms ?? 0
  if (v >= 10000) return (v / 1000).toFixed(0) + 's'
  if (v >= 1000) return (v / 1000).toFixed(1) + 's'
  return Math.round(v) + 'ms'
}

export function fmtTime(s?: string | null) {
  if (!s) return '—'
  const d = new Date(s)
  return d.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

export function fmtAgo(s?: string | null) {
  if (!s) return 'never'
  const sec = (Date.now() - new Date(s).getTime()) / 1000
  if (sec < 60) return 'just now'
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`
  if (sec < 86400 * 30) return `${Math.floor(sec / 86400)}d ago`
  return new Date(s).toLocaleDateString()
}

export function pctChange(cur: number, prev: number) {
  if (!prev) return cur ? null : 0
  return ((cur - prev) / prev) * 100
}

export const initials = (s: string) =>
  s.split(/[\s@._-]+/).filter(Boolean).slice(0, 2).map((p) => p[0]!.toUpperCase()).join('')

export const kindLabel: Record<string, string> = {
  anthropic: 'Anthropic', bedrock: 'AWS Bedrock', 'azure-foundry': 'Azure AI Foundry', 'azure-openai': 'Azure OpenAI', openai: 'OpenAI-compatible', custom: 'Custom credential', mock: 'Built-in mock',
  skill: 'Skill', mcp_server: 'MCP server', agent: 'Agent template', prompt: 'Prompt', plugin: 'Plugin',
}

export function download(filename: string, content: string, type = 'text/csv') {
  const url = URL.createObjectURL(new Blob([content], { type }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 1000)
}

export function toCSV(rows: Record<string, unknown>[]) {
  if (!rows.length) return ''
  const cols = Object.keys(rows[0]!)
  const esc = (v: unknown) => {
    const s = v === null || v === undefined ? '' : typeof v === 'object' ? JSON.stringify(v) : String(v)
    return /[",\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
  }
  return [cols.join(','), ...rows.map((r) => cols.map((c) => esc(r[c])).join(','))].join('\n')
}
