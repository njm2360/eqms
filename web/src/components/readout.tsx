import { jmaClass } from "../lib/jma"

export function Reading({ label, value, unit }: { label: string; value: string; unit?: string }) {
  return (
    <div>
      <div className="text-sm text-[var(--text-muted)]">{label}</div>
      <div className="text-3xl font-semibold tabular-nums">
        {value}
        {unit && <span className="ml-1 text-sm font-normal text-[var(--text-muted)]">{unit}</span>}
      </div>
    </div>
  )
}

export function Field({ label, value }: { label: string; value: string }) {
  return (
    <span className="whitespace-nowrap">
      <span className="text-[var(--text-muted)]">{label}</span> <span className="tabular-nums">{value}</span>
    </span>
  )
}

export function IntensityBadge({ value }: { value: number }) {
  const c = jmaClass(value)
  return (
    <span className="inline-flex items-baseline gap-2">
      <span
        className="min-w-8 rounded px-1.5 py-0.5 text-center text-sm font-bold"
        style={{ background: c.bg, color: c.fg }}
      >
        {c.label}
      </span>
      <span className="tabular-nums">{value.toFixed(1)}</span>
    </span>
  )
}
