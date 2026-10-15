package sip

import (
	"bytes"
	"errors"
	"net"
	"sync"

	"golang.org/x/sync/singleflight"
)

type Connection interface {
	// LocalAddr used for connection
	LocalAddr() net.Addr
	// WriteMsg marshals message and sends to socket
	WriteMsg(msg Message) error
	// Reference of connection can be increased/decreased to prevent closing to earlyss
	Ref(i int) int
	// Close decreases reference and if ref = 0 closes connection. Returns last ref. If 0 then it is closed
	TryClose() (int, error)

	Close() error
}

var bufPool = sync.Pool{
	New: func() interface{} {
		// The Pool's New function should generally only return pointer
		// types, since a pointer can be put into the return interface
		// value without an allocation:
		b := new(bytes.Buffer)
		// b.Grow(2048)
		return b
	},
}

type connectionPool struct {
	// TODO consider sync.Map way with atomic checks to reduce mutex contention
	sync.RWMutex
	m  map[string]Connection
	sf singleflight.Group
	// closed is set by Clear: the transport is closing, and a connection
	// created after that would belong to nobody — no Clear is coming for
	// it, its reader lives for the life of the process, and retransmits
	// of the transaction that asked for it go out of a socket the
	// application believes closed.
	closed bool
}

// ErrTransportClosed is returned when a connection is requested from a
// transport that has been closed.
var ErrTransportClosed = errors.New("transport closed")

func newConnectionPool() *connectionPool {
	p := &connectionPool{}
	p.init()
	return p
}

func (p *connectionPool) init() {
	p.m = make(map[string]Connection)
}

func (p *connectionPool) addSingleflight(raddr Addr, laddr Addr, reuse bool, do func() (Connection, error)) (Connection, error) {
	a := raddr.String()

	p.RLock()
	closed := p.closed
	p.RUnlock()
	if closed {
		return nil, ErrTransportClosed
	}

	if laddr.Port > 0 || reuse {
		// TODO: implement singleflight without  type conversion
		laddrStr := laddr.String()
		// We create or return existing
		conn, err, _ := p.sf.Do(laddrStr+a, func() (any, error) {
			if laddr.Port > 0 {
				if c := p.getUnref(laddrStr); c != nil {
					return c, nil
				}
			} else {
				if c := p.getUnref(a); c != nil {
					return c, nil
				}
			}

			c, err := do()
			if err != nil {
				return nil, err
			}
			// Decrease reference as it will be increased after
			// Singleflight will return cached so we need todo this
			c.Ref(-1)

			p.Lock()
			defer p.Unlock()

			if p.closed {
				// Clear ran while the socket was being opened
				c.Close()
				return nil, ErrTransportClosed
			}
			p.m[a] = c
			p.m[c.LocalAddr().String()] = c
			return c, nil
		})
		if err != nil {
			return nil, err
		}
		c := conn.(Connection)
		c.Ref(1)
		return c, nil
	}

	// There is nothing here to block
	c, err := do()
	if err != nil {
		return nil, err
	}

	if c.Ref(0) < 1 {
		c.Ref(1) // Make 1 reference count by default
	}
	p.Lock()
	defer p.Unlock()
	if p.closed {
		c.Close()
		return nil, ErrTransportClosed
	}
	p.m[a] = c
	p.m[c.LocalAddr().String()] = c
	return c, nil
}

func (p *connectionPool) Add(a string, c Connection) {
	// TODO how about multi connection support for same remote address
	// We can then check ref count

	p.Lock()
	defer p.Unlock()
	if p.closed {
		// A connection accepted after Clear would sit in a pool nobody
		// clears again. A UDP listener is re-added under each new sender
		// by its reader; closing it stays the caller's business.
		if u, ok := c.(*UDPConnection); !ok || !u.Listener {
			c.Close()
		}
		return
	}
	if c.Ref(0) < 1 {
		c.Ref(1) // Make 1 reference count by default
	}
	p.m[a] = c
}

// Getting connection pool increases reference
// Make sure you TryClose after finish
func (p *connectionPool) Get(a string) (c Connection) {
	// p.RLock()
	// c, exists := p.m[a]
	// p.RUnlock()
	// if !exists {
	// 	return nil
	// }
	c = p.getUnref(a)
	if c == nil {
		return nil
	}
	c.Ref(1)
	return c
}

func (p *connectionPool) getUnref(a string) (c Connection) {
	p.RLock()
	c, exists := p.m[a]
	p.RUnlock()
	if !exists {
		return nil
	}
	return c
}

// CloseAndDelete closes connection and deletes from pool
func (p *connectionPool) CloseAndDelete(c Connection, addr string) error {
	p.Lock()
	defer p.Unlock()
	delete(p.m, addr)
	ref, _ := c.TryClose() // Be nice. Saves from double closing
	if ref > 0 {
		return c.Close()
	}
	return nil
}

func (p *connectionPool) Delete(addr string) {
	p.Lock()
	defer p.Unlock()
	delete(p.m, addr)
}

func (p *connectionPool) DeleteMultiple(addrs []string) {
	p.Lock()
	defer p.Unlock()
	for _, a := range addrs {
		delete(p.m, a)
	}
}

// Clear will clear all connection from pool and close them
func (p *connectionPool) Clear() error {
	p.Lock()
	defer p.Unlock()
	p.closed = true

	defer func() {
		// Remove all
		p.m = make(map[string]Connection)
	}()

	var werr error
	for _, c := range p.m {
		if c.Ref(0) <= 0 {
			continue
		}
		werr = errors.Join(werr, c.Close())
	}
	return werr
}

func (p *connectionPool) Size() int {
	p.RLock()
	l := len(p.m)
	p.RUnlock()
	return l
}
