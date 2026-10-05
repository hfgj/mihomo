package dialer

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/metacubex/tfo-go"
)

var DisableTFO = false

// State locks never cover blocking network I/O: Close must unblock readers,
// writers and a lazy dial even while those operations are in progress.
type tfoConn struct {
	mu       sync.Mutex
	dialMu   sync.Mutex
	writeMu  sync.Mutex
	conn     net.Conn
	closed   bool
	dialErr  error
	dialed   chan struct{}
	dialDone sync.Once
	cancel   context.CancelFunc
	ctx      context.Context
	dialFn   func(ctx context.Context, earlyData []byte) (net.Conn, error)
}

func (c *tfoConn) snapshot() (net.Conn, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn, c.closed, c.dialErr
}

// dial reports whether this call's payload was consumed as TCP early data.
func (c *tfoConn) dial(earlyData []byte) (bool, error) {
	c.dialMu.Lock()
	defer c.dialMu.Unlock()
	conn, closed, err := c.snapshot()
	if closed {
		return false, io.ErrClosedPipe
	}
	if err != nil {
		return false, err
	}
	if conn != nil {
		return false, nil
	}
	conn, err = c.dialFn(c.ctx, earlyData)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		if conn != nil {
			_ = conn.Close()
		}
		c.dialDone.Do(func() { close(c.dialed) })
		return false, io.ErrClosedPipe
	}
	if err == nil && conn == nil {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		c.dialErr = err
	} else {
		c.conn = conn
	}
	c.mu.Unlock()
	if err != nil && conn != nil {
		_ = conn.Close()
	}
	c.dialDone.Do(func() { close(c.dialed) })
	return err == nil, err
}

func (c *tfoConn) Dial(earlyData []byte) error {
	_, err := c.dial(earlyData)
	return err
}

func (c *tfoConn) Read(b []byte) (int, error) {
	conn, closed, err := c.snapshot()
	if closed {
		return 0, io.ErrClosedPipe
	}
	if conn == nil && err == nil {
		select {
		case <-c.ctx.Done():
			_, closed, _ = c.snapshot()
			if closed {
				return 0, io.ErrClosedPipe
			}
			return 0, io.ErrUnexpectedEOF
		case <-c.dialed:
		}
		conn, closed, err = c.snapshot()
	}
	if closed {
		return 0, io.ErrClosedPipe
	}
	if err != nil {
		return 0, err
	}
	return conn.Read(b)
}

func (c *tfoConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	early, err := c.dial(b)
	if err != nil {
		return 0, err
	}
	if early {
		return len(b), nil
	}
	conn, closed, _ := c.snapshot()
	if closed {
		return 0, io.ErrClosedPipe
	}
	return conn.Write(b)
}

func (c *tfoConn) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	conn := c.conn
	c.mu.Unlock()
	c.cancel()
	if conn != nil {
		return conn.Close()
	}
	return nil
}

func (c *tfoConn) LocalAddr() net.Addr {
	conn, _, _ := c.snapshot()
	if conn == nil {
		return &net.TCPAddr{}
	}
	return conn.LocalAddr()
}

func (c *tfoConn) RemoteAddr() net.Addr {
	conn, _, _ := c.snapshot()
	if conn == nil {
		return &net.TCPAddr{}
	}
	return conn.RemoteAddr()
}

func (c *tfoConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}

func (c *tfoConn) SetReadDeadline(t time.Time) error {
	conn, _, _ := c.snapshot()
	if conn == nil {
		return nil
	}
	return conn.SetReadDeadline(t)
}

func (c *tfoConn) SetWriteDeadline(t time.Time) error {
	conn, _, _ := c.snapshot()
	if conn == nil {
		return nil
	}
	return conn.SetWriteDeadline(t)
}

func (c *tfoConn) Upstream() any {
	conn, _, _ := c.snapshot()
	if conn == nil {
		return nil
	}
	return conn
}

func (c *tfoConn) NeedAdditionalReadDeadline() bool { conn, _, _ := c.snapshot(); return conn == nil }
func (c *tfoConn) NeedHandshake() bool              { conn, _, _ := c.snapshot(); return conn == nil }
func (c *tfoConn) ReaderReplaceable() bool          { conn, _, _ := c.snapshot(); return conn != nil }
func (c *tfoConn) WriterReplaceable() bool          { conn, _, _ := c.snapshot(); return conn != nil }

func dialTFO(ctx context.Context, netDialer net.Dialer, network, address string) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), DefaultTCPTimeout)
	dialer := tfo.Dialer{Dialer: netDialer, DisableTFO: false}
	return &tfoConn{
		dialed: make(chan struct{}),
		cancel: cancel,
		ctx:    ctx,
		dialFn: func(ctx context.Context, earlyData []byte) (net.Conn, error) {
			return dialer.DialContext(ctx, network, address, earlyData)
		},
	}, nil
}
