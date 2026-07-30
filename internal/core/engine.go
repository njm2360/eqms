// Package core は受信データの解析からイベント検出、SSE 配信までを担う。
package core

import (
	"context"
	"errors"
	"log"
	"math"
	"sync/atomic"
	"time"

	"github.com/njm2360/eqms/internal/nmea"
	"github.com/njm2360/eqms/internal/source"
	"github.com/njm2360/eqms/internal/store"
)

const (
	ringCapacity = 30000 // 100Hz × 5分
	batchSize    = 10    // SSE 波形配信単位 (10Hz)
	chunkSize    = 100   // アーカイブのチャンク長 (1秒)

	defaultStartIntensity = 0.5 // 震度1相当で記録開始
	defaultEndIntensity   = 0.5
	defaultPreBufferMs    = 60000
	defaultEndQuietMs     = 30000
	// トリガーが止まらなくなったときの安全弁。分割のためではない
	defaultMaxEventMs = 6 * 60 * 60 * 1000

	// MaxPreBuffer はリングが保持する長さ。これを超える PreBuffer は意味を持たない
	MaxPreBuffer = ringCapacity * 10 * time.Millisecond

	staleSample = 60 * time.Second // サンプル途絶時の強制終了
	initWaveMs  = 30000

	pruneInterval = time.Hour

	// これを超える巻き戻りは再アンカーのゆらぎではなく壁時計の飛びとみなす
	clockStepBackMs = 5000

	// サンプルがこれだけ途切れたら異常とみなす。再接続には無音判定と
	// リトライ間隔がかかるので、正常な抜き差しで 503 にしない幅を取る
	healthStaleSample = 30 * time.Second
)

// ErrNotRunning は Run が動いていないエンジンに購読を求めたときに返る。
var ErrNotRunning = errors.New("core: engine is not running")

type sample struct {
	t          int64 // ms epoch
	x, y, z, c float32
}

// Engine の状態を触るのは Run ゴルーチンだけ。外部からの読み取りは snap 経由で、
// 書き込みは store.Writer へ渡すだけなので Run は I/O で止まらない。
type Engine struct {
	hub *Hub
	st  *store.Store
	w   *store.Writer

	ctl     chan func()
	stopped chan struct{}
	snap    atomic.Pointer[snapshot]

	startIntensity float64
	endIntensity   float64
	preBufferMs    int64
	endQuietMs     int64
	maxEventMs     int64
	retention      time.Duration

	clock     SampleClock
	connected bool
	port      string
	hw        *nmea.HWInfo

	ring    [ringCapacity]sample
	ringPos int
	ringN   int

	spsCount     int
	sps          int
	parseErrs    uint64
	lastDevErr   string
	lastDevErrAt int64
	intensity    *float64
	stable       bool

	lastSampleT int64 // SampleClock 上の時刻。イベントの時刻もこれに揃える
	// 途絶判定用。壁時計の飛びを拾わないよう time.Since で測る
	lastSampleAt time.Time

	// アーカイブ済みサンプルの終端。ここより前には遡って書かない
	persistCursor int64

	batchT0                int64
	batchX, batchY, batchZ []float32

	rec         *recorder
	nextEventID int64
	// 再開判定に使う直前の記録。起動時だけ DB から読む
	lastClosed *store.Event
}

// snapshot の中身は差し替え後に書き換えない。読み取り側はロックなしで参照する。
type snapshot struct {
	status       StatusMsg
	lastSampleAt time.Time
}

// Config のゼロ値フィールドはデフォルトになる (Retention の 0 は無期限)。
type Config struct {
	Retention      time.Duration
	StartIntensity float64
	EndIntensity   float64
	PreBuffer      time.Duration
	EndQuiet       time.Duration
	MaxEvent       time.Duration
}

