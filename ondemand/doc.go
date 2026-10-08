// Package ondemand reads JSON lazily, forward only: a port of C++ simdjson's
// On-Demand API (v5.0.2). [Parser.Iterate] runs stage 1 only, validating
// UTF-8 and finding the structural characters; each value is parsed when it
// is read, as the type it is read as.
//
// As in C++:
//
//   - Validate what you use: values that are never read are not validated,
//     so a document with a bad value in a skipped field reads without error.
//   - Objects and arrays are read once, in order. Count, Reset, Rewind and
//     AtPointer go back. A Value holding a scalar may be read later, after
//     the cursor has moved past it.
//   - Field names are compared as written: escapes are not decoded, so
//     {"\u0061":1} has no field "a".
//   - Nothing checks what follows a root array or object: "[1] [2]" reads as
//     [1]. Call [Document.AtEnd] after reading to reject trailing content.
//
// Lifetime: the Document, every handle obtained from it and every slice
// returned by Raw and RawKey are valid until the next Iterate on the same
// Parser; slices from StringBytes until the next Iterate or Rewind. The
// *Document itself is reused by its Parser, so an old *Document reads the
// new document after the next Iterate.
//
// Misuse (reading a container out of order, iterating one again after its
// iteration started, a stale Field, a zero handle, a handle after the next
// Iterate) returns ErrOutOfOrderIteration and never panics; release C++
// leaves it undefined. Looking a field up in a value already started as an
// array is ErrIncorrectType. Errors are package
// simdjson's sentinels, such as simdjson.ErrNoSuchField; compare them with
// errors.Is.
package ondemand
