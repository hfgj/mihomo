package dialer

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testTFO(fn func(context.Context, []byte) (net.Conn, error)) *tfoConn {
	ctx, cancel := context.WithCancel(context.Background())
	return &tfoConn{ctx: ctx, cancel: cancel, dialed: make(chan struct{}), dialFn: fn}
}
func awaitTFO(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("operation did not unblock")
	}
}

// A nonblocking connection isolates the state race from socket timing.
type tfoStateConn struct{ net.Conn }

func (tfoStateConn) Write(b []byte) (int, error) { return len(b), nil }
func (tfoStateConn) Close() error                { return nil }

func TestHFGJTFOConcurrentWriteAndClose(t *testing.T) {
	for i := 0; i < 30; i++ {
		c := testTFO(func(context.Context, []byte) (net.Conn, error) { return tfoStateConn{}, nil })
		if err := c.Dial(nil); err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_, _ = c.Write([]byte("x"))
			}
		}()
		go func() { defer wg.Done(); <-start; _ = c.Close() }()
		close(start)
		wg.Wait()
	}
}

func TestHFGJTFOCloseInterruptsLazyDialAndClosesLateConnection(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	a, b := net.Pipe()
	defer b.Close()
	c := testTFO(func(context.Context, []byte) (net.Conn, error) { close(started); <-release; return a, nil })
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := c.Write([]byte("early"))
		if !errors.Is(err, io.ErrClosedPipe) {
			t.Errorf("write error = %v", err)
		}
	}()
	<-started
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	close(release)
	awaitTFO(t, done)
	if _, err := a.Write([]byte("late")); err == nil {
		t.Fatal("late connection was leaked")
	}
}

func TestHFGJTFOCloseUnblocksWaitingReaders(t *testing.T) {
	c := testTFO(func(context.Context, []byte) (net.Conn, error) {
		t.Error("Read must not initiate dialing")
		return nil, nil
	})
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Read(make([]byte, 1))
			if !errors.Is(err, io.ErrClosedPipe) {
				t.Errorf("read error = %v", err)
			}
		}()
	}
	go func() { wg.Wait(); close(done) }()
	_ = c.Close()
	awaitTFO(t, done)
}

func TestHFGJTFOCloseUnblocksConnectedReadAndWrite(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := testTFO(func(context.Context, []byte) (net.Conn, error) { return a, nil })
	if err := c.Dial(nil); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = c.Read(make([]byte, 1)) }()
	go func() { defer wg.Done(); _, _ = c.Write([]byte("blocked")) }()
	go func() { wg.Wait(); close(done) }()
	_ = c.Close()
	awaitTFO(t, done)
}

func TestHFGJTFOLazyDialOnlyOncePreservesAllWrites(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	var calls atomic.Int32
	c := testTFO(func(_ context.Context, data []byte) (net.Conn, error) {
		calls.Add(1)
		_, err := a.Write(data)
		return a, err
	})
	defer c.Close()
	received := make(chan []byte, 1)
	go func() {
		data := make([]byte, 16)
		_, err := io.ReadFull(b, data)
		if err != nil {
			t.Error(err)
		}
		received <- data
	}()
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			n, err := c.Write([]byte("x"))
			if err != nil || n != 1 {
				t.Errorf("write = %d, %v", n, err)
			}
		}()
	}
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	awaitTFO(t, done)
	if data := <-received; string(data) != "xxxxxxxxxxxxxxxx" {
		t.Fatalf("lost or repeated early data: %q", data)
	}
	if calls.Load() != 1 {
		t.Fatalf("dial count = %d", calls.Load())
	}
}

func TestHFGJTFODialFailureWakesAllReaders(t *testing.T) {
	sentinel := errors.New("test dial failure")
	c := testTFO(func(context.Context, []byte) (net.Conn, error) { return nil, sentinel })
	defer c.Close()
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := c.Read(make([]byte, 1))
			if !errors.Is(err, sentinel) {
				t.Errorf("read error = %v", err)
			}
		}()
	}
	if _, err := c.Write([]byte("early")); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
	go func() { wg.Wait(); close(done) }()
	awaitTFO(t, done)
}

func TestHFGJTFOStateMethodsDuringDialAndClose(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	c := testTFO(func(context.Context, []byte) (net.Conn, error) { return a, nil })
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = c.LocalAddr()
				_ = c.RemoteAddr()
				_ = c.Upstream()
				_ = c.NeedHandshake()
				_ = c.NeedAdditionalReadDeadline()
				_ = c.ReaderReplaceable()
				_ = c.WriterReplaceable()
				_ = c.SetDeadline(time.Now())
			}
		}()
	}
	_ = c.Dial(nil)
	_ = c.Close()
	wg.Wait()
}
