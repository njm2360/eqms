package core

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/njm2360/eqms/internal/nmea"
	"github.com/njm2360/eqms/internal/source"
	"github.com/njm2360/eqms/internal/store"
)

type phase struct {
	ms        int64
	intensity float64
	amp       float64
}

type harness struct {
	e  *Engine
	st *store.Store
	w  *store.Writer
	ch chan source.Event
}

// newHarness はしきい値を縮めたエンジンを起動する。区間長は 200ms の倍数にすること。
func newHarness(t *testing.T, preBufferMs, endQuietMs, maxEventMs int64) *harness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(st)
	e, err := NewEngine(st, w, Config{
		PreBuffer: time.Duration(preBufferMs) * time.Millisecond,
		EndQuiet:  time.Duration(endQuietMs) * time.Millisecond,
		MaxEvent:  time.Duration(maxEventMs) * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}

	ch := make(chan source.Event, 4096)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx, ch)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		w.Close()
		st.Close()
	})
	return &harness{e: e, st: st, w: w, ch: ch}
}

// sync は流し込んだイベントが DB に届くまで待つ。
func (h *harness) sync(t *testing.T) {
	t.Helper()
	for len(h.ch) > 0 {
		time.Sleep(time.Millisecond)
	}
	if err := h.e.call(func() {}); err != nil {
		t.Fatal(err)
	}
	h.w.Sync()
}

// inspect は Run ゴルーチン上でエンジンの状態に触る。
func (h *harness) inspect(t *testing.T, fn func(*Engine)) {
	t.Helper()
	h.sync(t)
	if err := h.e.call(func() { fn(h.e) }); err != nil {
		t.Fatal(err)
	}
}

// drive は 100Hz で XSACC を、200ms ごとに XSINT を流す。絶対時刻で刻んでクロックの張り直しを避ける。
func (h *harness) drive(t *testing.T, phases ...phase) {
	t.Helper()
	start := time.Now()
	n := 0
	for _, p := range phases {
		for range int(p.ms / 10) {
			if d := time.Until(start.Add(time.Duration(n) * 10 * time.Millisecond)); d > 0 {
				time.Sleep(d)
			}
			now := time.Now()
			h.ch <- source.Line{Text: nmea.Format(fmt.Sprintf("XSRAW,%d,0,0", int16(p.amp))), Recv: now}
			h.ch <- source.Line{Text: nmea.Format(fmt.Sprintf("XSACC,%.2f,0.00,0.00", p.amp)), Recv: now}
			if n%20 == 0 {
				h.ch <- source.Line{Text: nmea.Format(fmt.Sprintf("XSINT,-1.0,%.1f", p.intensity)), Recv: now}
			}
			n++
		}
	}
	h.sync(t)
}

func (h *harness) eventsAsc(t *testing.T) []store.Event {
	t.Helper()
	h.sync(t)
	evs, err := h.st.ListEvents(100, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(evs)
	return evs
}

// feedAt は base+off から 100Hz で n サンプル流す。実時間で待たないので
// 受信時刻が飛ぶ状況を組み立てられる。
func (h *harness) feedAt(t *testing.T, base time.Time, off time.Duration, n int, intensity, amp float64) {
	t.Helper()
	for i := range n {
		recv := base.Add(off + time.Duration(i)*10*time.Millisecond)
		h.ch <- source.Line{Text: nmea.Format(fmt.Sprintf("XSRAW,%d,0,0", int16(amp))), Recv: recv}
		h.ch <- source.Line{Text: nmea.Format(fmt.Sprintf("XSACC,%.2f,0.00,0.00", amp)), Recv: recv}
		if i%20 == 0 {
			h.ch <- source.Line{Text: nmea.Format(fmt.Sprintf("XSINT,-1.0,%.1f", intensity)), Recv: recv}
		}
	}
	h.sync(t)
}

func (h *harness) subscribe(t *testing.T) (chan []byte, []byte) {
	t.Helper()
	ch, init, cancel, err := h.e.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cancel)
	return ch, init
}

func (h *harness) collectWaves(t *testing.T) func() []WaveMsg {
	t.Helper()
	ch, _ := h.subscribe(t)

	var mu sync.Mutex
	var out []WaveMsg
	go func() {
		for frame := range ch {
			if w, ok := decodeWave(frame); ok {
				mu.Lock()
				out = append(out, w)
				mu.Unlock()
			}
		}
	}()
	return func() []WaveMsg {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(out)
	}
}

