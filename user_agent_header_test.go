package sipgo

import (
	"context"
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
	"github.com/stretchr/testify/require"
)

// RFC 3261 20.41: the User-Agent header field describes the UAC originating
// the request, and implementers SHOULD make it configurable
// (WithUserAgentHeader). The client adds it on every send path — the default
// build, a request built by the caller's own options (as a dialog does for
// requests within itself) and a request written past any transaction (ACK to
// 2xx). A value the caller set is kept, the value is asked for on each
// request, and an empty one adds nothing.
func TestClientUserAgentHeader(t *testing.T) {
	value := "settaswitch"
	ua, err := NewUA(WithUserAgentHeader(func() string { return value }))
	require.NoError(t, err)
	t.Cleanup(func() { _ = ua.Close() })
	client, err := NewClient(ua, WithClientHostname("127.0.0.1"))
	require.NoError(t, err)

	var seen []*sip.Request
	client.TxRequester = &siptest.ClientTxRequesterResponder{
		OnRequest: func(req *sip.Request, w *siptest.ClientTxResponder) {
			seen = append(seen, req)
			w.Receive(sip.NewResponseFromRequest(req, 200, "OK", nil))
		},
	}
	target := sip.Uri{User: "bob", Host: "127.0.0.2", Port: 5060}
	userAgent := func(req *sip.Request) string {
		if h := req.GetHeader("User-Agent"); h != nil {
			return h.Value()
		}
		return ""
	}

	byDefault := sip.NewRequest(sip.OPTIONS, target)
	_, err = client.Do(context.Background(), byDefault)
	require.NoError(t, err)
	require.Equal(t, "settaswitch", userAgent(byDefault), "default build")

	withOptions := sip.NewRequest(sip.OPTIONS, target)
	_, err = client.TransactionRequest(context.Background(), withOptions, ClientRequestBuild)
	require.NoError(t, err)
	require.Equal(t, "settaswitch", userAgent(withOptions), "caller's options")

	ack := sip.NewRequest(sip.ACK, target)
	require.NoError(t, client.WriteRequest(ack))
	require.Equal(t, "settaswitch", userAgent(ack), "written past any transaction")

	own := sip.NewRequest(sip.OPTIONS, target)
	own.AppendHeader(sip.NewHeader("User-Agent", "caller/1"))
	_, err = client.Do(context.Background(), own)
	require.NoError(t, err)
	require.Len(t, own.GetHeaders("User-Agent"), 1)
	require.Equal(t, "caller/1", userAgent(own), "caller's value replaced")

	value = ""
	off := sip.NewRequest(sip.OPTIONS, target)
	_, err = client.Do(context.Background(), off)
	require.NoError(t, err)
	require.Nil(t, off.GetHeader("User-Agent"), "empty value still added a header")
	require.Nil(t, off.GetHeader("Server"), "Server belongs to responses (20.35)")
}

// The CANCEL of a dialog is built from its INVITE and carries the INVITE's
// User-Agent: both come from the same UAC (RFC 3261 20.41), and the client
// would otherwise stamp whatever value is configured by the time the caller
// hangs up.
func TestDialogCancelKeepsInviteUserAgent(t *testing.T) {
	invite := sip.NewRequest(sip.INVITE, sip.Uri{User: "bob", Host: "127.0.0.2", Port: 5060})
	invite.AppendHeader(&sip.ViaHeader{ProtocolName: "SIP", ProtocolVersion: "2.0",
		Transport: "UDP", Host: "127.0.0.1", Port: 5060, Params: sip.NewParams()})
	invite.AppendHeader(&sip.FromHeader{Address: sip.Uri{User: "alice", Host: "127.0.0.1"}, Params: sip.NewParams()})
	invite.AppendHeader(&sip.ToHeader{Address: sip.Uri{User: "bob", Host: "127.0.0.2"}, Params: sip.NewParams()})
	callID := sip.CallIDHeader("cancel-ua")
	invite.AppendHeader(&callID)
	invite.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	invite.AppendHeader(sip.NewHeader("User-Agent", "settaswitch/1"))

	cancel := newCancelRequest(invite)
	h := cancel.GetHeader("User-Agent")
	require.NotNil(t, h, "CANCEL lost the INVITE's User-Agent")
	require.Equal(t, "settaswitch/1", h.Value())
}
