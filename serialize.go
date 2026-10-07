package simdjson

import (
	"bytes"
	"math"
	"strconv"

	"simdjson-go/internal/stage1"
)

// AppendJSON appends e as minified JSON to dst. Parsing the output yields an
// equal tree, including element types.
func (e Element) AppendJSON(dst []byte) []byte {
	switch e.tag() {
	case '[':
		dst = append(dst, '[')
		for k, v := range (Array{e}).All() {
			if k > 0 {
				dst = append(dst, ',')
			}
			dst = v.AppendJSON(dst)
		}
		return append(dst, ']')
	case '{':
		dst = append(dst, '{')
		first := true
		for k, v := range (Object{e}).fields() {
			if !first {
				dst = append(dst, ',')
			}
			first = false
			dst = append(appendQuoted(dst, k), ':')
			dst = v.AppendJSON(dst)
		}
		return append(dst, '}')
	case '"':
		return appendQuoted(dst, e.rawString())
	case 'Z':
		return append(dst, e.rawString()...)
	case 'l':
		return strconv.AppendInt(dst, int64(e.value()), 10)
	case 'u':
		return strconv.AppendUint(dst, e.value(), 10)
	case 'd':
		start := len(dst)
		dst = strconv.AppendFloat(dst, math.Float64frombits(e.value()), 'g', -1, 64)
		if !bytes.ContainsAny(dst[start:], ".e") {
			dst = append(dst, ".0"...) // keep it a float when re-parsed
		}
		return dst
	case 't':
		return append(dst, "true"...)
	case tagFalse:
		return append(dst, "false"...)
	}
	return append(dst, "null"...)
}

// MarshalJSON implements json.Marshaler.
func (e Element) MarshalJSON() ([]byte, error) { return e.AppendJSON(nil), nil }

// appendQuoted appends s as a JSON string, escaping '"', '\\' and bytes below 0x20.
func appendQuoted(dst, s []byte) []byte {
	const hex = "0123456789abcdef"
	dst = append(dst, '"')
	start := 0
	for i, c := range s {
		if c >= 0x20 && c != '"' && c != '\\' {
			continue
		}
		dst = append(dst, s[start:i]...)
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		case '\b':
			dst = append(dst, `\b`...)
		case '\f':
			dst = append(dst, `\f`...)
		case '\n':
			dst = append(dst, `\n`...)
		case '\r':
			dst = append(dst, `\r`...)
		case '\t':
			dst = append(dst, `\t`...)
		default:
			dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xF])
		}
		start = i + 1
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
}

// Minify appends src to dst with all whitespace outside strings removed.
// Like C++ simdjson::minify it does not validate the document; it returns
// ErrUnclosedString for an unterminated string.
func Minify(dst, src []byte) ([]byte, error) { return stage1.Minify(dst, src) }
