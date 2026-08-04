import { useEffect, useRef, useState } from "react"
import type { IntMsg, Sample, Status, WaveMsg } from "./types"

const WINDOW_MS = 30_000

function decodeWave(b64: string): WaveMsg | null {
  const raw = atob(b64)
  const buf = new Uint8Array(raw.length)
  for (let i = 0; i < raw.length; i++) buf[i] = raw.charCodeAt(i)
  if (buf.length < 16) return null
  const dv = new DataView(buf.buffer)
  const n = dv.getInt32(12, true)
  if (buf.length !== 16 + 12 * n) return null
  return {
    t0: Number(dv.getBigInt64(0, true)),
    dt: dv.getInt32(8, true),
    x: new Float32Array(buf.buffer, 16, n),
    y: new Float32Array(buf.buffer, 16 + 4 * n, n),
    z: new Float32Array(buf.buffer, 16 + 8 * n, n),
  }
}

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

    const appendWave = (w: WaveMsg | null) => {
      if (!w) return
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
      const msg = JSON.parse(e.data) as { status: Status; waves: string[] }
      setStatus(msg.status)
      waveRef.current = []
      msg.waves.forEach((w) => appendWave(decodeWave(w)))
      setStreamOk(true)
    })
    es.addEventListener("status", (e) => setStatus(JSON.parse(e.data)))
    es.addEventListener("intensity", (e) => setIntensity(JSON.parse(e.data)))
    es.addEventListener("waveform", (e) => appendWave(decodeWave(e.data)))
    es.addEventListener("eqevent", () => setEqCount((c) => c + 1))
    es.onerror = () => setStreamOk(false) // 再接続は EventSource 任せ
    return () => es.close()
  }, [])

  return { status, intensity, streamOk, eqCount, waveRef }
}
