package sipgo

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

// restoreNode is one SIP node of the restore tests: a UA listening on UDP,
// with a client and a dialog UA whose Contact is the listening address. A
// node is stopped and started again on the same address to stand for a
// redundant partner taking the address over.
type restoreNode struct {
	addr string
	ua   *UserAgent
	cli  *Client
	dua  *DialogUA
	once sync.Once
	stop context.CancelFunc
}

// freeUDPAddr is a loopback address with a port nobody listens on.
func freeUDPAddr(t *testing.T) string {
	t.Helper()
	c, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := c.LocalAddr().String()
	require.NoError(t, c.Close())
	return addr
}

func startRestoreNode(t *testing.T, addr string, setup func(n *restoreNode, srv *Server)) *restoreNode {
	t.Helper()
	ua, err := NewUA()
	require.NoError(t, err)
	cli, err := NewClient(ua)
	require.NoError(t, err)
	srv, err := NewServer(ua)
	require.NoError(t, err)

	host, portText, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	n := &restoreNode{
		addr: addr, ua: ua, cli: cli,
		dua: &DialogUA{Client: cli, ContactHDR: sip.ContactHeader{Address: sip.Uri{User: "node", Host: host, Port: port}}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.stop = cancel
	t.Cleanup(n.close)
	setup(n, srv)

	ready := make(chan struct{})
	failed := make(chan error, 1)
	go func() {
		failed <- srv.ListenAndServe(context.WithValue(ctx, ListenReadyCtxKey, ListenReadyCtxValue(ready)), "udp", addr)
	}()
	select {
	case <-ready:
	case err := <-failed:
		t.Fatalf("listen %s: %v", addr, err)
	}
	return n
}

// close stops the node as a crash would: no BYE, no ACK, nothing in flight
// is finished. It returns once the address is free: the server closes its
// listener from a goroutine of its own (ListenAndServe), and the node
// started next on the address would otherwise find it still bound.
func (n *restoreNode) close() {
	n.once.Do(func() {
		n.stop()
		n.ua.Close()
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			if c, err := net.ListenPacket("udp", n.addr); err == nil {
				c.Close()
				return
			}
		}
	})
}

// snapshotAsText takes a dialog session the way a redundant partner gets it:
// messages as text and three counters. Parsing them back proves the text is
// enough — nothing of the in-memory request (source, transport, parsed
// header cache) is carried over.
func snapshotAsText(t *testing.T, d *Dialog) DialogSnapshot {
	t.Helper()
	parse := func(text string) sip.Message {
		msg, err := sip.ParseMessage([]byte(text))
		require.NoError(t, err)
		return msg
	}
	snap := DialogSnapshot{
		InviteRequest: parse(d.InviteRequest.String()).(*sip.Request),
		State:         d.LoadState(),
		CSeq:          d.CSEQ(), RemoteCSeq: d.RemoteCSEQ(),
	}
	if d.InviteResponse != nil {
		snap.InviteResponse = parse(d.InviteResponse.String()).(*sip.Response)
	}
	return snap
}

// respondOK answers an in-dialog request after checking it against the
// session's remote sequence number (RFC 3261 12.2.2).
func respondOK(d *Dialog, req *sip.Request, tx sip.ServerTransaction) error {
	if err := d.ReadRequest(req, tx); err != nil {
		_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
		return err
	}
	return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
}

func info(to *DialogServerSession) *sip.Request {
	return sip.NewRequest(sip.INFO, to.InviteRequest.Contact().Address)
}

// A UAS session built again from its INVITE, last response and counters
// continues the dialog as the same UA (RFC 3261 12.2): the peer matches its
// requests to the dialog by Call-ID and tags (12.2.1.1), the local sequence
// number goes on from the last one sent (12.2.1.1: "the CSeq ... MUST be
// incremented by one"), a request from the peer is checked against the last
// received number (12.2.2), and BYE ends the dialog on both sides. The
// node that answered the call is gone: the session lives on another one
// listening on the same address — a redundant partner after takeover.
func TestDialogServerSessionRestoredContinuesDialog(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	addrA, addrB := freeUDPAddr(t), freeUDPAddr(t)

	// A — the caller, alive all along
	var dc atomic.Pointer[DialogClientSession]
	infoA := make(chan *sip.Request, 4)
	byeA := make(chan *sip.Request, 1)
	nodeA := startRestoreNode(t, addrA, func(_ *restoreNode, srv *Server) {
		srv.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
			d := dc.Load()
			if id, err := sip.DialogIDFromRequestUAC(req); err != nil || id != d.ID {
				_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Unknown dialog", nil))
				infoA <- nil
				return
			}
			if respondOK(&d.Dialog, req, tx) != nil {
				infoA <- nil
				return
			}
			infoA <- req
		})
		srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
			d := dc.Load()
			if id, err := sip.DialogIDFromRequestUAC(req); err != nil || id != d.ID {
				_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Unknown dialog", nil))
				return
			}
			require.NoError(t, d.ReadBye(req, tx))
			byeA <- req
		})
	})

	// B — answers the call and later dies
	var ds atomic.Pointer[DialogServerSession]
	answered := make(chan error, 1)
	nodeB := startRestoreNode(t, addrB, func(nodeB *restoreNode, srv *Server) {
		srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
			d, err := nodeB.dua.ReadInvite(req, tx)
			if err != nil {
				answered <- err
				return
			}
			ds.Store(d)
			answered <- d.Respond(sip.StatusOK, "OK", nil)
		})
		srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
			_ = ds.Load().ReadAck(req, tx)
		})
		srv.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
			_ = respondOK(&ds.Load().Dialog, req, tx)
		})
	})

	hostB, portB := splitHostPort(t, addrB)
	d, err := nodeA.dua.Invite(ctx, sip.Uri{User: "b", Host: hostB, Port: portB}, nil)
	require.NoError(t, err)
	dc.Store(d)
	require.NoError(t, d.WaitAnswer(ctx, AnswerOptions{}))
	require.NoError(t, d.Ack(ctx))
	require.NoError(t, <-answered)

	// both counters move before the snapshot: one request each way
	res, err := d.Do(ctx, sip.NewRequest(sip.INFO, d.InviteResponse.Contact().Address))
	require.NoError(t, err)
	require.Equal(t, sip.StatusOK, res.StatusCode)
	old := ds.Load()
	res, err = old.Do(ctx, info(old))
	require.NoError(t, err)
	require.Equal(t, sip.StatusOK, res.StatusCode)
	sentByB := (<-infoA).CSeq().SeqNo

	snap := snapshotAsText(t, &old.Dialog)
	nodeB.close()

	// B' — another node on the same address, with nothing but the snapshot
	var restored atomic.Pointer[DialogServerSession]
	infoB := make(chan error, 1)
	nodeB2 := startRestoreNode(t, addrB, func(_ *restoreNode, srv *Server) {
		srv.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
			r := restored.Load()
			if id, err := sip.DialogIDFromRequestUAS(req); err != nil || id != r.ID {
				_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Unknown dialog", nil))
				infoB <- errors.New("request not matched to the restored dialog")
				return
			}
			infoB <- respondOK(&r.Dialog, req, tx)
		})
	})
	r, err := nodeB2.dua.RestoreServerSession(snap, nil)
	require.NoError(t, err)
	restored.Store(r)
	require.Equal(t, old.ID, r.ID)
	require.Equal(t, sip.DialogStateConfirmed, r.LoadState())
	// the caller's INFO before the snapshot moved the remote number past the
	// INVITE: a request with the INVITE's number is now out of order
	stale := sip.NewRequest(sip.INFO, r.InviteRequest.Contact().Address)
	stale.AppendHeader(&sip.CSeqHeader{SeqNo: snap.RemoteCSeq - 1, MethodName: sip.INFO})
	require.ErrorIs(t, r.ReadRequest(stale, nil), ErrDialogInvalidCseq,
		"a request below the carried remote number was accepted")

	// our request: matched by the peer, the number goes on
	res, err = r.Do(ctx, info(r))
	require.NoError(t, err)
	require.Equal(t, sip.StatusOK, res.StatusCode, "the caller did not take the request of the restored session")
	got := <-infoA
	require.NotNil(t, got, "the caller refused the request of the restored session")
	require.Equal(t, sentByB+1, got.CSeq().SeqNo, "the local sequence number did not go on from the snapshot")

	// the peer's request: matched and checked against the carried number
	res, err = d.Do(ctx, sip.NewRequest(sip.INFO, d.InviteResponse.Contact().Address))
	require.NoError(t, err)
	require.NoError(t, <-infoB)
	require.Equal(t, sip.StatusOK, res.StatusCode)
	require.Equal(t, d.CSEQ(), r.RemoteCSEQ())

	// the INVITE transaction did not survive: answering it is refused
	require.ErrorIs(t, r.Respond(sip.StatusOK, "OK", nil), ErrDialogRestoredNoTransaction)

	// BYE ends the dialog on both sides
	require.NoError(t, r.Bye(ctx))
	require.Equal(t, sip.DialogStateEnded, r.LoadState())
	<-byeA
	require.Equal(t, sip.DialogStateEnded, d.LoadState())
}

