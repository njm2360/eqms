import { useEffect, useRef, useState } from "react"
import type { IntMsg, Sample, Status, WaveMsg } from "./types"

const WINDOW_MS = 30_000

export interface Stream {
  status: Status | null
  intensity: IntMsg | null
  streamOk: boolean
  eqCount: number // eqevent 受信回数 (履歴の再取得トリガ)
  waveRef: React.RefObject<Sample[]>
}

// 波形は再レンダリングを避けるため ref に溜め、プロット側が rAF で読む。
export function useStream(): Stream {
  const [status, setStatus] = useState<Status | null>(null)
  const [intensity, setIntensity] = useState<IntMsg | null>(null)
  const [streamOk, setStreamOk] = useState(false)
  const [eqCount, setEqCount] = useState(0)
  const waveRef = useRef<Sample[]>([])

  useEffect(() => {
    const es = new EventSource("/api/stream")

    const appendWave = (w: WaveMsg) => {
      const buf = waveRef.current
      for (let i = 0; i < w.x.length; i++) {
        buf.push({ t: w.t0 + i * w.dt, x: w.x[i], y: w.y[i], z: w.z[i] })
      }
      const cutoff = buf[buf.length - 1].t - WINDOW_MS
      let drop = 0
      while (drop < buf.length && buf[drop].t < cutoff) drop++
      if (drop > 0) buf.splice(0, drop)
    }

    es.addEventListener("init", (e) => {
      const msg = JSON.parse(e.data) as { status: Status; waves: WaveMsg[] }
      setStatus(msg.status)
      waveRef.current = []
      msg.waves.forEach(appendWave)
      setStreamOk(true)
    })
    es.addEventListener("status", (e) => setStatus(JSON.parse(e.data)))
    es.addEventListener("intensity", (e) => setIntensity(JSON.parse(e.data)))
    es.addEventListener("waveform", (e) => appendWave(JSON.parse(e.data)))
    es.addEventListener("eqevent", () => setEqCount((c) => c + 1))
    es.onerror = () => setStreamOk(false) // 再接続は EventSource 任せ
    return () => es.close()
  }, [])

  return { status, intensity, streamOk, eqCount, waveRef }
}
