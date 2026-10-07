// time.Time handling is adapted from the Go standard library
// (src/encoding/json/v2/arshal_time.go), Copyright 2023 The Go Authors,
// under the BSD-style license in LICENSE-GO.

package simdjson

import (
	"errors"
	"reflect"
	"time"
)

// makeTimeDecoder handles time.Time (an RFC 3339 string, checked as strictly
// as v2 does) and time.Duration (no default JSON form in v2: always an error).
func makeTimeDecoder(t reflect.Type) decodeFunc {
	if t == timeDurationType {
		return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
			return d.semErr(e, t, errNoDefault)
		}
	}
	return func(d *decodeState, e Element, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return d.semErr(e, t, errInvalidStringTag)
		}
		switch e.tag() {
		case tagNull:
			v.SetZero()
			return nil
		case tagString:
		default:
			return d.valueErr(e, t, nil)
		}
		tt := v.Addr().Interface().(*time.Time)
		if err := unmarshalRFC3339(tt, e.rawString()); err != nil {
			return d.semErr(e, t, err)
		}
		return nil
	}
}

// unmarshalRFC3339 parses b into tt, rejecting what RFC 3339 forbids but
// time.Time.UnmarshalText accepts (v2 timeArshaler.unmarshal).
func unmarshalRFC3339(tt *time.Time, b []byte) error {
	var u time.Time
	if err := u.UnmarshalText(b); err != nil {
		return err
	}
	newParseError := func(layoutElem, valueElem, message string) error {
		return &time.ParseError{Layout: time.RFC3339, Value: string(b), LayoutElem: layoutElem, ValueElem: valueElem, Message: message}
	}
	switch {
	case b[len("2006-01-02T")+1] == ':': // hour must be two digits
		return newParseError("15", string(b[len("2006-01-02T"):][:1]), "")
	case b[len("2006-01-02T15:04:05")] == ',': // sub-second separator must be a period
		return newParseError(".", ",", "")
	case b[len(b)-1] != 'Z':
		switch {
		case parseDec2(b[len(b)-len("07:00"):]) >= 24:
			return newParseError("Z07:00", string(b[len(b)-len("Z07:00"):]), ": timezone hour out of range")
		case parseDec2(b[len(b)-len("00"):]) >= 60:
			return newParseError("Z07:00", string(b[len(b)-len("Z07:00"):]), ": timezone minute out of range")
		}
	}
	*tt = u
	return nil
}

func parseDec2(b []byte) byte {
	if len(b) < 2 {
		return 0
	}
	return 10*(b[0]-'0') + (b[1] - '0')
}

// makeTimeEncoder writes time.Time as an RFC 3339 string with nanoseconds,
// rejecting years and zone offsets RFC 3339 cannot express; time.Duration
// has no default JSON form in v2.
func makeTimeEncoder(t reflect.Type) encodeFunc {
	if t == timeDurationType {
		return func(s *encodeState, v reflect.Value, mode uint8) error {
			return s.semErr(t, errNoDefault)
		}
	}
	return func(s *encodeState, v reflect.Value, mode uint8) error {
		if mode&modeStringTag != 0 {
			return s.semErr(t, errInvalidStringTag)
		}
		tt := v.Interface().(time.Time)
		s.buf = append(s.buf, '"')
		n0 := len(s.buf)
		s.buf = tt.AppendFormat(s.buf, time.RFC3339Nano)
		switch b := s.buf[n0:]; {
		case b[len("9999")] != '-': // year must be exactly 4 digits wide
			return s.semErr(t, errors.New("year outside of range [0,9999]"))
		case b[len(b)-1] != 'Z':
			c := b[len(b)-len("Z07:00")]
			if ('0' <= c && c <= '9') || parseDec2(b[len(b)-len("07:00"):]) >= 24 {
				return s.semErr(t, errors.New("timezone hour outside of range [0,23]"))
			}
		}
		s.buf = append(s.buf, '"')
		return nil
	}
}
