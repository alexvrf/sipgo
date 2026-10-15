package sip

import (
	"context"
	"net"
	"sync/atomic"
	"testing"

	"github.com/emiago/sipgo/fakes"
)

func TestConnectionPool(t *testing.T) {
	pool := newConnectionPool()

	fakeConn := &fakes.TCPConn{
		LAddr:  net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5060},
		RAddr:  net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 5060},
		Reader: nil,
		Writer: nil,
	}
	conn := &TCPConnection{Conn: fakeConn}

	pool.Add(fakeConn.RAddr.String(), conn)

	c := pool.Get(fakeConn.RAddr.String())
	if c != conn {
		t.Fatal("Not found connection")
	}
}

func BenchmarkConnectionPool(b *testing.B) {
	pool := newConnectionPool()

	for i := 0; i < b.N; i++ {
		conn := &TCPConnection{Conn: &fakes.TCPConn{
			LAddr:  net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5060},
			RAddr:  net.TCPAddr{IP: net.ParseIP("127.0.0.2"), Port: 5060},
			Reader: nil,
			Writer: nil,
		}}
		a := &net.TCPAddr{
			IP:   net.IPv4('1', '2', '3', byte(i)),
			Port: 1000,
		}
		pool.Add(a.String(), conn)
		c := pool.Get(a.String())
		if c != conn {
			b.Fatal("mismatched function")
		}
	}
}

// stubConn counts Close calls; nothing else of a connection is needed here.
type stubConn struct {
	laddr  net.UDPAddr
	ref    atomic.Int32
	closes atomic.Int32
}

func (c *stubConn) LocalAddr() net.Addr    { return &c.laddr }
func (c *stubConn) WriteMsg(Message) error { return nil }
func (c *stubConn) Ref(i int) int          { return int(c.ref.Add(int32(i))) }
func (c *stubConn) TryClose() (int, error) { return int(c.ref.Add(-1)), nil }
func (c *stubConn) Close() error           { c.closes.Add(1); return nil }

// A closed transport opens no new connections. A request read just before
// the transport closed is handled in its own goroutine, and the response
// connection it asks for (serverRequestConnection) is no longer in the
// cleared pool; a client transaction retransmitting or a CANCEL on a closed
// client asks the same. Before, the pool opened a fresh socket for it,
// nobody ever closed it, and its reader lived for the life of the process,
// sending whatever the transaction retransmitted out of a socket the
// application had closed.
func TestClosedTransportOpensNoConnection(t *testing.T) {
	raddr := Addr{IP: net.ParseIP("127.0.0.1"), Port: 5060}
	var port atomic.Int32
	newConn := func() *stubConn {
		c := &stubConn{laddr: net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 40000 + int(port.Add(1))}}
		c.ref.Store(2)
		return c
	}

	for _, reuse := range []bool{true, false} {
		pool := newConnectionPool()
		_ = pool.Clear()
		opened := 0
		c, err := pool.addSingleflight(raddr, Addr{}, reuse, func() (Connection, error) {
			opened++
			return newConn(), nil
		})
		if err != ErrTransportClosed || c != nil {
			t.Fatalf("reuse=%v: after Clear got conn=%v err=%v, want ErrTransportClosed", reuse, c, err)
		}
		if opened != 0 {
			t.Errorf("reuse=%v: a socket was opened on a closed transport", reuse)
		}
		if pool.Size() != 0 {
			t.Errorf("reuse=%v: pool holds %d entries after Clear", reuse, pool.Size())
		}
	}

	// Clear lands while the socket is being opened: the new connection
	// must not enter the cleared pool, and it is closed right away
	for _, reuse := range []bool{true, false} {
		pool := newConnectionPool()
		made := newConn()
		c, err := pool.addSingleflight(raddr, Addr{}, reuse, func() (Connection, error) {
			_ = pool.Clear()
			return made, nil
		})
		if err != ErrTransportClosed || c != nil {
			t.Fatalf("reuse=%v: Clear during open: conn=%v err=%v, want ErrTransportClosed", reuse, c, err)
		}
		if made.closes.Load() != 1 {
			t.Errorf("reuse=%v: connection opened during Clear closed %d times, want 1", reuse, made.closes.Load())
		}
		if pool.Size() != 0 {
			t.Errorf("reuse=%v: pool holds %d entries after Clear", reuse, pool.Size())
		}
	}

	// a connection accepted after Clear (TCP, WS: initConnection → Add)
	// is closed and stays out of the pool
	pool := newConnectionPool()
	_ = pool.Clear()
	accepted := newConn()
	pool.Add("127.0.0.1:5060", accepted)
	if accepted.closes.Load() != 1 || pool.Size() != 0 {
		t.Errorf("accepted after Clear: closed %d times, pool size %d; want 1 and 0", accepted.closes.Load(), pool.Size())
	}
	// a UDP listener is re-added by its reader under each new sender; it
	// stays out of the closed pool, but closing it is the caller's business
	ln, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	pool.Add("127.0.0.1:5061", &UDPConnection{PacketConn: ln, PacketAddr: ln.LocalAddr().String(), Listener: true})
	if pool.Size() != 0 {
		t.Errorf("listener entered the closed pool")
	}
	if _, err := ln.WriteTo([]byte("x"), ln.LocalAddr()); err != nil {
		t.Errorf("closed pool closed the caller's listener: %v", err)
	}

	// the same through the transport, with a real socket
	tp := &TransportUDP{}
	tp.init(NewParser())
	_ = tp.Close()
	c, err := tp.CreateConnection(context.Background(), Addr{IP: net.ParseIP("127.0.0.1")}, raddr, nil)
	if err != ErrTransportClosed || c != nil {
		t.Fatalf("CreateConnection on a closed transport: conn=%v err=%v", c, err)
	}
}