func decodeWave(frame []byte) (WaveMsg, bool) {
	name, data, ok := splitFrame(frame)
	if !ok || name != "waveform" {
		return WaveMsg{}, false
	}
	return decodeWaveData(data)
}

func decodeWaveData(data []byte) (WaveMsg, bool) {
	raw, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil || len(raw) < 16 {
		return WaveMsg{}, false
	}
	n := int(binary.LittleEndian.Uint32(raw[12:]))
	if len(raw) != 16+12*n {
		return WaveMsg{}, false
	}
	w := WaveMsg{T0: int64(binary.LittleEndian.Uint64(raw)), Dt: int(binary.LittleEndian.Uint32(raw[8:]))}
	at := func(i int) float32 { return math.Float32frombits(binary.LittleEndian.Uint32(raw[16+4*i:])) }
	for i := range n {
		w.X = append(w.X, at(i))
		w.Y = append(w.Y, at(n+i))
		w.Z = append(w.Z, at(2*n+i))
	}
	return w, true
}

func waveFromB64(t *testing.T, s string) WaveMsg {
	t.Helper()
	w, ok := decodeWaveData([]byte(s))
	if !ok {
		t.Fatalf("cannot decode wave: %q", s)
	}
	return w
}

func splitFrame(frame []byte) (event string, data []byte, ok bool) {
	s := string(frame)
	if !strings.HasPrefix(s, "event: ") {
		return "", nil, false
	}
	nl := strings.IndexByte(s, '\n')
	i := strings.Index(s, "data: ")
	if nl < 0 || i < 0 {
		return "", nil, false
	}
	return s[len("event: "):nl], []byte(strings.TrimRight(s[i+len("data: "):], "\n")), true
}

func decodeInit(t *testing.T, frame []byte) InitMsg {
	t.Helper()
	name, data, ok := splitFrame(frame)
	if !ok || name != "init" {
		t.Fatalf("cannot read the init frame: %q", frame)
	}
	var msg InitMsg
	if err := json.Unmarshal(data, &msg); err != nil {
		t.Fatal(err)
	}
	return msg
}

func (h *harness) closeActive(t *testing.T) {
	t.Helper()
	h.inspect(t, func(e *Engine) {
		if e.rec != nil {
			e.finishEvent(e.eventTime())
		}
	})
}

func assertNoOverlap(t *testing.T, evs []store.Event) {
	t.Helper()
	for i := 1; i < len(evs); i++ {
		prev, cur := evs[i-1], evs[i]
		if prev.EndedAt == nil {
			t.Fatalf("event %d is still open", prev.ID)
		}
		if cur.StartedAt < *prev.EndedAt {
			t.Fatalf("event %d (started=%d) overlaps event %d (ended=%d) by %dms",
				cur.ID, cur.StartedAt, prev.ID, *prev.EndedAt, *prev.EndedAt-cur.StartedAt)
		}
	}
}

func TestResumeMergesNearbyTrigger(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 300, time.Hour.Milliseconds())
	h.drive(t,
		phase{800, 0.0, 0.1},  // 静穏
		phase{600, 2.0, 50.0}, // 1回目の揺れ
		phase{600, 0.0, 0.1},  // 静穏300ms超で終了
		phase{600, 2.0, 50.0}, // 200ms後に再トリガー
	)
	h.closeActive(t)

	evs := h.eventsAsc(t)
	if len(evs) != 1 {
		for _, e := range evs {
			t.Logf("event %d started=%d ended=%v", e.ID, e.StartedAt, e.EndedAt)
		}
		t.Fatalf("events=%d, want 1 (split instead of resumed)", len(evs))
	}
	assertNoOverlap(t, evs)

	ev := evs[0]
	wf, err := h.st.Range(t.Context(), ev.StartedAt, *ev.EndedAt, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.Segments) != 1 {
		t.Fatalf("segments=%d, want 1 (waveform is broken up)", len(wf.Segments))
	}
}

func TestSeparateEventsDoNotOverlap(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 300, time.Hour.Milliseconds())
	h.drive(t,
		phase{800, 0.0, 0.1},
		phase{600, 2.0, 50.0},
		phase{1600, 0.0, 0.1}, // 終了から1秒以上空ける
		phase{600, 2.0, 50.0},
	)
	h.closeActive(t)

	evs := h.eventsAsc(t)
	if len(evs) != 2 {
		for _, e := range evs {
			t.Logf("event %d started=%d ended=%v", e.ID, e.StartedAt, e.EndedAt)
		}
		t.Fatalf("events=%d, want 2", len(evs))
	}
	assertNoOverlap(t, evs)
}

