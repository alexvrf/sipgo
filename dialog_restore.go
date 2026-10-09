package sipgo

import (
	"errors"
	"fmt"

	"github.com/emiago/sipgo/sip"
)

// DialogSnapshot is the state of a dialog session kept as messages and
// counters, enough to build the session again in another process: a
// redundant node takes over calls established on its partner and has to go
// on with their dialogs (in-dialog requests, ACK for a 2xx, BYE) as the same
// UA. A dialog is defined by its Call-ID, tags, route set, remote target
// and sequence numbers (RFC 3261 12), and all of them are either in the
// INVITE and its last response or in the three counters below.
type DialogSnapshot struct {
	// InviteRequest is Dialog.InviteRequest. On the server side it is the
	// session's own copy, with our To tag already added (ReadInvite).
	InviteRequest *sip.Request
	// InviteResponse is the last response to the INVITE, sent or received.
	// It is nil while none was.
	InviteResponse *sip.Response
	State          sip.DialogState
	// CSeq is the last local sequence number (Dialog.CSEQ) and RemoteCSeq
	// the last one received from the peer (Dialog.RemoteCSEQ). They are restored as they are, zero included: a
	// UAC has an empty remote sequence number until the peer sends its first
	// request (RFC 3261 12.1.2), and filling it in from the INVITE — our own
	// number — would refuse that request whenever the peer counts from below
	// it.
	CSeq       uint32
	RemoteCSeq uint32
}

// RemoteCSEQ is the last sequence number received from the peer. Requests
// below it are refused (RFC 3261 12.2.2) and the ACK for our 2xx must match
// it, so a session built again from a snapshot needs it too.
func (d *Dialog) RemoteCSEQ() uint32 {
	return d.remoteCSeqNo.Load()
}

// ErrDialogRestoredNoTransaction is what a restored session returns when it
// is asked to answer or wait on the INVITE transaction it was built without.
var ErrDialogRestoredNoTransaction = errors.New("dialog: restored without its INVITE transaction")

// RestoreServerSession builds a UAS dialog session from a snapshot instead of
// a received INVITE (ReadInvite).
//
// tx is the INVITE server transaction the session answers on. A dialog whose
// final response is already sent has none left — pass nil, and the session
// gets a stand-in that is over from the start: responses on it fail with
// ErrDialogRestoredNoTransaction, everything that only terminates or waits
// for the transaction goes on at once. A caller that keeps transactions of
// its own across processes passes one of them instead.
func (c *DialogUA) RestoreServerSession(snap DialogSnapshot, tx sip.ServerTransaction) (*DialogServerSession, error) {
	if err := snap.validate(); err != nil {
		return nil, err
	}
	// The To tag was added by ReadInvite, so the ID is built the same way;
	// without it — our own tag — this is refused as for any request.
	id, err := sip.DialogIDFromRequestUAS(snap.InviteRequest)
	if err != nil {
		return nil, err
	}
	if res := snap.InviteResponse; res != nil && res.IsSuccess() {
		if resID, err := sip.DialogIDFromResponse(res); err != nil || resID != id {
			return nil, fmt.Errorf("dialog: response does not belong to the INVITE dialog %q", id)
		}
	}
	if tx == nil {
		tx = restoredTx{}
	}
	s := &DialogServerSession{
		Dialog:   Dialog{ID: id, InviteRequest: snap.InviteRequest, InviteResponse: snap.InviteResponse},
		inviteTx: tx,
		ua:       c,
	}
	s.restore(snap)
	return s, nil
}

// RestoreClientSession builds a UAC dialog session from a snapshot instead of
// a sent INVITE (WriteInvite). tx is the INVITE client transaction, nil when
// there is none left (RestoreServerSession). The ID is set once the INVITE
// was answered with a 2xx, as WaitAnswer does.
func (c *DialogUA) RestoreClientSession(snap DialogSnapshot, tx sip.ClientTransaction) (*DialogClientSession, error) {
	if err := snap.validate(); err != nil {
		return nil, err
	}
	var id string
	if res := snap.InviteResponse; res != nil && res.IsSuccess() {
		var err error
		if id, err = sip.DialogIDFromResponse(res); err != nil {
			return nil, err
		}
	} else if snap.State >= sip.DialogStateEstablished {
		return nil, fmt.Errorf("dialog: state %s without a 2xx response", snap.State)
	}
	if tx == nil {
		tx = restoredTx{}
	}
	s := &DialogClientSession{
		Dialog:   Dialog{ID: id, InviteRequest: snap.InviteRequest, InviteResponse: snap.InviteResponse},
		inviteTx: tx,
		UA:       c,
	}
	s.restore(snap)
	return s, nil
}

func (snap DialogSnapshot) validate() error {
	req := snap.InviteRequest
	switch {
	case req == nil:
		return errors.New("dialog: snapshot without INVITE")
	case !req.IsInvite():
		return fmt.Errorf("dialog: snapshot request is %s, not INVITE", req.Method)
	case req.CSeq() == nil:
		return errors.New("dialog: snapshot INVITE without CSeq")
	case snap.State == sip.DialogStateEnded:
		// An ended dialog has nothing left to continue, and its context
		// would have to be cancelled from the start.
		return errors.New("dialog: snapshot of an ended dialog")
	}
	return nil
}

// restore sets the counters and the state without running state callbacks:
// nothing has subscribed yet, and the state is not new — it is carried over.
func (d *Dialog) restore(snap DialogSnapshot) {
	d.Init()
	d.lastCSeqNo.Store(snap.CSeq)
	d.remoteCSeqNo.Store(snap.RemoteCSeq)
	d.state.Store(int32(snap.State))
}

// restoredTx stands in for the INVITE transaction of a restored session that
// has none left: it is over from the start.
type restoredTx struct{}

var restoredTxDone = func() chan struct{} {
	ch := make(chan struct{})
	close(ch)
	return ch
}()

func (restoredTx) Terminate()                             {}
func (restoredTx) OnTerminate(sip.FnTxTerminate) bool     { return false }
func (restoredTx) Done() <-chan struct{}                  { return restoredTxDone }
func (restoredTx) Err() error                             { return ErrDialogRestoredNoTransaction }
func (restoredTx) Respond(*sip.Response) error            { return ErrDialogRestoredNoTransaction }
func (restoredTx) Acks() <-chan *sip.Request              { return nil }
func (restoredTx) OnCancel(sip.FnTxCancel) bool           { return false }
func (restoredTx) Responses() <-chan *sip.Response        { return nil }
func (restoredTx) OnRetransmission(sip.FnTxResponse) bool { return false }
