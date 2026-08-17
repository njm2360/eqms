import { useEffect, useId, useLayoutEffect, useRef } from "react"
import uPlot from "uplot"
import { emptyPlot, livePlot, rangePlot, type Plot } from "../lib/plotData"
import type { Sample, WaveformRange } from "../lib/types"

export interface View {
  from: number // ms epoch
  to: number
}

interface Props {
  live?: React.RefObject<Sample[]> // 指定時は最新 spanMs を追従表示する
  spanMs?: number
  range?: WaveformRange
  view?: View // 表示範囲。拡大縮小は親が状態として持つ
  bounds?: View // 引き切れる限界。指定するとホイール/ドラッグ操作が有効になる
  onViewChange?: (view: View) => void
}

const AXES = ["NS", "EW", "UD"] as const

const PANE_MIN_H = 96
const PANE_MAX_H = 220
const BOTTOM_GAP = 16 // 画面下端に残す余白
const EDGE_PAD = 8 // 各段の上下端の目盛りラベルが半分はみ出さない幅
const X_AXIS_H = 28
const Y_AXIS_W = 40 // 非表示tickの既定サイズをsize:0で除いた上で "-2000" が収まる幅
const Y_LABEL_W = 16 // 回転させた軸名の帯。Y_AXIS_Wとは別枠で確保される
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

// 波形は100msごとに届く。右端をそこへ直接合わせると刻んで飛ぶので、実時間で進めて引き込む
const LIVE_LAG_MS = 200
const CATCHUP = 0.03
const CATCHUP_MAX = 0.25 // 大きいと引き込み中の動きが速く見える
const SNAP_MS = 1000
const FREEZE_MS = 300
const MAX_FRAME_MS = 100

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

// 既定のクリップはプロット領域より線幅ぶん広く、枠の外へ波形が漏れる。gaps の除外も引き受ける
const linearPath = uPlot.paths.linear!()
const clipToPlot: uPlot.Series.PathBuilder = (u, si, i0, i1) => {
  const p = linearPath(u, si, i0, i1)
  if (!p) return p
  const { left, top, width, height } = u.bbox
  const right = left + width
  const clip = new Path2D()
  let x = left
  for (const [g0, g1] of p.gaps ?? []) {
    const lo = Math.max(g0, left)
    const hi = Math.min(g1, right)
    if (hi <= lo) continue
    if (lo > x) clip.rect(x, top, lo - x, height)
    x = hi
  }
  if (right > x) clip.rect(x, top, right - x, height)
  p.clip = clip
  return p
}

