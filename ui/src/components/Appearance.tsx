import { useEffect, useRef, useState } from 'react'
import { Moon, Palette as PaletteIcon, Sun } from 'lucide-react'
import { PALETTES, useTheme, type ThemeMode } from '../lib/app'
import { Segmented } from './ui'

/** Mode (light / dark / system) and palette choice. */
export function AppearancePicker() {
  const { mode, palette, setMode, setPalette } = useTheme()
  return (
    <div className="stack" style={{ gap: 14 }}>
      <div>
        <h4>Mode</h4>
        <Segmented<ThemeMode> value={mode} onChange={setMode} options={[{ value: 'light', label: 'Light' }, { value: 'dark', label: 'Dark' }, { value: 'system', label: 'System' }]} />
      </div>
      <div>
        <h4>Palette</h4>
        <div className="swatches" role="radiogroup" aria-label="Palette">
          {PALETTES.map((p) => (
            <button key={p.id} type="button" role="radio" aria-checked={palette === p.id} className="swatch" onClick={() => setPalette(p.id)}>
              <i style={{ background: p.swatch }} />{p.label}
            </button>
          ))}
        </div>
      </div>
    </div>
  )
}

/** Header button that opens the picker. */
export function AppearanceMenu() {
  const { theme } = useTheme()
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const close = (e: MouseEvent | KeyboardEvent) => {
      if (e instanceof KeyboardEvent ? e.key === 'Escape' : !ref.current?.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', close)
    document.addEventListener('keydown', close)
    return () => { document.removeEventListener('mousedown', close); document.removeEventListener('keydown', close) }
  }, [open])
  return (
    <div ref={ref} style={{ position: 'relative' }}>
      <button className="btn ghost icon" onClick={() => setOpen((o) => !o)} aria-haspopup="dialog" aria-expanded={open} aria-label="Appearance" title="Appearance">
        {theme === 'dark' ? <Moon size={17} /> : <Sun size={17} />}<PaletteIcon size={13} style={{ marginLeft: -3 }} />
      </button>
      {open && <div className="popover appearance" role="dialog" aria-label="Appearance"><AppearancePicker /></div>}
    </div>
  )
}
