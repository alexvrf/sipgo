package sipgo

import (
	"errors"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
	"github.com/stretchr/testify/require"
)

// 2xx on INVITE is retransmitted at T1, 2*T1, 4*T1 … up to T2
// (RFC 3261 13.3.1.4), and without an ACK the wait ends after 64*T1 with
// the dialog confirmed, so that the session can be terminated with BYE.
// Upstream jumped to T2 after the first retransmission, and its 64*T1
// timer was recreated on every loop iteration and never fired.
func TestDialogServer2xxRetransmitsDoublingAndGivesUp(t *testing.T) {
	t1, t2, t4 := sip.T1, sip.T2, sip.T4
	sip.SetTimers(50*time.Millisecond, 400*time.Millisecond, 500*time.Millisecond)
	t.Cleanup(func() { sip.SetTimers(t1, t2, t4) })

	ua, _ := NewUA()
	defer ua.Close()
	cli, _ := NewClient(ua)
	dialogSrv := NewDialogServerCache(cli, sip.ContactHeader{
		Address: sip.Uri{User: "test", Host: "127.0.0.200", Port: 5099},
	})
	invite, _, _ := createTestInvite(t, "sip:uas@127.0.0.1", "udp", "127.0.0.1:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})
	tx := siptest.NewServerTxRecorder(invite)
	d, err := dialogSrv.ReadInvite(invite, tx)
	require.NoError(t, err)

	// sent at 0, T1, 3*T1, 7*T1 = 0, 50, 150, 350 ms; upstream: 0, 50, 450
	early := make(chan int, 1)
	go func() {
		time.Sleep(380 * time.Millisecond)
		early <- len(tx.Result())
	}()

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- d.WriteResponse(sip.NewResponseFromRequest(d.InviteRequest, 200, "OK", nil)) }()

	require.Equal(t, 4, <-early, "2xx retransmissions within 7*T1")
	select {
	case err := <-done:
		require.True(t, errors.Is(err, ErrDialogResponseNoACK), "err = %v", err)
		require.Less(t, time.Since(start), 64*sip.T1+time.Second)
		require.Equal(t, sip.DialogStateConfirmed, d.LoadState())
	case <-time.After(64*sip.T1 + 2*time.Second):
		t.Fatal("no ACK: the wait did not end after 64*T1")
	}
}
