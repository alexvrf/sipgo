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
//
// The route set is read value by value, not line by line: RFC 3261 7.3.1
// allows several values in one Route line, and a line built by the
// application is not split by the parser. Removing the first line would
// then drop the remainder of the route set along with the strict router.
//
// It reports whether the request was rewritten, so the caller does not
// decide by the top Route on its own: that one would miss a combined line.
func applyStrictRoute(req *sip.Request) bool {
	set := routeSet(req)
	if len(set) == 0 || set[0].UriParams.Has("lr") {
		return false
	}
	target := req.Recipient
	next := set[0].Clone()
	// 19.1.1: method and headers are not allowed in a Request-URI
	if next.UriParams != nil {
		next.UriParams.Remove("method")
	}
	next.Headers = nil
	req.Recipient = *next
	for req.RemoveHeader("Route") {
	}
	for _, hop := range set[1:] {
		req.AppendHeader(&sip.RouteHeader{Address: hop})
	}
	req.AppendHeader(sip.NewHeader("Route", "<"+target.String()+">"))

	port := next.Port
	if port == 0 {
		port = sip.DefaultPort(req.Transport())
	}
	req.SetDestination(fmt.Sprintf("%s:%d", next.Host, port))
	return true
}

// routeSet returns the Route values of req in order, one per value.
// A line that does not parse leaves the set as it is: rewriting a route
// set we could not read would send the request somewhere else.
func routeSet(req *sip.Request) []sip.Uri {
	var set []sip.Uri
	for _, h := range req.GetHeaders("Route") {
		parsed, err := sip.HeadersParser(sip.DefaultHeadersParser()).ParseHeader(nil, []byte("Route: "+h.Value()))
		if err != nil {
			return nil
		}
		for _, p := range parsed {
			route, ok := p.(*sip.RouteHeader)
			if !ok {
				return nil
			}
			set = append(set, route.Address)
		}
	}
	return set
}
