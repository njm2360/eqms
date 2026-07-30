package web

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/njm2360/eqms/internal/core"
	"github.com/njm2360/eqms/internal/source"
	"github.com/njm2360/eqms/internal/store"
)

const testMaxStreams = 4

func newTestServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	w := store.NewWriter(st)
	engine, err := core.NewEngine(st, w, core.Config{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		engine.Run(ctx, make(chan source.Event))
		close(done)
	}()

	srv := httptest.NewServer(NewServer(engine, st, Config{MaxStreams: testMaxStreams}).Handler())
	t.Cleanup(func() {
		srv.Close()
		cancel()
		<-done
		w.Close()
		st.Close()
	})
	return srv, st
}

// newEvent は ID を払い出して events へ1件入れる。
func newEvent(t *testing.T, st *store.Store, startedAt, triggeredAt int64) int64 {
	t.Helper()
	id, err := st.NextEventID()
	if err != nil {
		t.Fatal(err)
	}
	if err := st.InsertEvent(id, startedAt, triggeredAt); err != nil {
		t.Fatal(err)
	}
	return id
}

func get(t *testing.T, srv *httptest.Server, path string) (int, string) {
	t.Helper()
	res, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return res.StatusCode, string(b)
}

// 極端な from/to でハンドラを落とさないこと。
func TestWaveformRejectsBadRange(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{
		"/api/waveform?from=0&to=9223372036854775807",
		"/api/waveform?from=-1&to=1000",
		"/api/waveform?from=2000&to=1000",
		"/api/waveform?from=0&to=99999999999999",
		"/api/waveform?from=abc&to=1000",
		"/api/waveform?from=0&to=1000&points=xyz",
		"/api/waveform?to=1000",
		"/api/waveform",
	} {
		if code, body := get(t, srv, path); code != http.StatusBadRequest {
			t.Errorf("GET %s → %d %q, want 400", path, code, body)
		}
	}
}

func TestWaveformAcceptsValidRange(t *testing.T) {
	srv, st := newTestServer(t)
	x := make([]float32, 100)
	for i := range x {
		x[i] = float32(i)
	}
	if err := st.AppendChunk(100_000, x, x, x); err != nil {
		t.Fatal(err)
	}

	code, body := get(t, srv, "/api/waveform?from=100000&to=101000&points=1000")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var wf store.WaveformRange
	if err := json.Unmarshal([]byte(body), &wf); err != nil {
		t.Fatal(err)
	}
	if len(wf.Segments) != 1 || wf.Segments[0].N != 100 {
		t.Fatalf("no waveform returned: %+v", wf.Segments)
	}
}

// before は startedAt の ms epoch。nextBefore を辿ると全件を重複なく走査できること。
func TestEventsPaginationByTime(t *testing.T) {
	srv, st := newTestServer(t)
	for i := range 5 {
		newEvent(t, st, int64(i)*10_000, int64(i)*10_000)
	}

	var seen []int64
	before := int64(0)
	for range 10 {
		path := "/api/events?limit=2"
		if before > 0 {
			path += "&before=" + strconv.FormatInt(before, 10)
		}
		code, body := get(t, srv, path)
		if code != http.StatusOK {
			t.Fatalf("code=%d body=%s", code, body)
		}
		var page struct {
			Events     []store.Event `json:"events"`
			NextBefore *int64        `json:"nextBefore"`
		}
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatal(err)
		}
		for _, e := range page.Events {
			seen = append(seen, e.StartedAt)
		}
		if page.NextBefore == nil {
			break
		}
		before = *page.NextBefore
	}

	want := []int64{40_000, 30_000, 20_000, 10_000, 0}
	if !slices.Equal(seen, want) {
		t.Fatalf("walked %v, want %v", seen, want)
	}
}

