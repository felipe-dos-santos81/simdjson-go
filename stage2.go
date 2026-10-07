package simdjson

// scope is an open array or object.
type scope struct {
	tapeIndex uint32 // tape index of the opening word, written when the scope closes
	count     uint32 // number of elements (arrays) or fields (objects)
	isArray   bool
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
	switch last := b.buf[b.idx[len(b.idx)-1]]; c {
	case '{':
		if last != '}' {
			return ErrTape
		}
	case '[':
		if last != ']' {
			return ErrTape
		}
	}
	switch c {
	case '{':
		if b.peek() == '}' {
			b.advance()
			b.emptyContainer('{', tagEndObject)
			goto documentEnd
		}
		goto objectBegin
	case '[':
		if b.peek() == ']' {
			b.advance()
			b.emptyContainer('[', tagEndArray)
			goto documentEnd
		}
		goto arrayBegin
	}
	if err = b.primitive(c, off, true); err != nil {
		return err
	}
	goto documentEnd

objectBegin:
	if err = b.push(false); err != nil {
		return err
	}
	if c, off = b.advance(); c != '"' {
		return ErrTape
	}
	b.stack[len(b.stack)-1].count++
	if err = b.str(off); err != nil {
		return err
	}

objectField:
	if c, _ = b.advance(); c != ':' {
		return ErrTape
	}
	c, off = b.advance()
	switch c {
	case '{':
		if b.peek() == '}' {
			b.advance()
			b.emptyContainer('{', tagEndObject)
			goto objectContinue
		}
		goto objectBegin
	case '[':
		if b.peek() == ']' {
			b.advance()
			b.emptyContainer('[', tagEndArray)
			goto objectContinue
		}
		goto arrayBegin
	}
	if err = b.primitive(c, off, false); err != nil {
		return err
	}

objectContinue:
	switch c, _ = b.advance(); c {
	case ',':
		b.stack[len(b.stack)-1].count++
		if c, off = b.advance(); c != '"' {
			return ErrTape
		}
		if err = b.str(off); err != nil {
			return err
		}
		goto objectField
	case '}':
		b.endContainer('{', tagEndObject)
		goto scopeEnd
	}
	return ErrTape

scopeEnd:
	b.stack = b.stack[:len(b.stack)-1]
	if len(b.stack) == 0 {
		goto documentEnd
	}
	if b.stack[len(b.stack)-1].isArray {
		goto arrayContinue
	}
	goto objectContinue

arrayBegin:
	if err = b.push(true); err != nil {
		return err
	}
	b.stack[len(b.stack)-1].count++

arrayValue:
	c, off = b.advance()
	switch c {
	case '{':
		if b.peek() == '}' {
			b.advance()
			b.emptyContainer('{', tagEndObject)
			goto arrayContinue
		}
		goto objectBegin
	case '[':
		if b.peek() == ']' {
			b.advance()
			b.emptyContainer('[', tagEndArray)
			goto arrayContinue
		}
		goto arrayBegin
	}
	if err = b.primitive(c, off, false); err != nil {
		return err
	}

arrayContinue:
	switch c, _ = b.advance(); c {
	case ',':
		b.stack[len(b.stack)-1].count++
		goto arrayValue
	case ']':
		b.endContainer('[', tagEndArray)
		goto scopeEnd
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
func (b *builder) push(isArray bool) error {
	if len(b.stack)+1 >= b.maxDepth {
		return ErrDepth
	}
	b.stack = append(b.stack, scope{tapeIndex: uint32(len(b.tape)), isArray: isArray})
	b.tape = append(b.tape, 0) // written by endContainer
	return nil
}

func (b *builder) emptyContainer(start, end byte) {
	i := uint64(len(b.tape))
	b.tape = append(b.tape, word(start, i+2), word(end, i))
}

// endContainer writes the closing word (pointing at the opening one) and the
// opening word (index after the closing word, element count saturated to 24 bits).
func (b *builder) endContainer(start, end byte) {
	s := b.stack[len(b.stack)-1]
	b.tape = append(b.tape, word(end, uint64(s.tapeIndex)))
	count := min(s.count, 0xFFFFFF)
	b.tape[s.tapeIndex] = word(start, uint64(count)<<32|uint64(len(b.tape)))
}

// primitive parses a string, number or atom. Inside arrays and objects C++
// visit_primitive tests (c - '0') < 10 in int arithmetic, so every byte below
// '0' goes to the number parser (NUMBER_ERROR); at the root it is a TAPE_ERROR.
func (b *builder) primitive(c byte, off int, root bool) error {
	switch {
	case c == '"':
		return b.str(off)
	case c == '-' || isDigit(c), !root && c < '0':
		return b.number(off)
	case c == 't':
		if !b.atom(off, "true") {
			return ErrTAtom
		}
		b.tape = append(b.tape, word('t', 0))
	case c == 'f':
		if !b.atom(off, "false") {
			return ErrFAtom
		}
		b.tape = append(b.tape, word(tagFalse, 0))
	case c == 'n':
		if !b.atom(off, "null") {
			return ErrNAtom
		}
		b.tape = append(b.tape, word('n', 0))
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
