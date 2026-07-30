// Package web は HTTP API と SSE、埋め込み SPA の配信。
package web

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/njm2360/eqms/internal/core"
	"github.com/njm2360/eqms/internal/store"
)

// DefaultStreamWrite は SSE の1回の書き込みに許す時間。これを超えた購読者は切り離す。
const DefaultStreamWrite = 10 * time.Second

type Config struct {
	MaxStreams  int           // SSE の同時接続上限。0 以下で無制限
	StreamWrite time.Duration // 0 で DefaultStreamWrite
}

type Server struct {
	engine      *core.Engine
	st          *store.Store
	maxStreams  int
	streamWrite time.Duration
	streams     atomic.Int64
}

func NewServer(engine *core.Engine, st *store.Store, cfg Config) *Server {
	if cfg.StreamWrite <= 0 {
		cfg.StreamWrite = DefaultStreamWrite
	}
	return &Server{engine: engine, st: st, maxStreams: cfg.MaxStreams, streamWrite: cfg.StreamWrite}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/events/{id}", s.handleEvent)
	mux.HandleFunc("GET /api/waveform", s.handleWaveform)
	mux.HandleFunc("GET /health", s.handleHealth)
	// SPA フォールバックが API の名前空間まで飲み込まないようにする
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "no such endpoint", http.StatusNotFound)
	})
	mux.Handle("/", spaHandler())
	return secHeaders(mux)
}

func secHeaders(next http.Handler) http.Handler {
	// style の 'unsafe-inline' は React の style 属性と uPlot に必要
	const csp = "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; " +
		"object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'none'"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Strict-Transport-Security", "max-age=15552000")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	h := s.engine.Health()
	code := http.StatusOK
	if !h.OK {
		code = http.StatusServiceUnavailable
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSONStatus(w, r, code, h)
}

func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	rc := http.NewResponseController(w)
	// 期限を張れないコネクションは詰まったときに回収できない。
	// 空の期限は解除なので、ここでは対応可否だけを見る
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	if n := s.streams.Add(1); s.maxStreams > 0 && n > int64(s.maxStreams) {
		s.streams.Add(-1)
		http.Error(w, "too many streams", http.StatusServiceUnavailable)
		return
	}
	defer s.streams.Add(-1)

	ch, init, cancel, err := s.engine.Subscribe()
	if err != nil {
		http.Error(w, "service unavailable", http.StatusServiceUnavailable)
		return
	}
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no") // リバースプロキシのバッファリング抑止

	// 書き込みごとに期限を張り直す。読まなくなった購読者はここで切れる
	write := func(b []byte) error {
		if err := rc.SetWriteDeadline(time.Now().Add(s.streamWrite)); err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return err
		}
		return rc.Flush()
	}

	if write(init) != nil {
		return
	}
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if write([]byte(": ping\n\n")) != nil {
				return
			}
		case frame := <-ch:
			if write(frame) != nil {
				return
			}
		}
	}
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := intParam(q, "limit", 0)
	if !ok {
		http.Error(w, "limit must be an integer", http.StatusBadRequest)
		return
	}
	before, ok := intParam(q, "before", 0)
	if !ok {
		http.Error(w, "before must be ms epoch", http.StatusBadRequest)
		return
	}
	beforeID, ok := intParam(q, "beforeId", 0)
	if !ok {
		http.Error(w, "beforeId must be an integer", http.StatusBadRequest)
		return
	}
	n := int(limit)
	if n <= 0 {
		n = store.DefaultListLimit
	}
	if n > store.MaxListLimit {
		n = store.MaxListLimit
	}
	// 1件多く引いて、続きがあるかを呼び出し側に返す
	events, err := s.st.ListEvents(n+1, before, beforeID)
	if err != nil {
		internalError(w, "list events", err)
		return
	}
	res := eventsResponse{Events: events}
	if len(events) > n {
		res.Events = events[:n]
		last := &res.Events[n-1]
		res.NextBefore, res.NextBeforeID = &last.StartedAt, &last.ID
	}
	writeJSON(w, r, res)
}

type eventsResponse struct {
	Events []store.Event `json:"events"`
	// 次ページに渡す before / beforeId。続きがなければ null。
	// startedAt 同値の記録を飛ばさないため両方を渡すこと
	NextBefore   *int64 `json:"nextBefore"`
	NextBeforeID *int64 `json:"nextBeforeId"`
}

func (s *Server) handleEvent(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ev, err := s.st.GetEvent(id)
	if err != nil {
		internalError(w, "get event", err)
		return
	}
	if ev == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	writeJSON(w, r, ev)
}

func (s *Server) handleWaveform(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("from") == "" || q.Get("to") == "" {
		http.Error(w, "from and to are required (ms epoch)", http.StatusBadRequest)
		return
	}
	from, ok1 := intParam(q, "from", 0)
	to, ok2 := intParam(q, "to", 0)
	points, ok3 := intParam(q, "points", 0)
	if !ok1 || !ok2 || !ok3 {
		http.Error(w, "from, to and points must be integers", http.StatusBadRequest)
		return
	}
	wf, err := s.st.Range(from, to, int(points))
	if errors.Is(err, store.ErrBadRange) {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err != nil {
		internalError(w, "waveform range", err)
		return
	}
	writeJSON(w, r, wf)
}

// intParam は空なら def を返す。数値でなければ ok=false。
func intParam(q url.Values, name string, def int64) (int64, bool) {
	v := q.Get(name)
	if v == "" {
		return def, true
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}