// A UAC session built again from a snapshot taken after the 2xx and before
// the ACK sends the ACK (RFC 3261 13.2.2.4) — with the route set from the
// Record-Route of the response (12.1.2) and the sequence number of the
// INVITE — and the UAS, retransmitting its 2xx all the while, takes it as
// the ACK of its dialog instead of giving up after 64*T1 (13.3.1.4). Then
// in-dialog requests and BYE go on from the snapshot as on the server side.
func TestDialogClientSessionRestoredAcksAndContinues(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	addrA, addrB := freeUDPAddr(t), freeUDPAddr(t)
	hostB, portB := splitHostPort(t, addrB)

	// B — the callee, alive all along
	var ds atomic.Pointer[DialogServerSession]
	answered := make(chan error, 1)
	acks := make(chan *sip.Request, 1)
	infoB := make(chan *sip.Request, 2)
	byeB := make(chan struct{}, 1)
	startRestoreNode(t, addrB, func(nodeB *restoreNode, srv *Server) {
		srv.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
			d, err := nodeB.dua.ReadInvite(req, tx)
			if err != nil {
				answered <- err
				return
			}
			ds.Store(d)
			answered <- d.Respond(sip.StatusOK, "OK", nil)
		})
		srv.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
			if ds.Load().ReadAck(req, tx) == nil {
				acks <- req
			}
		})
		srv.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
			d := ds.Load()
			if id, err := sip.DialogIDFromRequestUAS(req); err != nil || id != d.ID {
				_ = tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Unknown dialog", nil))
				infoB <- nil
				return
			}
			if respondOK(&d.Dialog, req, tx) != nil {
				infoB <- nil
				return
			}
			infoB <- req
		})
		srv.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
			if ds.Load().ReadBye(req, tx) == nil {
				byeB <- struct{}{}
			}
		})
	})

	// A — calls, sees the 2xx and dies before the ACK
	nodeA := startRestoreNode(t, addrA, func(*restoreNode, *Server) {})
	route := sip.NewHeader("Record-Route", "<sip:"+addrB+";lr>")
	d, err := nodeA.dua.Invite(ctx, sip.Uri{User: "b", Host: hostB, Port: portB}, nil, route)
	require.NoError(t, err)
	require.NoError(t, d.WaitAnswer(ctx, AnswerOptions{}))
	snap := snapshotAsText(t, &d.Dialog)
	nodeA.close()

	// A' — on the same address, with nothing but the snapshot
	nodeA2 := startRestoreNode(t, addrA, func(*restoreNode, *Server) {})
	r, err := nodeA2.dua.RestoreClientSession(snap, nil)
	require.NoError(t, err)
	require.Equal(t, d.ID, r.ID)
	require.Equal(t, sip.DialogStateEstablished, r.LoadState())

	require.NoError(t, r.Ack(ctx))
	select {
	case ack := <-acks:
		require.Equal(t, d.InviteRequest.CSeq().SeqNo, ack.CSeq().SeqNo, "ACK must carry the CSeq of the INVITE")
		require.NotNil(t, ack.Route(), "ACK lost the route set of the dialog")
	case <-ctx.Done():
		t.Fatal("the callee did not take the ACK of the restored session")
	}
	require.NoError(t, <-answered, "the callee gave up on its 2xx")
	require.Equal(t, sip.DialogStateConfirmed, ds.Load().LoadState())

	res, err := r.Do(ctx, sip.NewRequest(sip.INFO, r.InviteResponse.Contact().Address))
	require.NoError(t, err)
	require.Equal(t, sip.StatusOK, res.StatusCode)
	got := <-infoB
	require.NotNil(t, got, "the callee refused the request of the restored session")
	require.Equal(t, d.InviteRequest.CSeq().SeqNo+1, got.CSeq().SeqNo)

	require.NoError(t, r.Bye(ctx))
	<-byeB
	require.Equal(t, sip.DialogStateEnded, r.LoadState())
}