func NewEngine(st *store.Store, w *store.Writer, cfg Config) (*Engine, error) {
	e := &Engine{
		hub:            NewHub(),
		st:             st,
		w:              w,
		ctl:            make(chan func()),
		stopped:        make(chan struct{}),
		startIntensity: floatOr(cfg.StartIntensity, defaultStartIntensity),
		endIntensity:   floatOr(cfg.EndIntensity, defaultEndIntensity),
		preBufferMs:    msOr(cfg.PreBuffer, defaultPreBufferMs),
		endQuietMs:     msOr(cfg.EndQuiet, defaultEndQuietMs),
		maxEventMs:     msOr(cfg.MaxEvent, defaultMaxEventMs),
		retention:      cfg.Retention,
	}

	cur, err := e.st.PersistCursor()
	if err != nil {
		return nil, err
	}
	e.persistCursor = cur

	// ID を自前で払い出すので、ここが狂うと記録が上書きされる
	id, err := e.st.NextEventID()
	if err != nil {
		return nil, err
	}
	e.nextEventID = id

	last, err := e.st.LastEvent()
	if err != nil {
		return nil, err
	}
	if last != nil && last.EndedAt != nil {
		e.lastClosed = last
	}

	e.refresh()
	return e, nil
}

func floatOr(v, def float64) float64 {
	if v > 0 {
		return v
	}
	return def
}

func msOr(d time.Duration, def int64) int64 {
	if d > 0 {
		return d.Milliseconds()
	}
	return def
}

// Run はソースイベントを消費し続ける。ctx キャンセルで進行中の記録を閉じて戻る。
func (e *Engine) Run(ctx context.Context, events <-chan source.Event) {
	defer close(e.stopped)

	status := time.NewTicker(time.Second)
	defer status.Stop()
	prune := time.NewTicker(pruneInterval)
	defer prune.Stop()
	e.prune()

	for {
		select {
		case <-ctx.Done():
			if e.rec != nil {
				e.finishEvent(e.eventTime())
				e.refresh()
			}
			return
		case ev := <-events:
			e.handle(ev)
		case fn := <-e.ctl:
			fn()
		case <-status.C:
			e.tick()
		case <-prune.C:
			e.prune()
		}
	}
}

func (e *Engine) prune() {
	if e.retention > 0 {
		e.w.Prune(nowMs() - e.retention.Milliseconds())
	}
}

// refresh は読み取り用のスナップショットを作り直す。状態を変えたら必ず通る。
func (e *Engine) refresh() StatusMsg {
	st := e.buildStatus()
	e.snap.Store(&snapshot{status: st, lastSampleAt: e.lastSampleAt})
	return st
}

func (e *Engine) publishStatus() {
	e.hub.Publish("status", e.refresh())
}

func (e *Engine) handle(ev source.Event) {
	defer e.refresh()

	switch v := ev.(type) {
	case source.Connected:
		e.connected = true
		e.port = v.Port
		e.clock.Reset()
		e.flushPartial()
		e.lastDevErr, e.lastDevErrAt = "", 0
		e.publishStatus()
	case source.Disconnected:
		e.connected = false
		e.clock.Reset()
		e.flushPartial()
		// 切れた時点の震度は現在値ではない
		e.intensity, e.stable = nil, false
		e.publishStatus()
	case source.Line:
		parsed, err := nmea.Parse(v.Text)
		if err != nil {
			e.parseErrs++
			return
		}
		switch s := parsed.(type) {
		case nmea.Acc:
			e.handleAcc(s, v.Recv)
		case nmea.Intensity:
			e.handleIntensity(s)
		case nmea.HWInfo:
			if s.InfoVersion != 1 {
				log.Printf("engine: ignoring unsupported XSHWI info version %d", s.InfoVersion)
				return
			}
			hw := s
			e.hw = &hw
			e.publishStatus()
		case nmea.DevErr:
			e.lastDevErr, e.lastDevErrAt = s.ID, e.eventTime()
			log.Printf("engine: device error: %s", s.ID)
			e.hub.PublishKeep("deverr", DevErrMsg{T: e.lastDevErrAt, ID: s.ID})
		case nmea.BootReason:
			log.Printf("engine: device booted: %s", s.ID)
		}
	}
}

