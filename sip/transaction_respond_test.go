package sip

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/emiago/sipgo/fakes"
)

// Respond returns the outcome of its own response, not of what the FSM
// did after it. Over a reliable transport a final response to a non-INVITE
// request arms Timer J with zero (RFC 3261 17.2.2: "Timer J ... zero
// seconds for reliable transports"), and the timer deletes the transaction
// from its own goroutine. Respond used to read Err() after releasing
// fsmMu, so whenever the timer won the lock first a response that went out
// was reported as "transaction terminated" — about one call in a hundred.
// TestServerTransactionNonInviteFSM/TCP failed that often; here the
// window is hit by repetition.
func TestServerRespondReliableFinalReturnsNil(t *testing.T) {
	tcp := func() *TCPConnection {
		return &TCPConnection{Conn: &fakes.TCPConn{
			LAddr:  net.TCPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5060},
			RAddr:  net.TCPAddr{IP: net.IPv4(127, 0, 0, 99), Port: 5060},
			Reader: bytes.NewBuffer(nil),
			Writer: io.Discard,
		}, refcount: 1}
	}
	failed := 0
	const runs = 20000
	for i := 0; i < runs; i++ {
		req := testCreateRequest(t, "OPTIONS", "sip:127.0.0.99:5060", "tcp", "127.0.0.2:5060")
		tx := NewServerTx("respond", req, tcp(), slog.New(slog.NewTextHandler(io.Discard, nil)))
		if err := tx.Init(); err != nil {
			t.Fatal(err)
		}
		if err := tx.Respond(NewResponseFromRequest(req, StatusOK, "OK", nil)); err != nil {
			failed++
		}
		<-tx.Done()
	}
	if failed > 0 {
		t.Errorf("Respond reported an error for %d sent responses of %d", failed, runs)
	}
}

// A transaction terminated before Respond still reports it: the cause is
// the one Terminate recorded (patch 20), not nil.
func TestServerRespondAfterTerminateFails(t *testing.T) {
	req := testCreateRequest(t, "OPTIONS", "sip:127.0.0.99:5060", "tcp", "127.0.0.2:5060")
	tx := NewServerTx("respond-terminated", req, &TCPConnection{Conn: &fakes.TCPConn{
		LAddr:  net.TCPAddr{IP: net.IPv4(127, 0, 0, 2), Port: 5060},
		RAddr:  net.TCPAddr{IP: net.IPv4(127, 0, 0, 99), Port: 5060},
		Reader: bytes.NewBuffer(nil),
		Writer: io.Discard,
	}, refcount: 1}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := tx.Init(); err != nil {
		t.Fatal(err)
	}
	tx.Terminate()
	if err := tx.Respond(NewResponseFromRequest(req, StatusOK, "OK", nil)); err == nil {
		t.Error("Respond on a terminated transaction returned nil")
	}
}