// A snapshot that cannot be a dialog is refused instead of giving a session
// that would send requests nobody can match.
func TestDialogRestoreRefusesBrokenSnapshot(t *testing.T) {
	ua, err := NewUA()
	require.NoError(t, err)
	t.Cleanup(func() { ua.Close() })
	cli, err := NewClient(ua)
	require.NoError(t, err)
	dua := &DialogUA{Client: cli}

	invite, _, _ := createTestInvite(t, "sip:uas@uas.com", "udp", "uas.com:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uac", Port: 1234}})
	tagged := invite.Clone()
	tagged.To().Params.Add("tag", "uas-tag")
	ok := sip.NewResponseFromRequest(tagged, sip.StatusOK, "OK", nil)
	other := sip.NewResponseFromRequest(invite, sip.StatusOK, "OK", nil)
	other.To().Params.Add("tag", "other-tag")
	bye := invite.Clone()
	bye.Method = sip.BYE

	for name, c := range map[string]struct {
		snap   DialogSnapshot
		server bool
	}{
		"no INVITE":                  {DialogSnapshot{State: sip.DialogStateConfirmed}, true},
		"not an INVITE":              {DialogSnapshot{InviteRequest: bye}, true},
		"ended":                      {DialogSnapshot{InviteRequest: tagged, InviteResponse: ok, State: sip.DialogStateEnded}, true},
		"server without our To tag":  {DialogSnapshot{InviteRequest: invite}, true},
		"response of another dialog": {DialogSnapshot{InviteRequest: tagged, InviteResponse: other, State: sip.DialogStateConfirmed}, true},
		"client established, no 2xx": {DialogSnapshot{InviteRequest: invite, State: sip.DialogStateConfirmed}, false},
	} {
		var err error
		if c.server {
			_, err = dua.RestoreServerSession(c.snap, nil)
		} else {
			_, err = dua.RestoreClientSession(c.snap, nil)
		}
		require.Error(t, err, name)
	}

	// an early client dialog has no ID yet, as after WriteInvite
	r, err := dua.RestoreClientSession(DialogSnapshot{InviteRequest: invite, CSeq: invite.CSeq().SeqNo}, nil)
	require.NoError(t, err)
	require.Empty(t, r.ID)
}

