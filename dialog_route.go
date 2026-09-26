package sipgo

import (
	"fmt"

	"github.com/emiago/sipgo/sip"
)

// applyStrictRoute rewrites a request within a dialog whose route set
// starts with a strict router — a URI without the lr parameter
// (RFC 2543 routing). RFC 3261 12.2.1.1:
//
//	If the route set is not empty, and its first URI does not contain the lr
//	parameter, the UAC MUST place the first URI from the route set into the
//	Request-URI, stripping any parameters that are not allowed in a
//	Request-URI. The UAC MUST add a Route header field containing the
//	remainder of the route set values in order, including all parameters.
//	The UAC MUST then place the remote target URI into the Route header
//	field as the last value.
//
// The request is sent to the new Request-URI: that is the strict router,
// and the top Route now names the hop after it.
func applyStrictRoute(req *sip.Request) {
	first := req.Route()
	if first == nil || first.Address.UriParams.Has("lr") {
		return
	}
	target := req.Recipient
	next := first.Address.Clone()
	// 19.1.1: method and headers are not allowed in a Request-URI
	if next.UriParams != nil {
		next.UriParams.Remove("method")
	}
	next.Headers = nil
	req.Recipient = *next
	req.RemoveHeader("Route")
	req.AppendHeader(sip.NewHeader("Route", "<"+target.String()+">"))

	port := next.Port
	if port == 0 {
		port = sip.DefaultPort(req.Transport())
	}
	req.SetDestination(fmt.Sprintf("%s:%d", next.Host, port))
}
