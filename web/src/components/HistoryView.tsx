import { useCallback, useEffect, useRef, useState } from "react"
import { EVENTS_PAGE_SIZE, fetchEvent, fetchEvents, fetchWaveform, nextCursor, type EventsCursor } from "../lib/api"
import { downloadWaveformCsv } from "../lib/csv"
import { fmtDuration, fmtTime } from "../lib/format"
import { jmaClass } from "../lib/jma"
import { pushQuery, replaceQuery, useQuery } from "../lib/urlState"
import type { EqEvent, WaveformRange } from "../lib/types"
import { Field, IntensityBadge } from "./readout"
import WaveformPlot, { type View } from "./WaveformPlot"

// 1px あたり min/max の2点。サーバー側の上限が 20000
const pointsFor = (width: number) => Math.min(20000, Math.max(500, Math.round(width * 2)))

// 並びは API のカーソルと揃える
function mergeEvents(prev: EqEvent[], next: EqEvent[]): EqEvent[] {
  const byId = new Map(prev.map((e) => [e.id, e]))
  for (const e of next) byId.set(e.id, e)
  return [...byId.values()].sort((a, b) => b.startedAt - a.startedAt || b.id - a.id)
}

export default function HistoryView({ eqCount, serverNow }: { eqCount: number; serverNow: number | null }) {
  const [events, setEvents] = useState<EqEvent[]>([])
  const [cursor, setCursor] = useState<EventsCursor | null>(null)
  const [end, setEnd] = useState(false)
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [sel, setSel] = useState<EqEvent | null>(null)
  const [exporting, setExporting] = useState(false)
  const [wave, setWave] = useState<WaveformRange | null>(null)
  const [bounds, setBounds] = useState<View | null>(null)
  const [view, setView] = useState<View | null>(null)
  const plotBoxRef = useRef<HTMLDivElement>(null)
  const loadingRef = useRef(false)
  const pagedRef = useRef(false) // 2ページ目以降を読み込んだか
  const loadMoreRef = useRef<HTMLDivElement>(null)
  const serverNowRef = useRef(serverNow)
  serverNowRef.current = serverNow
  const eventsRef = useRef(events)
  eventsRef.current = events

  const eventQ = useQuery("event")
  const eventId = Number.isInteger(Number(eventQ)) && Number(eventQ) > 0 ? Number(eventQ) : null

  const openEvent = (e: EqEvent) => pushQuery({ event: String(e.id) })
  const closeEvent = () => pushQuery({ event: null })

  useEffect(() => {
    if (eventId === null) {
      setSel(null)
      return
    }
    const local = eventsRef.current.find((e) => e.id === eventId)
    if (local) {
      setSel(local)
      return
    }
    let alive = true
    fetchEvent(eventId)
      .then((e) => alive && setSel(e))
      .catch(() => alive && replaceQuery({ event: null })) // 消えた記録は開かず URL からも外す
    return () => {
      alive = false
    }
  }, [eventId])

  useEffect(() => {
    let alive = true
    fetchEvents(EVENTS_PAGE_SIZE)
      .then((page) => {
        if (!alive) return
        setEvents((prev) => mergeEvents(prev, page.events))
        setError(null)
        if (pagedRef.current) return // 読み込み済みの続きを巻き戻さない
        const c = nextCursor(page)
        setCursor(c)
        setEnd(c === null)
      })
      .catch((e) => alive && setError(String(e)))
    return () => {
      alive = false
    }
  }, [eqCount])

  const loadMore = useCallback(() => {
    if (loadingRef.current || end || !cursor) return
    loadingRef.current = true
    setLoading(true)
    fetchEvents(EVENTS_PAGE_SIZE, cursor)
      .then((page) => {
        pagedRef.current = true
        setEvents((prev) => mergeEvents(prev, page.events))
        const c = nextCursor(page)
        setCursor(c)
        setEnd(c === null)
        setError(null)
      })
      .catch((e) => setError(String(e)))
      .finally(() => {
        loadingRef.current = false
        setLoading(false)
      })
  }, [cursor, end])

  // 行が増えるたびに張り直す。1ページで画面が埋まらないときはそのまま続きを読む
  useEffect(() => {
    const el = loadMoreRef.current
    if (!el) return
    const io = new IntersectionObserver(
      (entries) => {
        if (entries[0].isIntersecting) loadMore()
      },
      { rootMargin: "300px" },
    )
    io.observe(el)
    return () => io.disconnect()
  }, [events.length, loadMore])

  useEffect(() => {
    setWave(null)
    if (!sel) {
      setBounds(null)
      setView(null)
      return
    }
    // 記録中は終端がないので、クライアント時計ではなくサーバーの現在時刻で切る
    const b = { from: sel.startedAt, to: sel.endedAt ?? serverNowRef.current ?? Date.now() }
    setBounds(b)
    setView(b)
  }, [sel])

  // 表示範囲が変わるたびに引き直して解像度を上げる
  useEffect(() => {
    if (!view) return
    let alive = true
    const ctrl = new AbortController()
    fetchWaveform(view.from, view.to, pointsFor(plotBoxRef.current?.clientWidth ?? 800), ctrl.signal)
      .then((w) => {
        if (!alive) return
        setWave(w)
        setError(null)
      })
      .catch((e: Error) => {
        if (alive && e.name !== "AbortError") setError(String(e))
      })
    return () => {
      alive = false
      ctrl.abort()
    }
  }, [view])

  // モーダル表示中の取り残され防止。PC の右カラムでも閉じられて困らない
  useEffect(() => {
    if (!sel) return
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") closeEvent()
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [sel])

  const zoomed = !!(view && bounds && view.to - view.from < bounds.to - bounds.from - 1)
  const waveGone = wave !== null && wave.segments.length === 0 && !zoomed

  const exportCsv = async () => {
    if (!sel || !bounds || exporting) return
    setExporting(true)
    try {
      await downloadWaveformCsv(bounds.from, bounds.to, `EQMS-${sel.id}.csv`)
    } catch (e) {
      setError(String(e))
    } finally {
      setExporting(false)
    }
  }

  const detail = sel && (
    // スマホは全画面モーダル、lg 以上は右カラムとして固定表示
    <div className="fixed inset-0 z-50 overflow-y-auto bg-[var(--surface-0)] p-4 lg:sticky lg:inset-auto lg:top-4 lg:z-auto lg:min-w-0 lg:flex-1 lg:overflow-visible lg:rounded-lg lg:border lg:border-[var(--border)] lg:bg-[var(--surface-1)]">
      <div className="mb-3 flex items-start justify-between gap-4">
        <div className="flex flex-wrap items-baseline gap-x-6 gap-y-1 text-sm">
          <span className="font-medium">記録 #{sel.id}</span>
          <Field label="検知" value={fmtTime(sel.triggeredAt)} />
          {sel.maxIntensity !== null && (
            <Field label="最大震度" value={`${jmaClass(sel.maxIntensity).label} (${sel.maxIntensity.toFixed(1)})`} />
          )}
          {sel.maxPga !== null && <Field label="最大加速度" value={`${sel.maxPga.toFixed(1)} gal`} />}
          {wave && wave.segments.length > 1 && (
            <span className="text-sm text-[var(--text-muted)]">欠落 {wave.segments.length - 1} 箇所</span>
          )}
        </div>
        <div className="flex shrink-0 gap-2">
          {!waveGone && (
            <button
              className="rounded border border-[var(--border)] px-2 py-1 text-sm text-[var(--text-secondary)] hover:bg-[var(--border)] disabled:opacity-50"
              onClick={exportCsv}
              disabled={exporting}
            >
              CSV保存
            </button>
          )}
          <button
            className="rounded border border-[var(--border)] px-2 py-1 text-sm text-[var(--text-secondary)] hover:bg-[var(--border)] disabled:opacity-50"
            onClick={() => bounds && setView(bounds)}
            disabled={!zoomed}
          >
            全体表示
          </button>
          <button
            className="rounded border border-[var(--border)] px-2 py-1 text-sm text-[var(--text-secondary)] hover:bg-[var(--border)]"
            onClick={closeEvent}
          >
            閉じる
          </button>
        </div>
      </div>

      <div ref={plotBoxRef}>
        {waveGone ? (
          <div className="py-12 text-center text-sm text-[var(--text-muted)]">
            この記録の波形は保持期間を過ぎて削除されています
          </div>
        ) : (
          bounds &&
          view && (
            <WaveformPlot
              key={sel.id} // 記録を切り替えたら前の波形を持ち越さない
              label="加速度 gal"
              range={wave ?? undefined}
              view={view}
              bounds={bounds}
              onViewChange={setView}
            />
          )
        )}
      </div>
    </div>
  )

  return (
    <div className="flex flex-col gap-4">
      {error && <div className="text-sm text-[var(--bad)]">{error}</div>}

      <div className="flex flex-col gap-4 lg:flex-row lg:items-start">
        {/* 一覧は折り返さず内容幅、残りを波形に渡す */}
        <div className="overflow-x-auto rounded-lg border border-[var(--border)] bg-[var(--surface-1)] lg:shrink-0">
          <table className="w-full whitespace-nowrap text-sm">
            <thead>
              <tr className="border-b border-[var(--border)] text-left text-sm text-[var(--text-muted)]">
                <th className="px-4 py-2 font-normal">#</th>
                <th className="px-4 py-2 font-normal">検知時刻</th>
                <th className="px-4 py-2 font-normal">継続時間</th>
                <th className="px-4 py-2 font-normal">最大震度</th>
                <th className="px-4 py-2 font-normal">最大加速度</th>
              </tr>
            </thead>
            <tbody>
              {events.length === 0 && end && (
                <tr>
                  <td colSpan={5} className="px-4 py-8 text-center text-[var(--text-muted)]">
                    地震記録はまだありません
                  </td>
                </tr>
              )}
              {events.map((e) => (
                <tr
                  key={e.id}
                  className={`cursor-pointer border-b border-[var(--border)] last:border-b-0 hover:bg-[var(--border)]/40 ${sel?.id === e.id ? "bg-[var(--border)]/60" : ""
                    }`}
                  onClick={() => openEvent(e)}
                >
                  <td className="px-4 py-2 tabular-nums text-[var(--text-muted)]">{e.id}</td>
                  <td className="px-4 py-2 tabular-nums">{fmtTime(e.triggeredAt)}</td>
                  <td className="px-4 py-2 tabular-nums">
                    {e.endedAt !== null ? (
                      fmtDuration(e.endedAt - e.triggeredAt)
                    ) : (
                      <span className="text-[var(--bad)]">記録中</span>
                    )}
                  </td>
                  <td className="px-4 py-2">
                    {e.maxIntensity !== null ? <IntensityBadge value={e.maxIntensity} /> : "-"}
                  </td>
                  <td className="px-4 py-2 tabular-nums">{e.maxPga !== null ? `${e.maxPga.toFixed(1)} gal` : "-"}</td>
                </tr>
              ))}
            </tbody>
          </table>
          {/* 監視対象。全件読み終えたあとも消さない */}
          <div ref={loadMoreRef} className="py-3 text-center text-sm text-[var(--text-muted)]">
            {loading ? "読み込み中…" : !end && events.length > 0 ? "スクロールで続きを読み込み" : ""}
          </div>
        </div>

        {detail || (
          <div className="hidden min-h-64 flex-1 items-center justify-center rounded-lg border border-dashed border-[var(--border)] text-sm text-[var(--text-muted)] lg:flex">
            一覧から記録を選択すると波形が表示されます
          </div>
        )}
      </div>
    </div>
  )
}