func TestSafetyLimitSplitsWithoutOverlap(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 400, 300, 600)
	h.drive(t,
		phase{600, 0.0, 0.1},
		phase{2400, 2.0, 50.0}, // 上限を超えて揺れ続ける
	)
	h.closeActive(t)

	evs := h.eventsAsc(t)
	if len(evs) < 2 {
		t.Fatalf("events=%d, want >=2 (not split at the safety limit)", len(evs))
	}
	assertNoOverlap(t, evs)
	for _, e := range evs {
		if e.EndedAt != nil && *e.EndedAt-e.TriggeredAt > 1500 {
			t.Fatalf("event %d far exceeds the 600ms safety limit: %dms", e.ID, *e.EndedAt-e.TriggeredAt)
		}
	}
}

func TestNoDuplicateArchiveWrites(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 300, time.Hour.Milliseconds())
	h.drive(t,
		phase{800, 0.0, 0.1},
		phase{600, 2.0, 50.0},
		phase{600, 0.0, 0.1},
		phase{600, 2.0, 50.0},
	)
	h.closeActive(t)

	evs := h.eventsAsc(t)
	ev := evs[0]
	wf, err := h.st.Range(t.Context(), ev.StartedAt, *ev.EndedAt, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, seg := range wf.Segments {
		total += seg.N
	}
	span := *ev.EndedAt - ev.StartedAt
	// 重複が混ざれば範囲長から決まる想定数を超える
	if want := int(span / store.SampleDtMs); total > want+2 {
		t.Fatalf("samples=%d exceeds the %dms span budget of %d", total, span, want)
	}
}

// 切断をまたいだサンプルは 1 つの (t0, dt, n) に詰めない。
// 詰めると受信側が t0 + i*dt で復元したときに欠落が消える。
func TestDisconnectDoesNotHideGap(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 200, time.Hour.Milliseconds(), time.Hour.Milliseconds())
	waves := h.collectWaves(t)

	base := time.Now()
	h.ch <- source.Connected{Port: "p"}
	h.feedAt(t, base, 0, 55, 2.0, 50.0) // 記録開始。最後の 5 件はバッチ半端
	h.ch <- source.Disconnected{Err: "unplugged"}
	h.ch <- source.Connected{Port: "p"}
	h.feedAt(t, base, 10*time.Second, 50, 2.0, -50.0) // 10 秒あけて復帰
	h.closeActive(t)

	for _, w := range waves() {
		var hasPre, hasPost bool
		for _, v := range w.X {
			hasPre = hasPre || v > 0
			hasPost = hasPost || v < 0
		}
		if hasPre && hasPost {
			t.Fatalf("samples from across the disconnect share one batch: t0=%d n=%d", w.T0, len(w.X))
		}
	}

	ev := h.eventsAsc(t)[0]
	wf, err := h.st.Range(t.Context(), ev.StartedAt, *ev.EndedAt, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.Segments) != 2 {
		for _, s := range wf.Segments {
			t.Logf("segment t0=%d n=%d lastX=%.0f", s.T0, s.N, s.X[len(s.X)-1])
		}
		t.Fatalf("segments=%d, want 2 (the 10s gap is missing from the archive)", len(wf.Segments))
	}
	if gap := wf.Segments[1].T0 - (wf.Segments[0].T0 + int64(wf.Segments[0].N)*store.SampleDtMs); gap < 9000 {
		t.Fatalf("gap is only %dms, want ~10000", gap)
	}
	total := 0
	for _, s := range wf.Segments {
		total += s.N
	}
	if total < 100 {
		t.Fatalf("samples were lost: total=%d, want >=100", total)
	}
}

