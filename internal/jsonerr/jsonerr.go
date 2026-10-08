// Package jsonerr holds the error values shared by the simdjson and
// ondemand packages, one per C++ simdjson error_code (named in each
// comment). Package simdjson re-exports every one of them.
package jsonerr

import (
	"errors"
	"strconv"

	"simdjson-go/internal/stage1"
)

var (
	ErrCapacity           = errors.New("simdjson: document larger than 0xFFFFFFFC bytes") // CAPACITY
	ErrTape               = errors.New("simdjson: invalid JSON structure")                // TAPE_ERROR
	ErrDepth              = errors.New("simdjson: maximum nesting depth exceeded")        // DEPTH_ERROR
	ErrString             = errors.New("simdjson: invalid string escape")                 // STRING_ERROR
	ErrTAtom              = errors.New("simdjson: invalid value starting with 't'")       // T_ATOM_ERROR
	ErrFAtom              = errors.New("simdjson: invalid value starting with 'f'")       // F_ATOM_ERROR
	ErrNAtom              = errors.New("simdjson: invalid value starting with 'n'")       // N_ATOM_ERROR
	ErrNumber             = errors.New("simdjson: invalid number")                        // NUMBER_ERROR
	ErrBigInt             = errors.New("simdjson: integer does not fit in 64 bits")       // BIGINT_ERROR
	ErrIncorrectType      = errors.New("simdjson: incorrect type")                        // INCORRECT_TYPE
	ErrNumberOutOfRange   = errors.New("simdjson: number out of range")                   // NUMBER_OUT_OF_RANGE
	ErrIndexOutOfBounds   = errors.New("simdjson: index out of bounds")                   // INDEX_OUT_OF_BOUNDS
	ErrNoSuchField        = errors.New("simdjson: no such field")                         // NO_SUCH_FIELD
	ErrInvalidJSONPointer = errors.New("simdjson: invalid JSON pointer")                  // INVALID_JSON_POINTER

	// On-Demand.
	ErrOutOfOrderIteration     = errors.New("simdjson: objects and arrays can only be read once, in order") // OUT_OF_ORDER_ITERATION
	ErrIncompleteArrayOrObject = errors.New("simdjson: document ended in the middle of an object or array") // INCOMPLETE_ARRAY_OR_OBJECT
	ErrScalarDocumentAsValue   = errors.New("simdjson: a scalar document cannot be used as a value")        // SCALAR_DOCUMENT_AS_VALUE
	ErrTrailingContent         = errors.New("simdjson: unexpected content after the document")              // TRAILING_CONTENT

	ErrUnclosedString = stage1.ErrUnclosedString // UNCLOSED_STRING
	ErrUnescapedChars = stage1.ErrUnescapedChars // UNESCAPED_CHARS
	ErrEmpty          = stage1.ErrEmpty          // EMPTY
	ErrUTF8           = stage1.ErrUTF8           // UTF8_ERROR
)

// StreamError ends a document stream (simdjson.Parser.ParseMany,
// ondemand.Parser.IterateMany): Err happened in the document, or the
// dropped tail, that starts at byte Offset of the input.
type StreamError struct {
	Offset int
	Err    error
}

func (e *StreamError) Error() string {
	return e.Err.Error() + " (document at offset " + strconv.Itoa(e.Offset) + ")"
}

func (e *StreamError) Unwrap() error { return e.Err }