// The counters go on from the snapshot, each by its own rule: an in-dialog
// request takes the next local number (RFC 3261 12.2.1.1), and the peer's
// numbers are checked against the last one received (12.2.2).
func TestDialogRestoreCarriesCounters(t *testing.T) {
	ua, err := NewUA()
	require.NoError(t, err)
	t.Cleanup(func() { ua.Close() })
	cli, err := NewClient(ua)
	require.NoError(t, err)
	dua := &DialogUA{Client: cli, ContactHDR: sip.ContactHeader{Address: sip.Uri{User: "b2bua", Host: "127.0.0.200", Port: 5099}}}

	invite, _, _ := createTestInvite(t, "sip:uas@uas.com", "udp", "uas.com:5090")
	invite.AppendHeader(&sip.ContactHeader{Address: sip.Uri{Host: "uac", Port: 1234}})
	invite.To().Params.Add("tag", "uas-tag")
	res := sip.NewResponseFromRequest(invite, sip.StatusOK, "OK", nil)
	seq := invite.CSeq().SeqNo

	r, err := dua.RestoreServerSession(DialogSnapshot{
		InviteRequest: invite, InviteResponse: res, State: sip.DialogStateConfirmed,
		CSeq: seq + 7, RemoteCSeq: seq + 3,
	}, nil)
	require.NoError(t, err)

	// TransactionRequest with a cancelled context builds the request
	// without sending it
	req := sip.NewRequest(sip.INFO, sip.Uri{Host: "uac", Port: 1234})
	built, cancel := context.WithCancel(context.Background())
	cancel()
	_, _ = r.TransactionRequest(built, req)
	require.Equal(t, seq+8, req.CSeq().SeqNo)

	peer := func(n uint32) *sip.Request {
		p := sip.NewRequest(sip.INFO, sip.Uri{Host: "b2bua"})
		p.AppendHeader(&sip.CSeqHeader{SeqNo: n, MethodName: sip.INFO})
		return p
	}
	require.ErrorIs(t, r.ReadRequest(peer(seq+2), nil), ErrDialogInvalidCseq)
	require.NoError(t, r.ReadRequest(peer(seq+4), nil))

	// A UAC that has not heard from the peer yet has an empty remote number
	// (RFC 3261 12.1.2): the peer counts on its own, and its first request
	// may well be below our INVITE.
	tagged := invite.Clone()
	uac, err := dua.RestoreClientSession(DialogSnapshot{
		InviteRequest: tagged, InviteResponse: res, State: sip.DialogStateConfirmed,
		CSeq: seq,
	}, nil)
	require.NoError(t, err)
	require.NoError(t, uac.ReadRequest(peer(1), nil), "the peer's first request refused against our own INVITE number")
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portText)
	require.NoError(t, err)
	return host, port
}
