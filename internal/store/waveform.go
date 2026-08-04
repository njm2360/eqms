package store

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"log"
	"math"
)

// Segment は連続したサンプル列。間引かれた応答では X/Y/Z の代わりに軸ごとの Min/Max が入る。
type Segment struct {
	T0 int64 `json:"t0"`
	Dt int64 `json:"dt"`
	N  int   `json:"n"`

	X []float32 `json:"x,omitempty"`
	Y []float32 `json:"y,omitempty"`
	Z []float32 `json:"z,omitempty"`

	XMin []float32 `json:"xMin,omitempty"`
	XMax []float32 `json:"xMax,omitempty"`
	YMin []float32 `json:"yMin,omitempty"`
	YMax []float32 `json:"yMax,omitempty"`
	ZMin []float32 `json:"zMin,omitempty"`
	ZMax []float32 `json:"zMax,omitempty"`
}

type WaveformRange struct {
	From      int64     `json:"from"`
	To        int64     `json:"to"`
	Decimated bool      `json:"decimated"`
	Segments  []Segment `json:"segments"`
}

// PersistCursor はアーカイブ済みサンプルの終端時刻を返す。0 なら未記録。
func (s *Store) PersistCursor() (int64, error) {
	var t0, n int64
	err := s.r.QueryRow(`SELECT t0, n FROM chunks ORDER BY t0 DESC LIMIT 1`).Scan(&t0, &n)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return t0 + n*SampleDtMs, nil
}

// AppendChunk は x,y,z を interleave した float32 LE として書く。
func (s *Store) AppendChunk(t0 int64, x, y, z []float32) error {
	return s.appendChunk(t0, len(x), encodeChunk(x, y, z))
}

func encodeChunk(x, y, z []float32) []byte {
	buf := make([]byte, len(x)*3*4)
	for i := range x {
		binary.LittleEndian.PutUint32(buf[(i*3+0)*4:], math.Float32bits(x[i]))
		binary.LittleEndian.PutUint32(buf[(i*3+1)*4:], math.Float32bits(y[i]))
		binary.LittleEndian.PutUint32(buf[(i*3+2)*4:], math.Float32bits(z[i]))
	}
	return buf
}

func (s *Store) appendChunk(t0 int64, n int, data []byte) error {
	res, err := s.w.Exec(`INSERT OR IGNORE INTO chunks(t0, n, data) VALUES(?,?,?)`, t0, n, data)
	if err != nil {
		return err
	}
	// 時刻が巻き戻った直後は既存行とぶつかりうる
	if aff, err := res.RowsAffected(); err == nil && aff == 0 {
		log.Printf("store: chunk t0=%d collided with an existing row, dropped %d samples", t0, n)
	}
	return nil
}

// AppendRawChunk は x,y,z を interleave した int16 LE として書く。
func (s *Store) AppendRawChunk(t0 int64, x, y, z []int16) error {
	return s.appendRawChunk(t0, len(x), encodeRawChunk(x, y, z))
}

func encodeRawChunk(x, y, z []int16) []byte {
	buf := make([]byte, len(x)*3*2)
	for i := range x {
		binary.LittleEndian.PutUint16(buf[(i*3+0)*2:], uint16(x[i]))
		binary.LittleEndian.PutUint16(buf[(i*3+1)*2:], uint16(y[i]))
		binary.LittleEndian.PutUint16(buf[(i*3+2)*2:], uint16(z[i]))
	}
	return buf
}

func (s *Store) appendRawChunk(t0 int64, n int, data []byte) error {
	res, err := s.w.Exec(`INSERT OR IGNORE INTO raw_chunks(t0, n, data) VALUES(?,?,?)`, t0, n, data)
	if err != nil {
		return err
	}
	if aff, err := res.RowsAffected(); err == nil && aff == 0 {
		log.Printf("store: raw chunk t0=%d collided with an existing row, dropped %d samples", t0, n)
	}
	return nil
}

