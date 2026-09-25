package sipgo

import (
	"crypto/tls"
	"net"

	"github.com/emiago/sipgo/sip"
)

type UserAgent struct {
	name        string
	hostname    string
	dnsResolver *net.Resolver
	tlsConfig   *tls.Config
	parser      *sip.Parser
	txOptions   []sip.TransactionLayerOption
	tpOptions   []sip.TransportLayerOption
	tp          *sip.TransportLayer
	tx          *sip.TransactionLayer

	// header gives the User-Agent / Server header value
	// (WithUserAgentHeader); nil adds nothing.
	header func() string
}

type UserAgentOption func(s *UserAgent) error

// WithUserAgent changes user agent name
// Default: sipgo
func WithUserAgent(ua string) UserAgentOption {
	return func(s *UserAgent) error {
		s.name = ua
		return nil
	}
}

// WithUserAgentHeader sets the value of the User-Agent header field for
// requests the user agent sends (RFC 3261 20.41) and of the Server header
// field for its responses (RFC 3261 20.35). Both describe the software of the
// UA, the first as a UAC and the second as a UAS, so one value serves both.
//
// It is applied where messages leave: the client's send paths, whatever
// options built the request, and every response of a server transaction,
// including those the transaction layer builds on its own (100 Trying, 487
// and 200 on CANCEL). An ACK to a non-2xx response and a CANCEL copy the
// value of their INVITE.
//
// The value is asked for on every message, so it may change at run time. An
// empty value adds nothing, and a header the message already has is kept.
// Both sections say implementers SHOULD make the header configurable, since
// a software version helps an attacker; the UA name (WithUserAgent) is used
// elsewhere and is not sent in these headers by default.
func WithUserAgentHeader(value func() string) UserAgentOption {
	return func(s *UserAgent) error {
		s.header = value
		return nil
	}
}

// WithUserAgentHostname represents FQDN of user that can be presented in From header
func WithUserAgentHostname(hostname string) UserAgentOption {
	return func(s *UserAgent) error {
		s.hostname = hostname
		return nil
	}
}

// WithUserAgentDNSResolver allows customizing default DNS resolver for transport layer
func WithUserAgentDNSResolver(r *net.Resolver) UserAgentOption {
	return func(s *UserAgent) error {
		s.dnsResolver = r
		return nil
	}
}

// WithUserAgenTLSConfig allows customizing default tls config.
func WithUserAgenTLSConfig(c *tls.Config) UserAgentOption {
	return func(s *UserAgent) error {
		s.tlsConfig = c
		return nil
	}
}

// WithUserAgentParser allows removing default behavior of parser
// You can define and remove default headers parser map and pass here.
// Only use if your benchmarks are better than default
func WithUserAgentParser(p *sip.Parser) UserAgentOption {
	return func(s *UserAgent) error {
		s.parser = p
		return nil
	}
}

// WithUserAgentTransactionLayerOptions allows setting options for the transaction layer
func WithUserAgentTransactionLayerOptions(o ...sip.TransactionLayerOption) UserAgentOption {
	return func(s *UserAgent) error {
		s.txOptions = o
		return nil
	}
}

// WithUserAgentTransportLayerOptions allows setting options for the transport layer
func WithUserAgentTransportLayerOptions(o ...sip.TransportLayerOption) UserAgentOption {
	return func(s *UserAgent) error {
		s.tpOptions = o
		return nil
	}
}

// NewUA creates User Agent
// User Agent will create transport and transaction layer
// Check options for customizing user agent
func NewUA(options ...UserAgentOption) (*UserAgent, error) {
	ua := &UserAgent{
		name:        "sipgo",
		hostname:    "localhost",
		dnsResolver: net.DefaultResolver,
		parser:      sip.NewParser(),
	}

	for _, o := range options {
		if err := o(ua); err != nil {
			return nil, err
		}
	}

	ua.tp = sip.NewTransportLayer(ua.dnsResolver, ua.parser, ua.tlsConfig, ua.tpOptions...)
	txOptions := ua.txOptions
	if ua.header != nil {
		txOptions = append(txOptions[:len(txOptions):len(txOptions)],
			sip.WithTransactionLayerServerHeader(ua.header))
	}
	ua.tx = sip.NewTransactionLayer(ua.tp, txOptions...)
	return ua, nil
}

func (ua *UserAgent) Close() error {
	// stop transaction layer
	ua.tx.Close()

	// stop transport layer
	return ua.tp.Close()
}

func (ua *UserAgent) Name() string {
	return ua.name
}

func (ua *UserAgent) Hostname() string {
	return ua.hostname
}

func (ua *UserAgent) TransportLayer() *sip.TransportLayer {
	return ua.tp
}

func (ua *UserAgent) TransactionLayer() *sip.TransactionLayer {
	return ua.tx
}
