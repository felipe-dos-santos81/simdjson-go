package simdjson

import (
	"errors"

	"simdjson-go/internal/stage1"
)

// Errors returned by this package, one per C++ simdjson error_code it can
// produce (the C++ name is in each comment). Match them with errors.Is.
var (
	ErrCapacity           = errors.New("simdjson: document larger than 4 GiB")      // CAPACITY
	ErrTape               = errors.New("simdjson: invalid JSON structure")          // TAPE_ERROR
	ErrDepth              = errors.New("simdjson: maximum nesting depth exceeded")  // DEPTH_ERROR
	ErrString             = errors.New("simdjson: invalid string escape")           // STRING_ERROR
	ErrTAtom              = errors.New("simdjson: invalid value starting with 't'") // T_ATOM_ERROR
	ErrFAtom              = errors.New("simdjson: invalid value starting with 'f'") // F_ATOM_ERROR
	ErrNAtom              = errors.New("simdjson: invalid value starting with 'n'") // N_ATOM_ERROR
	ErrNumber             = errors.New("simdjson: invalid number")                  // NUMBER_ERROR
	ErrBigInt             = errors.New("simdjson: integer does not fit in 64 bits") // BIGINT_ERROR
	ErrIncorrectType      = errors.New("simdjson: incorrect type")                  // INCORRECT_TYPE
	ErrNumberOutOfRange   = errors.New("simdjson: number out of range")             // NUMBER_OUT_OF_RANGE
	ErrIndexOutOfBounds   = errors.New("simdjson: index out of bounds")             // INDEX_OUT_OF_BOUNDS
	ErrNoSuchField        = errors.New("simdjson: no such field")                   // NO_SUCH_FIELD
	ErrInvalidJSONPointer = errors.New("simdjson: invalid JSON pointer")            // INVALID_JSON_POINTER

	ErrUnclosedString = stage1.ErrUnclosedString // UNCLOSED_STRING
	ErrUnescapedChars = stage1.ErrUnescapedChars // UNESCAPED_CHARS
	ErrEmpty          = stage1.ErrEmpty          // EMPTY
	ErrUTF8           = stage1.ErrUTF8           // UTF8_ERROR
)
