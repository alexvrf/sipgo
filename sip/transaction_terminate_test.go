package sip

import (
	"bytes"
	"io"
	"log/slog"
	"testing"

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
