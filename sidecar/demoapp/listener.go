package demoapp

import (
	"net"
	"sync"
)

// Bound accepted sockets before TLS and headers allocate per-connection work.
// Once full, leave excess clients in the kernel backlog instead of accepting
// and spawning unbounded handlers. Closing interrupts a blocked accept.
type boundedListener struct {
	net.Listener
	slots chan struct{}
	done  chan struct{}
	once  sync.Once
}
type boundedConn struct {
	net.Conn
	release func()
	once    sync.Once
}

func (c *boundedConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
func limitListener(l net.Listener, n int) net.Listener {
	return &boundedListener{Listener: l, slots: make(chan struct{}, n), done: make(chan struct{})}
}
func (l *boundedListener) Accept() (net.Conn, error) {
	select {
	case l.slots <- struct{}{}:
	case <-l.done:
		return nil, net.ErrClosed
	}
	c, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &boundedConn{Conn: c, release: func() { <-l.slots }}, nil
}
func (l *boundedListener) Close() error {
	l.once.Do(func() { close(l.done) })
	return l.Listener.Close()
}
