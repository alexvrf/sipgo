package sip

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rfc4475Parse is what the parser must do with one message of RFC 4475
// section 3. The expectation is the RFC's, not ours: "parse" where the RFC
// says the message is valid or that "a parser must not break/fail" on it
// (the element answers it — 505, 400, 416... — above the parser, see
// internal/engine/rfc4475_test.go), "reject" where it is syntactically
// invalid and rejecting is what the RFC allows. Where the RFC allows either
// ("an element could choose to be liberal"), the entry says which we chose.
type rfc4475Parse struct {
	section string
	parse   bool
	note    string
}

var rfc4475Parser = map[string]rfc4475Parse{
	// 3.1.1 Valid Messages
	"wsinv":      {"3.1.1.1", true, "short tortuous INVITE"},
	"intmeth":    {"3.1.1.2", true, "wide range of valid characters"},
	"esc01":      {"3.1.1.3", true, "valid use of % escaping"},
	"escnull":    {"3.1.1.4", true, "escaped nulls in URIs"},
	"esc02":      {"3.1.1.5", true, "% that is not an escape"},
	"lwsdisp":    {"3.1.1.6", true, "no LWS between display name and <"},
	"longreq":    {"3.1.1.7", true, "long values in header fields"},
	"dblreq":     {"3.1.1.8", true, "extra trailing octets in a UDP datagram"},
	"semiuri":    {"3.1.1.9", true, "semicolon-separated parameters in URI user part"},
	"transports": {"3.1.1.10", true, "varied and unknown transport types"},
	"mpart01":    {"3.1.1.11", true, "multipart MIME message"},
	"unreason":   {"3.1.1.12", true, "unusual reason phrase"},
	"noreason":   {"3.1.1.13", true, "empty reason phrase"},

	// 3.1.2 Invalid Messages
	"badinv01": {"3.1.2.1", false, "extraneous header field separators"},
	"clerr":    {"3.1.2.2", false, "Content-Length larger than the datagram"},
	"ncl":      {"3.1.2.3", false, "negative Content-Length"},
	"scalar02": {"3.1.2.4", false, "CSeq above 2**32-1"},
	"scalarlg": {"3.1.2.5", false, "response scalars out of range"},
	"quotbal":  {"3.1.2.6", false, "unterminated quoted string in display name"},
	"ltgtruri": {"3.1.2.7", false, "<> around Request-URI"},
	"lwsruri":  {"3.1.2.8", false, "LWS inside Request-URI"},
	"lwsstart": {"3.1.2.9", false, "multiple SP in Request-Line; rejecting is acceptable"},
	"trws":     {"3.1.2.10", false, "SP at end of Request-Line; rejecting is acceptable"},
	"escruri":  {"3.1.2.11", true, "liberal: escaped headers in Request-URI are accepted, never forwarded"},
	"baddate":  {"3.1.2.12", true, "liberal: non-GMT Date; the node does not use Date"},
	"regbadct": {"3.1.2.13", true, "liberal: unambiguous addr-spec with escaped header"},
	"badaspec": {"3.1.2.14", false, "spaces within addr-spec"},
	// rejected for its missing empty line after the headers (RFC 3261 7);
	// the unquoted display names themselves get quotes inferred, which
	// 3.1.2.15 allows as long as the error is not passed on
	"baddn":      {"3.1.2.15", false, "non-token characters in unquoted display name"},
	"badvers":    {"3.1.2.16", true, "syntactically valid, answered 505 above the parser"},
	"mismatch01": {"3.1.2.17", true, "start line and CSeq method mismatch, answered 400 above the parser"},
	"mismatch02": {"3.1.2.18", true, "unknown method with CSeq mismatch, answered 501 above the parser"},
	"bigcode":    {"3.1.2.19", false, "response code above 699"},

	// 3.2 Transaction Layer Semantics
	"badbranch": {"3.2.1", true, "parser must not break; RFC 2543 matching applies"},

	// 3.3 Application-Layer Semantics: "a parser must not fail" on each
	"insuf":    {"3.3.1", true, "missing Call-ID, From, To"},
	"unkscm":   {"3.3.2", true, "unknown Request-URI scheme"},
	"novelsc":  {"3.3.3", true, "known but atypical Request-URI scheme"},
	"unksm2":   {"3.3.4", true, "unknown schemes in header fields"},
	"bext01":   {"3.3.5", true, "Proxy-Require and Require"},
	"invut":    {"3.3.6", true, "unknown Content-Type"},
	"regaut01": {"3.3.7", true, "unknown authorization scheme"},
	"multi01":  {"3.3.8", true, "multiple values in single-value fields"},
	"mcl01":    {"3.3.9", true, "multiple Content-Length values"},
	"bcast":    {"3.3.10", true, "200 OK with broadcast Via"},
	"zeromf":   {"3.3.11", true, "Max-Forwards of zero"},
	"cparam01": {"3.3.12", true, "REGISTER with a contact header parameter"},
	"cparam02": {"3.3.13", true, "REGISTER with a url-parameter"},
	"regescrt": {"3.3.14", true, "REGISTER with a URL escaped header"},
	"sdp01":    {"3.3.15", true, "unacceptable Accept offering"},

	// 3.4 Backward Compatibility
	"inv2543": {"3.4.1", true, "RFC 2543 syntax"},
}