// 壁時計が巻き戻っても波形の書き込みが再開すること。
func TestClockStepBackResumesArchiving(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 200, time.Hour.Milliseconds(), time.Hour.Milliseconds())

	base := time.Now()
	h.ch <- source.Connected{Port: "p"}
	h.feedAt(t, base, 0, 200, 2.0, 50.0) // 2 秒ぶん記録

	var cursorBefore int64
	h.inspect(t, func(e *Engine) { cursorBefore = e.persistCursor })

	// NTP が 1 時間巻き戻した
	h.ch <- source.Disconnected{Err: "reconnect"}
	h.ch <- source.Connected{Port: "p"}
	h.feedAt(t, base, -time.Hour, 300, 2.0, 99.0)
	h.closeActive(t)

	var cursorAfter int64
	h.inspect(t, func(e *Engine) { cursorAfter = e.persistCursor })
	if cursorAfter == cursorBefore {
		t.Fatalf("archive cursor was not re-anchored, nothing written after the step back (cursor=%d)", cursorAfter)
	}

	stepped := base.Add(-time.Hour).UnixMilli()
	wf, err := h.st.Range(t.Context(), stepped-1000, stepped+5000, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range wf.Segments {
		for _, v := range s.X {
			if v > 90 {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("the 99 gal samples after the step back are not in the archive")
	}
}

// init と、その後に届く waveform フレームが同じサンプルを運ばないこと。
// サンプルを流し込みながら購読を繰り返して確かめる。
func TestSubscribeDoesNotDuplicateInitSamples(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 200, time.Hour.Milliseconds(), time.Hour.Milliseconds())
	h.ch <- source.Connected{Port: "p"}

	base := time.Now()
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		for i := range 600 {
			h.ch <- source.Line{Text: nmea.Format("XSACC,7.00,0.00,0.00"),
				Recv: base.Add(time.Duration(i) * 10 * time.Millisecond)}
		}
	}()

	for range 30 {
		ch, init, cancel, err := h.e.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		var initEnd int64
		if waves := decodeInit(t, init).Waves; len(waves) > 0 {
			last := waveFromB64(t, waves[len(waves)-1])
			initEnd = last.T0 + int64(len(last.X))*int64(last.Dt)
		}
		time.Sleep(5 * time.Millisecond)
		for done := false; !done; {
			select {
			case frame := <-ch:
				if w, ok := decodeWave(frame); ok && initEnd > 0 && w.T0 < initEnd {
					t.Fatalf("init covers up to %d but waveform t0=%d arrived (delivered twice)", initEnd, w.T0)
				}
			default:
				done = true
			}
		}
		cancel()
	}
	<-fed
}

func TestHealth(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 200, time.Hour.Milliseconds(), time.Hour.Milliseconds())

	if got := h.e.Health(); got.OK {
		t.Fatalf("ok before connecting: %+v", got)
	}

	h.ch <- source.Connected{Port: "p"}
	h.feedAt(t, time.Now(), 0, 5, 0.0, 0.1)
	if got := h.e.Health(); !got.OK {
		t.Fatalf("not ok while samples are arriving: %+v", got)
	}

	h.inspect(t, func(e *Engine) {
		e.lastWriteErrAt = time.Now()
		e.refresh()
	})
	if got := h.e.Health(); got.OK {
		t.Fatalf("ok right after a write error: %+v", got)
	}
	h.inspect(t, func(e *Engine) {
		e.lastWriteErrAt = time.Now().Add(-2 * healthWriteErrQuiet)
		e.refresh()
	})
	if got := h.e.Health(); !got.OK {
		t.Fatalf("not ok after write errors stopped: %+v", got)
	}

	h.inspect(t, func(e *Engine) {
		e.lastSampleAt = time.Now().Add(-2 * healthStaleSample)
		e.refresh()
	})
	if got := h.e.Health(); got.OK {
		t.Fatalf("still ok after samples stopped: %+v", got)
	}

	h.ch <- source.Disconnected{Err: "unplugged"}
	h.sync(t)
	if got := h.e.Health(); got.OK {
		t.Fatalf("ok after disconnect: %+v", got)
	}
}

// 読み取りはエンジンの取り込みに巻き込まれないこと。
func TestStatusIsLockFree(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 200, 400, time.Hour.Milliseconds())

	base := time.Now()
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		for i := range 2000 {
			h.ch <- source.Line{Text: nmea.Format("XSACC,50.00,0.00,0.00"),
				Recv: base.Add(time.Duration(i) * 10 * time.Millisecond)}
			if i%20 == 0 {
				h.ch <- source.Line{Text: nmea.Format("XSINT,-1.0,3.0"),
					Recv: base.Add(time.Duration(i) * 10 * time.Millisecond)}
			}
		}
	}()

	start := time.Now()
	for range 20000 {
		h.e.Health()
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("20000 reads took %v while the engine was archiving", d)
	}
	<-fed
}

// Run が止まったあとの購読は待たされずにエラーになること。
func TestSubscribeAfterStop(t *testing.T) {
	t.Parallel()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	w := store.NewWriter(st)
	defer w.Close()

	e, err := NewEngine(st, w, Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		e.Run(ctx, make(chan source.Event))
		close(done)
	}()
	cancel()
	<-done

	if _, _, _, err := e.Subscribe(); err == nil {
		t.Fatal("Subscribe must not block or succeed after Run returned")
	}
}

