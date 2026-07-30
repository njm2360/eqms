// Package store は SQLite 永続化。chunks は地震記録中だけ書かれる波形アーカイブで、
// events はその範囲を指す注釈。記録していない間の波形は残らない。
package store

import (
	"database/sql"
	"errors"
	"math"

	_ "modernc.org/sqlite"
)

const (
	SampleDtMs = 10
	// サンプル列の時刻ズレがこれを超えたら欠落とみなす。書き込み側がチャンクを切る基準と、
	// 読み出し側がセグメントを割る基準の両方に使う
	GapToleranceMs = 50
	// 範囲検索の下限をこれだけ広げて、from をまたぐチャンクを取りこぼさない
	maxChunkSpanMs = 2000
	maxRangePoints = 20000
	// 1リクエストで走査を許す時間幅
	MaxRangeSpanMs = 90 * 24 * 3600 * 1000

	MaxListLimit     = 200
	DefaultListLimit = 50
	readPoolSize     = 4
)

var ErrBadRange = errors.New("store: invalid time range")

// Store は書き込みと読み込みでコネクションを分ける。1本に混ぜると長い範囲検索が
// エンジンのチャンク書き込みを止めてしまう。
type Store struct {
	w *sql.DB // 書き込みは1コネクションへ直列化して SQLITE_BUSY を避ける
	r *sql.DB
}

func Open(path string) (*Store, error) {
	w, err := sql.Open("sqlite", path+
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"+
		"&_pragma=auto_vacuum(incremental)")
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)

	s := &Store{w: w}
	// 先に書き込み側でファイルを作る。pragma はファイル生成時にしか効かないものがある
	if err := s.init(); err != nil {
		w.Close()
		return nil, err
	}

	r, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(readPoolSize)
	s.r = r
	return s, nil
}

func (s *Store) init() error {
	_, err := s.w.Exec(`
CREATE TABLE IF NOT EXISTS events (
  id            INTEGER PRIMARY KEY,
  started_at    INTEGER NOT NULL,
  triggered_at  INTEGER NOT NULL,
  ended_at      INTEGER,
  max_intensity REAL,
  max_pga       REAL
);
CREATE TABLE IF NOT EXISTS chunks (
  t0   INTEGER PRIMARY KEY,
  n    INTEGER NOT NULL,
  data BLOB    NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_started ON events(started_at DESC, id DESC);
`)
	if err != nil {
		return err
	}
	return s.finalizeOrphans()
}

// finalizeOrphans は開いたままの記録をアーカイブ上の最終チャンクで閉じる。
func (s *Store) finalizeOrphans() error {
	rows, err := s.w.Query(`SELECT id, started_at, triggered_at FROM events WHERE ended_at IS NULL ORDER BY started_at`)
	if err != nil {
		return err
	}
	type orphan struct{ id, startedAt, triggeredAt int64 }
	var orphans []orphan
	for rows.Next() {
		var o orphan
		if err := rows.Scan(&o.id, &o.startedAt, &o.triggeredAt); err != nil {
			rows.Close()
			return err
		}
		orphans = append(orphans, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i, o := range orphans {
		// 次の記録のチャンクを拾って範囲を重ねないよう上限を切る
		limit := int64(math.MaxInt64)
		if i+1 < len(orphans) {
			limit = orphans[i+1].startedAt
		}
		var t0, n sql.NullInt64
		err := s.w.QueryRow(`SELECT t0, n FROM chunks WHERE t0 >= ? AND t0 < ? ORDER BY t0 DESC LIMIT 1`,
			o.startedAt, limit).Scan(&t0, &n)
		if err != nil && err != sql.ErrNoRows {
			return err
		}
		// 波形が保持期間で消えていても triggered_at より前にはしない
		end := max(o.startedAt, o.triggeredAt)
		if t0.Valid {
			end = max(end, t0.Int64+n.Int64*SampleDtMs)
		}
		if _, err := s.w.Exec(`UPDATE events SET ended_at=? WHERE id=?`, end, o.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error {
	var err error
	if s.r != nil {
		err = s.r.Close()
	}
	if werr := s.w.Close(); err == nil {
		err = werr
	}
	return err
}
