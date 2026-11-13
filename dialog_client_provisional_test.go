package sipgo

import (
	"context"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
	"github.com/stretchr/testify/require"
)

// RFC 3261 13.3.1.1: "A UAS MAY send as many provisional responses as it
// likes", and in Proceeding "Any further provisional responses MUST be passed
// up to the TU" (17.1.1.2). WaitAnswer used to give up after ten responses
// with "more than 10 responses received", leaving the INVITE un-cancelled:
// the callee kept ringing, and its 2xx, should it answer, would never be
// ACKed. Thirty 180s and then a 200 must end in the answer.
func TestDialogClientManyProvisionals(t *testing.T) {
	const ringing = 30
	client := testClientResponder(t, func(req *sip.Request, w *siptest.ClientTxResponder) {
		if req.IsAck() {
			return
		}
		for range ringing {
			res := sip.NewResponseFromRequest(req, sip.StatusRinging, "Ringing", nil)
			res.AppendHeader(sip.NewHeader("Contact", "<sip:uas@uas.example.com>"))
			w.Receive(res)
		}
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		res.AppendHeader(sip.NewHeader("Contact", "<sip:uas@uas.example.com>"))
		w.Receive(res)
	})

	dua := DialogUA{Client: client}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := dua.Invite(ctx, sip.Uri{User: "test", Host: "uas.example.com"}, nil)
	require.NoError(t, err)
	provisional := 0
	require.NoError(t, d.WaitAnswer(ctx, AnswerOptions{OnResponse: func(res *sip.Response) error {
		if res.IsProvisional() {
			provisional++
		}
		return nil
	}}))
	require.Equal(t, ringing, provisional, "every provisional is passed up")
	require.Equal(t, sip.StatusOK, d.InviteResponse.StatusCode)
}