func readRFC4475(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "rfc4475", name+".dat"))
	require.NoError(t, err)
	return raw
}

// Every message of RFC 4475 section 3 is parsed the way the RFC says, and
// every accepted message is a fixed point of parse → build → parse: the
// node rebuilds what it accepts and passes it on, so accepting something it
// cannot write back makes it a source of broken signalling.
func TestRFC4475Parser(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "rfc4475", "*.dat"))
	require.NoError(t, err)
	sort.Strings(files)
	require.Len(t, files, len(rfc4475Parser), "every vector has an expectation and vice versa")

	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".dat")
		want, ok := rfc4475Parser[name]
		require.True(t, ok, "%s has no expectation", name)
		t.Run(want.section+"_"+name, func(t *testing.T) {
			msg, err := NewParser().ParseSIP(readRFC4475(t, name))
			if !want.parse {
				require.Error(t, err, "RFC 4475 %s (%s): must be rejected", want.section, want.note)
				return
			}
			require.NoError(t, err, "RFC 4475 %s (%s): must parse", want.section, want.note)

			once := msg.String()
			again, err := NewParser().ParseSIP([]byte(once))
			require.NoError(t, err, "rebuilt message does not parse:\n%s", once)
			require.Equal(t, once, again.String(), "parse → build is not a fixed point")
		})
	}
}

