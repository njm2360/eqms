package core

import (
	"encoding/base64"
	"encoding/binary"
	"math"
	"time"

	"github.com/njm2360/eqms/internal/store"
)

type WaveMsg struct {
	T0 int64     `json:"t0"`
	Dt int       `json:"dt"`
	X  []float32 `json:"x"`
	Y  []float32 `json:"y"`
	Z  []float32 `json:"z"`
}

type IntMsg struct {
	T         int64    `json:"t"`
	Intensity *float64 `json:"intensity"` // 安定化前は null
	Stable    bool     `json:"stable"`
}

type ActiveEvent struct {
	ID           int64   `json:"id"`
	StartedAt    int64   `json:"startedAt"`
	TriggeredAt  int64   `json:"triggeredAt"`
	MaxIntensity float64 `json:"maxIntensity"`
	MaxPga       float64 `json:"maxPga"`
}

type StatusMsg struct {
	Now       int64        `json:"now"`
	Connected bool         `json:"connected"`
	Intensity *float64     `json:"intensity"`
	Stable    bool         `json:"stable"`
	Active    *ActiveEvent `json:"active"`
}

type InitMsg struct {
	Status StatusMsg `json:"status"`
	// waveform フレームと同じ base64 バイナリ。切断や時刻の飛びで分かれる
	Waves []string `json:"waves"`
}

// 形式 (LE): t0 int64, dt int32, n int32, x float32×n, y float32×n, z float32×n
func encodeWave(w WaveMsg) []byte {
	n := len(w.X)
	buf := make([]byte, 16+12*n)
	binary.LittleEndian.PutUint64(buf[0:], uint64(w.T0))
	binary.LittleEndian.PutUint32(buf[8:], uint32(w.Dt))
	binary.LittleEndian.PutUint32(buf[12:], uint32(n))
	for i, v := range w.X {
		binary.LittleEndian.PutUint32(buf[16+4*i:], math.Float32bits(v))
	}
	for i, v := range w.Y {
		binary.LittleEndian.PutUint32(buf[16+4*(n+i):], math.Float32bits(v))
	}
	for i, v := range w.Z {
		binary.LittleEndian.PutUint32(buf[16+4*(2*n+i):], math.Float32bits(v))
	}
	out := make([]byte, base64.StdEncoding.EncodedLen(len(buf)))
	base64.StdEncoding.Encode(out, buf)
	return out
}

type EqMsg struct {
	Phase string      `json:"phase"` // "start" | "resume" | "end"
	Event store.Event `json:"event"`
}

type HealthMsg struct {
	OK bool `json:"ok"`
}

func (e *Engine) buildStatus() StatusMsg {
	st := StatusMsg{
		Now:       nowMs(),
		Connected: e.connected,
		Intensity: e.intensity,
		Stable:    e.stable,
	}
	if r := e.rec; r != nil {
		st.Active = &ActiveEvent{
			ID: r.id, StartedAt: r.startedAt, TriggeredAt: r.triggeredAt,
			MaxIntensity: r.maxInt, MaxPga: r.maxPga,
		}
	}
	return st
}

// Health は監視用に、サンプルが届いていて書き込みも失敗していないかを返す。
func (e *Engine) Health() HealthMsg {
	s := e.snap.Load()
	ok := s.status.Connected && !s.lastSampleAt.IsZero() && time.Since(s.lastSampleAt) <= healthStaleSample &&
		(s.lastWriteErrAt.IsZero() || time.Since(s.lastWriteErrAt) > healthWriteErrQuiet)
	return HealthMsg{OK: ok}
}

// Subscribe は購読登録とスナップショット作成を Run ゴルーチンへ渡す。サンプルの取り込みと
// 同じゴルーチンで順に走るので、init と waveform が同じサンプルを二重に運ばない。
func (e *Engine) Subscribe() (ch chan []byte, init []byte, cancel func(), err error) {
	var st StatusMsg
	var samples []sample
	if err := e.call(func() {
		e.flushBatch()
		ch = e.hub.Subscribe()
		st = e.buildStatus()
		samples = e.ringSince(nowMs() - initWaveMs)
	}); err != nil {
		return nil, nil, nil, err
	}
	var waves []WaveMsg
	for _, s := range samples {
		if n := len(waves); n > 0 {
			if w := &waves[n-1]; contiguous(w.T0, len(w.X), s.t) {
				w.X, w.Y, w.Z = append(w.X, s.x), append(w.Y, s.y), append(w.Z, s.z)
				continue
			}
		}
		waves = append(waves, WaveMsg{T0: s.t, Dt: store.SampleDtMs,
			X: []float32{s.x}, Y: []float32{s.y}, Z: []float32{s.z}})
	}
	msg := InitMsg{Status: st, Waves: []string{}}
	for _, w := range waves {
		msg.Waves = append(msg.Waves, string(encodeWave(w)))
	}
	init, _ = Frame("init", msg)
	return ch, init, func() { e.hub.Unsubscribe(ch) }, nil
}
