package store

import "database/sql"

type Event struct {
	ID           int64    `json:"id"`
	StartedAt    int64    `json:"startedAt"` // ms epoch (プリバッファ含む波形先頭)
	TriggeredAt  int64    `json:"triggeredAt"`
	EndedAt      *int64   `json:"endedAt"` // 記録中は nil。波形は [StartedAt, EndedAt) に収まる
	MaxIntensity *float64 `json:"maxIntensity"`
	MaxPga       *float64 `json:"maxPga"`
}

// NextEventID は次に使う ID を返す。呼び出し側で ID を決めれば、
// 記録開始時に INSERT の完了を待たなくて済む。
func (s *Store) NextEventID() (int64, error) {
	var id int64
	err := s.r.QueryRow(`SELECT COALESCE(MAX(id), 0) + 1 FROM events`).Scan(&id)
	return id, err
}

func (s *Store) InsertEvent(id, startedAt, triggeredAt int64) error {
	_, err := s.w.Exec(`INSERT INTO events(id, started_at, triggered_at) VALUES(?,?,?)`,
		id, startedAt, triggeredAt)
	return err
}

func (s *Store) ReopenEvent(id int64) error {
	_, err := s.w.Exec(`UPDATE events SET ended_at=NULL WHERE id=?`, id)
	return err
}

// UpdateEventProgress は記録中も統計値を書いておき、クラッシュ時に最新値を残す。
func (s *Store) UpdateEventProgress(id int64, maxIntensity, maxPga float64) error {
	_, err := s.w.Exec(`UPDATE events SET max_intensity=?, max_pga=? WHERE id=?`, maxIntensity, maxPga, id)
	return err
}

func (s *Store) CloseEvent(id, endedAt int64, maxIntensity, maxPga float64) error {
	_, err := s.w.Exec(`UPDATE events SET ended_at=?, max_intensity=?, max_pga=? WHERE id=?`,
		endedAt, maxIntensity, maxPga, id)
	return err
}

// ListEvents は started_at の新しい順に返す。before>0 ならそれより前に始まったもの (ms epoch)。
// beforeID>0 なら (started_at, id) のタプルで比較する。started_at 同値の行を飛ばさないため、
// ページ送りでは両方を渡すこと。
func (s *Store) ListEvents(limit int, before, beforeID int64) ([]Event, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	q := `SELECT id, started_at, triggered_at, ended_at, max_intensity, max_pga FROM events`
	args := []any{}
	switch {
	case before > 0 && beforeID > 0:
		q += ` WHERE (started_at, id) < (?, ?)`
		args = append(args, before, beforeID)
	case before > 0:
		q += ` WHERE started_at < ?`
		args = append(args, before)
	}
	q += ` ORDER BY started_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.r.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := []Event{}
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.ID, &e.StartedAt, &e.TriggeredAt, &e.EndedAt, &e.MaxIntensity, &e.MaxPga); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

func (s *Store) GetEvent(id int64) (*Event, error) {
	var e Event
	err := s.r.QueryRow(`SELECT id, started_at, triggered_at, ended_at, max_intensity, max_pga FROM events WHERE id=?`, id).
		Scan(&e.ID, &e.StartedAt, &e.TriggeredAt, &e.EndedAt, &e.MaxIntensity, &e.MaxPga)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

func (s *Store) LastEvent() (*Event, error) {
	evs, err := s.ListEvents(1, 0, 0)
	if err != nil || len(evs) == 0 {
		return nil, err
	}
	return &evs[0], nil
}
