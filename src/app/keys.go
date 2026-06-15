package app

// interpretKeyEscapes converts a user supplied keys string into the raw bytes
// that should be written to a process's stdin. It supports a small set of C
// style escape sequences so that control keys can be expressed in YAML config
// (shutdown.send_keys) and on the command line (process send-keys).
//
// Supported escapes: \\ \n \r \t \b \f \v \a \e \0 and \xHH (two hex digits).
// An unknown escape (e.g. \z) is left untouched as a literal backslash followed
// by the character, so existing literal strings are unaffected.
//
// Note: double quoted YAML scalars already process \x.. style escapes before
// this function ever sees them. Prefer single quoted values in YAML
// (send_keys: '\x03') so the literal backslash sequence reaches this function.
func interpretKeyEscapes(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\\' || i == len(s)-1 {
			out = append(out, c)
			continue
		}
		next := s[i+1]
		switch next {
		case '\\':
			out = append(out, '\\')
			i++
		case 'n':
			out = append(out, '\n')
			i++
		case 'r':
			out = append(out, '\r')
			i++
		case 't':
			out = append(out, '\t')
			i++
		case 'b':
			out = append(out, '\b')
			i++
		case 'f':
			out = append(out, '\f')
			i++
		case 'v':
			out = append(out, '\v')
			i++
		case 'a':
			out = append(out, '\a')
			i++
		case 'e':
			out = append(out, 0x1b) // ESC
			i++
		case '0':
			out = append(out, 0x00) // NUL
			i++
		case 'x', 'X':
			if i+3 < len(s) {
				if h, ok := parseHexByte(s[i+2], s[i+3]); ok {
					out = append(out, h)
					i += 3
					continue
				}
			}
			// not a valid \xHH sequence - keep the backslash literally
			out = append(out, c)
		default:
			// unknown escape - keep the backslash literally
			out = append(out, c)
		}
	}
	return out
}

// parseHexByte parses two hex digits into a byte. It returns ok=false if either
// character is not a valid hex digit.
func parseHexByte(hi, lo byte) (byte, bool) {
	h, ok := hexVal(hi)
	if !ok {
		return 0, false
	}
	l, ok := hexVal(lo)
	if !ok {
		return 0, false
	}
	return h<<4 | l, true
}

func hexVal(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}
