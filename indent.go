package simdjson

import (
	"errors"
	"strings"
)

// MarshalIndent is Marshal with each array element and object member on its
// own line, starting with prefix and indented by one copy of indent per
// nesting level, as encoding/json/v2 does with jsontext.WithIndentPrefix and
// jsontext.WithIndent. Empty arrays and objects stay on one line. Prefix and
// indent may contain only spaces and tabs: anything else is an error (v2
// panics on such options).
func MarshalIndent(v any, prefix, indent string, opts ...Option) ([]byte, error) {
	if strings.Trim(prefix, " \t") != "" || strings.Trim(indent, " \t") != "" {
		return nil, errBadIndent
	}
	b, err := Marshal(v, opts...)
	if err != nil {
		return nil, err
	}
	return appendIndented(make([]byte, 0, len(b)*2), b, prefix, indent), nil
}

var errBadIndent = errors.New("simdjson: indent prefix and indent may contain only spaces and tabs")

// appendIndented re-indents the compact, valid JSON src (as produced by Marshal).
func appendIndented(dst, src []byte, prefix, indent string) []byte {
	depth := 0
	newline := func() {
		dst = append(dst, '\n')
		dst = append(dst, prefix...)
		for range depth {
			dst = append(dst, indent...)
		}
	}
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case '"':
			n := stringEnd(src[i:])
			dst = append(dst, src[i:i+n]...)
			i += n - 1
		case '{', '[':
			if i+1 < len(src) && (src[i+1] == '}' || src[i+1] == ']') {
				dst = append(dst, c, src[i+1]) // empty: {} or []
				i++
				continue
			}
			dst = append(dst, c)
			depth++
			newline()
		case '}', ']':
			depth--
			newline()
			dst = append(dst, c)
		case ',':
			dst = append(dst, ',')
			newline()
		case ':':
			dst = append(dst, ':', ' ')
		default:
			dst = append(dst, c)
		}
	}
	return dst
}
