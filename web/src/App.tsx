import { useState } from "react"
import HistoryView from "./components/HistoryView"
import IntensityPanel from "./components/IntensityPanel"
import WaveformPlot from "./components/WaveformPlot"
import { fmtTime } from "./lib/jma"
import { readQuery, updateQuery } from "./lib/urlState"
import { useStream } from "./lib/useStream"

type Tab = "realtime" | "history"

const TABS: [Tab, string][] = [
  ["realtime", "リアルタイム"],
  ["history", "地震記録"],
]

export default function App() {
  const { status, intensity, streamOk, eqCount, waveRef } = useStream()
  const [tab, setTab] = useState<Tab>(() => (readQuery("tab") === "history" ? "history" : "realtime"))

  const switchTab = (t: Tab) => {
    setTab(t)
    updateQuery(t === "realtime" ? { tab: null, event: null } : { tab: t })
  }

  const serialOk = streamOk && (status?.connected ?? false)

  return (
    <div className="mx-auto flex min-h-screen max-w-[90rem] flex-col px-4">
      <header className="flex flex-wrap items-center gap-x-4 gap-y-2 border-b border-[var(--border)] py-3">
        <span className="font-semibold tracking-tight">eqms</span>
        <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-[var(--text-secondary)]">
          <span className="flex items-center gap-1.5">
            <span
              className="inline-block h-2 w-2 rounded-full"
              style={{ background: serialOk ? "var(--ok)" : "var(--bad)" }}
            />
            {!streamOk ? "サーバー再接続中" : serialOk ? status?.device || "地震計" : "地震計 未接続"}
          </span>
          {serialOk && <span className="tabular-nums">{status?.port}</span>}
          {serialOk && <span className="tabular-nums">{status?.sps ?? 0} sps</span>}
          {status?.lastDevErr && (
            <span className="text-[var(--bad)]">
              エラー {status.lastDevErr}
              {status.lastDevErrAt ? ` ${fmtTime(status.lastDevErrAt)}` : ""}
            </span>
          )}
        </div>
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
            <WaveformPlot label="加速度 gal　直近30秒" live={waveRef} spanMs={30_000} />
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
