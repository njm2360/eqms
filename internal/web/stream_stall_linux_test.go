package web

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/njm2360/eqms/internal/core"
	"github.com/njm2360/eqms/internal/nmea"
	"github.com/njm2360/eqms/internal/source"
	"github.com/njm2360/eqms/internal/store"
)

// 読まなくなった購読者が枠を握り続けないこと。接続は閉じないので、書き込み期限がなければ
// TCP が諦めるまで解放されない。
func TestStalledStreamIsDropped(t *testing.T) {
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
	events := make(chan source.Event, 8192)
	done := make(chan struct{})
	go func() {
		engine.Run(ctx, events)
		close(done)
	}()

	// init フレームが送信バッファに収まらないよう、リングを 30 秒ぶん埋める
	base := time.Now().Add(-30 * time.Second)
	for i := range 3000 {
		events <- source.Line{
			Text: nmea.Format("XSACC,-123.45,123.45,-123.45"),
			Recv: base.Add(time.Duration(i) * 10 * time.Millisecond),
		}
	}
	for len(events) > 0 {
		time.Sleep(time.Millisecond)
	}

	srv := httptest.NewUnstartedServer(NewServer(engine, st, Config{
		MaxStreams:  1,
		StreamWrite: 300 * time.Millisecond,
	}).Handler())
	srv.Listener = smallBufListener{srv.Listener}
	srv.Start()
	t.Cleanup(func() {
		srv.Close()
		cancel()
		<-done
		w.Close()
		st.Close()
	})

	conn := dialSmallWindow(t, srv.URL)
	defer conn.Close()
	if _, err := fmt.Fprint(conn, "GET /api/stream HTTP/1.1\r\nHost: eqms\r\n\r\n"); err != nil {
		t.Fatal(err)
	}

	waitStreamCode(t, srv, http.StatusServiceUnavailable, "the stalled client never took the slot")
	waitStreamCode(t, srv, http.StatusOK, "the stalled client kept the slot")
}

// smallBufListener は受け付けた接続の送信バッファを絞る。読まない相手で確実に詰まらせる。
type smallBufListener struct{ net.Listener }

func (l smallBufListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tc, ok := c.(*net.TCPConn); ok {
		if raw, err := tc.SyscallConn(); err == nil {
			raw.Control(func(fd uintptr) {
				syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_SNDBUF, 2048)
			})
		}
	}
	return c, nil
}

func dialSmallWindow(t *testing.T, url string) net.Conn {
	t.Helper()
	d := net.Dialer{Control: func(_, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, 512)
		}); err != nil {
			return err
		}
		return serr
	}}
	conn, err := d.Dial("tcp", strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func waitStreamCode(t *testing.T, srv *httptest.Server, want int, msg string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		res, err := http.Get(srv.URL + "/api/stream")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal(msg)
}
