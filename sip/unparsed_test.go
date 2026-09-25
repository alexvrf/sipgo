package sip

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func readUnparsedVector(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "rfc4475", name+".dat"))
	require.NoError(t, err)
	return raw
}

// A request the parser rejected gets a stateless 400 built from its own
// Via, From, To, Call-ID and CSeq when each of them parses (RFC 3261 18.3,
// 8.2.6.2, RFC 4475 3.1.2), and nothing when one does not: a response that
// repeated an unparsed field would be as malformed as the request.
func TestRFC4475BadRequestForUnparsed(t *testing.T) {
	answered := map[string]bool{
		"clerr": true, "ncl": true, "ltgtruri": true, "lwsruri": true,
		"lwsstart": true, "trws": true, "baddn": true,
		"badinv01": false, // Via values do not parse: nowhere to address it
		"quotbal":  false, // To does not parse
		"badaspec": false, // To does not parse
		"scalar02": false, // CSeq above 2**32-1
		"scalarlg": false, // a response: discarded (3.1.2.5)
		"bigcode":  false, // a response: dropped (3.1.2.19)
	}
	for name, want := range answered {
		t.Run(name, func(t *testing.T) {
			raw := readUnparsedVector(t, name)
			_, cause := NewParser().ParseSIP(raw)
			require.Error(t, cause)

			res, ok := NewParser().badRequestForUnparsed(raw, cause, func() string { return "settaswitch" })
			require.Equal(t, want, ok)
			if !ok {
				return
			}
			// well-formed by construction: our own parser reads it back
			back, err := NewParser().ParseSIP([]byte(res.String()))
			require.NoError(t, err, res.String())
			got := back.(*Response)
			require.Equal(t, StatusBadRequest, got.StatusCode)
			require.NotContains(t, got.Reason, "\r")
			require.NotContains(t, got.Reason, "\n")
			require.True(t, strings.HasPrefix(got.Reason, "Bad Request"), got.Reason)
			_, tagged := got.To().Params.Get("tag")
			require.True(t, tagged, "8.2.6.2: To of the response carries a tag")
			require.NotNil(t, got.CallID())
			require.NotNil(t, got.Via())
			require.Equal(t, "settaswitch", got.GetHeader("Server").Value())
			for _, h := range []string{"Call-ID", "CSeq"} {
				require.Contains(t, string(raw), strings.SplitN(got.GetHeader(h).Value(), " ", 2)[0],
					"%s is not the request's", h)
			}
		})
	}

	t.Run("ACK is never answered", func(t *testing.T) {
		raw := []byte("ACK <sip:user@example.com> SIP/2.0\r\nVia: SIP/2.0/UDP 192.0.2.1;branch=z9hG4bK1\r\n" +
			"From: <sip:a@example.com>;tag=1\r\nTo: <sip:user@example.com>;tag=2\r\n" +
			"Call-ID: ack-1\r\nCSeq: 1 ACK\r\nContent-Length: 0\r\n\r\n")
		_, cause := NewParser().ParseSIP(raw)
		require.Error(t, cause)
		_, ok := NewParser().badRequestForUnparsed(raw, cause, nil)
		require.False(t, ok)
	})
}

// The UDP transport answers an unparsed request only when the option is
// set and the sender is allowed, and it answers the datagram's source.
func TestTransportUDPAnswersUnparsedRequest(t *testing.T) {
	raw := readUnparsedVector(t, "ltgtruri") // <> around the Request-URI (RFC 4475 3.1.2.7)
	for _, tc := range []struct {
		name string
		opts []TransportLayerOption
		want bool
	}{
		{"allowed sender", []TransportLayerOption{WithTransportLayerUnparsedRequest(
			func(string) bool { return true }, func() string { return "sipgo" })}, true},
		{"refused sender", []TransportLayerOption{WithTransportLayerUnparsedRequest(
			func(string) bool { return false }, nil)}, false},
		{"option not set", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.ListenPacket("udp", "127.0.0.1:0")
			require.NoError(t, err)
			peer, err := net.ListenPacket("udp", "127.0.0.1:0")
			require.NoError(t, err)
			t.Cleanup(func() { _ = peer.Close() })

			tp := NewTransportLayer(net.DefaultResolver, NewParser(), nil, tc.opts...)
			t.Cleanup(func() { _ = tp.Close(); _ = listener.Close() })
			go func() { _ = tp.ServeUDP(listener) }()

			_, err = peer.WriteTo(raw, listener.LocalAddr())
			require.NoError(t, err)
			buf := make([]byte, 4096)
			require.NoError(t, peer.SetReadDeadline(time.Now().Add(500*time.Millisecond)))
			n, _, err := peer.ReadFrom(buf)
			if !tc.want {
				require.Error(t, err, "answered: %s", buf[:n])
				return
			}
			require.NoError(t, err)
			msg, err := NewParser().ParseSIP(buf[:n])
			require.NoError(t, err)
			res := msg.(*Response)
			require.Equal(t, StatusBadRequest, res.StatusCode)
			require.Equal(t, "ltgtruri.1@192.0.2.5", string(*res.CallID()))
			require.Equal(t, "sipgo", res.GetHeader("Server").Value())
		})
	}
}
