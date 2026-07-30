package store

import (
	"log"
	"sync/atomic"
)

const (
	writeQueueLen = 2048 // 記録中は毎秒2件。ディスクが十数分止まっても取りこぼさない深さ

	// 保守作業を1ステップで進める量。前景ジョブへ戻る間隔になる
	pruneBatchRows = 2000
	vacuumPages    = 256
)

// Writer は書き込みを1本のゴルーチンへ直列化する。呼び出し側はブロックしない。
// 送信順は保たれるので、events の INSERT が chunks の追記より後になることはない。
type Writer struct {
	st    *Store
	jobs  chan job
	maint chan int64
	quit  chan struct{}
	done  chan struct{}

	dropped atomic.Int64
	failed  atomic.Int64
}

type job struct {
	name string
	run  func(*Store) error
}

func NewWriter(st *Store) *Writer {
	w := &Writer{
		st:    st,
		jobs:  make(chan job, writeQueueLen),
		maint: make(chan int64, 1),
		quit:  make(chan struct{}),
		done:  make(chan struct{}),
	}
	go w.run()
	return w
}

// Close は予約済みの書き込みを流し切って戻る。保守作業は中断する。
func (w *Writer) Close() {
	close(w.quit)
	<-w.done
}

func (w *Writer) Dropped() int64 { return w.dropped.Load() }
func (w *Writer) Failed() int64  { return w.failed.Load() }

// Sync は予約済みの書き込みが終わるまで待つ。
func (w *Writer) Sync() {
	done := make(chan struct{})
	if !w.submit("sync", func(*Store) error { close(done); return nil }) {
		return
	}
	select {
	case <-done:
	case <-w.done:
	}
}

func (w *Writer) InsertEvent(id, startedAt, triggeredAt int64) {
	w.submit("insert event", func(s *Store) error { return s.InsertEvent(id, startedAt, triggeredAt) })
}

func (w *Writer) ReopenEvent(id int64) {
	w.submit("reopen event", func(s *Store) error { return s.ReopenEvent(id) })
}

func (w *Writer) UpdateEventProgress(id int64, maxIntensity, maxPga float64) {
	w.submit("update event", func(s *Store) error { return s.UpdateEventProgress(id, maxIntensity, maxPga) })
}

func (w *Writer) CloseEvent(id, endedAt int64, maxIntensity, maxPga float64) {
	w.submit("close event", func(s *Store) error { return s.CloseEvent(id, endedAt, maxIntensity, maxPga) })
}

// AppendChunk が戻った時点で x,y,z は再利用してよい。
func (w *Writer) AppendChunk(t0 int64, x, y, z []float32) {
	n := len(x)
	data := encodeChunk(x, y, z)
	w.submit("append chunk", func(s *Store) error { return s.appendChunk(t0, n, data) })
}

// Prune は before より古い波形の削除を予約する。実行は前景ジョブの合間に分割して進む。
func (w *Writer) Prune(before int64) {
	select {
	case <-w.maint: // 未着手の予約は新しい期限で置き換える
	default:
	}
	select {
	case w.maint <- before:
	default:
	}
}

// 満杯なら捨てる。待たせるとサンプルの取り込みが止まる。
func (w *Writer) submit(name string, run func(*Store) error) bool {
	select {
	case w.jobs <- job{name: name, run: run}:
		return true
	default:
		if n := w.dropped.Add(1); n == 1 || n%100 == 0 {
			log.Printf("store: write queue full, dropped %s (%d dropped)", name, n)
		}
		return false
	}
}

func (w *Writer) run() {
	defer close(w.done)
	var task *pruneTask
	for {
		select { // 前景優先。保守作業に記録の書き込みを待たせない
		case j := <-w.jobs:
			w.exec(j)
			continue
		default:
		}

		if task != nil {
			select {
			case <-w.quit:
				w.drain()
				return
			default:
			}
			if w.stepPrune(task) {
				task = nil
			}
			continue
		}

		select {
		case j := <-w.jobs:
			w.exec(j)
		case before := <-w.maint:
			task = &pruneTask{before: before}
		case <-w.quit:
			w.drain()
			return
		}
	}
}

func (w *Writer) drain() {
	for {
		select {
		case j := <-w.jobs:
			w.exec(j)
		default:
			return
		}
	}
}

func (w *Writer) exec(j job) {
	if err := j.run(w.st); err != nil {
		log.Printf("store: %s: %v (%d write errors)", j.name, err, w.failed.Add(1))
	}
}

type pruneTask struct {
	before   int64
	deleted  int64
	sweeping bool
	lastFree int64
}

// stepPrune は1バッチ進めて、終わったかを返す。
func (w *Writer) stepPrune(p *pruneTask) bool {
	if !p.sweeping {
		n, err := w.st.DeleteChunksBefore(p.before, pruneBatchRows)
		if err != nil {
			log.Printf("store: prune chunks: %v", err)
			return true
		}
		p.deleted += n
		if n == pruneBatchRows {
			return false
		}
		if p.deleted == 0 {
			return true
		}
		log.Printf("store: pruned %d chunks older than %d", p.deleted, p.before)
		p.sweeping = true
		return false
	}

	free, err := w.st.VacuumStep(vacuumPages)
	if err != nil {
		log.Printf("store: incremental vacuum: %v", err)
		return true
	}
	if free == 0 {
		return true
	}
	if p.lastFree > 0 && free >= p.lastFree {
		log.Printf("store: incremental vacuum is not shrinking the file, %d free pages remain; auto_vacuum is probably not incremental", free)
		return true
	}
	p.lastFree = free
	return false
}
