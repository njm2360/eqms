import { useEffect, useState } from "react";
import { fmtTime } from "../lib/format";
import { jmaClass, MIN_DISPLAY_INTENSITY } from "../lib/jma";
import type { IntMsg, Sample, Status } from "../lib/types";
import { Field, Reading } from "./readout";

interface Props {
  status: Status | null;
  intensity: IntMsg | null;
  waveRef: React.RefObject<Sample[]>;
}

export default function IntensityPanel({ status, intensity, waveRef }: Props) {
  const [nowGal, setNowGal] = useState(0);
  useEffect(() => {
    const id = setInterval(() => {
      const buf = waveRef.current;
      // チラつき防止に直近250msの最大値を現在の加速度として出す
      let max = 0;
      for (let i = buf.length - 1; i >= 0 && i >= buf.length - 25; i--) {
        const s = buf[i];
        const c = Math.sqrt(s.x * s.x + s.y * s.y + s.z * s.z);
        if (c > max) max = c;
      }
      setNowGal(max);
    }, 250);
    return () => clearInterval(id);
  }, [waveRef]);

  const stable = intensity ? intensity.stable : (status?.stable ?? false);
  const value = intensity?.intensity ?? status?.intensity ?? null;
  const cls = value !== null && value >= MIN_DISPLAY_INTENSITY ? jmaClass(value) : null;
  const active = status?.active ?? null;

  return (
    <div className="border-b border-[var(--border)]">
      <div className="flex flex-wrap items-center gap-x-14 gap-y-6 py-8">
        <div className="flex items-center gap-4">
          <div
            className="flex h-20 w-20 shrink-0 items-center justify-center rounded text-4xl font-bold"
            style={
              cls ? { background: cls.bg, color: cls.fg } : { background: "var(--border)", color: "var(--text-muted)" }
            }
          >
            {cls ? cls.label : "--"}
          </div>
          <Reading label="計測震度" value={value !== null ? value.toFixed(1) : "---"} />
        </div>
        <Reading label="加速度" value={nowGal.toFixed(1)} unit="gal" />
        {!stable && <span className="text-sm text-[var(--text-muted)]">震度の算出待ち</span>}
      </div>

      {active && (
        <div className="flex flex-wrap items-baseline gap-x-6 gap-y-1 border-t border-[var(--border)] py-2 text-sm">
          <span className="font-medium text-[var(--bad)]">記録中 #{active.id}</span>
          <Field label="検知" value={fmtTime(active.triggeredAt)} />
          <Field
            label="最大震度"
            value={`${jmaClass(active.maxIntensity).label} (${active.maxIntensity.toFixed(1)})`}
          />
          <Field label="最大加速度" value={`${active.maxPga.toFixed(1)} gal`} />
        </div>
      )}
    </div>
  );
}
