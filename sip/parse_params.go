package sip

import (
	"strings"
	"unicode"
)

const (
	paramsStateNone = iota
	paramsStateKey
	paramsStateEqual
	paramsStateValue
	paramsStateQuote
)

func UnmarshalHeaderParams(s string, seperator rune, ending rune, p *HeaderParams) (n int, err error) {
	var start, sep, quote int = 0, 0, -1
	state := paramsStateKey

	s = strings.TrimLeftFunc(s, unicode.IsSpace) // Remove trailing spaces
	n = len(s)
	for i, c := range s {
		if c == ending {
			n = i
			break
		}

		switch state {
		case paramsStateKey:
			sep = 0
			start = i
			state = paramsStateEqual

		case paramsStateEqual:
			if c == seperator {
				// Add support for empty values
				addParam(p, s[start:i], "")
				state = paramsStateKey
				continue
			}

			if c != '=' {
				continue
			}

			sep = i
			state = paramsStateValue

		case paramsStateValue:
			switch c {
			case '"':
				state = paramsStateQuote
				quote = i
			case seperator:
				addParam(p, s[start:sep], s[sep+1:i])
				start = sep + 1
				state = paramsStateKey
			}
		case paramsStateQuote:
			if c != '"' {
				//End quoute
				continue
			}
			p.Add(s[start:], s[quote+1:i])
			state = paramsStateKey
		}
	}

	// Do the last one
	if sep > 0 && n >= 0 && (start < sep) {
		addParam(p, s[start:sep], s[sep+1:n])
	}
	// No seperator
	if sep == 0 && start < n && n >= 0 {
		addParam(p, s[start:], "")
	}

	return n, nil
}

// addParam adds a parameter without the whitespace around its name and
// value. It belongs to the separators, not to the parameter: SEMI is
// `SWS ";" SWS` and EQUAL is `SWS "=" SWS` (RFC 3261 25.1). Otherwise
// `;tag = 1918181833n` became a parameter `tag ` with the value
// ` 1918181833n`, and the To tag was lost — as was the branch of a Via
// written `branch = z9hG4bK...` (RFC 4475 3.1.1.1). A quoted value does not
// come here: whitespace inside the quotes is the value.
func addParam(p *HeaderParams, key, value string) {
	p.Add(trimLWS(key), trimLWS(value))
}
