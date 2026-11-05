package sipgo

import (
	"log/slog"
	"net"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

// An ACK read while WriteResponse is starting confirms the dialog, and
// WriteResponse sees it (RFC 3261 13.3.1.4).
// Before the patch the state read channel was registered after the state
// was set to Established: an ACK read in between moved the dialog to
// Confirmed with nobody listening, every later ACK left Confirmed as it was
// and notified nobody, and the 2xx was retransmitted for 64*T1 and the
// session ended as unacknowledged. It took a UA that sent the 2xx again
// after taking a call over from another node while the caller's ACK was on
// its way (5 failures in 560 runs of that test next to other SIP test
// packages, none alone). The window is hit here
// exactly: the ACK is read from the state callback of Established, before
// WriteResponse waits.
//
// A dialog an ACK confirmed before WriteResponse is not taken back to
// Established either: the ACK acknowledged a 2xx to this INVITE, sent by
// the UA the call was taken over from.
func TestDialogServerAckWhileWriteResponseStarts(t *testing.T) {
	for _, c := range []struct {
		name  string
		early bool // the ACK is read before WriteResponse, not inside it
	}{
		{"ACK right after Established", false},
		{"ACK before WriteResponse", true},
	} {
		t.Run(c.name, func(t *testing.T) {
			ua, _ := NewUA()
			defer ua.Close()
			cli, _ := NewClient(ua)
			dialogSrv := NewDialogServerCache(cli, sip.ContactHeader{
				Address: sip.Uri{User: "test", Host: "127.0.0.200", Port: 5099},
			})
			invite, _, _ := createTestInvite(t, "sip:uas@127.0.0.1", "udp", "127.0.0.1:5090")
			invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})
			key, err := sip.ServerTxKeyMake(invite)
			require.NoError(t, err)
			tx := sip.NewServerTx(key, invite, discardConn{}, slog.Default())
			require.NoError(t, tx.Init())
			t.Cleanup(tx.Terminate)
			d, err := dialogSrv.ReadInvite(invite, tx)
			require.NoError(t, err)

			ack := sip.NewRequest(sip.ACK, invite.Recipient)
			ack.AppendHeader(&sip.CSeqHeader{SeqNo: invite.CSeq().SeqNo, MethodName: sip.ACK})
			if c.early {
				require.NoError(t, d.ReadAck(ack, nil))
			} else {
				var once atomic.Bool
				d.OnState(func(s sip.DialogState) {
					if s == sip.DialogStateEstablished && once.CompareAndSwap(false, true) {
						require.NoError(t, d.ReadAck(ack, nil))
					}
				})
			}

			done := make(chan error, 1)
			go func() {
				done <- d.WriteResponse(sip.NewResponseFromRequest(d.InviteRequest, sip.StatusOK, "OK", nil))
			}()
			select {
			case err := <-done:
				require.NoError(t, err)
			case <-time.After(time.Second):
				t.Fatal("WriteResponse(200) keeps waiting for an ACK the dialog has already read")
			}
			require.Equal(t, sip.DialogStateConfirmed, d.LoadState())
		})
	}
}

// Established set while an ACK confirms the dialog never takes it back from
// Confirmed: whichever of the two comes first, the dialog ends Confirmed
// (RFC 3261 13.3.1.4). With the check "not
// confirmed yet" and the write of Established apart, an ACK read between
// them was overwritten: WriteResponse returned, but the dialog stayed
// Established with its ACK consumed, Bye waited for Confirmed, and the
// engine took the call as unacknowledged. There is no seam in the code:
// the window is met by the schedule. A goroutine sets Established as soon
// as it is released, and the test confirms the dialog after a delay that
// differs from round to round, so the confirmation falls before, inside
// and after the window.
func TestDialogEstablishKeepsConcurrentConfirm(t *testing.T) {
	if runtime.GOMAXPROCS(0) < 2 {
		t.Skip("both sides have to run at once: GOMAXPROCS < 2")
	}
	const rounds = 4000
	lost := 0
	for i := 0; i < rounds; i++ {
		var d Dialog
		var ready, start atomic.Bool
		done := make(chan struct{})
		go func() {
			defer close(done)
			ready.Store(true)
			for !start.Load() {
			}
			d.establish()
		}()
		for !ready.Load() {
			runtime.Gosched()
		}
		start.Store(true)
		for j := 0; j < i%256; j++ {
			start.Load()
		}
		d.setState(sip.DialogStateConfirmed)
		<-done
		if d.LoadState() != sip.DialogStateConfirmed {
			lost++
		}
	}
	require.Zero(t, lost, "dialog taken back to Established after an ACK confirmed it, rounds of %d", rounds)
}

// Established only moves a dialog forward (RFC 3261 12): a dialog an ACK has confirmed or CANCEL, BYE or a dead
// transaction has ended keeps its state, and no state callback runs.
// Taken back from Ended, the dialog was Established with its context
// cancelled, and WriteResponse retransmitted the 2xx for 64*T1 and then
// marked the ended dialog Confirmed.
func TestDialogEstablishNeverGoesBack(t *testing.T) {
	for _, st := range []sip.DialogState{sip.DialogStateConfirmed, sip.DialogStateEnded} {
		t.Run(st.String(), func(t *testing.T) {
			d := Dialog{InviteRequest: sip.NewRequest(sip.INVITE, sip.Uri{Host: "uas"})}
			d.InitWithState(st)
			var called atomic.Int32
			d.OnState(func(sip.DialogState) { called.Add(1) })
			d.establish()
			require.Equal(t, st, d.LoadState())
			require.Zero(t, called.Load(), "state callback ran without a change of state")
		})
	}

	t.Run("WriteResponse on an ended dialog", func(t *testing.T) {
		ua, _ := NewUA()
		defer ua.Close()
		cli, _ := NewClient(ua)
		dialogSrv := NewDialogServerCache(cli, sip.ContactHeader{
			Address: sip.Uri{User: "test", Host: "127.0.0.200", Port: 5099},
		})
		invite, _, _ := createTestInvite(t, "sip:uas@127.0.0.1", "udp", "127.0.0.1:5090")
		invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})
		key, err := sip.ServerTxKeyMake(invite)
		require.NoError(t, err)
		tx := sip.NewServerTx(key, invite, discardConn{}, slog.Default())
		require.NoError(t, tx.Init())
		t.Cleanup(tx.Terminate)
		d, err := dialogSrv.ReadInvite(invite, tx)
		require.NoError(t, err)
		d.setState(sip.DialogStateEnded)

		done := make(chan error, 1)
		go func() {
			done <- d.WriteResponse(sip.NewResponseFromRequest(d.InviteRequest, sip.StatusOK, "OK", nil))
		}()
		select {
		case err := <-done:
			require.Error(t, err, "an ended dialog has no ACK to wait for")
		case <-time.After(time.Second):
			t.Fatal("WriteResponse(200) waits for an ACK on a dialog that has ended")
		}
		require.Equal(t, sip.DialogStateEnded, d.LoadState())
	})
}

// discardConn is a connection for a transaction whose messages nobody reads.
type discardConn struct{}

func (discardConn) LocalAddr() net.Addr            { return nil }
func (discardConn) WriteMsg(msg sip.Message) error { return nil }
func (discardConn) Ref(i int) int                  { return 1 }
func (discardConn) TryClose() (int, error)         { return 0, nil }
func (discardConn) Close() error                   { return nil }
