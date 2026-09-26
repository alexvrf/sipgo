package sip

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 200 on CANCEL carries the To tag the INVITE has already been answered
// with (RFC 3261 9.2: «The To tag of the response to the CANCEL and the To
// tag in the response to the original request SHOULD be the same»). The
// CANCEL carries no To tag, and the layer used to answer it with a fresh
// one — a third tag after the provisional responses and the 487.
func TestCancelOKKeepsInviteToTag(t *testing.T) {
	peer, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = peer.Close() })

	tp := NewTransportLayer(net.DefaultResolver, NewParser(), nil)
	t.Cleanup(func() { _ = tp.Close() })
	txl := NewTransactionLayer(tp)
	t.Cleanup(txl.Close)
	got := make(chan *ServerTx, 1)
	txl.OnRequest(func(req *Request, tx *ServerTx) { got <- tx })

	invite := testCreateRequest(t, "INVITE", "sip:127.0.0.1:5060", "UDP", peer.LocalAddr().String())
	require.NoError(t, txl.handleRequest(invite))
	tx := <-got
	ringing := NewResponseFromRequest(invite, StatusRinging, "Ringing", nil)
	ringing.To().Params.Add("tag", "uas-tag")
	require.NoError(t, tx.Respond(ringing))

	cancel := newCancelRequest(invite)
	require.NoError(t, txl.handleRequest(cancel))

	buf := make([]byte, 4096)
	require.NoError(t, peer.SetReadDeadline(time.Now().Add(2*time.Second)))
	for {
		n, _, err := peer.ReadFrom(buf)
		require.NoError(t, err, "no 200 on CANCEL")
		msg := string(buf[:n])
		if !strings.Contains(msg, "CANCEL\r\n") || !strings.HasPrefix(msg, "SIP/2.0 200") {
			continue
		}
		require.Contains(t, msg, "tag=uas-tag", "200 on CANCEL with a To tag other than the INVITE's:\n%s", msg)
		return
	}
}
