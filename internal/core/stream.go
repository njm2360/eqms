package core

import (
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
	Port      string       `json:"port"`
	Device    string       `json:"device,omitempty"`
	Firmware  string       `json:"firmware,omitempty"`
	Sps       int          `json:"sps"`
	Intensity *float64     `json:"intensity"`
	Stable    bool         `json:"stable"`
	Active    *ActiveEvent `json:"active"`
	ParseErrs uint64       `json:"parseErrs"`
	// 接続が切り替わるまで保持する。UI 側で経過時間を出せるよう発生時刻も返す
	LastDevErr   string `json:"lastDevErr,omitempty"`
	LastDevErrAt int64  `json:"lastDevErrAt,omitempty"`
}

type InitMsg struct {
	Status StatusMsg `json:"status"`
	// waveform フレームと同じ形式。切断や時刻の飛びで分かれる
	Waves []WaveMsg `json:"waves"`
}

type EqMsg struct {
	Phase string      `json:"phase"` // "start" | "resume" | "end"
	Event store.Event `json:"event"`
}

type DevErrMsg struct {
	T  int64  `json:"t"`
	ID string `json:"id"`
}

type HealthMsg struct {
	OK        bool   `json:"ok"`
	Connected bool   `json:"connected"`
	Sps       int    `json:"sps"`
	ParseErrs uint64 `json:"parseErrs"`
	Reason    string `json:"reason,omitempty"`
}

func (e *Engine) buildStatus() StatusMsg {
	st := StatusMsg{
		Now:          nowMs(),
		Connected:    e.connected,
		Port:         e.port,
		Sps:          e.sps,
		Intensity:    e.intensity,
		Stable:       e.stable,
		ParseErrs:    e.parseErrs,
		LastDevErr:   e.lastDevErr,
		LastDevErrAt: e.lastDevErrAt,
	}
	if e.hw != nil {
		st.Device = e.hw.Device
		st.Firmware = e.hw.Firmware
	}
	if r := e.rec; r != nil {
		st.Active = &ActiveEvent{
			ID: r.id, StartedAt: r.startedAt, TriggeredAt: r.triggeredAt,
			MaxIntensity: r.maxInt, MaxPga: r.maxPga,
		}
	}
	return st
}

func (e *Engine) Status() StatusMsg {
	st := e.snap.Load().status
	st.Now = nowMs()
	return st
}

// Health は監視用に、地震計からサンプルが届いているかを返す。
func (e *Engine) Health() HealthMsg {
	s := e.snap.Load()
	h := HealthMsg{Connected: s.status.Connected, Sps: s.status.Sps, ParseErrs: s.status.ParseErrs}
	switch {
	case !h.Connected:
		h.Reason = "device disconnected"
	case s.lastSampleAt.IsZero():
		h.Reason = "no samples yet"
	case time.Since(s.lastSampleAt) > healthStaleSample:
		h.Reason = "no samples for " + time.Since(s.lastSampleAt).Round(time.Second).String()
	default:
		h.OK = true
	}
	return h
}

// Subscribe は購読登録とスナップショット作成を Run ゴルーチンへ渡す。サンプルの取り込みと
// 同じゴルーチンで順に走るので、init と waveform が同じサンプルを二重に運ばない。
func (e *Engine) Subscribe() (ch chan []byte, init []byte, cancel func(), err error) {
	if err := e.call(func() {
		e.flushBatch()
		ch = e.hub.Subscribe()
		msg := InitMsg{Status: e.buildStatus(), Waves: []WaveMsg{}}
		for _, s := range e.ringSince(nowMs() - initWaveMs) {
			if n := len(msg.Waves); n > 0 {
				if w := &msg.Waves[n-1]; contiguous(w.T0, len(w.X), s.t) {
					w.X, w.Y, w.Z = append(w.X, s.x), append(w.Y, s.y), append(w.Z, s.z)
					continue
				}
			}
			msg.Waves = append(msg.Waves, WaveMsg{T0: s.t, Dt: store.SampleDtMs,
				X: []float32{s.x}, Y: []float32{s.y}, Z: []float32{s.z}})
		}
		init, _ = Frame("init", msg)
	}); err != nil {
		return nil, nil, nil, err
	}
	return ch, init, func() { e.hub.Unsubscribe(ch) }, nil
}
