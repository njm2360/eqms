export interface Sample {
  t: number // ms epoch
  x: number
  y: number
  z: number
}

export interface WaveMsg {
  t0: number
  dt: number
  x: number[]
  y: number[]
  z: number[]
}

export interface IntMsg {
  t: number
  intensity: number | null
  stable: boolean
}

export interface ActiveEvent {
  id: number
  startedAt: number
  triggeredAt: number
  maxIntensity: number
  maxPga: number
}

export interface Status {
  now: number
  connected: boolean
  port: string
  device?: string
  firmware?: string
  sps: number
  intensity: number | null
  stable: boolean
  active: ActiveEvent | null
  parseErrs: number
  lastDevErr?: string
  lastDevErrAt?: number
}

export interface EqEvent {
  id: number
  startedAt: number
  triggeredAt: number
  endedAt: number | null
  maxIntensity: number | null
  maxPga: number | null
}

// Segment は連続したサンプル列。間引かれた応答では x/y/z の代わりに軸ごとの min/max が入る。
export interface Segment {
  t0: number
  dt: number
  n: number
  x?: number[]
  y?: number[]
  z?: number[]
  xMin?: number[]
  xMax?: number[]
  yMin?: number[]
  yMax?: number[]
  zMin?: number[]
  zMax?: number[]
}

export interface EventsPage {
  events: EqEvent[]
  nextBefore: number | null
  nextBeforeId: number | null
}

export interface WaveformRange {
  from: number
  to: number
  decimated: boolean
  segments: Segment[]
}
