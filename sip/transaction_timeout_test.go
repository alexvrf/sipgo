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

// A non-INVITE client transaction ends by Timer F (RFC 3261 17.1.2.2), an
// INVITE one by Timer B (17.1.1.2), and the timeout error names the timer.
// Upstream armed Timer_B for both and said "Timer_B timed out" for a BYE or
// an OPTIONS — the values match by default (64*T1), so only the text gave it
// away, but a Timer_F set on its own was never read. Each case sets the
// other timer to an hour: a transaction armed with it would not end.
func TestClientTxTimeoutTimerPerMethod(t *testing.T) {
	t1, t2, t4 := T1, T2, T4
	SetTimers(5*time.Millisecond, 5*time.Millisecond, 5*time.Millisecond)
	t.Cleanup(func() { SetTimers(t1, t2, t4) })
	run := func(t *testing.T, req *Request) error {
		t.Helper()
		req.raddr = Addr{IP: net.ParseIP("127.0.0.99"), Port: 5060}
		conn := &UDPConnection{
			PacketConn: &fakes.UDPConn{
				Reader:  bytes.NewBuffer(nil),
				Writers: map[string]io.Writer{"127.0.0.99:5060": io.Discard},
			},
			// a connection built as a literal starts at zero, unlike any
			// connection in service; the transaction's release would take it
			// below zero and the pool would log "ref went negative"
			refcount: 1,
		}
		tx := NewClientTx("timer-f", req, conn, slog.New(slog.NewTextHandler(io.Discard, nil)))
		require.NoError(t, tx.Init())
		select {
		case <-tx.Done():
		case <-time.After(5 * time.Second):
			tx.Terminate()
			t.Fatalf("%s transaction did not end on its own timer", req.Method)
		}
		return tx.Err()
	}

	Timer_B, Timer_F = time.Hour, 50*time.Millisecond
	err := run(t, testCreateRequest(t, "OPTIONS", "sip:127.0.0.99:5060", "udp", "127.0.0.2:5060"))
	require.ErrorIs(t, err, ErrTransactionTimeout)
	require.Contains(t, err.Error(), "Timer_F")

	Timer_B, Timer_F = 50*time.Millisecond, time.Hour
	invite, _, _ := testCreateInvite(t, "sip:127.0.0.99:5060", "udp", "127.0.0.2:5060")
	err = run(t, invite)
	require.ErrorIs(t, err, ErrTransactionTimeout)
	require.Contains(t, err.Error(), "Timer_B")
}
