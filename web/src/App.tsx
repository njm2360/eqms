import { useEffect } from "react"
import HistoryView from "./components/HistoryView"
import IntensityPanel from "./components/IntensityPanel"
import WaveformPlot from "./components/WaveformPlot"
import { jmaClass, MIN_DISPLAY_INTENSITY } from "./lib/jma"
import { pushQuery, useQuery } from "./lib/urlState"
import { useStream } from "./lib/useStream"

type Tab = "realtime" | "history"

const TABS: [Tab, string][] = [
  ["realtime", "リアルタイム"],
  ["history", "地震記録"],
]

const BASE_TITLE = "EQMS 地震計モニター"

export default function App() {
  const { status, intensity, streamOk, eqCount, waveRef } = useStream()
  const tab: Tab = useQuery("tab") === "history" ? "history" : "realtime"

  const raw = intensity?.intensity ?? status?.intensity ?? null
  const intensityNow = raw !== null && raw >= MIN_DISPLAY_INTENSITY ? raw : null
  useEffect(() => {
    document.title =
      intensityNow !== null ? `震度${jmaClass(intensityNow).label} (${intensityNow.toFixed(1)}) - ${BASE_TITLE}` : BASE_TITLE
  }, [intensityNow])

  const switchTab = (t: Tab) => {
    pushQuery(t === "realtime" ? { tab: null, event: null } : { tab: t })
  }

  return (
    <div className="mx-auto flex min-h-screen max-w-[90rem] flex-col px-4">
      <header className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-[var(--border)] py-3">
        <span className="font-semibold tracking-tight">EQMS</span>
        {/* 正常時は何も出さない。初回ロード中 (status 未着) も未判定なので出さない */}
        {status !== null && !streamOk && <span className="text-sm text-[var(--bad)]">サーバーと再接続中</span>}
        {status !== null && streamOk && !status.connected && (
          <span className="text-sm text-[var(--bad)]">地震計からデータが届いていません</span>
        )}
        <nav className="ml-auto flex gap-1">
          {TABS.map(([key, label]) => (
            <button
              key={key}
              onClick={() => switchTab(key)}
              className={`border-b-2 px-2 pb-0.5 text-sm ${tab === key
                ? "border-[var(--text-primary)] text-[var(--text-primary)]"
                : "border-transparent text-[var(--text-muted)] hover:text-[var(--text-secondary)]"
                }`}
            >
              {label}
            </button>
          ))}
        </nav>
      </header>

      {tab === "realtime" ? (
        // 1カラムなので広げても間延びするだけ。幅はタブ側で絞り、ヘッダーは動かさない
        <main className="mx-auto w-full max-w-5xl">
          <IntensityPanel status={status} intensity={intensity} waveRef={waveRef} />
          <section className="py-3">
            <WaveformPlot label="加速度　直近30秒" live={waveRef} spanMs={30_000} />
          </section>
        </main>
      ) : (
        <main className="py-4">
          <HistoryView eqCount={eqCount} serverNow={status?.now ?? null} />
        </main>
      )}
    </div>
  )
}