// What the valid messages of 3.1.1 carry must come out of the parser as the
// RFC reads them, not merely "without an error". Each check below is a
// defect that parsed without an error: a tag lost to the whitespace around
// "=", a method upper-cased away from its CSeq, an escape decoded into a
// raw NUL.
func TestRFC4475ValidContent(t *testing.T) {
	parse := func(t *testing.T, name string) Message {
		t.Helper()
		msg, err := NewParser().ParseSIP(readRFC4475(t, name))
		require.NoError(t, err)
		return msg
	}
	param := func(t *testing.T, p HeaderParams, key string) string {
		t.Helper()
		v, ok := p.Get(key)
		require.True(t, ok, "parameter %q missing in %v", key, p)
		return v
	}

	t.Run("3.1.1.1_wsinv", func(t *testing.T) {
		// SEMI = SWS ";" SWS, EQUAL = SWS "=" SWS, SLASH = SWS "/" SWS
		req := parse(t, "wsinv").(*Request)
		require.Equal(t, "1918181833n", param(t, req.To().Params, "tag"))
		require.Equal(t, "98asjd8", param(t, req.From().Params, "tag"))
		require.EqualValues(t, 68, *req.MaxForwards())
		require.EqualValues(t, 9, req.CSeq().SeqNo)
		require.Equal(t, INVITE, req.CSeq().MethodName)

		vias := req.GetHeaders("Via")
		require.Len(t, vias, 3)
		for i, want := range []struct{ transport, host, branch string }{
			{"UDP", "192.0.2.2", "390skdjuw"},
			{"TCP", "spindle.example.com", "z9hG4bK9ikj8"},
			{"UDP", "192.168.255.111", "z9hG4bK30239"},
		} {
			via := vias[i].(*ViaHeader)
			require.Equal(t, want.transport, via.Transport, "Via %d", i)
			require.Equal(t, want.host, via.Host, "Via %d", i)
			require.Equal(t, want.branch, param(t, via.Params, "branch"), "Via %d", i)
		}

		contact := req.Contact()
		require.Equal(t, "newvalue", param(t, contact.Params, "newparam"))
		require.Equal(t, "0.33", param(t, contact.Params, "q"))
		_, ok := contact.Params.Get("secondparam")
		require.True(t, ok)
		require.Len(t, req.Body(), 150)
	})

	t.Run("3.1.1.2_intmeth", func(t *testing.T) {
		// INVITEm = %x49.4E.56.49.54.45 (RFC 3261 25.1): methods are
		// case-sensitive, and CSeq repeats the method as it is (20.16)
		req := parse(t, "intmeth").(*Request)
		const method = "!interesting-Method0123456789_*+`.%indeed'~"
		require.Equal(t, RequestMethod(method), req.Method)
		require.Equal(t, RequestMethod(method), req.CSeq().MethodName)
	})

	t.Run("3.1.1.3_esc01", func(t *testing.T) {
		// the escaped user is a user, not a sips: URI inside a URI
		req := parse(t, "esc01").(*Request)
		require.Equal(t, "example.net", req.Recipient.Host)
		require.True(t, strings.HasPrefix(req.String(), "INVITE sip:sips%3Auser%40example.com@example.net SIP/2.0\r\n"))
	})

	t.Run("3.1.1.4_escnull", func(t *testing.T) {
		// %00 stays an escape: written back raw, it would end the header
		// for a C parser on the far side
		built := parse(t, "escnull").String()
		require.NotContains(t, built, "\x00")
		require.Contains(t, built, "sip:null-%00-null@example.com")
		require.Contains(t, built, "sip:%00%00@host5.example.com")
	})

	t.Run("3.1.1.5_esc02", func(t *testing.T) {
		// "RE%47IST%45R" is an extension method, not REGISTER
		req := parse(t, "esc02").(*Request)
		require.Equal(t, RequestMethod("RE%47IST%45R"), req.Method)
		require.NotEqual(t, REGISTER, req.Method)
	})

	t.Run("3.1.1.6_lwsdisp", func(t *testing.T) {
		req := parse(t, "lwsdisp").(*Request)
		require.Equal(t, "caller", req.From().DisplayName)
	})

	t.Run("3.1.1.7_longreq", func(t *testing.T) {
		raw := readRFC4475(t, "longreq")
		req := parse(t, "longreq").(*Request)
		require.Len(t, req.Body(), 150)
		// the longest header survives intact
		h := req.GetHeader("Unknown-LongLongLongLongLongLongLongLongLongLongLongLongLongLongLongLongLongLongLongLong-Name")
		require.NotNil(t, h)
		require.True(t, bytes.Contains(raw, []byte(h.Value())))
	})

	t.Run("3.1.1.8_dblreq", func(t *testing.T) {
		// RFC 3261 18.3: octets after Content-Length in a datagram are
		// discarded — the INVITE glued behind the REGISTER is not parsed
		req := parse(t, "dblreq").(*Request)
		require.Equal(t, REGISTER, req.Method)
		require.Empty(t, req.Body())
		require.NotContains(t, req.String(), "INVITE")
	})

	t.Run("3.1.1.9_semiuri", func(t *testing.T) {
		req := parse(t, "semiuri").(*Request)
		require.Equal(t, "user;par=u%40example.net", req.Recipient.User)
		require.Equal(t, "example.com", req.Recipient.Host)
		require.Zero(t, req.Recipient.UriParams.Length())
	})

	t.Run("3.1.1.10_transports", func(t *testing.T) {
		req := parse(t, "transports").(*Request)
		var got []string
		for _, h := range req.GetHeaders("Via") {
			got = append(got, h.(*ViaHeader).Transport)
		}
		require.Equal(t, []string{"UDP", "SCTP", "TLS", "UNKNOWN", "TCP"}, got)
	})

	t.Run("3.1.1.11_mpart01", func(t *testing.T) {
		raw := readRFC4475(t, "mpart01")
		req := parse(t, "mpart01").(*Request)
		require.Len(t, req.Body(), 553)
		require.True(t, bytes.HasSuffix(raw, req.Body()), "binary body changed")
	})

	t.Run("3.1.1.12_unreason", func(t *testing.T) {
		res := parse(t, "unreason").(*Response)
		require.Equal(t, 200, res.StatusCode)
		require.Equal(t, "= 2**3 * 5**2 но сто девяносто девять - простое", res.Reason)
	})

	t.Run("3.1.1.13_noreason", func(t *testing.T) {
		// Status-Line = SIP-Version SP Status-Code SP Reason-Phrase CRLF:
		// the second SP stays even when the phrase is empty
		res := parse(t, "noreason").(*Response)
		require.Equal(t, 100, res.StatusCode)
		require.Empty(t, res.Reason)
		require.True(t, strings.HasPrefix(res.String(), "SIP/2.0 100 \r\n"))
	})
}

// ParseAddressValue is exported and gets the value as the caller has it, not
// only as the header parser trimmed it: the whitespace around an addr-spec
// belongs to HCOLON and SEMI (RFC 3261 25.1), with or without parameters
// after it.
func TestParseAddressValueTrimsLWS(t *testing.T) {
	for _, value := range []string{" sip:a@b.example ", "sip:a@b.example \t;tag=1"} {
		var uri Uri
		params := NewParams()
		_, err := ParseAddressValue(value, &uri, &params)
		require.NoError(t, err, "%q", value)
		require.Equal(t, "b.example", uri.Host, "%q", value)
	}
}
