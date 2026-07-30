import { useEffect, useId, useLayoutEffect, useRef, useState } from "react"
import uPlot from "uplot"
import { emptyPlot, livePlot, rangePlot, type Plot } from "../lib/plotData"
import type { Sample, WaveformRange } from "../lib/types"

export interface View {
  from: number // ms epoch
  to: number
}

interface Props {
  label: string
  live?: React.RefObject<Sample[]> // 指定時は最新 spanMs を追従表示する
  spanMs?: number
  range?: WaveformRange
  view?: View // 表示範囲。拡大縮小は親が状態として持つ
  bounds?: View // 引き切れる限界。指定するとホイール/ドラッグ操作が有効になる
  onViewChange?: (view: View) => void
}

const AXES = [
  { label: "X", cssVar: "--series-x" },
  { label: "Y", cssVar: "--series-y" },
  { label: "Z", cssVar: "--series-z" },
] as const

const PANE_MIN_H = 96
const PANE_MAX_H = 220
const BOTTOM_GAP = 16 // 画面下端に残す余白
const EDGE_PAD = 8 // 各段の上下端の目盛りラベルが半分はみ出さない幅
const X_AXIS_H = 28
const Y_AXIS_W = 52
const X_LABEL_PAD = 16 // 右端の時刻ラベルが欠けない幅

// 画面の残り高さを段数で割ってプロット領域の高さを決める
function paneHeight(host: HTMLElement): number {
  const avail = window.innerHeight - host.getBoundingClientRect().top - BOTTOM_GAP
  const h = Math.floor((avail - X_AXIS_H) / AXES.length) - EDGE_PAD * 2
  return Math.min(PANE_MAX_H, Math.max(PANE_MIN_H, h))
}

function fmtScale(v: number): string {
  if (v === 0) return "0"
  return (Math.abs(v) >= 10 ? v.toFixed(0) : v.toFixed(1)).replace(/\.0$/, "")
}
const MIN_SPAN_MS = 200 // 生サンプル20点。これ以上拡大しても情報は増えない
const NOTIFY_DELAY_MS = 150

