package store

import (
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// newEvent は ID を払い出して events へ1件入れる。
func newEvent(t *testing.T, s *Store, startedAt, triggeredAt int64) int64 {
	t.Helper()
	id, err := s.NextEventID()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.InsertEvent(id, startedAt, triggeredAt); err != nil {
		t.Fatal(err)
	}
	return id
}

// ramp は値が連番のチャンクを書く。位置のずれを検出できる。
func ramp(t *testing.T, s *Store, t0 int64, n int, base float32) {
	t.Helper()
	x := make([]float32, n)
	y := make([]float32, n)
	z := make([]float32, n)
	for i := range n {
		x[i] = base + float32(i)
		y[i] = -(base + float32(i))
		z[i] = 0
	}
	if err := s.AppendChunk(t0, x, y, z); err != nil {
		t.Fatal(err)
	}
}

func TestRangeRaw(t *testing.T) {
	s := open(t)
	ramp(t, s, 1000, 100, 0)

	got, err := s.Range(1000, 2000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Decimated {
		t.Fatal("1000 samples or fewer must not be decimated")
	}
	if len(got.Segments) != 1 {
		t.Fatalf("segments=%d, want 1", len(got.Segments))
	}
	seg := got.Segments[0]
	if seg.T0 != 1000 || seg.N != 100 || seg.Dt != SampleDtMs {
		t.Fatalf("bad segment: %+v", seg)
	}
	if seg.X[0] != 0 || seg.X[99] != 99 {
		t.Fatalf("values do not match: %v %v", seg.X[0], seg.X[99])
	}
}

func TestRangeGapSplitsSegments(t *testing.T) {
	s := open(t)
	ramp(t, s, 1000, 100, 0)   // 1000-2000ms
	ramp(t, s, 5000, 100, 100) // 3秒の欠落を挟む

	got, err := s.Range(1000, 6000, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 2 {
		t.Fatalf("segments=%d, want 2", len(got.Segments))
	}
	if got.Segments[0].T0 != 1000 || got.Segments[1].T0 != 5000 {
		t.Fatalf("segment t0 is not the actual time: %d %d", got.Segments[0].T0, got.Segments[1].T0)
	}
	if got.Segments[1].X[0] != 100 {
		t.Fatalf("values are misaligned: %v", got.Segments[1].X[0])
	}
}

func TestRangeContiguousChunksMerge(t *testing.T) {
	s := open(t)
	ramp(t, s, 1000, 100, 0)
	ramp(t, s, 2000, 100, 100)

	got, err := s.Range(1000, 3000, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 1 {
		t.Fatalf("segments=%d, want 1", len(got.Segments))
	}
	if got.Segments[0].N != 200 {
		t.Fatalf("n=%d, want 200", got.Segments[0].N)
	}
}

func TestRangeDecimatedKeepsPeaks(t *testing.T) {
	s := open(t)
	x := make([]float32, 100)
	y := make([]float32, 100)
	z := make([]float32, 100)
	x[50], x[51] = 999, -999 // 単発のピーク
	if err := s.AppendChunk(1000, x, y, z); err != nil {
		t.Fatal(err)
	}

	got, err := s.Range(1000, 2000, 10) // 100サンプル → 10バケット
	if err != nil {
		t.Fatal(err)
	}
	if !got.Decimated {
		t.Fatal("must be decimated")
	}
	var hiFound, loFound bool
	for _, seg := range got.Segments {
		for i := range seg.N {
			if seg.XMax[i] == 999 {
				hiFound = true
			}
			if seg.XMin[i] == -999 {
				loFound = true
			}
		}
	}
	if !hiFound || !loFound {
		t.Fatalf("peaks were lost: hi=%v lo=%v", hiFound, loFound)
	}
}

func TestRangeEmpty(t *testing.T) {
	s := open(t)
	for _, tc := range [][2]int64{{0, 0}, {50000, 60000}} {
		got, err := s.Range(tc[0], tc[1], 1000)
		if err != nil {
			t.Fatal(err)
		}
		if len(got.Segments) != 0 {
			t.Fatalf("range(%d,%d): segments=%d, want 0", tc[0], tc[1], len(got.Segments))
		}
	}
}

// 極端な from/to で panic せず ErrBadRange を返すこと。
func TestRangeRejectsBadInput(t *testing.T) {
	s := open(t)
	for _, tc := range []struct {
		name     string
		from, to int64
	}{
		{"reversed", 2000, 1000},
		{"negative from", -1, 1000},
		{"to=MaxInt64", 0, math.MaxInt64},
		{"from=MinInt64", math.MinInt64, math.MaxInt64},
		{"over the span limit", 0, MaxRangeSpanMs + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Range(tc.from, tc.to, 20000)
			if !errors.Is(err, ErrBadRange) {
				t.Fatalf("err=%v got=%v, want ErrBadRange", err, got)
			}
		})
	}
	if _, err := s.Range(0, MaxRangeSpanMs, 2000); err != nil {
		t.Fatalf("exactly the span limit must pass: %v", err)
	}
}

// ページングは started_at でカーソルを進め、インデックスが実際に使われること。
func TestListEventsPaginatesByStartedAt(t *testing.T) {
	s := open(t)
	for i := range 5 {
		newEvent(t, s, int64(i)*1000, int64(i)*1000+5)
	}

	first, err := s.ListEvents(2, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || first[0].StartedAt != 4000 || first[1].StartedAt != 3000 {
		t.Fatalf("not ordered newest first: %+v", first)
	}
	next, err := s.ListEvents(2, first[1].StartedAt, first[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 2 || next[0].StartedAt != 2000 || next[1].StartedAt != 1000 {
		t.Fatalf("cursor had no effect: %+v", next)
	}

	var plan string
	err = s.r.QueryRow(`EXPLAIN QUERY PLAN
SELECT id FROM events WHERE started_at < 1 ORDER BY started_at DESC, id DESC LIMIT 1`).
		Scan(new(int), new(int), new(int), &plan)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan, "idx_events_started") {
		t.Fatalf("idx_events_started is not used: %s", plan)
	}
}

// 読み込みプールは書き込みと別コネクションで、書き込みを塞がないこと。
func TestReadDoesNotBlockWrite(t *testing.T) {
	s := open(t)
	x := make([]float32, 100)
	for i := range 20000 {
		if err := s.AppendChunk(int64(i)*1000, x, x, x); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, err := s.Range(0, 20000*1000, 20000); err != nil {
			t.Error(err)
		}
	}()

	start := time.Now()
	if err := s.AppendChunk(20001*1000, x, x, x); err != nil {
		t.Fatal(err)
	}
	waited := time.Since(start)
	<-done
	if waited > 30*time.Millisecond {
		t.Fatalf("the write was dragged down by the read: %v", waited)
	}
}

func TestPersistCursor(t *testing.T) {
	s := open(t)
	if cur, err := s.PersistCursor(); err != nil || cur != 0 {
		t.Fatalf("an empty DB must give 0: cur=%d err=%v", cur, err)
	}
	ramp(t, s, 1000, 100, 0)
	cur, err := s.PersistCursor()
	if err != nil {
		t.Fatal(err)
	}
	if cur != 2000 {
		t.Fatalf("cur=%d, want 2000", cur)
	}
}

func TestAppendChunkIdempotent(t *testing.T) {
	s := open(t)
	ramp(t, s, 1000, 100, 0)
	ramp(t, s, 1000, 100, 0)
	got, err := s.Range(1000, 2000, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 1 || got.Segments[0].N != 100 {
		t.Fatalf("written twice: %+v", got.Segments)
	}
}

func TestPruneKeepsEvents(t *testing.T) {
	s := open(t)
	ramp(t, s, 1000, 100, 0)
	ramp(t, s, 100000, 100, 0)
	id := newEvent(t, s, 1000, 1000)
	if err := s.CloseEvent(id, 2000, 3.0, 100); err != nil {
		t.Fatal(err)
	}

	n, err := s.DeleteChunksBefore(50000, pruneBatchRows)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("deleted=%d, want 1", n)
	}
	ev, err := s.GetEvent(id)
	if err != nil || ev == nil {
		t.Fatalf("the event was deleted: %v %v", ev, err)
	}
	got, err := s.Range(1000, 2000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 0 {
		t.Fatal("waveform past the retention period is still there")
	}
}

func TestFinalizeOrphansOnReopen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "t.db")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := newEvent(t, s, 1000, 1000)
	ramp(t, s, 1000, 100, 0)
	ramp(t, s, 2000, 100, 0)
	s.Close() // ended_at を書かずに落ちた状況

	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ev, err := s2.GetEvent(id)
	if err != nil {
		t.Fatal(err)
	}
	if ev.EndedAt == nil {
		t.Fatal("the open event was not closed")
	}
	if *ev.EndedAt != 3000 {
		t.Fatalf("ended_at=%d, want 3000 (end of the last chunk)", *ev.EndedAt)
	}
}

// started_at が同値でもカーソルで全件辿れること。
func TestListEventsCursorKeepsSameStartedAt(t *testing.T) {
	s := open(t)
	for range 3 {
		newEvent(t, s, 1000, 1000)
	}
	newEvent(t, s, 500, 500)

	seen := map[int64]bool{}
	var before, beforeID int64
	for {
		page, err := s.ListEvents(2, before, beforeID)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, e := range page {
			if seen[e.ID] {
				t.Fatalf("event %d was returned twice", e.ID)
			}
			seen[e.ID] = true
		}
		last := page[len(page)-1]
		before, beforeID = last.StartedAt, last.ID
	}
	if len(seen) != 4 {
		t.Fatalf("reached only %d/4 events: %v", len(seen), seen)
	}
}

// t0 が重なるチャンクを1セグメントに繋ぐと (t0, dt, n) からの復元がずれる。
func TestRangeSplitsOverlappingChunks(t *testing.T) {
	s := open(t)
	ramp(t, s, 1000, 100, 0)
	ramp(t, s, 1500, 100, 1000) // 巻き戻って500ms重なる

	wf, err := s.Range(0, 4000, maxRangePoints)
	if err != nil {
		t.Fatal(err)
	}
	if len(wf.Segments) != 2 {
		t.Fatalf("not split at the overlap: %+v", wf.Segments)
	}
	for _, seg := range wf.Segments {
		if seg.T0+int64(seg.N)*seg.Dt > seg.T0+1000 {
			t.Fatalf("segment is longer than the actual data: t0=%d n=%d", seg.T0, seg.N)
		}
	}
	if wf.Segments[1].T0 != 1500 || wf.Segments[1].X[0] != 1000 {
		t.Fatalf("the second segment is misplaced: %+v", wf.Segments[1])
	}
}

// 波形が保持期間で消えた記録を閉じても、終了時刻が検知時刻より前にならないこと。
func TestFinalizeOrphanWithoutWaveform(t *testing.T) {
	path := filepath.Join(t.TempDir(), "o.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	id := newEvent(t, s, 1_000_000, 1_060_000) // startedAt はプリバッファぶん前
	s.Close()

	s2, err := Open(path) // ここで finalizeOrphans が走る
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	ev, err := s2.GetEvent(id)
	if err != nil {
		t.Fatal(err)
	}
	if ev.EndedAt == nil {
		t.Fatal("the event was not closed")
	}
	if *ev.EndedAt < ev.TriggeredAt {
		t.Fatalf("duration is negative: triggered=%d ended=%d", ev.TriggeredAt, *ev.EndedAt)
	}
}