// contiguous は t を「t0 から n サンプル並べた次の位置」とみなせるかを返す。
// SampleClock のドリフト補正でサンプルごとに ±0.5ms ほど揺れるため許容幅を持たせる。
func contiguous(t0 int64, n int, t int64) bool {
	d := t - (t0 + int64(n)*store.SampleDtMs)
	return d <= store.GapToleranceMs && d >= -store.GapToleranceMs
}

// flushPartial は溜まりかけのバッチとチャンクを切断境界で閉じる。
// またいで繋ぐと (t0, dt, n) の等間隔前提が壊れる。
func (e *Engine) flushPartial() {
	e.flushBatch()
	if e.rec != nil {
		e.flushChunk()
	}
}

func (e *Engine) flushBatch() {
	if len(e.batchX) == 0 {
		return
	}
	e.hub.Publish("waveform", WaveMsg{T0: e.batchT0, Dt: store.SampleDtMs, X: e.batchX, Y: e.batchY, Z: e.batchZ})
	e.batchX, e.batchY, e.batchZ = nil, nil, nil
}

func (e *Engine) handleAcc(a nmea.Acc, recv time.Time) {
	t := e.clock.Stamp(recv)
	comp := math.Sqrt(a.X*a.X + a.Y*a.Y + a.Z*a.Z)
	s := sample{t: t, x: float32(a.X), y: float32(a.Y), z: float32(a.Z), c: float32(comp)}
	e.lastSampleT = t
	e.lastSampleAt = time.Now()
	e.spsCount++

	e.ring[e.ringPos] = s
	e.ringPos = (e.ringPos + 1) % ringCapacity
	if e.ringN < ringCapacity {
		e.ringN++
	}

	if len(e.batchX) > 0 && !contiguous(e.batchT0, len(e.batchX), t) {
		e.flushBatch()
	}
	if len(e.batchX) == 0 {
		e.batchT0 = t
	}
	e.batchX = append(e.batchX, s.x)
	e.batchY = append(e.batchY, s.y)
	e.batchZ = append(e.batchZ, s.z)
	if len(e.batchX) >= batchSize {
		e.flushBatch()
	}

	if e.rec != nil {
		e.recAppend(s)
	}
}

// eventTime は震度由来のイベントに使う時刻。波形と同じ時間軸に揃える。
func (e *Engine) eventTime() int64 {
	if e.lastSampleT > 0 {
		return e.lastSampleT
	}
	return nowMs()
}

func (e *Engine) tick() {
	e.sps = e.spsCount
	e.spsCount = 0
	if e.rec != nil && !e.lastSampleAt.IsZero() && time.Since(e.lastSampleAt) > staleSample {
		log.Printf("engine: no samples for %s, closing event %d", staleSample, e.rec.id)
		e.finishEvent(e.lastSampleT)
	}
	e.publishStatus()
}

func (e *Engine) ringSince(t0 int64) []sample {
	out := []sample{}
	start := (e.ringPos - e.ringN + ringCapacity) % ringCapacity
	for i := 0; i < e.ringN; i++ {
		s := e.ring[(start+i)%ringCapacity]
		if s.t >= t0 {
			out = append(out, s)
		}
	}
	return out
}

// call は fn を Run ゴルーチンで実行して、終わるまで待つ。Run が止まっていれば ErrNotRunning。
func (e *Engine) call(fn func()) error {
	done := make(chan struct{})
	select {
	case e.ctl <- func() { fn(); close(done) }:
	case <-e.stopped:
		return ErrNotRunning
	}
	select {
	case <-done:
		return nil
	case <-e.stopped:
		// 渡した直後に Run が戻った場合、実行済みかどうかは done でしか判別できない
		select {
		case <-done:
			return nil
		default:
			return ErrNotRunning
		}
	}
}

func nowMs() int64 { return time.Now().UnixMilli() }

func round2(v float64) float64 { return math.Round(v*100) / 100 }