func TestEventsLimitIsCapped(t *testing.T) {
	srv, st := newTestServer(t)
	for i := range 3 {
		newEvent(t, st, int64(i)*10_000, int64(i)*10_000)
	}
	code, body := get(t, srv, "/api/events?limit=99999")
	if code != http.StatusOK {
		t.Fatalf("code=%d body=%s", code, body)
	}
	var page struct {
		Events     []store.Event `json:"events"`
		NextBefore *int64        `json:"nextBefore"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 3 || page.NextBefore != nil {
		t.Fatalf("a limit over the cap must still return everything: %d events next=%v", len(page.Events), page.NextBefore)
	}
}

func TestJSONIsGzippedWhenAccepted(t *testing.T) {
	srv, st := newTestServer(t)
	x := make([]float32, 100)
	if err := st.AppendChunk(100_000, x, x, x); err != nil {
		t.Fatal(err)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/waveform?from=100000&to=101000&points=1000", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Accept-Encoding", "gzip")
	res, err := http.DefaultTransport.RoundTrip(req) // 自動展開を避ける
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.Header.Get("Content-Encoding") != "gzip" {
		t.Fatalf("not gzipped: %v", res.Header)
	}
	gz, err := gzip.NewReader(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var wf store.WaveformRange
	if err := json.NewDecoder(gz).Decode(&wf); err != nil {
		t.Fatal(err)
	}
	if len(wf.Segments) != 1 {
		t.Fatalf("decompressed body is broken: %+v", wf)
	}
}

func TestStaticCacheHeaders(t *testing.T) {
	srv, _ := newTestServer(t)

	res, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	etag := res.Header.Get("ETag")
	if etag == "" || res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("index.html headers: %v", res.Header)
	}

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("If-None-Match", etag)
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusNotModified {
		t.Fatalf("returned %d even though the ETag matches", res2.StatusCode)
	}
}

func TestEventsRejectsNonNumericParams(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{"/api/events?before=notanumber", "/api/events?limit=lots"} {
		if code, body := get(t, srv, path); code != http.StatusBadRequest {
			t.Errorf("GET %s → %d %q, want 400", path, code, body)
		}
	}
	if code, _ := get(t, srv, "/api/events?limit=&before="); code != http.StatusOK {
		t.Error("an empty parameter must be treated as omitted")
	}
}

func TestEventNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	if code, _ := get(t, srv, "/api/events/999"); code != http.StatusNotFound {
		t.Error("a missing ID must be 404")
	}
	if code, _ := get(t, srv, "/api/events/1e5"); code != http.StatusBadRequest {
		t.Error("a non-numeric ID must be 400")
	}
}

func TestHealthEndpoint(t *testing.T) {
	srv, _ := newTestServer(t)

	res, err := http.Get(srv.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("must be 503 while the seismometer is disconnected: %d", res.StatusCode)
	}
	if res.Header.Get("Cache-Control") != "no-store" {
		t.Fatalf("the health result may be cached: %v", res.Header)
	}
	var h core.HealthMsg
	if err := json.NewDecoder(res.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	if h.OK {
		t.Fatalf("must not be ok while disconnected: %+v", h)
	}
}

// SPA フォールバックが API の名前空間を飲み込まないこと。
func TestUnknownAPIPathIsNotFound(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{"/api/unknown", "/api/events/"} {
		code, body := get(t, srv, path)
		if code != http.StatusNotFound {
			t.Errorf("%s: code=%d body=%q, want 404", path, code, body)
		}
	}
	res, err := http.Post(srv.URL+"/api/status", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode == http.StatusOK {
		t.Errorf("POST /api/status returned 200")
	}
}

// q=0 は明示的な拒否なので gzip しないこと。
func TestGzipRejectedByZeroQuality(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, ae := range []string{"gzip;q=0", "br, notgzipatall", "identity"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept-Encoding", ae)
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if enc := res.Header.Get("Content-Encoding"); enc != "" {
			t.Errorf("Accept-Encoding=%q but Content-Encoding=%q", ae, enc)
		}
	}
}

// JSON にできない値が DB に残っていても、200 で空ボディを返さないこと。
func TestUnencodableValueIsAnError(t *testing.T) {
	srv, st := newTestServer(t)
	id := newEvent(t, st, 1000, 1000)
	if err := st.CloseEvent(id, 2000, math.Inf(1), 5); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/events", "/api/events/" + strconv.FormatInt(id, 10)} {
		code, body := get(t, srv, path)
		if code == http.StatusOK {
			t.Errorf("%s: returned 200 with body=%q", path, body)
		}
	}
}

// Accept-Encoding のトークンと q パラメータは大文字小文字を区別しない。
func TestGzipHeaderIsCaseInsensitive(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, ae := range []string{"GZIP", "GZip;Q=0.5", "br, Gzip"} {
		req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/status", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Accept-Encoding", ae)
		res, err := http.DefaultTransport.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if enc := res.Header.Get("Content-Encoding"); enc != "gzip" {
			t.Errorf("Accept-Encoding=%q but Content-Encoding=%q", ae, enc)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv, _ := newTestServer(t)
	for _, path := range []string{"/", "/api/status"} {
		res, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		for h, want := range map[string]string{
			"X-Content-Type-Options": "nosniff",
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
		} {
			if got := res.Header.Get(h); got != want {
				t.Errorf("%s: %s = %q, want %q", path, h, got, want)
			}
		}
		if csp := res.Header.Get("Content-Security-Policy"); csp == "" {
			t.Errorf("%s: Content-Security-Policy missing", path)
		}
	}
}

// 上限までは接続でき、超えた1本だけが 503 になること。
func TestStreamConnectionLimit(t *testing.T) {
	srv, _ := newTestServer(t)

	var conns []*http.Response
	defer func() {
		for _, res := range conns {
			res.Body.Close()
		}
	}()
	for range testMaxStreams {
		res, err := http.Get(srv.URL + "/api/stream")
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, res)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("stream %d: code = %d", len(conns), res.StatusCode)
		}
	}

	code, _ := get(t, srv, "/api/stream")
	if code != http.StatusServiceUnavailable {
		t.Errorf("over limit: code = %d, want 503", code)
	}

	// 1本切れば次が繋がること
	conns[0].Body.Close()
	conns = conns[1:]
	waitStreamSlot(t, srv)
}

func waitStreamSlot(t *testing.T, srv *httptest.Server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(srv.URL + "/api/stream")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == http.StatusOK {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stream slot was not released")
}
