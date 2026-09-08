package sipgo

import (
	"context"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
	"github.com/stretchr/testify/require"
)

// A gateway must put a Reason header on the CANCEL it sends: RFC 3326
// defines the header, and ITU-T Q.1912.5 7.7.1 requires the Q.850 cause
// of the release that triggered the cancellation to travel in it. The
// CANCEL is built inside WaitAnswer, so the caller can only pass the
// header through the answer options.
func TestDialogClientCancelCarriesHeaders(t *testing.T) {
	cancels := make(chan *sip.Request, 1)
	invites := make(chan *sip.Request, 1)
	inviteTx := make(chan *siptest.ClientTxResponder, 1)
	client := testClientResponder(t, func(req *sip.Request, w *siptest.ClientTxResponder) {
		switch req.Method {
		case sip.INVITE:
			// CANCEL is allowed only after a provisional response
			// (RFC 3261 9.1), so the answer waits in Proceeding
			invites <- req
			inviteTx <- w
			w.Receive(sip.NewResponseFromRequest(req, 180, "Ringing", nil))
		case sip.CANCEL:
			cancels <- req
			w.Receive(sip.NewResponseFromRequest(req, 200, "OK", nil))
			// 487 for the INVITE, so the answer does not wait out 64*T1
			(<-inviteTx).Receive(sip.NewResponseFromRequest(<-invites, 487, "Request Terminated", nil))
		}
	})

	invite := sip.NewRequest(sip.INVITE, sip.Uri{User: "test", Host: "uas.example.com"})
	invite.AppendHeader(sip.NewHeader("Contact", "<sip:uac@uac.example.com>"))
	require.NoError(t, clientRequestBuildReq(client, invite))

	ua := &DialogUA{
		Client:     client,
		ContactHDR: sip.ContactHeader{Address: sip.Uri{User: "uac", Host: "uac.example.com"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dialog, err := ua.WriteInvite(ctx, invite)
	require.NoError(t, err)

	answered := make(chan error, 1)
	go func() {
		answered <- dialog.WaitAnswer(ctx, AnswerOptions{
			CancelHeaders: func() []sip.Header {
				return []sip.Header{sip.NewHeader("Reason", `Q.850;cause=16;text="Normal call clearing"`)}
			},
			OnResponse: func(res *sip.Response) error {
				if res.StatusCode == 180 {
					cancel()
				}
				return nil
			},
		})
	}()

	select {
	case req := <-cancels:
		h := req.GetHeader("Reason")
		require.NotNil(t, h, "CANCEL was sent without the Reason header")
		require.Equal(t, `Q.850;cause=16;text="Normal call clearing"`, h.Value())
	case <-time.After(5 * time.Second):
		t.Fatal("no CANCEL was sent")
	}
	<-answered
}

// A cancelled INVITE leaves its client transaction to finish on its own.
//
// RFC 3261 17.1.1.2 keeps a client INVITE transaction in Completed for
// Timer D after a 300-699 so it can ACK retransmissions of that response.
// Terminating the transaction as soon as WaitAnswer returned took that away:
// a UAS that gets no ACK keeps resending its final response until its own
// Timer H, and with no transaction to match, none of those retransmissions
// is answered.
func TestDialogClientCancelKeepsTransactionForRetransmits(t *testing.T) {
	invites := make(chan *sip.Request, 1)
	inviteTx := make(chan *siptest.ClientTxResponder, 1)
	client := testClientResponder(t, func(req *sip.Request, w *siptest.ClientTxResponder) {
		switch req.Method {
		case sip.INVITE:
			invites <- req
			inviteTx <- w
			w.Receive(sip.NewResponseFromRequest(req, 180, "Ringing", nil))
		case sip.CANCEL:
			w.Receive(sip.NewResponseFromRequest(req, 200, "OK", nil))
			// the UAS answers the cancelled INVITE, as RFC 3261 9.2 asks
			(<-inviteTx).Receive(sip.NewResponseFromRequest(<-invites, 487, "Request Terminated", nil))
		}
	})

	invite := sip.NewRequest(sip.INVITE, sip.Uri{User: "test", Host: "uas.example.com"})
	invite.AppendHeader(sip.NewHeader("Contact", "<sip:uac@uac.example.com>"))
	require.NoError(t, clientRequestBuildReq(client, invite))

	ua := &DialogUA{
		Client:     client,
		ContactHDR: sip.ContactHeader{Address: sip.Uri{User: "uac", Host: "uac.example.com"}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dialog, err := ua.WriteInvite(ctx, invite)
	require.NoError(t, err)

	answered := make(chan error, 1)
	go func() {
		answered <- dialog.WaitAnswer(ctx, AnswerOptions{
			OnResponse: func(res *sip.Response) error {
				if res.StatusCode == 180 {
					cancel()
				}
				return nil
			},
		})
	}()
	select {
	case <-answered:
	case <-time.After(5 * time.Second):
		t.Fatal("WaitAnswer did not return after the cancellation")
	}

	select {
	case <-dialog.inviteTx.Done():
		t.Fatal("the client transaction was terminated: a retransmitted final response would go unanswered")
	default:
	}
}
