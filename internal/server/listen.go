package server

import (
	"bufio"
	"crypto/tls"
	"errors"
	"net"
	"sync"
	"time"
)

// sniffListener accepts both HTTPS and plain HTTP on one port, telling them
// apart by the first byte (0x16 starts a TLS handshake). That lets a plain
// http:// visit be redirected to https://, and lets a port switch between
// HTTP and HTTPS while a network change waits to be confirmed.
type sniffListener struct {
	raw   net.Listener
	port  int
	tls   *tls.Config
	allow func() bool // whether TLS handshakes are accepted right now
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func newSniffListener(raw net.Listener, port int, cfg *tls.Config, allowTLS func() bool) *sniffListener {
	l := &sniffListener{raw: raw, port: port, tls: cfg, allow: allowTLS,
		conns: make(chan net.Conn), done: make(chan struct{})}
	go l.loop()
	return l
}

func (l *sniffListener) loop() {
	for {
		c, err := l.raw.Accept()
		if err != nil {
			select {
			case <-l.done:
				return
			default:
			}
			if errors.Is(err, net.ErrClosed) {
				l.Close()
				return
			}
			time.Sleep(50 * time.Millisecond)
			continue
		}
		go l.sniff(c)
	}
}

func (l *sniffListener) sniff(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(10 * time.Second))
	br := bufio.NewReader(c)
	first, err := br.Peek(1)
	_ = c.SetReadDeadline(time.Time{})
	if err != nil {
		c.Close()
		return
	}
	var out net.Conn = &peekedConn{Conn: c, r: br}
	if first[0] == 0x16 {
		if !l.allow() {
			c.Close()
			return
		}
		out = tls.Server(out, l.tls)
	}
	select {
	case l.conns <- out:
	case <-l.done:
		out.Close()
	}
}

func (l *sniffListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *sniffListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.raw.Close()
	})
	return err
}

func (l *sniffListener) Addr() net.Addr { return l.raw.Addr() }

type peekedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *peekedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
