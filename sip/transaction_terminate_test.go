package sip

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo/fakes"
	"github.com/stretchr/testify/require"
)

// Terminate sets the transaction error before it closes Done. Whoever waits
// on Done() reads Err() right away: Client.Do and DialogClientSession.Do
// return (nil, tx.Err()), and with a nil error a caller gets (nil, nil) and
// dereferences the response. Terminate used to close Done first and set the
// error after it; the window was hit when a transport layer closed
// (TransactionLayer.Close → terminateAll) while a CANCEL was in flight, and
// DialogClientSession.inviteCancel panicked on res.StatusCode.
//
// Termination callbacks run after Done is closed, so an error read there is
// the error any waiter can see: without the fix it is nil every time — the
// check needs no race to fail.
func TestTerminateSetsErrBeforeDone(t *testing.T) {
	conn := func() *UDPConnection {
		return &UDPConnection{PacketConn: &fakes.UDPConn{
			Reader:  bytes.NewBuffer(nil),
			Writers: map[string]io.Writer{"127.0.0.2:5060": io.Discard, "127.0.0.99:5060": io.Discard},
		}, refcount: 1}
	}
	req, _, _ := testCreateInvite(t, "sip:127.0.0.99:5060", "udp", "127.0.0.2:5060")

	t.Run("client", func(t *testing.T) {
		tx := NewClientTx("client", req, conn(), slog.Default())
		require.NoError(t, tx.Init())
		var seen error
		require.True(t, tx.OnTerminate(func(string, error) { seen = tx.Err() }))
		tx.Terminate()
		require.ErrorIs(t, seen, ErrTransactionCanceled, "Done closed while Err() was nil")
		require.ErrorIs(t, tx.Err(), ErrTransactionCanceled)
	})

	t.Run("server", func(t *testing.T) {
		tx := NewServerTx("server", req, conn(), slog.Default())
		require.NoError(t, tx.Init())
		var seen error
		require.True(t, tx.OnTerminate(func(string, error) { seen = tx.Err() }))
		tx.Terminate()
		require.ErrorIs(t, seen, ErrTransactionTerminated, "Done closed while Err() was nil")
	})

	// a second Terminate keeps the cause of the first termination
	t.Run("twice", func(t *testing.T) {
		tx := NewServerTx("twice", req, conn(), slog.Default())
		require.NoError(t, tx.Init())
		tx.Terminate()
		tx.Terminate()
		require.ErrorIs(t, tx.Err(), ErrTransactionTerminated)
	})
}

// Terminate on a closed transaction returns without taking fsmMu. Handlers
// on the FSM path run under fsmMu — delete is called from actDelete inside
// spinFsm — and one that calls Terminate on its own transaction used to get
// a no-op; taking fsmMu first would make it deadlock. Over a reliable
// transport the final response deletes the transaction without waiting for
// Timer K or J (the client inside Receive, the server on a zero timer), so
// the check needs no timer settings: before the fix the handler never
// returned.
func TestTerminateFromFSMHandlerReturns(t *testing.T) {
	tcp := func() *TCPConnection {
		return &TCPConnection{Conn: &fakes.TCPConn{
			LAddr:  net.TCPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5060},
			RAddr:  net.TCPAddr{IP: net.IPv4(127, 0, 0, 99), Port: 5060},
			Reader: bytes.NewBuffer(nil),
			Writer: io.Discard,
		}, refcount: 1}
	}
	// reenter sets a termination handler that calls Terminate on its own
	// transaction and waits for the handler to return
	reenter := func(t *testing.T, tx interface {
		OnTerminate(FnTxTerminate) bool
		Terminate()
	}, final func()) {
		t.Helper()
		back := make(chan struct{})
		require.True(t, tx.OnTerminate(func(string, error) {
			tx.Terminate()
			close(back)
		}))
		go final()
		select {
		case <-back:
		case <-time.After(3 * time.Second):
			t.Fatal("Terminate from a termination handler on the FSM path deadlocked on fsmMu")
		}
	}

	t.Run("client", func(t *testing.T) {
		req := testCreateRequest(t, "OPTIONS", "sip:127.0.0.99:5060", "tcp", "127.0.0.2:5060")
		tx := NewClientTx("client-reenter", req, tcp(), slog.Default())
		require.NoError(t, tx.Init())
		go func() {
			for range tx.Responses() {
			}
		}()
		reenter(t, tx, func() { tx.Receive(NewResponseFromRequest(req, StatusOK, "OK", nil)) })
	})

	t.Run("server", func(t *testing.T) {
		req := testCreateRequest(t, "OPTIONS", "sip:127.0.0.99:5060", "tcp", "127.0.0.2:5060")
		tx := NewServerTx("server-reenter", req, tcp(), slog.Default())
		require.NoError(t, tx.Init())
		reenter(t, tx, func() { _ = tx.Respond(NewResponseFromRequest(req, StatusOK, "OK", nil)) })
	})
}

// Terminate does not wait for fsmMu. The FSM holds fsmMu while it passes a
// response up (fsmPassUp), and it is released either by a reader of
// Responses() or by Done. A caller that stops reading and terminates — as
// DialogClientSession.inviteCancel does when its 64*T1 wait for 487 ends at
// the moment 487 arrives (RFC 3261 9.1) — used to block on fsmMu while the
// FSM blocked on it: neither ever returned, and Engine.Close hung in
// TransactionLayer.Close behind them. Seen once in the engine tests
// (TestCallerCancelStopsCallee, ten minutes to the test timeout); here the
// FSM is held in fsmPassUp deterministically by not reading Responses().
func TestTerminateWhileFSMPassesUpReturns(t *testing.T) {
	conn := &UDPConnection{PacketConn: &fakes.UDPConn{
		Reader:  bytes.NewBuffer(nil),
		Writers: map[string]io.Writer{"127.0.0.2:5060": io.Discard, "127.0.0.99:5060": io.Discard},
	}, refcount: 1}
	req, _, _ := testCreateInvite(t, "sip:127.0.0.99:5060", "udp", "127.0.0.2:5060")
	tx := NewClientTx("passup", req, conn, slog.Default())
	require.NoError(t, tx.Init())

	received := make(chan struct{})
	go func() {
		tx.Receive(NewResponseFromRequest(req, StatusRequestTerminated, "Request Terminated", nil))
		close(received)
	}()
	// the FSM is inside fsmPassUp once it holds fsmMu: nobody reads
	// Responses(), so it stays there until Done
	deadline := time.Now().Add(3 * time.Second)
	for tx.fsmMu.TryLock() {
		tx.fsmMu.Unlock()
		if time.Now().After(deadline) {
			t.Fatal("the FSM never took fsmMu")
		}
		time.Sleep(time.Millisecond)
	}

	terminated := make(chan struct{})
	go func() {
		tx.Terminate()
		close(terminated)
	}()
	select {
	case <-terminated:
	case <-time.After(3 * time.Second):
		t.Fatal("Terminate blocked on fsmMu held by the FSM passing a response up")
	}
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("the FSM was not released by Done")
	}
	require.Error(t, tx.Err())
}
