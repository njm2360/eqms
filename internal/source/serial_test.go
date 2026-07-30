package source

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

const testSilence = 100 * time.Millisecond

func superviseAsync(t *testing.T, ctx context.Context, r io.ReadCloser, ch chan Event) chan error {
	t.Helper()
	out := make(chan error, 1)
	go func() {
		_, err := supervise(ctx, r, "test", testSilence, ch)
		out <- err
	}()
	return out
}

func waitErr(t *testing.T, out chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-out:
		return err
	case <-time.After(within):
		t.Fatal("supervise did not return")
		return nil
	}
}

// ポートが開いたままデータが止まったら打ち切ること。
func TestSuperviseFiresOnSilence(t *testing.T) {
	pr, pw := io.Pipe()
	ch := make(chan Event, 16)
	out := superviseAsync(t, context.Background(), pr, ch)

	if _, err := pw.Write([]byte("$XSACC,1,2,3*00\n")); err != nil {
		t.Fatal(err)
	}
	if err := waitErr(t, out, 5*testSilence); !errors.Is(err, errSilent) {
		t.Fatalf("err=%v, want errSilent", err)
	}

	// Connected と行が届いていること
	if len(ch) != 2 {
		t.Fatalf("events=%d, want 2", len(ch))
	}
	if _, ok := (<-ch).(Connected); !ok {
		t.Fatal("Connected was not sent first")
	}
	if _, ok := (<-ch).(Line); !ok {
		t.Fatal("the line was not forwarded")
	}

	// 読み取りゴルーチンが解除されて Close が済んでいること
	if _, err := pw.Write([]byte("x")); err == nil {
		t.Fatal("the reader was left holding the port")
	}
}

// データが来ている間は打ち切らないこと。
func TestSuperviseResetsWhileFlowing(t *testing.T) {
	pr, pw := io.Pipe()
	ch := make(chan Event, 256)
	out := superviseAsync(t, context.Background(), pr, ch)

	deadline := time.Now().Add(3 * testSilence)
	for time.Now().Before(deadline) {
		if _, err := pw.Write([]byte("$XSACC,1,2,3*00\n")); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-out:
			t.Fatalf("gave up while data was still arriving: %v", err)
		case <-time.After(testSilence / 3):
		}
	}
	if err := waitErr(t, out, 5*testSilence); !errors.Is(err, errSilent) {
		t.Fatalf("err=%v, want errSilent", err)
	}
}

// 相手が閉じたら無音判定を待たずに戻ること。
func TestSuperviseReturnsOnEOF(t *testing.T) {
	pr, pw := io.Pipe()
	ch := make(chan Event, 16)
	out := superviseAsync(t, context.Background(), pr, ch)

	pw.Close()
	start := time.Now()
	if err := waitErr(t, out, 5*testSilence); !errors.Is(err, errClosed) {
		t.Fatalf("err=%v, want errClosed", err)
	}
	if time.Since(start) >= testSilence {
		t.Fatal("waited for the silence timeout instead of noticing EOF")
	}
}

// 1行も来なかった試行を見分けられること。リセットするかの判断に使う。
func TestSuperviseReportsWhetherDataArrived(t *testing.T) {
	for _, tc := range []struct {
		name    string
		write   bool
		gotData bool
	}{
		{"行が届いた", true, true},
		{"最初から無音", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr, pw := io.Pipe()
			defer pw.Close()
			ch := make(chan Event, 16)
			type result struct {
				gotData bool
				err     error
			}
			out := make(chan result, 1)
			go func() {
				gotData, err := supervise(context.Background(), pr, "test", testSilence, ch)
				out <- result{gotData, err}
			}()
			if tc.write {
				if _, err := pw.Write([]byte("$XSACC,1,2,3*00\n")); err != nil {
					t.Fatal(err)
				}
			}
			select {
			case got := <-out:
				if !errors.Is(got.err, errSilent) {
					t.Fatalf("err=%v, want errSilent", got.err)
				}
				if got.gotData != tc.gotData {
					t.Fatalf("gotData=%v, want %v", got.gotData, tc.gotData)
				}
			case <-time.After(5 * testSilence):
				t.Fatal("supervise did not return")
			}
		})
	}
}

// エンジンが読まなくなっても ctx キャンセルで抜けること。
func TestSuperviseUnblocksOnCancel(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	ch := make(chan Event) // 誰も読まない
	ctx, cancel := context.WithCancel(context.Background())
	out := superviseAsync(t, ctx, pr, ch)

	time.Sleep(10 * time.Millisecond) // Connected の送信で待たせる
	cancel()
	if err := waitErr(t, out, 5*testSilence); err != nil {
		t.Fatalf("err=%v, want nil", err)
	}
}
