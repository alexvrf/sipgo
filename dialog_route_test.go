package sipgo

import (
	"testing"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

// RFC 3261 12.2.1.1, strict router at the top of the route set: its URI
// becomes the Request-URI, it leaves the Route list, the remote target goes
// last, and the request is sent to the strict router. Upstream only
// replaced the Request-URI, so the remote target was lost and the strict
// router was named twice.
func TestApplyStrictRoute(t *testing.T) {
	target := sip.Uri{Scheme: "sip", User: "bob", Host: "192.0.2.20", Port: 5070}
	req := sip.NewRequest(sip.BYE, target)
	req.AppendHeader(sip.NewHeader("Route", "<sip:p1.example.com;transport=udp>"))
	req.AppendHeader(sip.NewHeader("Route", "<sip:p2.example.com;lr>"))

	applyStrictRoute(req)

	require.Equal(t, "p1.example.com", req.Recipient.Host)
	routes := req.GetHeaders("Route")
	require.Len(t, routes, 2)
	require.Contains(t, routes[0].Value(), "p2.example.com")
	require.Contains(t, routes[1].Value(), "sip:bob@192.0.2.20:5070")
	// the parsed top Route follows the removal (headers.unref): a stale
	// cache would still name the strict router
	require.Contains(t, req.Route().Value(), "p2.example.com")
	require.Equal(t, "p1.example.com:5060", req.Destination())
}

// A loose router (lr) leaves the request as it is.
func TestApplyStrictRouteLeavesLooseRoute(t *testing.T) {
	target := sip.Uri{Scheme: "sip", User: "bob", Host: "192.0.2.20", Port: 5070}
	req := sip.NewRequest(sip.BYE, target)
	req.AppendHeader(sip.NewHeader("Route", "<sip:p1.example.com;lr>"))

	applyStrictRoute(req)

	require.Equal(t, "192.0.2.20", req.Recipient.Host)
	require.Len(t, req.GetHeaders("Route"), 1)
}