function cssColor(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

const pad = (n: number) => String(n).padStart(2, "0")

// 1 秒未満に拡大したときはラベルが重複しないよう刻み幅に応じたミリ秒を足す。
function tickLabel(sec: number, incrSec: number): string {
  const d = new Date(Math.round(sec * 1000))
  if (incrSec >= 60) return `${pad(d.getHours())}:${pad(d.getMinutes())}`
  const hms = `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`
  if (incrSec >= 1) return hms
  const digits = incrSec >= 0.1 ? 1 : incrSec >= 0.01 ? 2 : 3
  return hms + (d.getMilliseconds() / 1000).toFixed(digits).slice(1)
}

function clockLabel(sec: number): string {
  const d = new Date(sec * 1000)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, "0")}`
}

export default function WaveformPlot({ label, live, spanMs = 30_000, range, view, bounds, onViewChange }: Props) {
  const interactive = bounds !== undefined
  const syncKey = useId()
  const [paneH, setPaneH] = useState(PANE_MIN_H)
  const hostRef = useRef<HTMLDivElement>(null)
  const timeRef = useRef<HTMLSpanElement>(null)
  const valueRefs = useRef<(HTMLSpanElement | null)[]>([])

  const plotsRef = useRef<uPlot[]>([])
  const plotDataRef = useRef<Plot>(emptyPlot())
  const scaledRef = useRef(false) // y スケールを一度でも入れたか
  const viewRef = useRef<View | null>(null)
  const boundsRef = useRef(bounds)
  const viewPropRef = useRef(view)
  const notifyRef = useRef(onViewChange)
  boundsRef.current = bounds
  viewPropRef.current = view
  notifyRef.current = onViewChange

  // スケールはすぐ動かし、親への通知だけ遅らせる
  const applyViewRef = useRef<(v: View, notify?: boolean) => void>(() => {})

  useLayoutEffect(() => {
    const host = hostRef.current
    if (!host) return

    const grid = cssColor("--grid")
    const muted = cssColor("--text-muted")
    let notifyTimer = 0
    let disposed = false

    const applyView = (v: View, notify = true) => {
      if (disposed) return
      let { from, to } = v
      if (to - from < MIN_SPAN_MS) {
        const mid = (from + to) / 2
        from = mid - MIN_SPAN_MS / 2
        to = mid + MIN_SPAN_MS / 2
      }
      const b = boundsRef.current
      if (b) {
        const span = Math.min(to - from, b.to - b.from)
        if (from < b.from) from = b.from
        if (from + span > b.to) from = b.to - span
        to = from + span
      }
      viewRef.current = { from, to }
      for (const u of plotsRef.current) u.setScale("x", { min: from / 1000, max: to / 1000 })
      if (!notify) return
      clearTimeout(notifyTimer)
      notifyTimer = setTimeout(() => notifyRef.current?.({ from: Math.floor(from), to: Math.ceil(to) }), NOTIFY_DELAY_MS)
    }
    applyViewRef.current = applyView

    const showReadout = (u: uPlot) => {
      const idx = u.cursor.idx
      const { data } = plotDataRef.current
      const has = idx != null && idx >= 0 && idx < data[0].length
      if (timeRef.current) timeRef.current.textContent = has ? clockLabel(data[0][idx!]) : ""
      valueRefs.current.forEach((el, a) => {
        if (!el) return
        const v = has ? data[a + 1][idx!] : null
        el.textContent = has && v !== null ? v.toFixed(2) : "--"
      })
    }

    const ph = paneHeight(host)
    setPaneH(ph)

    const plots = AXES.map((axis, i) => {
      const isLast = i === AXES.length - 1
      const stroke = cssColor(axis.cssVar)
      const opts: uPlot.Options = {
        width: host.clientWidth || 600,
        height: ph + EDGE_PAD * 2 + (isLast ? X_AXIS_H : 0),
        // 明示しないと、時刻ラベルを出す段だけ右パディングが自動で広がって段がずれる
        padding: [EDGE_PAD, X_LABEL_PAD, EDGE_PAD, 0],
        legend: { show: false },
        cursor: {
          sync: { key: syncKey },
          drag: { x: false, y: false }, // 範囲選択は使わない。拡大はホイール、移動はドラッグ
          points: { show: false },
          y: false, // 横線は読み取れる情報がない
        },
        // どちらも setScale で明示的に入れる。auto に任せると 3 段でスケールが割れる
        scales: {
          x: { time: true, auto: false, range: (_u, min, max) => [min, max] },
          y: { auto: false, range: (_u, min, max) => [min, max] },
        },
        axes: [
          {
            // 時刻ラベルは最下段だけ。グリッドは全段に要るので軸自体は消さず高さを0にする
            size: isLast ? X_AXIS_H : 0,
            space: 90, // 最長の HH:MM:SS.mmm ラベルが隣と被らない間隔
            stroke: muted,
            font: "12px system-ui",
            grid: { stroke: grid, width: 1 },
            ticks: { show: false },
            values: (_u, splits, _ai, _space, incr) =>
              isLast ? splits.map((s) => tickLabel(s, incr)) : splits.map(() => ""),
          },
          {
            size: Y_AXIS_W,
            stroke: muted,
            font: "12px system-ui",
            grid: { stroke: grid, width: 1 },
            ticks: { show: false },
            // スケール範囲外の値を返すとラベルが枠の外へ出る。全幅はピークちょうどに置く
            splits: () => {
              const p = plotDataRef.current.peak
              return [-p, 0, p]
            },
            values: (_u, splits) => splits.map(fmtScale),
          },
        ],
        series: [{}, { stroke, width: 1.25, points: { show: false }, spanGaps: false }],
        hooks: { setCursor: [showReadout] },
      }
      // データは後から setData で入れる。スケール未確定のまま描かせない
      const u = new uPlot(opts, [[], []] as uPlot.AlignedData, host)
      u.over.style.cursor = interactive ? "grab" : "crosshair"
      return u
    })
    plotsRef.current = plots

    // 先に表示範囲を入れる。x が未設定だと setData が勝手に自動スケールする
    const initial = viewPropRef.current ?? boundsRef.current
    if (initial) {
      applyView(initial, false)
    } else if (live) {
      const buf = live.current
      const end = buf.length > 0 ? buf[buf.length - 1].t : Date.now()
      applyView({ from: end - spanMs, to: end }, false)
    }

    // 作り直し時は直近のデータを入れ直す
    const cur = plotDataRef.current
    if (cur.data[0].length > 0) {
      for (const [i, u] of plots.entries()) {
        u.setData([cur.data[0], cur.data[i + 1]] as uPlot.AlignedData, true)
        u.setScale("y", { min: -cur.peak, max: cur.peak })
      }
      scaledRef.current = true
    }

    let lastW = host.clientWidth
    let lastPaneH = ph
    const applySize = () => {
      const w = host.clientWidth
      if (w === 0) return
      const h = paneHeight(host)
      if (w === lastW && h === lastPaneH) return
      lastW = w
      lastPaneH = h
      setPaneH(h)
      for (const [i, u] of plots.entries())
        u.setSize({ width: w, height: h + EDGE_PAD * 2 + (i === AXES.length - 1 ? X_AXIS_H : 0) })
    }
    const ro = new ResizeObserver(applySize)
    ro.observe(host)
    // ビューポートの高さだけ変わってもホストの幅は変わらず RO が発火しない
    window.addEventListener("resize", applySize)

    const cleanups: (() => void)[] = []
    if (interactive) {
      for (const u of plots) {
        const onWheel = (e: WheelEvent) => {
          e.preventDefault()
          const v = viewRef.current
          if (!v) return
          const rect = u.over.getBoundingClientRect()
          const frac = Math.min(1, Math.max(0, (e.clientX - rect.left) / rect.width))
          const at = v.from + (v.to - v.from) * frac
          const k = e.deltaY < 0 ? 0.8 : 1.25
          applyView({ from: at - (at - v.from) * k, to: at + (v.to - at) * k })
        }
        const onDouble = () => {
          const b = boundsRef.current
          if (b) applyView(b)
        }

        // ドラッグ移動とピンチ拡大。マウスもタッチも Pointer Events で扱う。
        // 指の下の時刻が動かないように、指の数が変わるたびに基準を取り直す
        const pointers = new Map<number, number>() // pointerId -> 幅に対する割合
        let base: View | null = null
        let baseFracs: number[] = []
        let lastTap = 0
        const frac = (e: PointerEvent) => {
          const rect = u.over.getBoundingClientRect()
          return (e.clientX - rect.left) / rect.width
        }
        const rebase = () => {
          base = viewRef.current
          baseFracs = [...pointers.values()]
        }
        const onDown = (e: PointerEvent) => {
          if (e.pointerType === "mouse" && e.button !== 0) return
          if (e.pointerType !== "mouse" && pointers.size === 0) {
            // タッチには dblclick が来ないので自前でダブルタップ判定
            const now = performance.now()
            if (now - lastTap < 300) onDouble()
            lastTap = now
          }
          u.over.setPointerCapture(e.pointerId)
          pointers.set(e.pointerId, frac(e))
          rebase()
          u.over.style.cursor = "grabbing"
        }
        const onMove = (e: PointerEvent) => {
          if (!pointers.has(e.pointerId) || !base) return
          pointers.set(e.pointerId, frac(e))
          const cur = [...pointers.values()]
          const span = base.to - base.from
          if (cur.length >= 2 && Math.abs(cur[1] - cur[0]) > 0.01) {
            const newSpan = (span * Math.abs(baseFracs[1] - baseFracs[0])) / Math.abs(cur[1] - cur[0])
            const from = base.from + ((baseFracs[0] + baseFracs[1]) / 2) * span - ((cur[0] + cur[1]) / 2) * newSpan
            applyView({ from, to: from + newSpan })
          } else {
            const from = base.from + (baseFracs[0] - cur[0]) * span
            applyView({ from, to: from + span })
          }
        }
        const onUp = (e: PointerEvent) => {
          if (!pointers.delete(e.pointerId)) return
          rebase()
          if (pointers.size === 0) u.over.style.cursor = "grab"
        }

        u.over.style.touchAction = "none" // ブラウザのスクロール/ズームに取られない
        u.over.addEventListener("wheel", onWheel, { passive: false })
        u.over.addEventListener("pointerdown", onDown)
        u.over.addEventListener("pointermove", onMove)
        u.over.addEventListener("pointerup", onUp)
        u.over.addEventListener("pointercancel", onUp)
        u.over.addEventListener("dblclick", onDouble)
        cleanups.push(() => {
          u.over.removeEventListener("wheel", onWheel)
          u.over.removeEventListener("pointerdown", onDown)
          u.over.removeEventListener("pointermove", onMove)
          u.over.removeEventListener("pointerup", onUp)
          u.over.removeEventListener("pointercancel", onUp)
          u.over.removeEventListener("dblclick", onDouble)
        })
      }
    }

    return () => {
      disposed = true
      clearTimeout(notifyTimer)
      ro.disconnect()
      window.removeEventListener("resize", applySize)
      for (const c of cleanups) c()
      for (const u of plots) u.destroy()
      plotsRef.current = []
      // 作り直したインスタンスへ表示範囲と y スケールを入れ直させる
      viewRef.current = null
      scaledRef.current = false
    }
  }, [syncKey, interactive, live, spanMs])

  // 各インスタンスは1系列なので、共有の x と自分の軸だけを渡す。
  // y は 3 段共有なので、データと一緒に必ず入れ直す
  const pushData = (plot: Plot) => {
    const peakChanged = Math.abs(plot.peak - plotDataRef.current.peak) > 1e-6 || !scaledRef.current
    plotDataRef.current = plot
    scaledRef.current = true
    for (const [i, u] of plotsRef.current.entries()) {
      // resetScales=false だと可視インデックスが前のデータのまま残り再描画も走らない。
      // true でも x.auto=false なので、現在の表示範囲がそのまま再適用されるだけ
      u.setData([plot.data[0], plot.data[i + 1]] as uPlot.AlignedData, true)
      if (peakChanged) u.setScale("y", { min: -plot.peak, max: plot.peak })
    }
  }
  const pushDataRef = useRef(pushData)
  pushDataRef.current = pushData

  // 履歴: データ差し替え。スケールは維持したまま解像度だけ上げ直す
  useEffect(() => {
    if (!range) return
    pushDataRef.current(rangePlot(range))
  }, [range])

  useEffect(() => {
    if (!view) return
    const cur = viewRef.current
    if (cur && Math.abs(cur.from - view.from) < 1 && Math.abs(cur.to - view.to) < 1) return
    applyViewRef.current(view, false)
  }, [view])

  // ライブ: バッファが変わったときだけ組み直す
  useEffect(() => {
    if (!live) return
    let raf = 0
    let key = ""
    const tick = () => {
      const buf = live.current
      const k = buf.length === 0 ? "" : `${buf.length}:${buf[0].t}:${buf[buf.length - 1].t}`
      if (k !== key) {
        key = k
        pushDataRef.current(livePlot(buf))
        const end = buf.length > 0 ? buf[buf.length - 1].t : Date.now()
        applyViewRef.current({ from: end - spanMs, to: end }, false)
      }
      raf = requestAnimationFrame(tick)
    }
    tick()
    return () => cancelAnimationFrame(raf)
  }, [live, spanMs])

  return (
    <div>
      <div className="flex items-baseline justify-between gap-4 pb-1 text-sm text-[var(--text-muted)]">
        <span>{label}</span>
        <span className="flex items-baseline gap-x-3 tabular-nums">
          {/* 値の桁数が変わっても隣が動かないよう固定幅で右寄せ */}
          <span ref={timeRef} className="inline-block w-[12ch] whitespace-nowrap" />
          {AXES.map((axis, i) => (
            <span key={axis.label} className="whitespace-nowrap">
              <span style={{ color: `var(${axis.cssVar})` }}>{axis.label}</span>{" "}
              <span
                className="inline-block w-[7ch] text-right"
                ref={(el) => {
                  valueRefs.current[i] = el
                }}
              />
            </span>
          ))}
        </span>
      </div>
      <div ref={hostRef} className="wave-host relative">
        {AXES.map((axis, i) => (
          <span
            key={axis.label}
            className="pointer-events-none absolute z-10 text-xs font-bold"
            style={{ color: `var(${axis.cssVar})`, left: Y_AXIS_W + 6, top: i * (paneH + EDGE_PAD * 2) + EDGE_PAD + 3 }}
          >
            {axis.label}
          </span>
        ))}
      </div>
    </div>
  )
}
