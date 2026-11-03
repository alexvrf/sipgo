package sipgo

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// RFC 3261 13.2.2.4: "The ACK MUST be passed to the client transport every
// time a retransmission of the 2xx final response that triggered the ACK
// arrives" — the same ACK, Via branch included, not a new request. Here the
// 2xx retransmissions arrive after the first ACK has left: an ACK sent later
// than T1 crosses one in the network.
func TestDialogClientAckRetransmissionIsSameAck(t *testing.T) {
	type sent struct{ text, branch string }
	acks := make(chan sent, 8)
	acked := make(chan struct{})
	var once sync.Once
	client := testClientResponder(t, func(req *sip.Request, w *siptest.ClientTxResponder) {
		if req.IsAck() {
			b, _ := req.Via().Params.Get("branch")
			acks <- sent{req.String(), b}
			once.Do(func() { close(acked) })
			return
		}
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Contact", "<sip:uas@uas.example.com>"))
		w.Receive(res)
		// the 2xx retransmissions cross the first ACK
		<-acked
		w.Receive(res)
		w.Receive(res)
	})

	dua := DialogUA{Client: client}
	d, err := dua.Invite(context.Background(), sip.Uri{User: "test", Host: "uas.example.com"}, nil)
	require.NoError(t, err)
	require.NoError(t, d.WaitAnswer(context.Background(), AnswerOptions{}))
	require.NoError(t, d.Ack(context.Background()))

	var got []sent
	for len(got) < 3 {
		select {
		case a := <-acks:
			got = append(got, a)
		case <-time.After(5 * time.Second):
			t.Fatalf("ACKs within 5s: %d of 3 (the first and two retransmissions)", len(got))
		}
	}
	for i, a := range got[1:] {
		assert.Equal(t, got[0].branch, a.branch, "retransmission %d: Via branch of the first ACK", i+1)
		assert.Equal(t, got[0].text, a.text, "retransmission %d: the same ACK, byte for byte", i+1)
	}
	assert.Equal(t, sip.DialogStateConfirmed, d.LoadState())
}
