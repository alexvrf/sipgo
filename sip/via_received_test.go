package sip

import "testing"

// RFC 3261 18.2.1: «If the host portion of the "sent-by" parameter contains
// a domain name, or if it contains an IP address that differs from the
// packet source address, the server MUST add a "received" parameter to that
// Via header field value». Upstream added it only together with rport
// (RFC 3581), and a request from behind NAT without rport got a response
// with no trace of where it had actually come from.
func TestResponseViaReceived(t *testing.T) {
	cases := []struct {
		name, sentBy, source, want string
	}{
		{"IP differs from source", "192.0.2.10", "198.51.100.7:5060", "198.51.100.7"},
		{"domain name", "pbx.example.com", "198.51.100.7:5060", "198.51.100.7"},
		{"same IP", "198.51.100.7", "198.51.100.7:5060", ""},
		{"not from the network", "192.0.2.10", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := NewRequest(OPTIONS, Uri{Host: "203.0.113.1"})
			via := &ViaHeader{ProtocolName: "SIP", ProtocolVersion: "2.0", Transport: "UDP",
				Host: c.sentBy, Port: 5060, Params: NewParams()}
			via.Params.Add("branch", "z9hG4bK-1")
			req.AppendHeader(via)
			if c.source != "" {
				req.SetSource(c.source)
			}
			res := NewResponseFromRequest(req, StatusOK, "OK", nil)
			got, ok := res.Via().Params.Get("received")
			if c.want == "" {
				if ok {
					t.Fatalf("received=%q, ожидалось без него", got)
				}
				return
			}
			if got != c.want {
				t.Fatalf("received=%q, ожидалось %q", got, c.want)
			}
		})
	}
}
