package sip

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo/fakes"
	"github.com/stretchr/testify/require"
)

// RFC 3261 20.35: the Server header field describes the software used by the
// UAS, and implementers SHOULD make it configurable. Every response of a
// server transaction carries the configured value — the TU's own and those
// the transaction builds itself (487 on CANCEL), which the TU never sees and
// so could not stamp. A Server header the TU set is kept, and an empty value
// adds nothing.
func TestServerTransactionServerHeader(t *testing.T) {
	newTx := func(t *testing.T, value func() string) (*ServerTx, *Request, *bytes.Buffer) {
		t.Helper()
		req, _, _ := testCreateInvite(t, "sip:127.0.0.99:5060", "udp", "127.0.0.2:5060")
		outgoing := bytes.NewBuffer(nil)
		conn := &UDPConnection{PacketConn: &fakes.UDPConn{
			Reader:  bytes.NewBuffer(nil),
			Writers: map[string]io.Writer{"127.0.0.2:5060": outgoing},
		}}
		tx := NewServerTx("123", req, conn, slog.Default())
		tx.serverHeader = value
		require.NoError(t, tx.Init())
		t.Cleanup(tx.Terminate)
		return tx, req, outgoing
	}
	product := func() string { return "settaswitch" }

	t.Run("own and 487", func(t *testing.T) {
		tx, req, out := newTx(t, product)
		require.NoError(t, tx.Respond(NewResponseFromRequest(req, StatusRinging, "Ringing", nil)))
		cancel := NewRequest(CANCEL, req.Recipient)
		cancel.AppendHeader(HeaderClone(req.Via()))
		cancel.AppendHeader(HeaderClone(req.From()))
		cancel.AppendHeader(HeaderClone(req.To()))
		cancel.AppendHeader(HeaderClone(req.CallID()))
		require.NoError(t, tx.Receive(cancel))

		ringing, final, ok := strings.Cut(out.String(), "SIP/2.0 487")
		require.True(t, ok, "487 was not sent:\n%s", out.String())
		require.Contains(t, ringing, "Server: settaswitch\r\n", "TU response without Server")
		require.Contains(t, final, "Server: settaswitch\r\n", "487 built by the transaction without Server")
		require.NotContains(t, out.String(), "User-Agent:", "User-Agent belongs to requests (20.41)")
	})

	t.Run("TU value kept", func(t *testing.T) {
		tx, req, out := newTx(t, product)
		res := NewResponseFromRequest(req, StatusRinging, "Ringing", nil)
		res.AppendHeader(NewHeader("Server", "gateway/2"))
		require.NoError(t, tx.Respond(res))
		require.Contains(t, out.String(), "Server: gateway/2\r\n")
		require.Equal(t, 1, strings.Count(out.String(), "Server:"), "second Server header added")
	})

	t.Run("empty adds nothing", func(t *testing.T) {
		tx, req, out := newTx(t, func() string { return "" })
		require.NoError(t, tx.Respond(NewResponseFromRequest(req, StatusRinging, "Ringing", nil)))
		require.NotContains(t, out.String(), "Server:")
	})
}

// RFC 3261 20.41: the User-Agent header field describes the UAC originating
// the request. The ACK to a non-2xx response is originated by the same UAC
// as the INVITE, but is built inside the client transaction (17.1.1.3) where
// the TU cannot add anything, so it copies the value of the INVITE.
func TestAckNon2xxCarriesUserAgent(t *testing.T) {
	invite, _, _ := testCreateInvite(t, "sip:127.0.0.99:5060", "udp", "127.0.0.2:5060")
	invite.AppendHeader(NewHeader("User-Agent", "settaswitch"))
	res := NewResponseFromRequest(invite, StatusBusyHere, "Busy Here", nil)

	ack := newAckRequestNon2xx(invite, res, nil)
	h := ack.GetHeader("User-Agent")
	require.NotNil(t, h, "ACK to non-2xx without User-Agent:\n%s", ack.String())
	require.Equal(t, "settaswitch", h.Value())

	cancel := newCancelRequest(invite)
	require.NotNil(t, cancel.GetHeader("User-Agent"), "CANCEL without User-Agent")
}

// The layer hands its value to every server transaction it creates, on
// whatever path it creates them. The test above sets the field on a
// transaction built by hand, so it could not notice a path that forgets to:
// merging upstream 86ec9f7 moved the creation into serverTxRequest and the
// value was lost there, which only the engine's wire check caught. This one
// goes through handleRequest and reads the response off the peer's socket.
func TestTransactionLayerServerTxCarriesServerHeader(t *testing.T) {
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	tp := NewTransportLayer(net.DefaultResolver, NewParser(), nil)
	t.Cleanup(func() { _ = tp.Close() })
	txl := NewTransactionLayer(tp, WithTransactionLayerServerHeader(func() string { return "settaswitch" }))
	t.Cleanup(txl.Close)
	got := make(chan *ServerTx, 1)
	txl.OnRequest(func(req *Request, tx *ServerTx) { got <- tx })

	req := testCreateRequest(t, "OPTIONS", "sip:127.0.0.1:5060", "UDP", peer.LocalAddr().String())
	require.NoError(t, txl.handleRequest(req))
	tx := <-got
	require.NoError(t, tx.Respond(NewResponseFromRequest(req, StatusOK, "OK", nil)))

	buf := make([]byte, 4096)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(2*time.Second)))
	n, _, err := peer.ReadFrom(buf)
	require.NoError(t, err)
	require.Contains(t, string(buf[:n]), "Server: settaswitch\r\n",
		"response of a transaction created by the layer went out without Server")
}
