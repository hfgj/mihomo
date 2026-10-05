package session

import (
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestHFGJStreamReadWriteWhileClosed(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	sess := NewServerSession(a, nil, nil)
	s := newStream(1, sess)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 100; j++ {
				_, _ = s.Write(nil)
			}
		}()
	}
	wg.Add(1)
	readDone := make(chan struct{})
	go func() {
		defer wg.Done()
		defer close(readDone)
		_, err := s.Read(make([]byte, 1))
		if !errors.Is(err, net.ErrClosed) {
			t.Errorf("read error = %v", err)
		}
	}()
	close(start)
	s.closeLocally()
	wg.Wait()
	if _, err := s.Write([]byte("after close")); !errors.Is(err, net.ErrClosed) {
		t.Fatal(err)
	}
}

func TestHFGJStreamLateCleanupHookRunsExactlyOnce(t *testing.T) {
	for i := 0; i < 100; i++ {
		s := newStream(1, nil)
		var hooks atomic.Int32
		var wg sync.WaitGroup
		start := make(chan struct{})
		wg.Add(2)
		go func() { defer wg.Done(); <-start; s.closeLocally() }()
		go func() { defer wg.Done(); <-start; s.setDieHook(func() { hooks.Add(1); s.closeLocally() }) }()
		close(start)
		wg.Wait()
		s.closeLocally()
		if hooks.Load() != 1 {
			t.Fatalf("cleanup hook count = %d", hooks.Load())
		}
	}
}

func TestHFGJStreamSessionCloseUnblocksBlockedWrite(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	sess := NewServerSession(a, nil, nil)
	s := newStream(1, sess)
	sess.streams[1] = s
	done := make(chan struct{})
	go func() { defer close(done); _, _ = s.Write([]byte("blocked")) }()
	_ = sess.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("session close did not unblock stream write")
	}
}
