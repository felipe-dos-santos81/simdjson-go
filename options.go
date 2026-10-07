package simdjson

// Option configures Unmarshal, Marshal, MarshalAppend and MarshalIndent.
// The defaults are those of encoding/json/v2; each option turns on the v2
// option of the same name, which restores a behaviour of encoding/json (v1).
type Option func(*options)

type options struct {
	caseInsensitive bool
	nilSliceAsNull  bool
	nilMapAsNull    bool
	deterministic   bool
	rejectUnknown   bool
}

func makeOptions(opts []Option) options {
	var o options
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// MatchCaseInsensitiveNames matches JSON object names to struct fields
// ignoring case, dashes and underscores. A field tagged `case:strict` or
// `case:ignore` keeps its own rule.
func MatchCaseInsensitiveNames(v bool) Option { return func(o *options) { o.caseInsensitive = v } }

// FormatNilSliceAsNull marshals a nil slice as null instead of [].
func FormatNilSliceAsNull(v bool) Option { return func(o *options) { o.nilSliceAsNull = v } }

// FormatNilMapAsNull marshals a nil map as null instead of {}.
func FormatNilMapAsNull(v bool) Option { return func(o *options) { o.nilMapAsNull = v } }

// Deterministic marshals map entries sorted by their JSON names.
func Deterministic(v bool) Option { return func(o *options) { o.deterministic = v } }

// RejectUnknownMembers makes an object name that matches no struct field an error.
func RejectUnknownMembers(v bool) Option { return func(o *options) { o.rejectUnknown = v } }
