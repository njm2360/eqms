package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newWriter(t *testing.T) (*Writer, *Store) {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	w := NewWriter(s)
	t.Cleanup(func() {
		w.Close()
		s.Close()
	})
	return w, s
}

// events の INSERT が chunks の追記に追い越されないこと。
func TestWriterKeepsOrder(t *testing.T) {
	w, s := newWriter(t)
	x := make([]float32, 100)
	for i := range 50 {
		id := int64(i + 1)
		w.InsertEvent(id, int64(i)*1000, int64(i)*1000)
		w.AppendChunk(int64(i)*1000, x, x, x)
		w.CloseEvent(id, int64(i)*1000+1000, 1.0, 2.0)
	}
	w.Sync()

	evs, err := s.ListEvents(100, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 50 {
		t.Fatalf("events=%d, want 50", len(evs))
	}
	for _, e := range evs {
		if e.EndedAt == nil {
			t.Fatalf("event %d was closed before it was inserted", e.ID)
		}
	}
	if w.Failed() != 0 || w.Dropped() != 0 {
		t.Fatalf("failed=%d dropped=%d", w.Failed(), w.Dropped())
	}
}

// AppendChunk から戻ったらスライスを再利用してよいこと。
func TestWriterCopiesSamples(t *testing.T) {
	w, s := newWriter(t)
	buf := make([]float32, 100)
	for i := range buf {
		buf[i] = float32(i)
	}
	w.AppendChunk(1000, buf, buf, buf)
	for i := range buf {
		buf[i] = -1
	}
	w.Sync()

	got, err := s.Range(t.Context(), 1000, 2000, 100000)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Segments) != 1 || got.Segments[0].X[42] != 42 {
		t.Fatalf("the reused slice leaked into the archive: %+v", got.Segments)
	}
}

// Close は予約済みの書き込みを捨てないこと。
func TestWriterDrainsOnClose(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	w := NewWriter(s)
	x := make([]float32, 100)
	for i := range 100 {
		w.AppendChunk(int64(i)*1000, x, x, x)
	}
	w.Close()

	cur, err := s.PersistCursor()
	if err != nil {
		t.Fatal(err)
	}
	if cur != 100_000 {
		t.Fatalf("cursor=%d, want 100000 (writes were dropped at close)", cur)
	}
}

// 保持期間切れの削除中でも記録の書き込みが通ること。
func TestPruneYieldsToWrites(t *testing.T) {
	s := open(t)
	x := make([]float32, 1)
	for i := range 20_000 {
		if err := s.AppendChunk(int64(i)*1000, x, x, x); err != nil {
			t.Fatal(err)
		}
	}
	w := NewWriter(s)
	t.Cleanup(w.Close)

	start := time.Now()
	w.Prune(20_000 * 1000)
	w.AppendChunk(500_000_000, x, x, x)
	w.Sync()
	write := time.Since(start)

	remaining := func() int64 {
		var n int64
		if err := s.r.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if remaining() <= 1 {
		t.Fatal("the prune finished before the write, so nothing was interleaved")
	}
	if write > 50*time.Millisecond {
		t.Fatalf("the write waited %v for the prune", write)
	}

	deadline := time.Now().Add(30 * time.Second)
	for remaining() > 1 {
		if time.Now().After(deadline) {
			t.Fatalf("the prune did not finish, %d chunks left", remaining())
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// 削除は limit で区切られ、書き込みコネクションを長く握らないこと。
func TestDeleteChunksBeforeIsBatched(t *testing.T) {
	s := open(t)
	x := make([]float32, 1)
	for i := range 10 {
		if err := s.AppendChunk(int64(i)*1000, x, x, x); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.DeleteChunksBefore(10_000, 4)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 {
		t.Fatalf("deleted=%d, want 4", n)
	}
	got, err := s.Range(t.Context(), 0, 10_000, 100000)
	if err != nil {
		t.Fatal(err)
	}
	total := 0
	for _, seg := range got.Segments {
		total += seg.N
	}
	if total != 6 {
		t.Fatalf("remaining=%d, want 6", total)
	}
}

// 空きページを返し切ったら止まること。
func TestVacuumStepTerminates(t *testing.T) {
	s := open(t)
	x := make([]float32, 100)
	for i := range 200 {
		if err := s.AppendChunk(int64(i)*1000, x, x, x); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.DeleteChunksBefore(200_000, 1000); err != nil {
		t.Fatal(err)
	}
	for i := range 100 {
		free, err := s.VacuumStep(vacuumPages)
		if err != nil {
			t.Fatal(err)
		}
		if free == 0 {
			return
		}
		if i == 99 {
			t.Fatalf("still %d free pages after 100 steps", free)
		}
	}
}
