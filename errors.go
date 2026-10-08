package simdjson

import "simdjson-go/internal/jsonerr"

// Errors returned by this package and by package ondemand, one per C++
// simdjson error_code they can produce (the C++ name is in each comment).
// Match them with errors.Is.
var (
	ErrCapacity           = jsonerr.ErrCapacity           // CAPACITY
	ErrTape               = jsonerr.ErrTape               // TAPE_ERROR
	ErrDepth              = jsonerr.ErrDepth              // DEPTH_ERROR
	ErrString             = jsonerr.ErrString             // STRING_ERROR
	ErrTAtom              = jsonerr.ErrTAtom              // T_ATOM_ERROR
	ErrFAtom              = jsonerr.ErrFAtom              // F_ATOM_ERROR
	ErrNAtom              = jsonerr.ErrNAtom              // N_ATOM_ERROR
	ErrNumber             = jsonerr.ErrNumber             // NUMBER_ERROR
	ErrBigInt             = jsonerr.ErrBigInt             // BIGINT_ERROR
	ErrIncorrectType      = jsonerr.ErrIncorrectType      // INCORRECT_TYPE
	ErrNumberOutOfRange   = jsonerr.ErrNumberOutOfRange   // NUMBER_OUT_OF_RANGE
	ErrIndexOutOfBounds   = jsonerr.ErrIndexOutOfBounds   // INDEX_OUT_OF_BOUNDS
	ErrNoSuchField        = jsonerr.ErrNoSuchField        // NO_SUCH_FIELD
	ErrInvalidJSONPointer = jsonerr.ErrInvalidJSONPointer // INVALID_JSON_POINTER

	ErrOutOfOrderIteration     = jsonerr.ErrOutOfOrderIteration     // OUT_OF_ORDER_ITERATION (On-Demand)
	ErrIncompleteArrayOrObject = jsonerr.ErrIncompleteArrayOrObject // INCOMPLETE_ARRAY_OR_OBJECT (On-Demand)
	ErrScalarDocumentAsValue   = jsonerr.ErrScalarDocumentAsValue   // SCALAR_DOCUMENT_AS_VALUE (On-Demand)
	ErrTrailingContent         = jsonerr.ErrTrailingContent         // TRAILING_CONTENT (On-Demand)

	ErrUnclosedString = jsonerr.ErrUnclosedString // UNCLOSED_STRING
	ErrUnescapedChars = jsonerr.ErrUnescapedChars // UNESCAPED_CHARS
	ErrEmpty          = jsonerr.ErrEmpty          // EMPTY
	ErrUTF8           = jsonerr.ErrUTF8           // UTF8_ERROR
)
