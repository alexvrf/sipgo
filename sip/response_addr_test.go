package sip

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// ResponseAddr — where a response to a network request goes (RFC 3261
// 18.2.2, RFC 3581 4): the packet source address with the Via port, the
// source port only when the Via carries an empty rport, and the default
// port of the transport when the Via names none. The server transport and a
// UA answering from a rebuilt transaction use this one rule — a copy of it
// would drift.
func TestResponseAddr(t *testing.T) {
	for _, c := range []struct {
		name, via, want string
	}{
		{"via port, no rport", "SIP/2.0/UDP 198.51.100.7:5070;branch=z9hG4bK-1", "192.0.2.1:5070"},
		{"empty rport", "SIP/2.0/UDP 198.51.100.7:5070;rport;branch=z9hG4bK-1", "192.0.2.1:40000"},
		{"no via port", "SIP/2.0/UDP 198.51.100.7;branch=z9hG4bK-1", "192.0.2.1:5060"},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := "INVITE sip:bob@192.0.2.9 SIP/2.0\r\n" +
				"Via: " + c.via + "\r\n" +
				"From: <sip:alice@198.51.100.7>;tag=a\r\n" +
				"To: <sip:bob@192.0.2.9>\r\n" +
				"Call-ID: response-addr\r\n" +
				"CSeq: 1 INVITE\r\n" +
				"Content-Length: 0\r\n\r\n"
			msg, err := ParseMessage([]byte(raw))
			require.NoError(t, err)
			req := msg.(*Request)
			req.SetSource("192.0.2.1:40000")
			got, err := ResponseAddr(req, "udp")
			require.NoError(t, err)
			require.Equal(t, c.want, got.String())
		})
	}
}

// The transport resolves its responses with ResponseAddr itself, not with a
// copy of the rule: a UA that answers from a rebuilt transaction calls the
// same function, and a copy left in serverRequestConnection would let the
// two drift apart unnoticed — their behaviour is the same until one of them
// changes.
func TestServerRequestConnectionUsesResponseAddr(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "transport_layer.go", nil, 0)
	require.NoError(t, err)
	var fn *ast.FuncDecl
	for _, d := range f.Decls {
		if d, ok := d.(*ast.FuncDecl); ok && d.Name.Name == "serverRequestConnection" && d.Recv != nil {
			fn = d
		}
	}
	require.NotNil(t, fn, "serverRequestConnection not found")
	calls := 0
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok {
			if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "ResponseAddr" {
				calls++
			}
		}
		return true
	})
	require.Equal(t, 1, calls, "serverRequestConnection must resolve the response address with ResponseAddr")
}
