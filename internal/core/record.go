package core

import (
	"log"

	"github.com/njm2360/eqms/internal/nmea"
	"github.com/njm2360/eqms/internal/store"
)

type recorder struct {
	id          int64
	startedAt   int64
	triggeredAt int64
	maxInt      float64
	maxPga      float64
	belowSince  int64 // 0 = 直近は閾値以上
	chunkT0     int64
	cx, cy, cz  []float32
}

func (e *Engine) handleIntensity(in nmea.Intensity) {
	now := e.eventTime()
	if in.Stable {
		v := in.Value
		e.intensity = &v
	} else {
		e.intensity = nil
	}
	e.stable = in.Stable
	e.hub.Publish("intensity", IntMsg{T: now, Intensity: e.intensity, Stable: in.Stable})

	if e.rec == nil {
		if in.Stable && in.Value >= e.startIntensity {
			e.startEvent(now, in.Value)
		}
		return
	}
	if in.Stable && in.Value > e.rec.maxInt {
		e.rec.maxInt = in.Value
	}
	if !in.Stable || in.Value < e.endIntensity {
		if e.rec.belowSince == 0 {
			e.rec.belowSince = now
		} else if now-e.rec.belowSince >= e.endQuietMs {
			e.finishEvent(now)
			return
		}
	} else {
		e.rec.belowSince = 0
	}
	if now-e.rec.triggeredAt > e.maxEventMs {
		log.Printf("engine: event %d hit safety limit, closing", e.rec.id)
		e.finishEvent(now)
	}
}

func (e *Engine) startEvent(trigT int64, intensity float64) {
	preStart := max(trigT-e.preBufferMs, e.persistCursor)
	pre := e.ringSince(preStart)

	if e.resumeLastEvent(trigT, intensity, pre) {
		return
	}

	startedAt := trigT
	if len(pre) > 0 {
		startedAt = pre[0].t
	}
	id := e.nextEventID
	e.nextEventID++
	e.w.InsertEvent(id, startedAt, trigT)

	e.rec = &recorder{id: id, startedAt: startedAt, triggeredAt: trigT, maxInt: intensity}
	for _, s := range pre {
		e.recAppend(s)
	}
	log.Printf("engine: event %d started (intensity %.1f, prebuffer %d samples)", id, intensity, len(pre))
	e.hub.PublishKeep("eqevent", EqMsg{Phase: "start", Event: store.Event{
		ID: id, StartedAt: startedAt, TriggeredAt: trigT,
	}})
}

// resumeLastEvent は直前のイベントが endQuietMs 以内に終わっていれば再開し、続く揺れを1件にまとめる。
func (e *Engine) resumeLastEvent(trigT int64, intensity float64, pre []sample) bool {
	last := e.lastClosed
	if last == nil || last.EndedAt == nil {
		return false
	}
	quiet := trigT - *last.EndedAt
	if quiet < 0 || quiet > e.endQuietMs || trigT-last.TriggeredAt >= e.maxEventMs {
		return false
	}
	e.w.ReopenEvent(last.ID)

	r := &recorder{id: last.ID, startedAt: last.StartedAt, triggeredAt: last.TriggeredAt, maxInt: intensity}
	if last.MaxIntensity != nil {
		r.maxInt = max(*last.MaxIntensity, intensity)
	}
	if last.MaxPga != nil {
		r.maxPga = *last.MaxPga
	}
	e.rec = r
	e.lastClosed = nil
	for _, s := range pre {
		e.recAppend(s)
	}
	log.Printf("engine: event %d resumed after %.1fs quiet", r.id, float64(quiet)/1000)
	e.hub.PublishKeep("eqevent", EqMsg{Phase: "resume", Event: store.Event{
		ID: r.id, StartedAt: r.startedAt, TriggeredAt: r.triggeredAt,
	}})
	return true
}

func (e *Engine) recAppend(s sample) {
	r := e.rec
	if s.t < e.persistCursor {
		if e.persistCursor-s.t <= clockStepBackMs {
			return // 再アンカーのゆらぎ。アーカイブ済みの区間に重ねない
		}
		// 弾き続けると壁時計が追いつくまで波形を一切書けない
		log.Printf("engine: clock stepped back %.1fs, re-anchoring archive cursor to %d",
			float64(e.persistCursor-s.t)/1000, s.t)
		e.flushChunk()
		e.persistCursor = s.t
	}
	if len(r.cx) > 0 && !contiguous(r.chunkT0, len(r.cx), s.t) {
		e.flushChunk()
	}
	if len(r.cx) == 0 {
		r.chunkT0 = s.t
	}
	r.cx = append(r.cx, s.x)
	r.cy = append(r.cy, s.y)
	r.cz = append(r.cz, s.z)
	// 丸めてから持つ。DB と status で桁が食い違わないようにする
	if v := round2(float64(s.c)); v > r.maxPga {
		r.maxPga = v
	}
	if len(r.cx) >= chunkSize {
		e.flushChunk()
	}
}

func (e *Engine) flushChunk() {
	r := e.rec
	if len(r.cx) == 0 {
		return
	}
	e.w.AppendChunk(r.chunkT0, r.cx, r.cy, r.cz)
	// 渡した時点で書いたものとして扱う。ここで巻き戻すと同じ区間を二重に書きにいく
	e.persistCursor = r.chunkT0 + int64(len(r.cx))*store.SampleDtMs
	e.w.UpdateEventProgress(r.id, r.maxInt, r.maxPga)
	r.cx, r.cy, r.cz = r.cx[:0], r.cy[:0], r.cz[:0]
}

func (e *Engine) finishEvent(endT int64) {
	r := e.rec
	e.flushChunk()
	// 壁時計だと次のイベントの開始点とわずかに重なりうるので、波形の終端に揃える
	if e.persistCursor > r.startedAt {
		endT = e.persistCursor
	}
	e.w.CloseEvent(r.id, endT, r.maxInt, r.maxPga)
	log.Printf("engine: event %d finished (max intensity %.1f, max pga %.1f gal)", r.id, r.maxInt, r.maxPga)

	maxInt, maxPga, ended := r.maxInt, r.maxPga, endT
	ev := store.Event{
		ID: r.id, StartedAt: r.startedAt, TriggeredAt: r.triggeredAt,
		EndedAt: &ended, MaxIntensity: &maxInt, MaxPga: &maxPga,
	}
	e.lastClosed = &ev
	e.hub.PublishKeep("eqevent", EqMsg{Phase: "end", Event: ev})
	e.rec = nil
}