function clockLabel(sec: number): string {
  const d = new Date(sec * 1000)
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${String(d.getMilliseconds()).padStart(3, "0")}`
}

export default function WaveformPlot({ live, spanMs = 30_000, range, view, bounds, onViewChange }: Props) {
  const interactive = bounds !== undefined
  const readout = live === undefined
  const syncKey = useId()
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
    const series = cssColor("--series")

    const plots = AXES.map((axis, i) => {
      const isLast = i === AXES.length - 1
      const opts: uPlot.Options = {
        width: host.clientWidth || 600,
        height: ph + EDGE_PAD * 2 + (isLast ? X_AXIS_H : 0),
        // 明示しないと、時刻ラベルを出す段だけ右パディングが自動で広がって段がずれる
        padding: [EDGE_PAD, X_LABEL_PAD, EDGE_PAD, 0],
        legend: { show: false },
        cursor: {
          show: readout,
          sync: { key: syncKey },
          drag: { x: false, y: false }, // 範囲選択は使わない。拡大はホイール、移動はドラッグ
          points: { show: false },
          y: false, // 横線は読み取れる情報がない
          // 右端ちょうどだと線の1pxが枠の外へ出る。負値は非表示位置なので通す
          move: (u, left, top) => [left > 0 ? Math.min(left, u.over.clientWidth - 1) : left, top],
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
            ticks: { show: false, size: 0 },
            values: (_u, splits, _ai, _space, incr) =>
              isLast ? splits.map((s) => tickLabel(s, incr)) : splits.map(() => ""),
          },
          {
            size: Y_AXIS_W,
            label: `${axis} (gal)`,
            labelSize: Y_LABEL_W,
            labelFont: "600 12px system-ui",
            stroke: muted,
            font: "12px system-ui",
            grid: { stroke: grid, width: 1 },
            ticks: { show: false, size: 0 },
            // スケール範囲外の値を返すとラベルが枠の外へ出る。全幅はピークちょうどに置く
            splits: () => {
              const p = plotDataRef.current.peak
              return [-p, 0, p]
            },
            values: (_u, splits) => splits.map(fmtScale),
          },
        ],
        // pxAlign:0 で描画前の平行移動が消え、クリップが領域ちょうどに効く。x も丸められない
        series: [
          {},
          { stroke: series, width: 1.25, points: { show: false }, spanGaps: false, pxAlign: 0, paths: clipToPlot },
        ],
        hooks: readout ? { setCursor: [showReadout] } : {},
      }
      // データは後から setData で入れる。スケール未確定のまま描かせない
      const u = new uPlot(opts, [[], []] as uPlot.AlignedData, host)
      u.over.style.cursor = interactive ? "grab" : readout ? "crosshair" : "default"
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
      for (const [i, u] of plots.entries())
        u.setSize({ width: w, height: h + EDGE_PAD * 2 + (i === AXES.length - 1 ? X_AXIS_H : 0) })
    }
    const ro = new ResizeObserver(applySize)
    ro.observe(host)
    // ビューポートの高さだけ変わってもホストの幅は変わらず RO が発火しない
    window.addEventListener("resize", applySize)

    const dropCursor = () => {
      for (const u of plots) u.setCursor({ left: -10, top: -10 })
    }
    window.addEventListener("blur", dropCursor)

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
      window.removeEventListener("blur", dropCursor)
      for (const c of cleanups) c()
      for (const u of plots) u.destroy()
      plotsRef.current = []
      // 作り直したインスタンスへ表示範囲と y スケールを入れ直させる
      viewRef.current = null
      scaledRef.current = false
    }
  }, [syncKey, interactive, readout, live, spanMs])

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

  // ライブ: データはバッファが変わったときだけ組み直し、表示範囲は毎フレーム進める
  useEffect(() => {
    if (!live) return
    let raf = 0
    let key = ""
    let end = 0 // 表示右端 (ms epoch)。0 は未初期化
    let prev = 0
    const tick = (now: number) => {
      raf = requestAnimationFrame(tick)
      const dt = prev === 0 ? 0 : Math.min(now - prev, MAX_FRAME_MS)
      prev = now

      const buf = live.current
      const k = buf.length === 0 ? "" : `${buf.length}:${buf[0].t}:${buf[buf.length - 1].t}`
      if (k !== key) {
        key = k
        pushDataRef.current(livePlot(buf))
      }
      if (buf.length === 0) return

      const err = buf[buf.length - 1].t - LIVE_LAG_MS - end
      if (end === 0 || Math.abs(err) > SNAP_MS) end += err
      else if (err > -FREEZE_MS) end += dt + Math.max(-dt * CATCHUP_MAX, Math.min(dt * CATCHUP_MAX, err * CATCHUP))
      else return
      applyViewRef.current({ from: end - spanMs, to: end }, false)
    }
    raf = requestAnimationFrame(tick)
    return () => cancelAnimationFrame(raf)
  }, [live, spanMs])

  return (
    <div>
      {/* 3軸は一体のまま、狭い幅では時刻とのあいだで折る */}
      {readout && (
        <div className="flex flex-wrap items-baseline justify-end gap-x-3 pb-1 text-sm tabular-nums">
          <span
            ref={timeRef}
            className="inline-block w-[12ch] whitespace-nowrap text-[var(--text-secondary)] empty:hidden"
          />
          <span className="flex items-baseline gap-x-3">
            {AXES.map((axis, i) => (
              <span key={axis} className="whitespace-nowrap">
                <span className="text-[var(--text-muted)]">{axis}</span>{" "}
                {/* 値の桁数が変わっても隣が動かないよう固定幅で右寄せ */}
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
      )}
      <div ref={hostRef} className="wave-host" />
    </div>
  )
}
