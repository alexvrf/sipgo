package sip

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A request in RFC 2543 syntax may carry no From tag, and then the tag is
// null: "A UAS MUST be prepared to receive a request without a tag in the
// From field, in which case the tag is considered to have a value of null.
// This is to maintain backwards compatibility with RFC 2543" (RFC 3261
// 12.1.1; the same for a UAC and the To tag of a response, 12.1.2). Such a
// request matches its transaction by the procedure of 17.2.3, and a null
// tag there matches a null tag. Both refused before, so every RFC 2543
// INVITE (RFC 4475 3.4.1) was answered 400.
func TestRFC2543NullTags(t *testing.T) {
	parse := func(t *testing.T, raw string) Message {
		t.Helper()
		msg, err := NewParser().ParseSIP([]byte(raw))
		require.NoError(t, err)
		return msg
	}
	const invite = "INVITE sip:UserB@example.com SIP/2.0\r\n" +
		"Via: SIP/2.0/UDP iftgw.example.com\r\n" +
		"From: <sip:+13035551111@ift.client.example.net;user=phone>\r\n" +
		"To: sip:+16505552222@ss1.example.net;user=phone\r\n" +
		"Call-ID: inv2543.1717@ift.client.example.com\r\n" +
		"CSeq: 56 INVITE\r\n" +
		"Contact: <sip:+13035551111@192.0.2.5>\r\n\r\n"

	t.Run("transaction key without From tag", func(t *testing.T) {
		req := parse(t, invite).(*Request)
		key, err := ServerTxKeyMake(req)
		require.NoError(t, err)
		// the ACK of the same transaction (17.2.3: matched as INVITE)
		ack := parse(t, strings.Replace(invite, "CSeq: 56 INVITE", "CSeq: 56 ACK", 1)).(*Request)
		ackKey, err := ServerTxKeyMake(ack)
		require.NoError(t, err)
		require.Equal(t, key, ackKey)
	})

	t.Run("dialog of a request without From tag", func(t *testing.T) {
		req := parse(t, strings.Replace(invite, "user=phone\r\nCall-ID", "user=phone;tag=local\r\nCall-ID", 1)).(*Request)
		id, err := DialogIDFromRequestUAS(req)
		require.NoError(t, err)
		require.Equal(t, DialogIDMake("inv2543.1717@ift.client.example.com", "local", ""), id)
	})

	t.Run("dialog of a response without To tag", func(t *testing.T) {
		res := parse(t, "SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP 192.0.2.1;branch=z9hG4bK1\r\n"+
			"From: <sip:a@example.com>;tag=local\r\nTo: <sip:b@example.com>\r\n"+
			"Call-ID: c1\r\nCSeq: 1 INVITE\r\nContent-Length: 0\r\n\r\n").(*Response)
		id, err := DialogIDFromResponse(res)
		require.NoError(t, err)
		require.Equal(t, DialogIDMake("c1", "", "local"), id)
	})

	t.Run("the tag this side sets is still required", func(t *testing.T) {
		// To of a request received as UAS carries our tag once in a dialog
		_, err := DialogIDFromRequestUAS(parse(t, invite).(*Request))
		require.Error(t, err)
		// From of a response is ours
		_, err = DialogIDFromResponse(parse(t, "SIP/2.0 200 OK\r\nVia: SIP/2.0/UDP 192.0.2.1;branch=z9hG4bK1\r\n"+
			"From: <sip:a@example.com>\r\nTo: <sip:b@example.com>;tag=remote\r\n"+
			"Call-ID: c1\r\nCSeq: 1 INVITE\r\nContent-Length: 0\r\n\r\n").(*Response))
		require.Error(t, err)
	})
}
