package sipgo

import (
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgo/siptest"
	"github.com/stretchr/testify/require"
)

// A final non-2xx response returns as soon as it is sent. RFC 3261 17.2.1:
// the ACK for it "is absorbed by the server transaction" and is not passed
// up; the transaction also retransmits the response until that ACK or
// Timer H on its own. Upstream WriteResponse waited for the ACK or for the
// transaction to end, so a UAC that never acknowledged a rejection held
// the caller for 64*T1 (32 s): a B2BUA keeps the call, its trunk slot
// included, until the rejection returns.
func TestDialogServerFinalNon2xxDoesNotWaitAck(t *testing.T) {
	ua, _ := NewUA()
	defer ua.Close()
	cli, _ := NewClient(ua)
	dialogSrv := NewDialogServerCache(cli, sip.ContactHeader{
		Address: sip.Uri{User: "test", Host: "127.0.0.200", Port: 5099},
	})
	invite, _, _ := createTestInvite(t, "sip:uas@127.0.0.1", "udp", "127.0.0.1:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uas", Port: 1234}})
	tx := siptest.NewServerTxRecorder(invite)
	t.Cleanup(tx.Terminate)
	d, err := dialogSrv.ReadInvite(invite, tx)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- d.WriteResponse(sip.NewResponseFromRequest(d.InviteRequest, sip.StatusBusyHere, "Busy Here", nil))
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("WriteResponse(486) waits for an ACK the transaction absorbs itself")
	}
	require.Equal(t, sip.DialogStateEnded, d.LoadState())
	// the transaction stays for its retransmissions (Timer G) and the ACK
	select {
	case <-tx.Done():
		t.Fatal("the transaction ended with the response: retransmissions and the ACK have nowhere to go")
	default:
	}
}
