// 計測震度から気象庁震度階級への変換。色は気象庁の震度配色。

export interface JmaClass {
  label: string
  bg: string
  fg: string
}

const classes: { min: number; c: JmaClass }[] = [
  { min: 6.5, c: { label: "7", bg: "#b40068", fg: "#ffffff" } },
  { min: 6.0, c: { label: "6強", bg: "#a50021", fg: "#ffffff" } },
  { min: 5.5, c: { label: "6弱", bg: "#ff2800", fg: "#ffffff" } },
  { min: 5.0, c: { label: "5強", bg: "#ff9900", fg: "#111110" } },
  { min: 4.5, c: { label: "5弱", bg: "#ffe600", fg: "#111110" } },
  { min: 3.5, c: { label: "4", bg: "#fae696", fg: "#111110" } },
  { min: 2.5, c: { label: "3", bg: "#0041ff", fg: "#ffffff" } },
  { min: 1.5, c: { label: "2", bg: "#00aaff", fg: "#111110" } },
  { min: 0.5, c: { label: "1", bg: "#f2f2ff", fg: "#111110" } },
]

export const MIN_DISPLAY_INTENSITY = -0.5

export function jmaClass(intensity: number): JmaClass {
  for (const { min, c } of classes) {
    if (intensity >= min) return c
  }
  return { label: "0", bg: "#2e2e2b", fg: "#ffffff" }
}

export function fmtTime(ms: number): string {
  return new Date(ms).toLocaleString("ja-JP", {
    year: "numeric", month: "2-digit", day: "2-digit",
    hour: "2-digit", minute: "2-digit", second: "2-digit",
  })
}

export function fmtDuration(ms: number): string {
  const s = Math.round(ms / 1000)
  if (s < 60) return `${s}秒`
  return `${Math.floor(s / 60)}分${s % 60}秒`
}
