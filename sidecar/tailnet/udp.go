package tailnet

import (
	"errors"
	"net"
	"sync"
	"time"
)

const maximumUDPClients = 64
const udpClientIdleTimeout = 2 * time.Minute

// UDPForwarder preserves each peer's local endpoint so mosh-server, rather than
// an unauthenticated packet, decides which client endpoint owns the session.
// One forwarder serves one configured Mosh port, never an arbitrary destination.
type UDPForwarder struct {
	frontend net.PacketConn
	backend  *net.UDPAddr
	mutex    sync.Mutex
	clients  map[string]*net.UDPConn
	closed   bool
	workers  sync.WaitGroup
}

func NewUDPForwarder(frontend net.PacketConn, backend *net.UDPAddr) (*UDPForwarder, error) {
	if frontend == nil || backend == nil || !backend.IP.IsLoopback() || backend.Port < 1 || backend.Port > 65535 {
		return nil, errors.New("UDP forwarding requires a concrete loopback destination")
	}
	return &UDPForwarder{frontend: frontend, backend: backend, clients: make(map[string]*net.UDPConn)}, nil
}

func (forwarder *UDPForwarder) Serve() error {
	packet := make([]byte, 65535)
	for {
		count, remote, err := forwarder.frontend.ReadFrom(packet)
		if err != nil {
			return err
		}
		forwarder.mutex.Lock()
		if forwarder.closed {
			forwarder.mutex.Unlock()
			return net.ErrClosed
		}
		backend := forwarder.clients[remote.String()]
		if backend == nil && len(forwarder.clients) < maximumUDPClients {
			backend, err = net.DialUDP("udp", nil, forwarder.backend)
			if err != nil {
				forwarder.mutex.Unlock()
				return err
			}
			forwarder.clients[remote.String()] = backend
			forwarder.workers.Add(1)
			go forwarder.reply(backend, remote)
		}
		if backend != nil {
			_ = backend.SetReadDeadline(time.Now().Add(udpClientIdleTimeout))
			_, err = backend.Write(packet[:count])
		}
		forwarder.mutex.Unlock()
		// An expired/deleted local Mosh lease may reject a datagram. The other
		// clients and future leases on this port must continue to work.
		if err != nil && backend != nil {
			_ = backend.Close()
		}
	}
}

func (forwarder *UDPForwarder) reply(backend *net.UDPConn, remote net.Addr) {
	defer forwarder.workers.Done()
	defer backend.Close()
	defer func() {
		forwarder.mutex.Lock()
		if forwarder.clients[remote.String()] == backend {
			delete(forwarder.clients, remote.String())
		}
		forwarder.mutex.Unlock()
	}()
	packet := make([]byte, 65535)
	for {
		count, err := backend.Read(packet)
		if err != nil {
			return
		}
		_ = backend.SetReadDeadline(time.Now().Add(udpClientIdleTimeout))
		if _, err := forwarder.frontend.WriteTo(packet[:count], remote); err != nil {
			return
		}
	}
}

func (forwarder *UDPForwarder) Close() {
	forwarder.mutex.Lock()
	forwarder.closed = true
	_ = forwarder.frontend.Close()
	for _, backend := range forwarder.clients {
		_ = backend.Close()
	}
	forwarder.mutex.Unlock()
	forwarder.workers.Wait()
}
