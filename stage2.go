package simdjson

import "encoding/binary"

// scope is an open array or object.
type scope struct {
	tapeIndex uint32 // tape index of the opening word, written when the scope closes
	count     uint32 // number of elements (arrays) or fields (objects)
	keyStart  uint32 // binding mode: where this object's names start in builder.keys
	open      byte   // '[' or '{', which is also the scope's tape tag
}

// builder is stage 2: it walks the structural indices found by stage 1 and
// writes the tape. It is a port of src/generic/stage2/json_iterator.h
// (walk_document) and tape_builder.h; the gotos mirror the C++ labels.
type builder struct {
	buf            []byte
	idx            []uint32
	pos            int // next entry of idx
	tape           []uint64
	strs           []byte
	stack          []scope
	maxDepth       int
	bigIntAsString bool
	binding        bool      // see Parser.binding
	offs           []uint32  // binding mode: offs[i] is the input offset behind tape word i
	dups           []uint32  // binding mode: first repeated name per object (see checkNames)
	keys           []nameKey // binding mode: names of the open objects, innermost last
	seen           []uint32  // scratch for checkNames: hash table of keys indices + 1
}

// nameKey is an object name seen in binding mode: its tape index and hash.
type nameKey struct {
	i    uint32
	hash uint64
}

// mark records, in binding mode, that the tape word written next starts at
// input offset off. Only value, key and closing-bracket words are marked.
func (b *builder) mark(off int) { b.markAt(len(b.tape), off) }

func (b *builder) markAt(i, off int) {
	if i >= len(b.offs) {
		b.offs = append(b.offs, make([]uint32, i+1-len(b.offs)+64)...)
	}
	b.offs[i] = uint32(off)
}

// isStructuralOrSpace is C++ structural_or_whitespace: the bytes that may
// follow a number or atom.
var isStructuralOrSpace = [256]bool{
	' ': true, '\t': true, '\n': true, '\r': true,
	',': true, ':': true, '[': true, ']': true, '{': true, '}': true,
}

// advance returns the byte at the next structural index and its offset.
// Past the last index it returns 0, like the C++ unpadded sentinel.
func (b *builder) advance() (byte, int) {
	if b.pos >= len(b.idx) {
		// Every current caller fails on the 0 byte, but counting the overrun
		// keeps it safe regardless: documentEnd requires pos == len(idx), so a
		// walk that read past the end is never accepted.
		b.pos++
		return 0, len(b.buf)
	}
	off := int(b.idx[b.pos])
	b.pos++
	return b.buf[off], off
}

func (b *builder) peek() byte {
	if b.pos >= len(b.idx) {
		return 0
	}
	return b.buf[b.idx[b.pos]]
}

func (b *builder) walk() error {
	var (
		c   byte
		off int
		err error
	)
	b.tape = append(b.tape, 0) // root word, written at documentEnd

	c, off = b.advance()
	// An unmatched outer brace or bracket is rejected up front (simdjson issue 906).
	if last := b.buf[b.idx[len(b.idx)-1]]; c == '{' && last != '}' ||
		c == '[' && last != ']' {
		return ErrTape
	}
	goto value

objectBegin:
	if c, off = b.advance(); c != '"' {
		return ErrTape
	}
	b.stack[len(b.stack)-1].count++
	if b.binding {
		b.mark(off)
	}
	if err = b.str(off); err != nil {
		return err
	}
	if b.binding {
		b.addKey()
	}

objectField:
	if c, _ = b.advance(); c != ':' {
		return ErrTape
	}
	c, off = b.advance()
	goto value

objectContinue:
	switch c, off = b.advance(); c {
	case ',':
		b.stack[len(b.stack)-1].count++
		if c, off = b.advance(); c != '"' {
			return ErrTape
		}
		if b.binding {
			b.mark(off)
		}
		if err = b.str(off); err != nil {
			return err
		}
		if b.binding {
			b.addKey()
		}
		goto objectField
	case '}':
		if b.binding {
			b.mark(off)
			b.checkNames(b.stack[len(b.stack)-1].keyStart)
		}
		b.endContainer()
		goto valueEnd
	}
	return ErrTape

arrayBegin:
	b.stack[len(b.stack)-1].count++

arrayValue:
	c, off = b.advance()

value: // c, off start a value: the root, an object field's value or an array element
	if b.binding {
		b.mark(off)
	}
	switch c {
	case '{', '[':
		if b.binding && len(b.stack)+1 >= b.maxDepth { // v2 counts empty containers too
			return ErrDepth
		}
		if b.empty(c) {
			goto valueEnd
		}
		if err = b.push(c); err != nil {
			return err
		}
		if c == '{' {
			goto objectBegin
		}
		goto arrayBegin
	}
	if err = b.primitive(c, off); err != nil {
		return err
	}

valueEnd: // continue the enclosing scope, or finish the document
	if len(b.stack) == 0 {
		goto documentEnd
	}
	if b.stack[len(b.stack)-1].open == '[' {
		goto arrayContinue
	}
	goto objectContinue

arrayContinue:
	switch c, off = b.advance(); c {
	case ',':
		b.stack[len(b.stack)-1].count++
		goto arrayValue
	case ']':
		if b.binding {
			b.mark(off)
		}
		b.endContainer()
		goto valueEnd
	}
	return ErrTape

documentEnd:
	b.tape = append(b.tape, word(tagRoot, 0))
	b.tape[0] = word(tagRoot, uint64(len(b.tape)))
	if b.pos != len(b.idx) { // more than one root value, or trailing content
		return ErrTape
	}
	return nil
}