// init の波形はギャップで分割されること。1ブロックに潰すと t0 + i*dt の復元がずれる。
func TestInitWavesSplitOnGap(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 1000, time.Hour.Milliseconds(), time.Hour.Milliseconds())

	base := time.Now().Add(-20 * time.Second)
	h.feedAt(t, base, 0, 100, 0.0, 0.1)
	h.ch <- source.Disconnected{Err: "unplug"}
	h.ch <- source.Connected{Port: "p"}
	h.feedAt(t, base, 11*time.Second, 100, 0.0, 0.1)

	_, init := h.subscribe(t)
	msg := decodeInit(t, init)
	if len(msg.Waves) != 2 {
		t.Fatalf("not split at the gap: %d segments", len(msg.Waves))
	}
	var lastT int64
	h.inspect(t, func(e *Engine) { lastT = e.lastSampleT })
	last := waveFromB64(t, msg.Waves[len(msg.Waves)-1])
	if end := last.T0 + int64(len(last.X)-1)*int64(last.Dt); end != lastT {
		t.Fatalf("reconstructed end is off by %dms", lastT-end)
	}
}

// 購読者が詰まっていても eqevent は届くこと。波形と同じに捨てると記録を取りこぼす。
func TestEqEventSurvivesSlowSubscriber(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 200, 400, time.Hour.Milliseconds())
	ch, _ := h.subscribe(t) // 読まずに放置してバッファを埋める

	base := time.Now().Add(-30 * time.Second)
	h.feedAt(t, base, 0, 2000, 0.0, 0.1)
	h.feedAt(t, base, 20*time.Second, 200, 3.0, 50.0)
	if len(h.eventsAsc(t)) == 0 {
		t.Fatal("no event was recorded")
	}

	for {
		select {
		case frame := <-ch:
			if name, _, ok := splitFrame(frame); ok && name == "eqevent" {
				return
			}
		default:
			t.Fatal("eqevent did not reach the subscriber")
		}
	}
}

// 生カウントは波形と同じ範囲・同じサンプル数でアーカイブされること。
func TestRawArchivedAlongsideWaveform(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 600, 300, time.Hour.Milliseconds())
	h.drive(t,
		phase{800, 0.0, 0.1},
		phase{600, 2.0, 50.0},
	)
	h.closeActive(t)

	ev := h.eventsAsc(t)[0]
	wf, err := h.st.Range(t.Context(), ev.StartedAt, *ev.EndedAt, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	raws, err := h.st.RawRange(t.Context(), ev.StartedAt, *ev.EndedAt)
	if err != nil {
		t.Fatal(err)
	}

	wfN, rawN := 0, 0
	for _, s := range wf.Segments {
		wfN += s.N
	}
	var found bool
	for _, s := range raws {
		rawN += s.N
		for _, v := range s.X {
			if v == 50 {
				found = true
			}
		}
	}
	if wfN == 0 || rawN != wfN {
		t.Fatalf("raw samples=%d, waveform samples=%d", rawN, wfN)
	}
	if !found {
		t.Fatal("raw counts were not archived")
	}
}

// PGA は丸めてから保存する。status と /api/events で桁が食い違わないこと。
func TestMaxPgaIsRounded(t *testing.T) {
	t.Parallel()
	h := newHarness(t, 400, 400, time.Hour.Milliseconds())
	h.drive(t,
		phase{400, 0.0, 0.10},
		phase{600, 2.0, 12.35},
		phase{800, 0.0, 0.10},
	)

	evs := h.eventsAsc(t)
	if len(evs) != 1 || evs[0].MaxPga == nil {
		t.Fatalf("event is not closed: %+v", evs)
	}
	if got := *evs[0].MaxPga; got != 12.35 {
		t.Fatalf("maxPga=%v", got)
	}
}

// 再起動しても ID を払い出し直して、既存の記録を上書きしないこと。
func TestEventIDSurvivesRestart(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(st)
	for i := range 3 {
		id, err := st.NextEventID()
		if err != nil {
			t.Fatal(err)
		}
		if err := st.InsertEvent(id, int64(i)*1000, int64(i)*1000); err != nil {
			t.Fatal(err)
		}
		if err := st.CloseEvent(id, int64(i)*1000+500, 1, 1); err != nil {
			t.Fatal(err)
		}
	}
	e, err := NewEngine(st, w, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if e.nextEventID != 4 {
		t.Fatalf("nextEventID=%d, want 4", e.nextEventID)
	}
	if e.lastClosed == nil || e.lastClosed.ID != 3 {
		t.Fatalf("the last closed event was not loaded: %+v", e.lastClosed)
	}
	w.Close()
	st.Close()
}
