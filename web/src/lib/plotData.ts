import type { Sample, WaveformRange } from "./types";

// uPlot のデータは [x, y0, y1, y2] の列指向。x は秒 (小数で ms まで持つ) で厳密昇順、
// 欠落は y に null を1点入れて線を切る。
export type PlotData = [number[], ...(number | null)[][]];

export interface Plot {
  data: PlotData;
  peak: number; // y スケールの片振幅。3軸で共有する
}

const MIN_PEAK = 5; // 静穏時の微小なノイズを画面いっぱいに拡大しないための下限 (gal)
const HEADROOM = 1.05;
const GAP_TOLERANCE_MS = 50;

export const emptyPlot = (): Plot => ({ data: [[], [], [], []], peak: MIN_PEAK });

class Builder {
  xs: number[] = [];
  ys: (number | null)[][] = [[], [], []];
  private max = 0;
  private lastMs = -Infinity;

  // 重なるチャンクが返ってきても x の昇順を壊さない
  add(ms: number, v: readonly (number | null)[]) {
    if (ms <= this.lastMs) return;
    this.lastMs = ms;
    this.xs.push(ms / 1000);
    for (let a = 0; a < 3; a++) {
      const y = v[a];
      this.ys[a].push(y);
      if (y !== null && Math.abs(y) > this.max) this.max = Math.abs(y);
    }
  }

  gap(ms: number) {
    this.add(ms, [null, null, null]);
  }

  done(): Plot {
    return { data: [this.xs, ...this.ys] as PlotData, peak: Math.max(MIN_PEAK, this.max * HEADROOM) };
  }
}

// 間引き応答はバケットごとに min と max の2点へ展開する。
// 通常の折れ線として描けば、包絡がアンチエイリアス付きで塗られる。
export function rangePlot(range: WaveformRange): Plot {
  const b = new Builder();
  range.segments.forEach((seg, si) => {
    if (si > 0) b.gap(seg.t0 - 1);
    const lo = [seg.xMin, seg.yMin, seg.zMin];
    const hi = [seg.xMax, seg.yMax, seg.zMax];
    const raw = [seg.x, seg.y, seg.z];
    for (let i = 0; i < seg.n; i++) {
      const t = seg.t0 + i * seg.dt;
      if (lo[0] && hi[0]) {
        b.add(t, [lo[0][i], lo[1]![i], lo[2]![i]]);
        b.add(t + seg.dt / 2, [hi[0][i], hi[1]![i], hi[2]![i]]);
      } else if (raw[0]) {
        b.add(t, [raw[0][i], raw[1]![i], raw[2]![i]]);
      }
    }
  });
  return b.done();
}

export function livePlot(buf: Sample[]): Plot {
  const b = new Builder();
  let prev = -Infinity;
  for (const s of buf) {
    if (s.t - prev > GAP_TOLERANCE_MS && prev > -Infinity) b.gap(prev + 1);
    b.add(s.t, [s.x, s.y, s.z]);
    prev = s.t;
  }
  return b.done();
}