// push opens a scope. Like C++, depth counts open scopes and must stay below maxDepth.
func (b *builder) push(open byte) error {
	if len(b.stack)+1 >= b.maxDepth {
		return ErrDepth
	}
	b.stack = append(b.stack, scope{tapeIndex: uint32(len(b.tape)), keyStart: uint32(len(b.keys)), open: open})
	b.tape = append(b.tape, 0) // written by endContainer
	return nil
}

// empty writes an array or object that closes right after its opening byte
// c, without opening a scope (C++ visit_empty_array/visit_empty_object), and
// reports whether it did. Bracket bytes are their own tape tags.
func (b *builder) empty(c byte) bool {
	end := closer(c)
	if b.peek() != end {
		return false
	}
	_, closeOff := b.advance()
	if b.binding {
		b.markAt(len(b.tape)+1, closeOff)
	}
	i := uint64(len(b.tape))
	b.tape = append(b.tape, word(c, i+2), word(end, i))
	return true
}

// closer returns the byte (and tape tag) that closes an array or object.
func closer(open byte) byte {
	if open == '{' {
		return '}'
	}
	return ']'
}

// endContainer closes and pops the innermost scope: it writes the closing word
// (pointing at the opening one) and the opening word (index after the closing
// word, element count saturated to 24 bits).
func (b *builder) endContainer() {
	s := b.stack[len(b.stack)-1]
	b.stack = b.stack[:len(b.stack)-1]
	b.tape = append(b.tape, word(closer(s.open), uint64(s.tapeIndex)))
	count := min(s.count, saturated)
	b.tape[s.tapeIndex] = word(s.open, uint64(count)<<32|uint64(len(b.tape)))
}

// primitive parses a string, number or atom. Inside arrays and objects C++
// visit_primitive tests (c - '0') < 10 in int arithmetic, so every byte below
// '0' goes to the number parser (NUMBER_ERROR); at the root it is a TAPE_ERROR.
func (b *builder) primitive(c byte, off int) error {
	root := len(b.stack) == 0
	switch {
	case c == '"':
		return b.str(off)
	case c == '-' || isDigit(c), !root && c < '0':
		return b.number(off)
	case c == 't':
		if !b.atom(off, "true") {
			return ErrTAtom
		}
		b.tape = append(b.tape, word(tagTrue, 0))
	case c == 'f':
		if !b.atom(off, "false") {
			return ErrFAtom
		}
		b.tape = append(b.tape, word(tagFalse, 0))
	case c == 'n':
		if !b.atom(off, "null") {
			return ErrNAtom
		}
		b.tape = append(b.tape, word(tagNull, 0))
	default:
		return ErrTape
	}
	return nil
}

// atom reports whether buf[off:] is lit followed by a structural byte,
// whitespace, or the end of the input.
func (b *builder) atom(off int, lit string) bool {
	end := off + len(lit)
	return end <= len(b.buf) && string(b.buf[off:end]) == lit && terminates(b.buf, end)
}

func terminates(buf []byte, p int) bool { return p == len(buf) || isStructuralOrSpace[buf[p]] }

// addKey records the name just written to the tape (binding mode).
func (b *builder) addKey() {
	i := uint32(len(b.tape) - 1)
	b.keys = append(b.keys, nameKey{i, nameHash(b.name(i))})
}

// checkNames records, in binding mode, the first name of the closing object
// (whose names start at b.keys[start]) that repeats an earlier one:
// encoding/json/v2 rejects duplicate names in every object. Names are
// compared by hash first: pairwise in small objects, through a hash table in
// larger ones.
func (b *builder) checkNames(start uint32) {
	keys := b.keys[start:]
	b.keys = b.keys[:start]
	if len(keys) <= 8 {
		for j := 1; j < len(keys); j++ {
			for _, k := range keys[:j] {
				if k.hash == keys[j].hash && string(b.name(k.i)) == string(b.name(keys[j].i)) {
					b.dups = append(b.dups, keys[j].i)
					return
				}
			}
		}
		return
	}
	size := 32
	for size < 2*len(keys) {
		size *= 2
	}
	if cap(b.seen) < size {
		b.seen = make([]uint32, size)
	}
	seen := b.seen[:size]
	clear(seen)
	for j, k := range keys {
		h := int(k.hash) & (size - 1)
		for seen[h] != 0 {
			if prev := keys[seen[h]-1]; prev.hash == k.hash && string(b.name(prev.i)) == string(b.name(k.i)) {
				b.dups = append(b.dups, k.i)
				return
			}
			h = (h + 1) & (size - 1)
		}
		seen[h] = uint32(j + 1)
	}
}

// nameHash is a cheap hash of an object name for checkNames, which compares
// the names themselves on a match: its length and first and last 8 bytes.
func nameHash(s []byte) uint64 {
	h := uint64(len(s)) * 0x9E3779B97F4A7C15
	if len(s) >= 8 {
		h ^= binary.LittleEndian.Uint64(s)
		h *= 0xBF58476D1CE4E5B9
		h ^= binary.LittleEndian.Uint64(s[len(s)-8:])
	} else {
		for _, c := range s {
			h = h<<8 | uint64(c)
		}
	}
	h *= 0x94D049BB133111EB
	return h ^ h>>31
}

// name returns the unescaped name of the string word at tape index i.
func (b *builder) name(i uint32) []byte {
	off := b.tape[i] & (1<<56 - 1)
	n := uint64(binary.LittleEndian.Uint32(b.strs[off:]))
	return b.strs[off+4 : off+4+n]
}
