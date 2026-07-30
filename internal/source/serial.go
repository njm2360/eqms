package source

import (
	"bufio"
	"context"
	"io"
	"log"
	"time"

	"go.bug.st/serial"
)

const (
	DefaultSilence = 15 * time.Second

	reconnectDelayMin = 2 * time.Second
	reconnectDelayMax = 5 * time.Minute

	resetAfterSilentTries = 3
	resetPulse            = 100 * time.Millisecond

	scanBufSize  = 4096
	lineQueueLen = 64
)

var (
	errClosed = errString("port closed (EOF)")
	errSilent = errString("no data from the device")
)

func RunSerial(ctx context.Context, portName string, baudRate int, silence time.Duration, ch chan<- Event) {
	if silence <= 0 {
		silence = DefaultSilence
	}
	delay := reconnectDelayMin
	silentTries := 0
	for ctx.Err() == nil {
		opened, gotData, err := readPort(ctx, portName, baudRate, silence, ch)
		switch {
		case gotData:
			delay = reconnectDelayMin
			silentTries = 0
		case opened:
			silentTries++
		}
		if err != nil {
			send(ctx, ch, Disconnected{Err: err.Error()})
			log.Printf("serial: %s: %v (reconnecting in %s)", portName, err, delay)
		}
		if silentTries >= resetAfterSilentTries {
			silentTries = 0
			if err := resetDevice(ctx, portName, baudRate); err != nil {
				log.Printf("serial: %s: reset failed: %v", portName, err)
			} else {
				log.Printf("serial: %s: reset the device (silent %d times)", portName, resetAfterSilentTries)
			}
		}
		sleep(ctx, delay)
		delay = nextDelay(delay)
	}
}

func resetDevice(ctx context.Context, name string, baudRate int) error {
	port, err := serial.Open(name, &serial.Mode{
		BaudRate:          baudRate,
		InitialStatusBits: &serial.ModemOutputBits{DTR: false, RTS: false},
	})
	if err != nil {
		return err
	}
	defer port.Close()

	if err := port.SetDTR(false); err != nil {
		return err
	}
	if err := port.SetRTS(false); err != nil {
		return err
	}
	if err := port.SetRTS(true); err != nil {
		return err
	}
	sleep(ctx, resetPulse)
	return port.SetRTS(false)
}

func nextDelay(d time.Duration) time.Duration {
	if d *= 2; d > reconnectDelayMax {
		return reconnectDelayMax
	}
	return d
}

func readPort(ctx context.Context, name string, baudRate int, silence time.Duration, ch chan<- Event) (bool, bool, error) {
	port, err := serial.Open(name, &serial.Mode{
		BaudRate:          baudRate,
		InitialStatusBits: &serial.ModemOutputBits{DTR: false, RTS: false},
	})
	if err != nil {
		return false, false, err
	}
	if err := port.SetReadTimeout(serial.NoTimeout); err != nil {
		port.Close()
		return true, false, err
	}
	if _, err := port.Write([]byte("HWINFO\r\n")); err != nil {
		port.Close()
		return true, false, err
	}
	gotData, err := supervise(ctx, port, name, silence, ch)
	return true, gotData, err
}

func supervise(ctx context.Context, rc io.ReadCloser, name string, silence time.Duration, ch chan<- Event) (bool, error) {
	lines := make(chan Line, lineQueueLen)
	stop := make(chan struct{})
	readDone := make(chan struct{})
	var readErr error
	go func() {
		defer close(readDone)
		readErr = scanLines(rc, lines, stop)
	}()
	defer func() {
		close(stop)
		rc.Close()
		<-readDone // ポートを掴んだゴルーチンを残さない
	}()

	if !send(ctx, ch, Connected{Port: name}) {
		return false, nil
	}

	gotData := false
	watchdog := time.NewTimer(silence)
	defer watchdog.Stop()
	for {
		select {
		case <-ctx.Done():
			return gotData, nil
		case <-readDone:
			return gotData, readErr
		case <-watchdog.C:
			return gotData, errSilent
		case ln := <-lines:
			gotData = true
			watchdog.Reset(silence)
			if !send(ctx, ch, ln) {
				return gotData, nil
			}
		}
	}
}

func scanLines(r io.Reader, out chan<- Line, stop <-chan struct{}) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, scanBufSize), scanBufSize)
	for sc.Scan() {
		text := sc.Text()
		if text == "" {
			continue
		}
		// 受信時刻はここで打つ。SampleClock がジッタを吸収する基準になる
		select {
		case out <- Line{Text: text, Recv: time.Now()}:
		case <-stop:
			return nil
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	return errClosed
}

// send は ctx が切れたら諦める。エンジンが止まった後に送信で固まらないようにする。
func send(ctx context.Context, ch chan<- Event, ev Event) bool {
	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
