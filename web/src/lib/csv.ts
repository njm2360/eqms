import { fetchWaveform } from "./api"

// 18000サンプル。サーバー上限の20000点に収まり間引きされない
const RAW_WINDOW_MS = 180_000

// 生サンプルの CSV。全期間を一度に引くと間引かれるため、窓に分けて集める
export async function downloadWaveformCsv(from: number, to: number, filename: string) {
  const rows = ["t_ms,ns_gal,ew_gal,ud_gal"]
  for (let t = from; t < to; t += RAW_WINDOW_MS) {
    const w = await fetchWaveform(t, Math.min(t + RAW_WINDOW_MS, to), 20000)
    for (const s of w.segments) {
      if (!s.x || !s.y || !s.z) continue
      for (let i = 0; i < s.n; i++) rows.push(`${s.t0 + i * s.dt},${s.x[i]},${s.y[i]},${s.z[i]}`)
    }
  }
  const url = URL.createObjectURL(new Blob([rows.join("\n")], { type: "text/csv" }))
  const a = document.createElement("a")
  a.href = url
  a.download = filename
  a.click()
  URL.revokeObjectURL(url)
}
