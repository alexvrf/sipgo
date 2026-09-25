package sip

import (
	"net"
	"strings"
)

// badRequestForUnparsed builds the stateless 400 for a request datagram the
// parser rejected, or reports that none can be built.
//
// RFC 3261 wants the answer: a request whose body is shorter than its
// Content-Length "SHOULD generate a 400 (Bad Request) response" (18.3), and
// RFC 4475 3.1.2 asks the same of the other malformed requests. Dropping
// them silently leaves the sender retransmitting to Timer F without ever
// learning what is wrong.
//
// A response has to repeat Via, From, To, Call-ID and CSeq of the request
// (8.2.6.2), so each is parsed on its own from the header lines. The
// response is built from the parsed values, which makes it well-formed by
// construction: echoing an unparsed value back would make the node itself
// the sender of a malformed message. When any of the five does not parse —
// an unreadable Via (RFC 4475 3.1.2.1), an unterminated display name
// (3.1.2.6), a CSeq above 2**32-1 (3.1.2.4) — nothing is built: there is no
// response that could both be valid and match the request. Responses are
// never answered (3.1.2.5, 3.1.2.19), and neither is ACK (17).
func (p *Parser) badRequestForUnparsed(data []byte, cause error, server func() string) (*Response, bool) {
	startLine, n, err := nextLine(data)
	if err != nil {
		return nil, false
	}
	fields := strings.Fields(string(startLine))
	if len(fields) < 2 || strings.HasPrefix(strings.ToUpper(fields[0]), "SIP/") ||
		!strings.HasPrefix(strings.ToUpper(fields[len(fields)-1]), "SIP/") || fields[0] == string(ACK) {
		return nil, false
	}

	var (
		vias   []Header
		from   *FromHeader
		to     *ToHeader
		callID *CallIDHeader
		cseq   *CSeqHeader
		broken bool
	)
	for rest := data[n:]; ; {
		line, ln, err := nextLine(rest)
		if err != nil || len(line) == 0 {
			break
		}
		if ln < len(rest) && isHeaderContinuation(rest[ln]) {
			if line, ln, err = foldHeaderLine(line, ln, rest); err != nil {
				break
			}
		}
		rest = rest[ln:]

		parsed, err := p.headersParsers.ParseHeader(nil, line)
		for _, h := range parsed {
			switch h := h.(type) {
			case *ViaHeader:
				vias = append(vias, h)
			case *FromHeader:
				if from == nil {
					from = h
				}
			case *ToHeader:
				if to == nil {
					to = h
				}
			case *CallIDHeader:
				if callID == nil {
					callID = h
				}
			case *CSeqHeader:
				if cseq == nil {
					cseq = h
				}
			}
		}
		if err != nil && isTransactionHeader(line) {
			broken = true
		}
	}
	if broken || len(vias) == 0 || from == nil || to == nil || callID == nil || cseq == nil {
		return nil, false
	}

	res := NewResponse(StatusBadRequest, badRequestReason(cause))
	for _, via := range vias {
		res.AppendHeader(via)
	}
	res.AppendHeader(from)
	to = to.headerClone().(*ToHeader)
	if _, ok := to.Params.Get("tag"); !ok {
		// 8.2.6.2: "the UAS MUST add a tag to the To header field in the
		// response"
		if to.Params == nil {
			to.Params = NewParams()
		}
		to.Params.Add("tag", GenerateTagN(16))
	}
	res.AppendHeader(to)
	res.AppendHeader(callID)
	res.AppendHeader(cseq)
	if server != nil {
		if v := server(); v != "" {
			res.AppendHeader(NewHeader("Server", v))
		}
	}
	res.SetBody(nil)
	return res, true
}

// isTransactionHeader tells whether a header line carries one of the fields
// a response has to repeat (RFC 3261 8.2.6.2), compact forms included.
func isTransactionHeader(line []byte) bool {
	name, _, ok := strings.Cut(string(line), ":")
	if !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "via", "v", "from", "f", "to", "t", "call-id", "i", "cseq":
		return true
	}
	return false
}

// badRequestReason names the problem in the reason phrase: "The reason
// phrase SHOULD identify the syntax problem in more detail" (RFC 3261
// 21.4.1). The parser's error may quote the offending input, so only the
// characters Reason-Phrase allows unescaped are kept (25.1: reserved,
// unreserved, SP) — never CR or LF — and the length is bounded.
func badRequestReason(cause error) string {
	const prefix, max = "Bad Request: ", 96
	if cause == nil {
		return "Bad Request"
	}
	var b strings.Builder
	for _, c := range cause.Error() {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			strings.ContainsRune(" -_.!~*()';/?:@&=+$,", c):
			b.WriteRune(c)
		default:
			b.WriteByte(' ')
		}
		if b.Len() >= max {
			break
		}
	}
	detail := strings.Join(strings.Fields(b.String()), " ")
	if detail == "" {
		return "Bad Request"
	}
	return prefix + detail
}

// answerUnparsed sends the stateless 400 for a datagram the parser rejected,
// when the layer was told to (WithTransportLayerUnparsedRequest) and the
// sender is one it answers. The response goes back to the datagram's source:
// Via is what normally addresses it (RFC 3261 18.2.2), but in a message
// that failed to parse the source is the only address known to be real —
// the same one the transport answers by default (RFC 3581).
func (t *TransportUDP) answerUnparsed(conn net.PacketConn, raddr net.Addr, data []byte, cause error) {
	if t.unparsed == nil || !t.unparsed(raddr.String()) {
		return
	}
	res, ok := t.parser.badRequestForUnparsed(data, cause, t.serverHeader)
	if !ok {
		return
	}
	if _, err := conn.WriteTo([]byte(res.String()), raddr); err != nil {
		t.log.Error("stateless 400 for unparsed request not sent", "raddr", raddr.String(), "error", err)
	}
}
