package simdjson

import (
	"bytes"
	"math"
	"strconv"

	"simdjson-go/internal/stage1"
)

// AppendJSON appends e as minified JSON to dst. Parsing the output yields an
// equal tree, including element types. It walks the tape in order without
// recursion, so nesting depth is limited only by memory.
func (e Element) AppendJSON(dst []byte) []byte { return e.appendJSON(dst, nil) }

// appendJSON is AppendJSON; if numberText is not nil, numbers are written as
// the text it returns instead of being reformatted.
func (e Element) appendJSON(dst []byte, numberText func(Element) []byte) []byte {
	type open struct {
		object   bool
		nonEmpty bool // an element or field was written: the next needs a comma
	}
	var buf [32]open
	stack := buf[:0]
	for i, end := e.i, e.next(); i < end; {
		v := Element{e.doc, i}
		tag := v.tag()
		if tag == tagEndArray || tag == tagEndObject {
			dst = append(dst, tag)
			stack = stack[:len(stack)-1]
			i++
			continue
		}
		if len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.nonEmpty {
				dst = append(dst, ',')
			}
			top.nonEmpty = true
			if top.object { // v is a key (one tape word): write it, then its value
				dst = append(appendQuoted(dst, v.rawString()), ':')
				i++
				v = Element{e.doc, i}
				tag = v.tag()
			}
		}
		if tag == tagStartArray || tag == tagStartObject {
			dst = append(dst, tag)
			stack = append(stack, open{object: tag == tagStartObject})
			i++
			continue
		}
		if numberText != nil && (tag == tagInt64 || tag == tagUint64 || tag == tagDouble || tag == tagBigInt) {
			dst = append(dst, numberText(v)...)
		} else {
			dst = v.appendScalar(dst)
		}
		i = v.next()
	}
	return dst
}

// appendScalar appends a value that is not an array or object.
func (e Element) appendScalar(dst []byte) []byte {
	switch e.tag() {
	case tagString:
		return appendQuoted(dst, e.rawString())
	case tagBigInt:
		return append(dst, e.rawString()...)
	case tagInt64:
		return strconv.AppendInt(dst, int64(e.value()), 10)
	case tagUint64:
		return strconv.AppendUint(dst, e.value(), 10)
	case tagDouble:
		start := len(dst)
		dst = strconv.AppendFloat(dst, math.Float64frombits(e.value()), 'g', -1, 64)
		if !bytes.ContainsAny(dst[start:], ".e") {
			dst = append(dst, ".0"...) // keep it a float when re-parsed
		}
		return dst
	case tagTrue:
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
// ErrUnclosedString for an unterminated string. On error the returned slice
// holds dst plus whatever was written before the error and must not be
// treated as minified output.
func Minify(dst, src []byte) ([]byte, error) { return stage1.Minify(dst, src) }