// DeleteChunksBefore は before より古い波形を最大 limit 行消して、消した数を返す。
// events は残す。1回の DELETE を短く保って記録中の追記を待たせない。
func (s *Store) DeleteChunksBefore(before int64, limit int) (int64, error) {
	res, err := s.w.Exec(`DELETE FROM chunks WHERE t0 IN (
  SELECT t0 FROM chunks WHERE t0 < ? ORDER BY t0 LIMIT ?)`, before, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// DeleteRawChunksBefore は before より古い生波形を最大 limit 行消して、消した数を返す。
func (s *Store) DeleteRawChunksBefore(before int64, limit int) (int64, error) {
	res, err := s.w.Exec(`DELETE FROM raw_chunks WHERE t0 IN (
  SELECT t0 FROM raw_chunks WHERE t0 < ? ORDER BY t0 LIMIT ?)`, before, limit)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// VacuumStep は空きページを最大 pages 件だけ OS へ返し、残りの空きページ数を返す。
// 返さないとファイルは保持期間ぶんの最大サイズのまま残る。
func (s *Store) VacuumStep(pages int) (int64, error) {
	if _, err := s.w.Exec(fmt.Sprintf(`PRAGMA incremental_vacuum(%d)`, pages)); err != nil {
		return 0, err
	}
	var free int64
	err := s.w.QueryRow(`PRAGMA freelist_count`).Scan(&free)
	return free, err
}

// Range は [from,to) の波形を返す。points を超える場合は min/max で間引く。
// 範囲が不正か MaxRangeSpanMs を超える場合は ErrBadRange。
func (s *Store) Range(ctx context.Context, from, to int64, points int) (*WaveformRange, error) {
	// from>=0 を先に弾いておかないと to-from が int64 を溢れて桁が壊れる
	if from < 0 || to < from || to-from > MaxRangeSpanMs {
		return nil, fmt.Errorf("%w: from=%d to=%d (from >= 0, span <= %d ms)", ErrBadRange, from, to, int64(MaxRangeSpanMs))
	}
	if to == from {
		return &WaveformRange{From: from, To: to, Segments: []Segment{}}, nil
	}
	if points <= 0 {
		points = 2000
	}
	if points > maxRangePoints {
		points = maxRangePoints
	}
	if (to-from)/SampleDtMs <= int64(points) {
		return s.rangeRaw(ctx, from, to)
	}
	bucket := (to - from) / int64(points)
	if bucket < SampleDtMs {
		bucket = SampleDtMs
	}
	return s.rangeDecimated(ctx, from, to, bucket)
}

func (s *Store) rangeRaw(ctx context.Context, from, to int64) (*WaveformRange, error) {
	out := &WaveformRange{From: from, To: to, Segments: []Segment{}}
	cur := -1
	var curEnd int64
	err := s.eachChunk(ctx, from, to, func(t0 int64, n int, data []byte) error {
		for i := range n {
			t := t0 + int64(i)*SampleDtMs
			if t < from || t >= to {
				continue
			}
			// 前進しすぎも巻き戻りも境界にする。t0 が重なるチャンクを繋ぐと
			// (t0, dt, n) から復元した時刻が実データからずれる
			if cur < 0 || t-curEnd > GapToleranceMs || curEnd-t > GapToleranceMs {
				out.Segments = append(out.Segments, Segment{T0: t, Dt: SampleDtMs})
				cur = len(out.Segments) - 1
			}
			x, y, z := sampleAt(data, i)
			seg := &out.Segments[cur]
			seg.X = append(seg.X, x)
			seg.Y = append(seg.Y, y)
			seg.Z = append(seg.Z, z)
			seg.N++
			curEnd = t + SampleDtMs
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (s *Store) rangeDecimated(ctx context.Context, from, to, bucket int64) (*WaveformRange, error) {
	nb := int((to - from + bucket - 1) / bucket)
	type acc struct {
		xmin, xmax, ymin, ymax, zmin, zmax float32
		ok                                 bool
	}
	buckets := make([]acc, nb)
	err := s.eachChunk(ctx, from, to, func(t0 int64, n int, data []byte) error {
		for i := range n {
			t := t0 + int64(i)*SampleDtMs
			if t < from || t >= to {
				continue
			}
			bi := int((t - from) / bucket)
			if bi < 0 || bi >= nb {
				continue
			}
			x, y, z := sampleAt(data, i)
			b := &buckets[bi]
			if !b.ok {
				b.ok = true
				b.xmin, b.xmax, b.ymin, b.ymax, b.zmin, b.zmax = x, x, y, y, z, z
				continue
			}
			b.xmin, b.xmax = min(b.xmin, x), max(b.xmax, x)
			b.ymin, b.ymax = min(b.ymin, y), max(b.ymax, y)
			b.zmin, b.zmax = min(b.zmin, z), max(b.zmax, z)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	out := &WaveformRange{From: from, To: to, Decimated: true, Segments: []Segment{}}
	cur := -1
	for i := range buckets {
		b := &buckets[i]
		if !b.ok {
			cur = -1
			continue
		}
		if cur < 0 {
			out.Segments = append(out.Segments, Segment{T0: from + int64(i)*bucket, Dt: bucket})
			cur = len(out.Segments) - 1
		}
		seg := &out.Segments[cur]
		seg.XMin = append(seg.XMin, b.xmin)
		seg.XMax = append(seg.XMax, b.xmax)
		seg.YMin = append(seg.YMin, b.ymin)
		seg.YMax = append(seg.YMax, b.ymax)
		seg.ZMin = append(seg.ZMin, b.zmin)
		seg.ZMax = append(seg.ZMax, b.zmax)
		seg.N++
	}
	return out, nil
}

func (s *Store) eachChunk(ctx context.Context, from, to int64, fn func(t0 int64, n int, data []byte) error) error {
	rows, err := s.r.QueryContext(ctx, `SELECT t0, n, data FROM chunks WHERE t0 >= ? AND t0 < ? ORDER BY t0`,
		from-maxChunkSpanMs, to)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var t0 int64
		var n int
		var data []byte
		if err := rows.Scan(&t0, &n, &data); err != nil {
			return err
		}
		if len(data) < n*3*4 {
			return fmt.Errorf("store: chunk too short at t0=%d", t0)
		}
		if err := fn(t0, n, data); err != nil {
			return err
		}
	}
	return rows.Err()
}

// RawSegment は連続した生カウント列。
type RawSegment struct {
	T0 int64   `json:"t0"`
	Dt int64   `json:"dt"`
	N  int     `json:"n"`
	X  []int16 `json:"x"`
	Y  []int16 `json:"y"`
	Z  []int16 `json:"z"`
}

// RawRange は [from,to) の生カウントを返す。間引きはしない。
func (s *Store) RawRange(ctx context.Context, from, to int64) ([]RawSegment, error) {
	if from < 0 || to < from || to-from > MaxRangeSpanMs {
		return nil, fmt.Errorf("%w: from=%d to=%d (from >= 0, span <= %d ms)", ErrBadRange, from, to, int64(MaxRangeSpanMs))
	}
	out := []RawSegment{}
	cur := -1
	var curEnd int64
	rows, err := s.r.QueryContext(ctx, `SELECT t0, n, data FROM raw_chunks WHERE t0 >= ? AND t0 < ? ORDER BY t0`,
		from-maxChunkSpanMs, to)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var t0 int64
		var n int
		var data []byte
		if err := rows.Scan(&t0, &n, &data); err != nil {
			return nil, err
		}
		if len(data) < n*3*2 {
			return nil, fmt.Errorf("store: raw chunk too short at t0=%d", t0)
		}
		for i := range n {
			t := t0 + int64(i)*SampleDtMs
			if t < from || t >= to {
				continue
			}
			if cur < 0 || t-curEnd > GapToleranceMs || curEnd-t > GapToleranceMs {
				out = append(out, RawSegment{T0: t, Dt: SampleDtMs})
				cur = len(out) - 1
			}
			o := i * 6
			seg := &out[cur]
			seg.X = append(seg.X, int16(binary.LittleEndian.Uint16(data[o:])))
			seg.Y = append(seg.Y, int16(binary.LittleEndian.Uint16(data[o+2:])))
			seg.Z = append(seg.Z, int16(binary.LittleEndian.Uint16(data[o+4:])))
			seg.N++
			curEnd = t + SampleDtMs
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func sampleAt(data []byte, i int) (x, y, z float32) {
	o := i * 12
	return math.Float32frombits(binary.LittleEndian.Uint32(data[o:])),
		math.Float32frombits(binary.LittleEndian.Uint32(data[o+4:])),
		math.Float32frombits(binary.LittleEndian.Uint32(data[o+8:]))
}
